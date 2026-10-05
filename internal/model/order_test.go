package model

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
)

// Готовит корректный заказ для тестов.
func validOrder() Order {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	return Order{
		ID:           "order-1",
		UserID:       "user-1",
		RestaurantID: "restaurant-1",
		Items: []OrderItem{{
			MenuItemID:       "item-1",
			NameSnapshot:     "Пицца",
			UnitPriceKopecks: 25_000,
			Quantity:         2,
		}},
		Status:          StatusCreated,
		Version:         1,
		TotalKopecks:    69_900,
		DeliveryAddress: "Москва, Тестовая улица, 1",
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

// Проверяет бизнес-ограничения модели заказа.
func TestOrderValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Order)
		wantErr bool
	}{
		{name: "valid order"},
		{name: "delivered is valid", mutate: func(o *Order) { o.Status = StatusDelivered }},
		{name: "cancelled is valid", mutate: func(o *Order) { o.Status = StatusCancelled }},
		{name: "later update is valid", mutate: func(o *Order) { o.UpdatedAt = o.CreatedAt.Add(time.Minute); o.Version = 2 }},
		{name: "empty order ID", mutate: func(o *Order) { o.ID = "" }, wantErr: true},
		{name: "blank user ID", mutate: func(o *Order) { o.UserID = " \t" }, wantErr: true},
		{name: "empty restaurant ID", mutate: func(o *Order) { o.RestaurantID = "" }, wantErr: true},
		{name: "blank address", mutate: func(o *Order) { o.DeliveryAddress = " \t\n" }, wantErr: true},
		{name: "300 Cyrillic characters", mutate: func(o *Order) { o.DeliveryAddress = strings.Repeat("я", 300) }},
		{name: "301 Cyrillic characters", mutate: func(o *Order) { o.DeliveryAddress = strings.Repeat("я", 301) }, wantErr: true},
		{name: "unknown status", mutate: func(o *Order) { o.Status = "unknown" }, wantErr: true},
		{name: "zero version", mutate: func(o *Order) { o.Version = 0 }, wantErr: true},
		{name: "negative version", mutate: func(o *Order) { o.Version = -1 }, wantErr: true},
		{name: "zero creation time", mutate: func(o *Order) { o.CreatedAt = time.Time{} }, wantErr: true},
		{name: "zero update time", mutate: func(o *Order) { o.UpdatedAt = time.Time{} }, wantErr: true},
		{name: "update before creation", mutate: func(o *Order) { o.UpdatedAt = o.CreatedAt.Add(-time.Second) }, wantErr: true},
		{name: "no items", mutate: func(o *Order) { o.Items = nil }, wantErr: true},
		{name: "invalid quantity", mutate: func(o *Order) { o.Items[0].Quantity = 0 }, wantErr: true},
		{name: "invalid price", mutate: func(o *Order) { o.Items[0].UnitPriceKopecks = -1 }, wantErr: true},
		{name: "blank menu item ID", mutate: func(o *Order) { o.Items[0].MenuItemID = " \t" }, wantErr: true},
		{name: "blank name snapshot", mutate: func(o *Order) { o.Items[0].NameSnapshot = " \t" }, wantErr: true},
		{name: "wrong total", mutate: func(o *Order) { o.TotalKopecks++ }, wantErr: true},
		{
			name: "duplicate item with otherwise correct total",
			mutate: func(o *Order) {
				o.Items = append(o.Items, o.Items[0])
				o.TotalKopecks = 119_900
			},
			wantErr: true,
		},
		{
			name: "distinct items with correct total",
			mutate: func(o *Order) {
				o.Items = append(o.Items, OrderItem{MenuItemID: "item-2", NameSnapshot: "Сок", UnitPriceKopecks: 10_000, Quantity: 1})
				o.TotalKopecks = 79_900
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			order := validOrder()
			if tc.mutate != nil {
				tc.mutate(&order)
			}
			err := order.Validate()
			if tc.wantErr {
				if !errors.Is(err, orderserrors.ErrInvalidInput) {
					t.Fatalf("expected orderserrors.ErrInvalidInput, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// Проверяет, что валидация не меняет заказ.
func TestOrderValidateDoesNotMutate(t *testing.T) {
	order := validOrder()
	order.DeliveryAddress = "  " + order.DeliveryAddress + "  "
	before := order
	before.Items = append([]OrderItem(nil), order.Items...)
	if err := order.Validate(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, before) {
		t.Fatal("Validate changed the order")
	}
}
