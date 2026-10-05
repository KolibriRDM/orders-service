package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
	"github.com/KolibriRDM/orders-service/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Записывает основные поля заказа в переданную транзакцию.
func insertOrder(ctx context.Context, tx pgx.Tx, order model.Order) error {
	const query = `
	INSERT INTO orders 
	(id, user_id, restaurant_id, status, version, total_kopecks, delivery_address, created_at, updated_at)
	VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7, $8, $9);
	`
	_, err := tx.Exec(ctx, query, order.ID, order.UserID, order.RestaurantID, order.Status, order.Version, order.TotalKopecks, order.DeliveryAddress, order.CreatedAt, order.UpdatedAt)
	if err != nil {
		return databaseError(ctx, "insert order", err)
	}
	return nil
}

// Записывает снимки всех позиций в переданную транзакцию.
func insertOrderItems(ctx context.Context, tx pgx.Tx, order model.Order) error {
	const query = `
	INSERT INTO order_items
	(order_id, menu_item_id, name_snapshot, unit_price_kopecks, quantity)
	VALUES ($1::uuid, $2::uuid, $3, $4, $5);	
	`
	for _, item := range order.Items {
		_, err := tx.Exec(ctx, query, order.ID, item.MenuItemID, item.NameSnapshot, item.UnitPriceKopecks, item.Quantity)
		if err != nil {
			return databaseError(ctx, "insert order item", err)
		}
	}
	return nil
}

// Откатывает транзакцию с отдельным ограниченным контекстом.
func rollbackTransaction(tx pgx.Tx) {
	// Откат должен работать даже после отмены исходного запроса.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}

// Атомарно записывает заказ, позиции, ключ и событие.
func (r *Repository) createOrderTransaction(ctx context.Context, params model.CreateOrderParams) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return databaseError(ctx, "begin create order", err)
	}
	// После успешного Commit повторный Rollback безопасен.
	defer rollbackTransaction(tx)
	if err := insertOrder(ctx, tx, params.Order); err != nil {
		return err
	}

	const idempotencyQuery = `
		INSERT INTO idempotency_keys 
		(user_id, key, request_hash, order_id, created_at)
		VALUES ($1::uuid, $2, $3, $4::uuid, $5);
	`
	_, err = tx.Exec(ctx, idempotencyQuery, params.Order.UserID, params.IdempotencyKey, params.RequestHash, params.Order.ID, params.Order.CreatedAt)
	if err != nil {
		return databaseError(ctx, "insert idempotency key", err)
	}
	if err := insertOrderItems(ctx, tx, params.Order); err != nil {
		return err
	}

	const outboxQuery = `
		INSERT INTO outbox_events
		(event_id, order_id, type, payload, created_at)
		VALUES ($1::uuid, $2::uuid, $3, $4::jsonb, $5)
	`
	_, err = tx.Exec(ctx, outboxQuery, params.Event.EventID, params.Event.OrderID, params.Event.Type, string(params.Event.Payload), params.Event.CreatedAt)
	if err != nil {
		return databaseError(ctx, "insert order event", err)
	}
	err = tx.Commit(ctx)
	if err != nil {
		return databaseError(ctx, "commit create order", err)
	}
	return nil
}

// Сохраняет заказ либо возвращает результат повтора по ключу.
func (r *Repository) CreateIdempotent(ctx context.Context, params model.CreateOrderParams) (model.CreateOrderResult, error) {
	if err := ctx.Err(); err != nil {
		return model.CreateOrderResult{}, fmt.Errorf("create idempotent: %w", err)
	}
	if params.Event.OrderID != params.Order.ID {
		return model.CreateOrderResult{}, fmt.Errorf("event order ID does not match order: %w", orderserrors.ErrInvalidInput)
	}
	if err := r.createOrderTransaction(ctx, params); err != nil {
		if err := ctx.Err(); err != nil {
			return model.CreateOrderResult{}, fmt.Errorf("create idempotent: %w", err)
		}
		var pgErr *pgconn.PgError
		// SQLSTATE 23505 означает нарушение уникальности.
		if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
			return model.CreateOrderResult{}, err
		}
		if pgErr.ConstraintName != "idempotency_keys_pkey" && pgErr.ConstraintName != "orders_pkey" {
			return model.CreateOrderResult{}, err
		}
		// Транзакция уже откатилась и освободила соединение для чтения.
		existing, lookupErr := r.GetByIdempotencyKey(ctx, params.Order.UserID, params.IdempotencyKey)
		if errors.Is(lookupErr, orderserrors.ErrNotFound) {
			return model.CreateOrderResult{}, err
		}
		if lookupErr != nil {
			return model.CreateOrderResult{}, lookupErr
		}
		if existing.RequestHash != params.RequestHash {
			return model.CreateOrderResult{}, orderserrors.ErrIdempotencyConflict
		}
		return model.CreateOrderResult{Order: existing.Order, Created: false}, nil
	}
	return model.CreateOrderResult{Order: params.Order, Created: true}, nil
}
