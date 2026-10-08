package repository

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
	"github.com/KolibriRDM/orders-service/internal/model"
	"github.com/jackc/pgx/v5"
)

// Меняет статус при совпадении версии и допустимом переходе.
func (r *Repository) UpdateStatus(
	ctx context.Context,
	id string,
	expectedVersion int64,
	nextStatus model.OrderStatus,
	updatedAt time.Time,
) (model.Order, error) {
	if ctx.Err() != nil {
		return model.Order{}, ctx.Err()
	}
	normalizedID, err := model.NormalizeID(id)
	if err != nil {
		return model.Order{}, fmt.Errorf("update order status: order ID must be a UUID: %w", orderserrors.ErrInvalidInput)
	}
	if expectedVersion < 1 || expectedVersion == math.MaxInt64 {
		return model.Order{}, fmt.Errorf("update order status: expected version is out of range: %w", orderserrors.ErrInvalidInput)
	}
	if !nextStatus.IsValid() {
		return model.Order{}, fmt.Errorf("update order status: unknown status: %w", orderserrors.ErrInvalidInput)
	}
	if updatedAt.IsZero() {
		return model.Order{}, fmt.Errorf("update order status: update time is required: %w", orderserrors.ErrInvalidInput)
	}
	order, err := r.GetByID(ctx, normalizedID)
	if err != nil {
		return model.Order{}, fmt.Errorf("update order status: %w", err)
	}
	if order.Version != expectedVersion {
		return model.Order{}, fmt.Errorf("version conflict: %w", orderserrors.ErrVersionConflict)
	}
	if !order.Status.CanTransitionTo(nextStatus) {
		return model.Order{}, fmt.Errorf("transition conflict: %w", orderserrors.ErrInvalidTransition)
	}
	if updatedAt.Before(order.UpdatedAt) {
		return model.Order{}, fmt.Errorf("update order status: update time precedes current value: %w", orderserrors.ErrInvalidInput)
	}
	// Версия и исходный статус защищают проверенный переход от конкурентного изменения.
	const query = `
		UPDATE orders SET status = $4, version = version + 1, updated_at = $5
		WHERE id = $1::uuid AND version = $2 AND status = $3
		RETURNING version, updated_at
	`
	err = r.pool.QueryRow(
		ctx,
		query,
		normalizedID,
		expectedVersion,
		string(order.Status),
		string(nextStatus),
		updatedAt.UTC(),
	).Scan(&order.Version, &order.UpdatedAt)
	if err != nil {
		if ctx.Err() != nil {
			return model.Order{}, ctx.Err()
		}
		// После чтения заказ изменился: условный UPDATE не нашёл прежнюю версию.
		if errors.Is(err, pgx.ErrNoRows) {
			return model.Order{}, orderserrors.ErrVersionConflict
		}
		return model.Order{}, databaseError(ctx, "update order status", err)
	}
	order.Status = nextStatus
	order.UpdatedAt = order.UpdatedAt.UTC()
	return order, nil
}
