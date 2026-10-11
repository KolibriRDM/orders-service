package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/KolibriRDM/orders-service/internal/model"
)

// Проверяет немедленный первый проход и повтор после ошибки с паузой.
func TestOutboxRunRetriesBatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	var logs bytes.Buffer
	calls := 0
	var completed time.Time
	repo := &outboxRepositoryStub{list: func(context.Context, int) ([]model.OutboxEvent, error) {
		calls++
		if calls == 1 {
			completed = time.Now()
			return nil, errors.New("database unavailable")
		}
		if time.Since(completed) < outboxPollInterval {
			t.Error("missing pause after failed batch")
		}
		cancel()
		return nil, nil
	}}
	s := NewOutboxService(repo, nil, nil, slog.New(slog.NewTextHandler(&logs, nil)))
	started := time.Now()
	s.Run(ctx)
	if calls != 2 || completed.Sub(started) >= outboxPollInterval {
		t.Fatalf("calls=%d first batch delay=%v", calls, completed.Sub(started))
	}
	for _, fragment := range []string{"outbox relay started", "outbox batch failed", "database unavailable", "outbox relay stopped"} {
		if !strings.Contains(logs.String(), fragment) {
			t.Fatalf("missing %q in %s", fragment, logs.String())
		}
	}
	if strings.Count(logs.String(), "outbox batch failed") != 1 {
		t.Fatalf("cancellation logged as error: %s", logs.String())
	}
}

// Проверяет отмену уже завершённого контекста без обращения к зависимостям.
func TestOutboxRunAlreadyCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	(&OutboxService{}).Run(ctx)
}

// Проверяет отсутствие наложения пачек и остановку текущей операции.
func TestOutboxRunCancellationDuringBatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	started := make(chan struct{})
	done := make(chan struct{})
	calls := 0
	repo := &outboxRepositoryStub{list: func(ctx context.Context, _ int) ([]model.OutboxEvent, error) {
		calls++
		if calls == 1 {
			close(started)
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	s := NewOutboxService(repo, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	go func() { s.Run(ctx); close(done) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("relay did not start")
	}
	// Первый запрос остаётся активным дольше интервала опроса.
	time.Sleep(outboxPollInterval + 50*time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("relay did not stop")
	}
	if calls != 1 {
		t.Fatalf("overlapping batches: %d", calls)
	}
}

// Проверяет прерывание ожидания между пустыми пачками.
func TestOutboxRunCancellationWhileIdle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	readCtx := make(chan context.Context, 1)
	done := make(chan struct{})
	calls := 0
	repo := &outboxRepositoryStub{list: func(ctx context.Context, _ int) ([]model.OutboxEvent, error) {
		calls++
		readCtx <- ctx
		return nil, nil
	}}
	s := NewOutboxService(repo, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	go func() { s.Run(ctx); close(done) }()
	select {
	case child := <-readCtx:
		<-child.Done()
	case <-time.After(time.Second):
		t.Fatal("relay did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("relay waited for timer instead of cancellation")
	}
	if calls != 1 {
		t.Fatalf("unexpected batch count=%d", calls)
	}
}

// Проверяет быстрый разбор полной очереди и паузу после её опустошения.
func TestOutboxRunDrainsBacklogWithoutPollingDelay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		started := time.Now()
		calls, published, marked := 0, 0, 0
		repo := &outboxRepositoryStub{
			list: func(context.Context, int) ([]model.OutboxEvent, error) {
				calls++
				switch calls {
				case 1, 2:
					if elapsed := time.Since(started); elapsed != 0 {
						t.Fatalf("delay before full batch %d: %v", calls, elapsed)
					}
					events := make([]model.OutboxEvent, outboxBatchSize)
					for i := range events {
						events[i] = outboxFixture((calls-1)*outboxBatchSize + i)
					}
					return events, nil
				case 3:
					if time.Since(started) != 0 {
						t.Fatal("delay while backlog remains")
					}
					return nil, nil
				case 4:
					if elapsed := time.Since(started); elapsed != outboxPollInterval {
						t.Fatalf("idle delay=%v", elapsed)
					}
					cancel()
					return nil, nil
				default:
					t.Fatal("relay ignored cancellation")
					return nil, nil
				}
			},
			mark: func(context.Context, string, time.Time) (bool, error) { marked++; return true, nil },
		}
		publisher := outboxPublisherStub(func(context.Context, model.OutboxEvent) error { published++; return nil })
		NewOutboxService(repo, publisher, nil, slog.New(slog.NewTextHandler(io.Discard, nil))).Run(ctx)
		if calls != 4 || published != 2*outboxBatchSize || marked != published {
			t.Fatalf("calls=%d published=%d marked=%d", calls, published, marked)
		}
	})
}
