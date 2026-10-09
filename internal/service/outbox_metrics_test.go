package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/KolibriRDM/orders-service/internal/model"
)

// Запоминает последнее измерение и количество обновлений.
type outboxMetricsStub struct {
	mu                           sync.Mutex
	stats                        model.OutboxStats
	available                    bool
	updates, availabilityUpdates int
}

// Сохраняет размер и возраст очереди.
func (m *outboxMetricsStub) SetOutboxStats(pending int64, age float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stats = model.OutboxStats{PendingEvents: pending, OldestPendingAgeSeconds: age}
	m.updates++
}

// Сохраняет доступность последнего измерения.
func (m *outboxMetricsStub) SetOutboxStatsAvailable(available bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.available = available
	m.availabilityUpdates++
}

// Проверяет сохранение старого измерения при ошибке и восстановление после неё.
func TestOutboxRefreshMetrics(t *testing.T) {
	var logs bytes.Buffer
	m := &outboxMetricsStub{}
	want := model.OutboxStats{PendingEvents: 7, OldestPendingAgeSeconds: 12.5}
	var cause error
	var dbCtx context.Context
	repo := &outboxRepositoryStub{stats: func(ctx context.Context) (model.OutboxStats, error) {
		assertOutboxDatabaseContext(t, ctx)
		dbCtx = ctx
		return want, cause
	}}
	s := NewOutboxService(repo, nil, m, slog.New(slog.NewJSONHandler(&logs, nil)))
	s.refreshMetrics(context.Background())
	if m.stats != want || !m.available || m.updates != 1 || dbCtx.Err() != context.Canceled {
		t.Fatalf("metrics=%+v context=%v", m, dbCtx.Err())
	}
	cause = errors.New("database unavailable")
	s.refreshMetrics(context.Background())
	if m.stats != want || m.available || m.updates != 1 {
		t.Fatalf("failed query changed stats: %+v", m)
	}
	if !strings.Contains(logs.String(), "outbox statistics query failed") {
		t.Fatalf("logs=%s", &logs)
	}
	cause = nil
	want = model.OutboxStats{}
	s.refreshMetrics(context.Background())
	if m.stats != want || !m.available || m.updates != 2 || m.availabilityUpdates != 3 {
		t.Fatalf("recovery=%+v", m)
	}
}

// Проверяет двухсекундный дедлайн запроса без отмены родительского контекста.
func TestOutboxMetricsDatabaseTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := &outboxMetricsStub{stats: model.OutboxStats{PendingEvents: 3}, available: true}
		repo := &outboxRepositoryStub{stats: func(ctx context.Context) (model.OutboxStats, error) {
			<-ctx.Done()
			return model.OutboxStats{}, ctx.Err()
		}}
		s := NewOutboxService(repo, nil, m, slog.New(slog.NewTextHandler(io.Discard, nil)))
		started := time.Now()
		s.refreshMetrics(context.Background())
		if time.Since(started) != 2*time.Second || m.available || m.stats.PendingEvents != 3 || m.updates != 0 {
			t.Fatalf("elapsed=%v metrics=%+v", time.Since(started), m)
		}
	})
}

// Не меняет метрики и не пишет предупреждения при штатной отмене.
func TestOutboxMetricsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	(&OutboxService{}).refreshMetrics(ctx)
	(&OutboxService{}).RunMetrics(ctx)
	for _, cause := range []error{nil, context.Canceled} {
		var logs bytes.Buffer
		ctx, cancel := context.WithCancel(context.Background())
		m := &outboxMetricsStub{available: true, stats: model.OutboxStats{PendingEvents: 3}}
		repo := &outboxRepositoryStub{stats: func(context.Context) (model.OutboxStats, error) {
			cancel()
			return model.OutboxStats{PendingEvents: 99}, cause
		}}
		NewOutboxService(repo, nil, m, slog.New(slog.NewJSONHandler(&logs, nil))).refreshMetrics(ctx)
		cancel()
		if m.updates != 0 || m.availabilityUpdates != 0 || !m.available || logs.Len() != 0 {
			t.Fatalf("metrics=%+v logs=%s", m, &logs)
		}
	}
}

