package service

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/KolibriRDM/orders-service/internal/model"
)

const (
	outboxBatchSize       = 20
	outboxDatabaseTimeout = 2 * time.Second
)

// Контракт чтения событий и сохранения результата публикации.
type OutboxRepository interface {
	ListPendingEvents(ctx context.Context, limit int) ([]model.OutboxEvent, error)
	MarkEventPublished(ctx context.Context, eventID string, publishedAt time.Time) (bool, error)
	ScheduleEventRetry(ctx context.Context, eventID string, nextAttemptAt time.Time) (bool, error)
	GetOutboxStats(ctx context.Context) (model.OutboxStats, error)
}

// Контракт обновления показателей очереди outbox.
type OutboxMetrics interface {
	SetOutboxStats(pending int64, oldestAgeSeconds float64)
	SetOutboxStatsAvailable(available bool)
}

// Контракт отправки события с ожиданием подтверждения.
type EventPublisher interface {
	Publish(ctx context.Context, event model.OutboxEvent) error
}

// Координирует отправку событий outbox и обновление их состояния.
type OutboxService struct {
	repo      OutboxRepository
	publisher EventPublisher
	logger    *slog.Logger
	metrics   OutboxMetrics
}

// Связывает outbox с репозиторием, отправителем и логированием.
func NewOutboxService(repo OutboxRepository, publisher EventPublisher, metrics OutboxMetrics, logger *slog.Logger) *OutboxService {
	return &OutboxService{
		repo:      repo,
		publisher: publisher,
		logger:    logger,
		metrics:   metrics,
	}
}

// Рассчитывает растущую задержку со случайным разбросом до 30 секунд.
func retryDelay(attempts int) time.Duration {
	if attempts < 0 {
		attempts = 0
	}
	if attempts > 5 {
		attempts = 5
	}
	// Сдвиг удваивает задержку на каждую неудачную попытку.
	base := time.Second << uint(attempts)
	if base > 30*time.Second {
		base = 30 * time.Second
	}
	half := base / 2
	// Разброс уменьшает одновременные повторные обращения.
	return half + time.Duration(rand.Int64N(int64(half)+1))
}

// Публикует событие и фиксирует успех либо следующую попытку.
func (s *OutboxService) publishEvent(ctx context.Context, event model.OutboxEvent) error {
	err := ctx.Err()
	if err != nil {
		return err
	}
	publishErr := s.publisher.Publish(ctx, event)
	err = ctx.Err()
	if err != nil {
		return err
	}
	dbCtx, cancel := context.WithTimeout(ctx, outboxDatabaseTimeout)
	defer cancel()
	if publishErr != nil {
		nextAttemptAt := time.Now().UTC().Add(retryDelay(event.Attempts))
		updated, err := s.repo.ScheduleEventRetry(dbCtx, event.EventID, nextAttemptAt)
		if err != nil {
			return fmt.Errorf("schedule outbox retry: %w", err)
		}
		if updated == false {
			s.logger.Warn("outbox event was not updated",
				"event_id", event.EventID,
				"order_id", event.OrderID,
				"operation", "schedule_retry",
			)
			return nil
		}
		s.logger.Warn("outbox publication failed; retry scheduled",
			"event_id", event.EventID,
			"order_id", event.OrderID,
			"attempts", event.Attempts+1,
			"next_attempt_at", nextAttemptAt,
			"error", publishErr,
		)
		return nil
	}
	updated, err := s.repo.MarkEventPublished(dbCtx, event.EventID, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("mark outbox event published: %w", err)
	}
	if updated == false {
		s.logger.Warn("outbox event was not updated",
			"event_id", event.EventID,
			"order_id", event.OrderID,
			"operation", "mark_published",
		)
		return nil
	}
	s.logger.InfoContext(ctx, "outbox event published", "event_id", event.EventID, "order_id", event.OrderID)
	return nil
}

// Последовательно обрабатывает ограниченную пачку готовых событий.
func (s *OutboxService) PublishBatch(ctx context.Context) error {
	err := ctx.Err()
	if err != nil {
		return err
	}
	dbCtx, cancel := context.WithTimeout(ctx, outboxDatabaseTimeout)
	events, err := s.repo.ListPendingEvents(dbCtx, outboxBatchSize)
	cancel()
	if err != nil {
		return fmt.Errorf("list pending outbox events: %w", err)
	}
	for _, event := range events {
		err := s.publishEvent(ctx, event)
		if err != nil {
			return fmt.Errorf("publish outbox batch: event_id=%s order_id=%s: %w", event.EventID, event.OrderID, err)
		}
	}
	return nil
}

const outboxPollInterval = time.Second

// Обрабатывает outbox до отмены контекста с паузой между пачками.
func (s *OutboxService) Run(ctx context.Context) {
	err := ctx.Err()
	if err != nil {
		return
	}
	s.logger.Info("outbox relay started")
	defer s.logger.Info("outbox relay stopped")
	// Нулевой таймер запускает первую пачку сразу.
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			err := s.PublishBatch(ctx)
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				s.logger.Error("outbox batch failed", "error", err)
			}
			// Следующий проход начинается после паузы, без наложения пачек.
			timer.Reset(outboxPollInterval)
		}
	}
}
