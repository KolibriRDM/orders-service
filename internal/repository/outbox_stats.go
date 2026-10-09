package repository

import (
	"context"

	"github.com/KolibriRDM/orders-service/internal/model"
)

// Возвращает количество и возраст всех неопубликованных событий.
func (r *Repository) GetOutboxStats(ctx context.Context) (model.OutboxStats, error) {
	if ctx.Err() != nil {
		return model.OutboxStats{}, ctx.Err()
	}
	// Учитываем и отложенные повторы; пустая очередь возвращает нули.
	const query = `
  SELECT count(*),
         GREATEST(
           COALESCE(EXTRACT(EPOCH FROM (statement_timestamp() - MIN(created_at))), 0),
           0
         )::double precision
  FROM outbox_events
  WHERE published_at IS NULL
 `
	var stats model.OutboxStats
	err := r.pool.QueryRow(ctx, query).Scan(&stats.PendingEvents, &stats.OldestPendingAgeSeconds)
	if err != nil {
		return model.OutboxStats{}, databaseError(ctx, "get outbox stats", err)
	}
	return stats, nil
}
