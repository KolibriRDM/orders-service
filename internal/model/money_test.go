package model

import (
	"errors"
	"math"
	"testing"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
)

// Проверяет расчёт стоимости и ограничения денег и количества.
func TestCalculateTotal(t *testing.T) {
	maxItems := make([]OrderItem, 20)
	for i := range maxItems {
		maxItems[i] = OrderItem{UnitPriceKopecks: 1_000_000, Quantity: 20}
	}
	tooManyItems := append(append([]OrderItem(nil), maxItems...), maxItems[0])

	tests := []struct {
		name     string
		items    []OrderItem
		delivery Money
		want     Money
		wantErr  bool
	}{
		{
			name: "normal order",
			items: []OrderItem{
				{UnitPriceKopecks: 25_000, Quantity: 2},
				{UnitPriceKopecks: 10_000, Quantity: 1},
			},
			delivery: DeliveryFee,
			want:     79_900,
		},
		{name: "free delivery", items: []OrderItem{{UnitPriceKopecks: 10_000, Quantity: 1}}, want: 10_000},
		{name: "maximum business bounds", items: maxItems, delivery: DeliveryFee, want: 400_019_900},
		{name: "nil items", delivery: DeliveryFee, wantErr: true},
		{name: "empty items", items: []OrderItem{}, delivery: DeliveryFee, wantErr: true},
		{name: "too many items", items: tooManyItems, delivery: DeliveryFee, wantErr: true},
		{name: "zero quantity", items: []OrderItem{{UnitPriceKopecks: 100, Quantity: 0}}, wantErr: true},
		{name: "negative quantity", items: []OrderItem{{UnitPriceKopecks: 100, Quantity: -1}}, wantErr: true},
		{name: "quantity above limit", items: []OrderItem{{UnitPriceKopecks: 100, Quantity: 21}}, wantErr: true},
		{name: "zero price", items: []OrderItem{{UnitPriceKopecks: 0, Quantity: 1}}, wantErr: true},
		{name: "negative price", items: []OrderItem{{UnitPriceKopecks: -1, Quantity: 1}}, wantErr: true},
		{name: "price above limit", items: []OrderItem{{UnitPriceKopecks: MaxUnitPrice + 1, Quantity: 1}}, wantErr: true},
		{name: "negative delivery", items: []OrderItem{{UnitPriceKopecks: 100, Quantity: 1}}, delivery: -1, wantErr: true},
		{name: "exact int64 boundary", items: []OrderItem{{UnitPriceKopecks: 1, Quantity: 1}}, delivery: Money(math.MaxInt64) - 1, want: Money(math.MaxInt64)},
		{name: "overflow on first item", items: []OrderItem{{UnitPriceKopecks: 1, Quantity: 1}}, delivery: Money(math.MaxInt64), wantErr: true},
		{
			name: "overflow on later item",
			items: []OrderItem{
				{UnitPriceKopecks: 1, Quantity: 1},
				{UnitPriceKopecks: 1, Quantity: 1},
			},
			delivery: Money(math.MaxInt64) - 1,
			wantErr:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CalculateTotal(tc.items, tc.delivery)
			if tc.wantErr {
				if !errors.Is(err, orderserrors.ErrInvalidInput) {
					t.Fatalf("expected orderserrors.ErrInvalidInput, got %v", err)
				}
				if got != 0 {
					t.Fatalf("expected zero result on error, got %d", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("total = %d, want %d", got, tc.want)
			}
		})
	}
}
