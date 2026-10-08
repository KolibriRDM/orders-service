package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
	"github.com/KolibriRDM/orders-service/internal/model"
)

// Подменяет чтение и изменение статуса, выявляя неожиданные вызовы.
type statusOrderRepositoryStub struct {
	Repository
	get    func(context.Context, string) (model.Order, error)
	update func(context.Context, string, int64, model.OrderStatus, time.Time) (model.Order, error)
}

// Вызывает подмену чтения заказа.
func (r *statusOrderRepositoryStub) GetByID(ctx context.Context, id string) (model.Order, error) {
	if r.get == nil {
		panic("unexpected GetByID")
	}
	return r.get(ctx, id)
}

// Вызывает подмену изменения статуса.
func (r *statusOrderRepositoryStub) UpdateStatus(ctx context.Context, id string, version int64, status model.OrderStatus, at time.Time) (model.Order, error) {
	if r.update == nil {
		panic("unexpected UpdateStatus")
	}
	return r.update(ctx, id, version, status, at)
}

// Проверяет ошибку без возврата частичного заказа.
func assertStatusOrderError(t *testing.T, got model.Order, err, want error) {
	t.Helper()
	if !errors.Is(err, want) || !reflect.DeepEqual(got, model.Order{}) {
		t.Fatalf("got=%+v err=%v want=%v", got, err, want)
	}
}

// Проверяет владельца, нормализацию UUID и возврат результата репозитория.
func TestCancelOrderOwnOrder(t *testing.T) {
	before := getOrderFixture()
	before.Status = model.StatusCreated
	before.Version = 1
	for _, compact := range []bool{false, true} {
		t.Run(fmt.Sprintf("compact UUID=%t", compact), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			userID, orderID := before.UserID, before.ID
			if compact {
				userID = strings.ReplaceAll(strings.ToUpper(userID), "-", "")
				orderID = strings.ReplaceAll(strings.ToUpper(orderID), "-", "")
			}
			var calls []string
			var want model.Order
			started := time.Now()
			repo := &statusOrderRepositoryStub{
				get: func(gotCtx context.Context, id string) (model.Order, error) {
					calls = append(calls, "read")
					if gotCtx != ctx || id != before.ID {
						t.Fatalf("wrong read arguments: id=%s", id)
					}
					return before, nil
				},
				update: func(gotCtx context.Context, id string, version int64, status model.OrderStatus, at time.Time) (model.Order, error) {
					calls = append(calls, "update")
					if gotCtx != ctx || id != before.ID || version != 1 || status != model.StatusCancelled {
						t.Fatalf("wrong update arguments: id=%s version=%d status=%s", id, version, status)
					}
					if at.Location() != time.UTC || at.Before(started) || at.After(time.Now()) {
						t.Fatalf("wrong update time: %s", at)
					}
					want = before
					want.Status = status
					want.Version = 2
					want.UpdatedAt = at.Truncate(time.Microsecond)
					return want, nil
				},
			}
			got, err := NewService(repo, nil).CancelOrder(ctx, userID, orderID, 1)
			if err != nil || !reflect.DeepEqual(got, want) || !reflect.DeepEqual(calls, []string{"read", "update"}) {
				t.Fatalf("got=%+v err=%v calls=%v want=%+v", got, err, calls, want)
			}
		})
	}
}

// Проверяет валидацию до обращения к репозиторию.
func TestCancelOrderInvalidInput(t *testing.T) {
	order := getOrderFixture()
	for _, tc := range []struct {
		name, userID, orderID string
		version               int64
	}{
		{"empty user", "", order.ID, 1}, {"invalid user", "user", order.ID, 1},
		{"empty order", order.UserID, "", 1}, {"invalid order", order.UserID, "order", 1},
		{"zero version", order.UserID, order.ID, 0}, {"negative version", order.UserID, order.ID, -1},
		{"overflow version", order.UserID, order.ID, math.MaxInt64},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NewService(nil, nil).CancelOrder(context.Background(), tc.userID, tc.orderID, tc.version)
			assertStatusOrderError(t, got, err, orderserrors.ErrInvalidInput)
		})
	}
}

// Проверяет сокрытие чужого заказа и ошибки чтения без попытки изменения.
func TestCancelOrderReadFailures(t *testing.T) {
	order := getOrderFixture()
	unexpected := errors.New("unexpected repository error")
	for _, tc := range []struct {
		name, userID string
		cause, want  error
	}{
		{"foreign order", "dddddddd-dddd-4ddd-8ddd-dddddddddddd", nil, orderserrors.ErrNotFound},
		{"missing order", order.UserID, orderserrors.ErrNotFound, orderserrors.ErrNotFound},
		{"database unavailable", order.UserID, orderserrors.ErrDatabaseUnavailable, orderserrors.ErrDatabaseUnavailable},
		{"unexpected error", order.UserID, unexpected, unexpected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			repo := &statusOrderRepositoryStub{get: func(context.Context, string) (model.Order, error) {
				calls++
				if tc.cause != nil {
					return order, fmt.Errorf("read: %w", tc.cause)
				}
				return order, nil
			}}
			got, err := NewService(repo, nil).CancelOrder(context.Background(), tc.userID, order.ID, 1)
			assertStatusOrderError(t, got, err, tc.want)
			if calls != 1 {
				t.Fatalf("reads=%d", calls)
			}
		})
	}
}

// Проверяет отмену до валидации и во время проверки владельца.
func TestCancelOrderContext(t *testing.T) {
	order := getOrderFixture()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	for _, ctx := range []context.Context{cancelled, expired} {
		got, err := NewService(nil, nil).CancelOrder(ctx, "", "", 0)
		assertStatusOrderError(t, got, err, ctx.Err())
	}
	ctx, cancelRead := context.WithCancel(context.Background())
	defer cancelRead()
	repo := &statusOrderRepositoryStub{get: func(context.Context, string) (model.Order, error) { cancelRead(); return order, nil }}
	got, err := NewService(repo, nil).CancelOrder(ctx, order.UserID, order.ID, 1)
	assertStatusOrderError(t, got, err, context.Canceled)
}

// Проверяет сохранение ошибок записи и версии запроса вместо прочитанной версии.
func TestCancelOrderUpdateFailures(t *testing.T) {
	order := getOrderFixture()
	for _, cause := range []error{orderserrors.ErrVersionConflict, orderserrors.ErrInvalidTransition, orderserrors.ErrNotFound, orderserrors.ErrDatabaseUnavailable, context.Canceled, context.DeadlineExceeded, errors.New("unexpected update error")} {
		t.Run(cause.Error(), func(t *testing.T) {
			calls := 0
			repo := &statusOrderRepositoryStub{
				get: func(context.Context, string) (model.Order, error) { return order, nil },
				update: func(_ context.Context, id string, version int64, status model.OrderStatus, _ time.Time) (model.Order, error) {
					calls++
					// Прочитанная версия равна двум, но запрос по-прежнему ожидает первую.
					if id != order.ID || version != 1 || status != model.StatusCancelled {
						t.Fatalf("id=%s version=%d status=%s", id, version, status)
					}
					return order, fmt.Errorf("update: %w", cause)
				},
			}
			got, err := NewService(repo, nil).CancelOrder(context.Background(), order.UserID, order.ID, 1)
			assertStatusOrderError(t, got, err, cause)
			if calls != 1 {
				t.Fatalf("updates=%d", calls)
			}
		})
	}
}
