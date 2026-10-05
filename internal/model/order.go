package model

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
)

// Снимок названия и цены блюда с заказанным количеством.
type OrderItem struct {
	MenuItemID       string
	NameSnapshot     string
	UnitPriceKopecks Money
	Quantity         int
}

// Заказ пользователя с позициями, стоимостью, статусом и версией.
type Order struct {
	ID              string
	UserID          string
	RestaurantID    string
	Items           []OrderItem
	Status          OrderStatus
	Version         int64
	TotalKopecks    Money
	DeliveryAddress string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Проверяет поля заказа, уникальность позиций и итоговую сумму.
func (o Order) Validate() error {
	if strings.TrimSpace(o.ID) == "" {
		return fmt.Errorf("%w: order ID is required", orderserrors.ErrInvalidInput)
	}
	if strings.TrimSpace(o.UserID) == "" {
		return fmt.Errorf("%w: user ID is required", orderserrors.ErrInvalidInput)
	}
	if strings.TrimSpace(o.RestaurantID) == "" {
		return fmt.Errorf("%w: restaurant ID is required", orderserrors.ErrInvalidInput)
	}

	address := strings.TrimSpace(o.DeliveryAddress)
	if address == "" {
		return fmt.Errorf("%w: delivery address is required", orderserrors.ErrInvalidInput)
	}
	if utf8.RuneCountInString(address) > 300 {
		return fmt.Errorf("%w: delivery address must not exceed 300 characters", orderserrors.ErrInvalidInput)
	}
	if !o.Status.IsValid() {
		return fmt.Errorf("%w: unknown order status %q", orderserrors.ErrInvalidInput, o.Status)
	}
	if o.Version < 1 {
		return fmt.Errorf("%w: version must be positive", orderserrors.ErrInvalidInput)
	}
	if o.CreatedAt.IsZero() || o.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: creation and update times are required", orderserrors.ErrInvalidInput)
	}
	if o.UpdatedAt.Before(o.CreatedAt) {
		return fmt.Errorf("%w: update time precedes creation time", orderserrors.ErrInvalidInput)
	}

	expectedTotal, err := CalculateTotal(o.Items, DeliveryFee)
	if err != nil {
		return fmt.Errorf("order total: %w", err)
	}

	seen := make(map[string]struct{}, len(o.Items))
	for i, item := range o.Items {
		if strings.TrimSpace(item.MenuItemID) == "" {
			return fmt.Errorf("%w: item %d menu item ID is required", orderserrors.ErrInvalidInput, i)
		}
		if strings.TrimSpace(item.NameSnapshot) == "" {
			return fmt.Errorf("%w: item %d name snapshot is required", orderserrors.ErrInvalidInput, i)
		}
		if _, exists := seen[item.MenuItemID]; exists {
			return fmt.Errorf("%w: duplicate menu item ID %q", orderserrors.ErrInvalidInput, item.MenuItemID)
		}
		seen[item.MenuItemID] = struct{}{}
	}
	if o.TotalKopecks != expectedTotal {
		return fmt.Errorf("%w: total is %d, expected %d", orderserrors.ErrInvalidInput, o.TotalKopecks, expectedTotal)
	}
	return nil
}