// Проверяет немедленный опрос, паузу после запроса и остановку таймера.
func TestOutboxRunMetricsPollingAndShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var calls atomic.Int32
		m := &outboxMetricsStub{}
		repo := &outboxRepositoryStub{stats: func(context.Context) (model.OutboxStats, error) {
			calls.Add(1)
			time.Sleep(time.Second)
			if calls.Load() == 1 {
				return model.OutboxStats{}, errors.New("temporary failure")
			}
			return model.OutboxStats{PendingEvents: 2}, nil
		}}
		s := NewOutboxService(repo, nil, m, slog.New(slog.NewTextHandler(io.Discard, nil)))
		done := make(chan struct{})
		go func() { defer close(done); s.RunMetrics(ctx) }()
		synctest.Wait()
		if calls.Load() != 1 {
			t.Fatalf("first call=%d", calls.Load())
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if m.snapshot().available || m.snapshot().availabilityUpdates != 1 {
			t.Fatalf("failed sample=%+v", m.snapshot())
		}
		time.Sleep(4 * time.Second)
		synctest.Wait()
		if calls.Load() != 1 {
			t.Fatal("polling pause started before query completion")
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if calls.Load() != 2 {
			t.Fatalf("second call=%d", calls.Load())
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if !m.snapshot().available || m.snapshot().stats.PendingEvents != 2 {
			t.Fatalf("recovery=%+v", m.snapshot())
		}
		cancel()
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("metrics loop did not stop")
		}
		time.Sleep(10 * time.Second)
		synctest.Wait()
		if calls.Load() != 2 {
			t.Fatalf("query after shutdown: %d", calls.Load())
		}
	})
}

// Остановка прерывает запрос статистики и ожидание подтверждения Kafka.
func TestOutboxMetricsIndependentOfBlockedPublisher(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var queries, publishes atomic.Int32
		m := &outboxMetricsStub{}
		repo := &outboxRepositoryStub{
			list: func(context.Context, int) ([]model.OutboxEvent, error) {
				return []model.OutboxEvent{outboxFixture(1)}, nil
			},
			stats: func(ctx context.Context) (model.OutboxStats, error) {
				queries.Add(1)
				if queries.Load() == 1 {
					return model.OutboxStats{PendingEvents: 1}, nil
				}
				<-ctx.Done()
				return model.OutboxStats{}, ctx.Err()
			},
		}
		publisher := outboxPublisherStub(func(ctx context.Context, _ model.OutboxEvent) error { publishes.Add(1); <-ctx.Done(); return ctx.Err() })
		s := NewOutboxService(repo, publisher, m, slog.New(slog.NewTextHandler(io.Discard, nil)))
		relayDone, metricsDone := make(chan struct{}), make(chan struct{})
		go func() { defer close(relayDone); s.Run(ctx) }()
		go func() { defer close(metricsDone); s.RunMetrics(ctx) }()
		synctest.Wait()
		if publishes.Load() != 1 || queries.Load() != 1 || !m.snapshot().available {
			t.Fatalf("publishes=%d queries=%d metrics=%+v", publishes.Load(), queries.Load(), m.snapshot())
		}
		time.Sleep(5 * time.Second)
		synctest.Wait()
		if queries.Load() != 2 {
			t.Fatal("blocked publisher stopped metrics")
		}
		cancel()
		synctest.Wait()
		select {
		case <-relayDone:
		default:
			t.Fatal("relay leaked")
		}
		select {
		case <-metricsDone:
		default:
			t.Fatal("metrics loop leaked")
		}
		if m.snapshot().availabilityUpdates != 1 || !m.snapshot().available {
			t.Fatal("shutdown changed last measurement")
		}
	})
}

// Читает согласованное состояние во время фонового обновления.
func (m *outboxMetricsStub) snapshot() *outboxMetricsStub {
	m.mu.Lock()
	defer m.mu.Unlock()
	return &outboxMetricsStub{stats: m.stats, available: m.available, updates: m.updates, availabilityUpdates: m.availabilityUpdates}
}
