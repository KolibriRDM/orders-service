package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
	"github.com/KolibriRDM/orders-service/internal/model"
)

// Тестовая подмена чтения заказа по ID.
type getOrderRepositoryStub struct {
	Repository
	getByID func(context.Context, string) (model.Order, error)
}

// Вызывает тестовую подмену операции.
func (r *getOrderRepositoryStub) GetByID(ctx context.Context, id string) (model.Order, error) {
	return r.getByID(ctx, id)
}

// Готовит заказ с владельцем и снимками для проверки чтения.
func getOrderFixture() model.Order {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	return model.Order{
		ID: "abcdef01-2345-4678-9abc-def012345678", UserID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		RestaurantID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", Status: model.StatusCooking, Version: 2,
		TotalKopecks: 44900, DeliveryAddress: "Москва, Тестовая, 1", CreatedAt: now, UpdatedAt: now.Add(time.Minute),
		Items: []model.OrderItem{{MenuItemID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", NameSnapshot: "Пицца", UnitPriceKopecks: 25000, Quantity: 1}},
	}
}

// Проверяет чтение своего заказа с разными формами UUID.
func TestGetOrderOwnOrder(t *testing.T) {
	want := getOrderFixture()
	for _, tc := range []struct{ name, userID, orderID string }{
		{"canonical UUIDs", want.UserID, want.ID},
		{"uppercase UUIDs", strings.ToUpper(want.UserID), strings.ToUpper(want.ID)},
		{"compact UUIDs", strings.ReplaceAll(want.UserID, "-", ""), strings.ReplaceAll(want.ID, "-", "")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			calls := 0
			repo := &getOrderRepositoryStub{getByID: func(gotCtx context.Context, id string) (model.Order, error) {
				calls++
				if gotCtx != ctx || id != want.ID {
					t.Fatalf("wrong context or ID: %s", id)
				}
				return want, nil
			}}
			got, err := NewService(repo, nil).GetOrder(ctx, tc.userID, tc.orderID)
			if err != nil || !reflect.DeepEqual(got, want) || calls != 1 {
				t.Fatalf("got=%+v err=%v calls=%d", got, err, calls)
			}
		})
	}
}

// Проверяет сокрытие чужого заказа за ошибкой отсутствия.
func TestGetOrderHidesAnotherUsersOrder(t *testing.T) {
	order := getOrderFixture()
	repo := &getOrderRepositoryStub{getByID: func(context.Context, string) (model.Order, error) { return order, nil }}
	got, err := NewService(repo, nil).GetOrder(context.Background(), "dddddddd-dddd-4ddd-8ddd-dddddddddddd", order.ID)
	if !errors.Is(err, orderserrors.ErrNotFound) || errors.Is(err, orderserrors.ErrForbidden) || !reflect.DeepEqual(got, model.Order{}) {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

// Проверяет неверные UUID и отмену до обращения к БД.
func TestGetOrderRejectsBeforeRepository(t *testing.T) {
	order := getOrderFixture()
	for _, tc := range []struct{ name, userID, orderID string }{
		{"invalid user", "user", order.ID},
		{"empty user", "", order.ID},
		{"invalid order", order.UserID, "order"},
		{"empty order", order.UserID, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NewService(nil, nil).GetOrder(context.Background(), tc.userID, tc.orderID)
			if !errors.Is(err, orderserrors.ErrInvalidInput) || !reflect.DeepEqual(got, model.Order{}) {
				t.Fatalf("got=%+v err=%v", got, err)
			}
		})
	}
	t.Run("cancelled context takes precedence", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		got, err := NewService(nil, nil).GetOrder(ctx, "", "")
		if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, model.Order{}) {
			t.Fatalf("got=%+v err=%v", got, err)
		}
	})
}

// Проверяет передачу ошибок чтения без данных заказа.
func TestGetOrderPreservesRepositoryErrorsWithoutData(t *testing.T) {
	order := getOrderFixture()
	for _, cause := range []error{orderserrors.ErrNotFound, orderserrors.ErrDatabaseUnavailable, context.DeadlineExceeded, errors.New("unexpected repository error")} {
		t.Run(cause.Error(), func(t *testing.T) {
			repo := &getOrderRepositoryStub{getByID: func(context.Context, string) (model.Order, error) {
				return order, fmt.Errorf("repository: %w", cause)
			}}
			got, err := NewService(repo, nil).GetOrder(context.Background(), order.UserID, order.ID)
			if !errors.Is(err, cause) || !reflect.DeepEqual(got, model.Order{}) {
				t.Fatalf("got=%+v err=%v", got, err)
			}
		})
	}
}

// Проверяет отмену во время чтения заказа.
func TestGetOrderCancellationDuringRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	order := getOrderFixture()
	repo := &getOrderRepositoryStub{getByID: func(gotCtx context.Context, _ string) (model.Order, error) {
		if gotCtx != ctx {
			t.Fatal("lost request context")
		}
		cancel()
		return order, nil
	}}
	got, err := NewService(repo, nil).GetOrder(ctx, order.UserID, order.ID)
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, model.Order{}) {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}
