package repository

import (
	"context"
	"errors"
	"fmt"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
	"github.com/KolibriRDM/orders-service/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SQL-репозиторий с пулом соединений PostgreSQL.
type Repository struct {
	pool *pgxpool.Pool
}

// Создаёт SQL-репозиторий на готовом пуле соединений.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// Читает заказ вместе со снимками его позиций.
func (r *Repository) GetByID(ctx context.Context, id string) (model.Order, error) {
	if err := ctx.Err(); err != nil {
		return model.Order{}, err
	}
	const query = `SELECT id::text, user_id::text, restaurant_id::text, status, version,
 total_kopecks, delivery_address, created_at, updated_at
 FROM orders WHERE id = $1::uuid`
	var order model.Order
	var status string
	var total int64
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&order.ID, &order.UserID, &order.RestaurantID, &status, &order.Version,
		&total, &order.DeliveryAddress, &order.CreatedAt, &order.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Order{}, orderserrors.ErrNotFound
	}
	if err != nil {
		return model.Order{}, databaseError(ctx, "get order", err)
	}
	order.Status = model.OrderStatus(status)
	order.TotalKopecks = model.Money(total)
	order.CreatedAt = order.CreatedAt.UTC()
	order.UpdatedAt = order.UpdatedAt.UTC()

	const itemsQuery = `SELECT menu_item_id::text, name_snapshot, unit_price_kopecks, quantity
 FROM order_items WHERE order_id = $1::uuid ORDER BY menu_item_id`
	rows, err := r.pool.Query(ctx, itemsQuery, id)
	if err != nil {
		return model.Order{}, databaseError(ctx, "get order items", err)
	}
	defer rows.Close()
	order.Items = make([]model.OrderItem, 0)
	for rows.Next() {
		var item model.OrderItem
		var price int64
		if err := rows.Scan(&item.MenuItemID, &item.NameSnapshot, &price, &item.Quantity); err != nil {
			return model.Order{}, databaseError(ctx, "scan order item", err)
		}
		item.UnitPriceKopecks = model.Money(price)
		order.Items = append(order.Items, item)
	}
	if err := rows.Err(); err != nil {
		return model.Order{}, databaseError(ctx, "read order items", err)
	}
	return order, nil
}

// Находит заказ и хеш запроса по пользователю и ключу.
func (r *Repository) GetByIdempotencyKey(ctx context.Context, userID string, key string) (model.IdempotentOrder, error) {
	if err := ctx.Err(); err != nil {
		return model.IdempotentOrder{}, err
	}
	const query = `SELECT order_id::text, request_hash FROM idempotency_keys WHERE user_id = $1::uuid AND key = $2`
	var orderID string
	var requestHash string
	err := r.pool.QueryRow(ctx, query, userID, key).Scan(&orderID, &requestHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.IdempotentOrder{}, orderserrors.ErrNotFound
	}
	if err != nil {
		return model.IdempotentOrder{}, databaseError(ctx, "get idempotency key", err)
	}

	order, err := r.GetByID(ctx, orderID)
	if err != nil {
		return model.IdempotentOrder{}, fmt.Errorf("get by id error: %w", err)
	}
	return model.IdempotentOrder{Order: order, RequestHash: requestHash}, nil
}
