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

// Проверяет передачу нормализованного ID, статуса и версии без проверки владельца.
func TestUpdateOrderStatusStaff(t *testing.T) {
	order := getOrderFixture()
	for _, version := range []int64{1, math.MaxInt64 - 1} {
		t.Run(fmt.Sprintf("version=%d", version), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			started := time.Now()
			calls := 0
			var want model.Order
			repo := &statusOrderRepositoryStub{update: func(gotCtx context.Context, id string, gotVersion int64, status model.OrderStatus, at time.Time) (model.Order, error) {
				calls++
				if gotCtx != ctx || id != order.ID || gotVersion != version || status != model.StatusReady {
					t.Fatalf("id=%s version=%d status=%s", id, gotVersion, status)
				}
				if at.Location() != time.UTC || at.Before(started) || at.After(time.Now()) {
					t.Fatalf("wrong update time: %s", at)
				}
				want = order
				want.Status = status
				want.Version = version + 1
				want.UpdatedAt = at.Truncate(time.Microsecond)
				return want, nil
			}}
			id := strings.ReplaceAll(strings.ToUpper(order.ID), "-", "")
			got, err := NewService(repo, nil).UpdateOrderStatus(ctx, true, id, version, model.StatusReady)
			if err != nil || calls != 1 || !reflect.DeepEqual(got, want) {
				t.Fatalf("got=%+v err=%v calls=%d want=%+v", got, err, calls, want)
			}
		})
	}
}

// Проверяет отказ без прав до валидации и доступа к БД.
func TestUpdateOrderStatusForbidden(t *testing.T) {
	order := getOrderFixture()
	for _, tc := range []struct {
		id      string
		version int64
		status  model.OrderStatus
	}{
		{order.ID, 1, model.StatusCooking}, {"", 0, ""},
	} {
		got, err := NewService(nil, nil).UpdateOrderStatus(context.Background(), false, tc.id, tc.version, tc.status)
		assertStatusOrderError(t, got, err, orderserrors.ErrForbidden)
	}
}

// Проверяет валидацию аргументов до вызова репозитория.
func TestUpdateOrderStatusInvalidInput(t *testing.T) {
	order := getOrderFixture()
	for _, tc := range []struct {
		name, id string
		version  int64
		status   model.OrderStatus
	}{
		{"empty ID", "", 1, model.StatusCooking}, {"invalid ID", "order", 1, model.StatusCooking},
		{"zero version", order.ID, 0, model.StatusCooking}, {"negative version", order.ID, -1, model.StatusCooking},
		{"overflow version", order.ID, math.MaxInt64, model.StatusCooking},
		{"empty status", order.ID, 1, ""}, {"unknown status", order.ID, 1, "paid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NewService(nil, nil).UpdateOrderStatus(context.Background(), true, tc.id, tc.version, tc.status)
			assertStatusOrderError(t, got, err, orderserrors.ErrInvalidInput)
		})
	}
}

// Проверяет приоритет отмены над правами и валидацией.
func TestUpdateOrderStatusContext(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	for _, ctx := range []context.Context{cancelled, expired} {
		got, err := NewService(nil, nil).UpdateOrderStatus(ctx, false, "", 0, "")
		assertStatusOrderError(t, got, err, ctx.Err())
	}
}

// Проверяет передачу ошибок репозитория без частичного результата и повторной записи.
func TestUpdateOrderStatusRepositoryFailures(t *testing.T) {
	order := getOrderFixture()
	for _, cause := range []error{orderserrors.ErrNotFound, orderserrors.ErrVersionConflict, orderserrors.ErrInvalidTransition, orderserrors.ErrDatabaseUnavailable, context.Canceled, context.DeadlineExceeded, errors.New("unexpected update error")} {
		t.Run(cause.Error(), func(t *testing.T) {
			calls := 0
			repo := &statusOrderRepositoryStub{update: func(context.Context, string, int64, model.OrderStatus, time.Time) (model.Order, error) {
				calls++
				return order, fmt.Errorf("repository: %w", cause)
			}}
			got, err := NewService(repo, nil).UpdateOrderStatus(context.Background(), true, order.ID, 1, model.StatusCooking)
			assertStatusOrderError(t, got, err, cause)
			if calls != 1 {
				t.Fatalf("updates=%d", calls)
			}
		})
	}
}
