package service

import (
	"context"
	"fmt"
	"math"
	"time"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
	"github.com/KolibriRDM/orders-service/internal/model"
)

// Меняет статус заказа при наличии прав сотрудника.
func (s *Service) UpdateOrderStatus(ctx context.Context, isStaff bool, orderID string, expectedVersion int64, nextStatus model.OrderStatus) (model.Order, error) {
	if ctx.Err() != nil {
		return model.Order{}, fmt.Errorf("update order status: %w", ctx.Err())
	}
	if !isStaff {
		return model.Order{}, fmt.Errorf("update order status: %w", orderserrors.ErrForbidden)
	}
	normalizedOrderID, err := model.NormalizeID(orderID)
	if err != nil {
		return model.Order{}, fmt.Errorf("update order status: order ID must be a UUID: %w", orderserrors.ErrInvalidInput)
	}
	if expectedVersion < 1 || expectedVersion == math.MaxInt64 {
		return model.Order{}, fmt.Errorf("update order status: expected version is out of range: %w", orderserrors.ErrInvalidInput)
	}
	if !nextStatus.IsValid() {
		return model.Order{}, fmt.Errorf("update order status: unknown status: %w", orderserrors.ErrInvalidInput)
	}
	updatedOrder, err := s.repo.UpdateStatus(ctx, normalizedOrderID, expectedVersion, nextStatus, time.Now().UTC())
	if err != nil {
		return model.Order{}, fmt.Errorf("update order status: %w", err)
	}
	return updatedOrder, nil
}
