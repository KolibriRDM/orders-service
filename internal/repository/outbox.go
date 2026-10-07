package repository

import (
	"context"
	"fmt"
	"time"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
	"github.com/KolibriRDM/orders-service/internal/model"
)

// Читает события, для которых наступило время отправки.
func (r *Repository) ListPendingEvents(ctx context.Context, limit int) ([]model.OutboxEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 100 {
		return nil, fmt.Errorf("list pending events: limit must be between 1 and 100: %w", orderserrors.ErrInvalidInput)
	}
	const query = `
		SELECT event_id::text, order_id::text, type, payload, created_at, attempts
		FROM outbox_events
		WHERE published_at IS NULL
		AND next_attempt_at <= now()
		ORDER BY next_attempt_at, created_at, event_id
		LIMIT $1
	`
	rows, err := r.pool.Query(ctx, query, limit)
	if err != nil {
		return nil, databaseError(ctx, "list pending events", err)
	}
	defer rows.Close()
	events := make([]model.OutboxEvent, 0, limit)
	for rows.Next() {
		var event model.OutboxEvent
		err := rows.Scan(&event.EventID, &event.OrderID, &event.Type, &event.Payload, &event.CreatedAt, &event.Attempts)
		if err != nil {
			return nil, databaseError(ctx, "scan pending event", err)
		}
		event.CreatedAt = event.CreatedAt.UTC()
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, databaseError(ctx, "read pending events", err)
	}
	return events, nil
}

// Отмечает событие опубликованным после подтверждения Kafka.
func (r *Repository) MarkEventPublished(ctx context.Context, eventID string, publishedAt time.Time) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if publishedAt.IsZero() {
		return false, fmt.Errorf("mark event published: published_at must not be zero: %w", orderserrors.ErrInvalidInput)
	}
	const query = `
		UPDATE outbox_events
		SET published_at = $2
		WHERE event_id = $1::uuid AND published_at IS NULL
	`
	tag, err := r.pool.Exec(ctx, query, eventID, publishedAt.UTC())
	if err != nil {
		return false, databaseError(ctx, "mark event published", err)
	}
	return tag.RowsAffected() == 1, nil
}

// Увеличивает число неудачных попыток и откладывает отправку.
func (r *Repository) ScheduleEventRetry(ctx context.Context, eventID string, nextAttemptAt time.Time) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if nextAttemptAt.IsZero() {
		return false, fmt.Errorf("schedule event retry: next_attempt_at must not be zero: %w", orderserrors.ErrInvalidInput)
	}
	const query = `
		UPDATE outbox_events
		SET attempts = attempts + 1, next_attempt_at = $2
		WHERE event_id = $1::uuid AND published_at IS NULL
	`
	// Счётчик увеличивается атомарно на стороне PostgreSQL.
	tag, err := r.pool.Exec(ctx, query, eventID, nextAttemptAt.UTC())
	if err != nil {
		return false, databaseError(ctx, "schedule event retry", err)
	}
	return tag.RowsAffected() == 1, nil
}
