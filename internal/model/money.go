package model

import (
	"fmt"
	"math"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
)

// Денежная сумма в целых копейках.
type Money int64

const (
	DeliveryFee  Money = 19_900
	MaxUnitPrice Money = 1_000_000
)

// Проверяет цены и количества, затем считает сумму с доставкой.
func CalculateTotal(items []OrderItem, delivery Money) (Money, error) {
	if len(items) == 0 || len(items) > 20 {
		return 0, fmt.Errorf("%w: order must contain 1 to 20 items", orderserrors.ErrInvalidInput)
	}
	if delivery < 0 {
		return 0, fmt.Errorf("%w: delivery must not be negative", orderserrors.ErrInvalidInput)
	}

	total := delivery
	for i, item := range items {
		if item.Quantity < 1 || item.Quantity > 20 {
			return 0, fmt.Errorf("%w: item %d quantity must be between 1 and 20", orderserrors.ErrInvalidInput, i)
		}
		if item.UnitPriceKopecks <= 0 || item.UnitPriceKopecks > MaxUnitPrice {
			return 0, fmt.Errorf("%w: item %d price must be between 1 and %d kopecks", orderserrors.ErrInvalidInput, i, MaxUnitPrice)
		}

		lineTotal := item.UnitPriceKopecks * Money(item.Quantity)
		// Проверка до сложения защищает итоговую сумму от переполнения.
		if lineTotal > Money(math.MaxInt64)-total {
			return 0, fmt.Errorf("%w: total amount overflows int64", orderserrors.ErrInvalidInput)
		}
		total += lineTotal
	}
	return total, nil
}
