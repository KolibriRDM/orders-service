package service

import (
	"context"
	"fmt"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
	"github.com/KolibriRDM/orders-service/internal/model"
)

// Возвращает свой заказ, скрывая чужой за ErrNotFound.
func (s *Service) GetOrder(ctx context.Context, userID string, orderID string) (model.Order, error) {
	if err := ctx.Err(); err != nil {
		return model.Order{}, fmt.Errorf("get order: %w", err)
	}
	normalizedUserID, err := model.NormalizeID(userID)
	if err != nil {
		return model.Order{}, fmt.Errorf("user ID must be a UUID: %w", orderserrors.ErrInvalidInput)
	}
	normalizedOrderID, err := model.NormalizeID(orderID)
	if err != nil {
		return model.Order{}, fmt.Errorf("order ID must be a UUID: %w", orderserrors.ErrInvalidInput)
	}
	order, err := s.repo.GetByID(ctx, normalizedOrderID)
	if ctx.Err() != nil {
		return model.Order{}, fmt.Errorf("get order: %w", ctx.Err())
	}
	if err != nil {
		return model.Order{}, fmt.Errorf("get order: %w", err)
	}
	if order.UserID != normalizedUserID {
		return model.Order{}, orderserrors.ErrNotFound
	}
	return order, nil
}
