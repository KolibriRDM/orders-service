package service

import (
	"context"
	"time"
)

const outboxMetricsInterval = 5 * time.Second

// Читает статистику с таймаутом и обновляет показатели очереди.
func (s *OutboxService) refreshMetrics(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	dbCtx, cancel := context.WithTimeout(ctx, outboxDatabaseTimeout)
	defer cancel()
	stats, err := s.repo.GetOutboxStats(dbCtx)
	if ctx.Err() != nil {
		return
	}
	// При ошибке сохраняем предыдущие значения и отмечаем их недоступность.
	if err != nil {
		s.metrics.SetOutboxStatsAvailable(false)
		s.logger.WarnContext(ctx, "outbox statistics query failed", "error", err)
		return
	}
	s.metrics.SetOutboxStats(stats.PendingEvents, stats.OldestPendingAgeSeconds)
	s.metrics.SetOutboxStatsAvailable(true)
}

// Обновляет статистику независимо от скорости отправки событий.
func (s *OutboxService) RunMetrics(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	// Первое измерение запускается сразу, следующие — после паузы.
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			s.refreshMetrics(ctx)
			if ctx.Err() != nil {
				return
			}
			timer.Reset(outboxMetricsInterval)
		}
	}
}
