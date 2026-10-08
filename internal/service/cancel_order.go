package service

import (
	"context"
	"fmt"
	"math"
	"time"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
	"github.com/KolibriRDM/orders-service/internal/model"
)

// Отменяет свой заказ с проверкой ожидаемой версии.
func (s *Service) CancelOrder(ctx context.Context, userID string, orderID string, expectedVersion int64) (model.Order, error) {
	if ctx.Err() != nil {
		return model.Order{}, fmt.Errorf("cancel order: %w", ctx.Err())
	}
	if expectedVersion < 1 || expectedVersion == math.MaxInt64 {
		return model.Order{}, fmt.Errorf("cancel order: expected version is out of range: %w", orderserrors.ErrInvalidInput)
	}
	order, err := s.GetOrder(ctx, userID, orderID)
	if err != nil {
		return model.Order{}, fmt.Errorf("cancel order: %w", err)
	}
	// Репозиторий атомарно проверяет версию и допустимость отмены.
	updatedOrder, err := s.repo.UpdateStatus(ctx, order.ID, expectedVersion, model.StatusCancelled, time.Now().UTC())
	if err != nil {
		return model.Order{}, fmt.Errorf("cancel order: %w", err)
	}
	return updatedOrder, nil
}
