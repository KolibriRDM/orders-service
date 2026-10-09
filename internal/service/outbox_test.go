package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
	"github.com/KolibriRDM/orders-service/internal/model"
)

// Подменяет операции outbox для проверки порядка и ошибок.
type outboxRepositoryStub struct {
	list  func(context.Context, int) ([]model.OutboxEvent, error)
	mark  func(context.Context, string, time.Time) (bool, error)
	retry func(context.Context, string, time.Time) (bool, error)
	stats func(context.Context) (model.OutboxStats, error)
}

// Передаёт чтение пачки тестовому сценарию.
func (r *outboxRepositoryStub) ListPendingEvents(ctx context.Context, limit int) ([]model.OutboxEvent, error) {
	return r.list(ctx, limit)
}

// Передаёт отметку публикации тестовому сценарию.
func (r *outboxRepositoryStub) MarkEventPublished(ctx context.Context, id string, at time.Time) (bool, error) {
	return r.mark(ctx, id, at)
}

// Передаёт назначение повтора тестовому сценарию.
func (r *outboxRepositoryStub) ScheduleEventRetry(ctx context.Context, id string, at time.Time) (bool, error) {
	return r.retry(ctx, id, at)
}

// Подменяет отправку события в Kafka.
type outboxPublisherStub func(context.Context, model.OutboxEvent) error

// Передаёт событие тестовой функции отправки.
func (f outboxPublisherStub) Publish(ctx context.Context, e model.OutboxEvent) error {
	return f(ctx, e)
}

// Создаёт разные события для проверки последовательности пачки.
func outboxFixture(n int) model.OutboxEvent {
	id := fmt.Sprintf("00000000-0000-4000-8000-%012d", n)
	return model.OutboxEvent{EventID: id, OrderID: id, Type: model.OrderCreatedEventType,
		Payload: []byte(fmt.Sprintf(`{"event_id":%q}`, id)), CreatedAt: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)}
}

// Проверяет ограничение и активность контекста операции БД.
func assertOutboxDatabaseContext(t *testing.T, ctx context.Context) {
	t.Helper()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > outboxDatabaseTimeout || ctx.Err() != nil {
		t.Fatalf("invalid database context: deadline=%v err=%v", deadline, ctx.Err())
	}
}

// Проверяет порядок отправки и отметки, передачу события и освобождение контекстов.
func TestOutboxPublishBatchSuccess(t *testing.T) {
	events := []model.OutboxEvent{outboxFixture(1), outboxFixture(2)}
	parent := context.Background()
	var calls []string
	var readCtx context.Context
	var writeContexts []context.Context
	started := time.Now()
	repo := &outboxRepositoryStub{
		list: func(ctx context.Context, limit int) ([]model.OutboxEvent, error) {
			if limit != 20 {
				t.Fatalf("limit=%d", limit)
			}
			assertOutboxDatabaseContext(t, ctx)
			readCtx = ctx
			calls = append(calls, "list")
			return events, nil
		},
		mark: func(ctx context.Context, id string, at time.Time) (bool, error) {
			assertOutboxDatabaseContext(t, ctx)
			writeContexts = append(writeContexts, ctx)
			if at.Location() != time.UTC || at.Before(started) || at.After(time.Now()) {
				t.Fatalf("published at=%v", at)
			}
			calls = append(calls, "mark:"+id)
			return true, nil
		},
	}
	index := 0
	publisher := outboxPublisherStub(func(ctx context.Context, event model.OutboxEvent) error {
		if ctx != parent || ctx.Err() != nil || readCtx.Err() != context.Canceled {
			t.Fatal("publisher received wrong context or read context leaked")
		}
		if index > 0 && writeContexts[index-1].Err() != context.Canceled {
			t.Fatal("previous write context leaked")
		}
		if !reflect.DeepEqual(event, events[index]) {
			t.Fatalf("event=%+v", event)
		}
		calls = append(calls, "publish:"+event.EventID)
		index++
		return nil
	})
	s := NewOutboxService(repo, publisher, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := s.PublishBatch(parent); err != nil {
		t.Fatal(err)
	}
	want := []string{"list", "publish:" + events[0].EventID, "mark:" + events[0].EventID, "publish:" + events[1].EventID, "mark:" + events[1].EventID}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%v want=%v", calls, want)
	}
	for _, ctx := range writeContexts {
		if ctx.Err() != context.Canceled {
			t.Fatal("write context leaked")
		}
	}
}

// Проверяет сохранение повтора и продолжение пачки после ошибки отправки.
func TestOutboxPublishBatchRetry(t *testing.T) {
	// Внутренний deadline publisher не должен отменять действующий родительский контекст.
	for _, cause := range []error{errors.New("Kafka unavailable"), context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			events := []model.OutboxEvent{outboxFixture(1), outboxFixture(2)}
			events[0].Attempts = 2
			var calls []string
			var retryCtx context.Context
			var logs bytes.Buffer
			started := time.Now()
			repo := &outboxRepositoryStub{
				list: func(context.Context, int) ([]model.OutboxEvent, error) { return events, nil },
				retry: func(ctx context.Context, id string, at time.Time) (bool, error) {
					assertOutboxDatabaseContext(t, ctx)
					retryCtx = ctx
					if id != events[0].EventID || at.Location() != time.UTC || at.Before(started.Add(2*time.Second)) || at.After(time.Now().Add(4*time.Second)) {
						t.Fatalf("retry id=%s at=%v", id, at)
					}
					calls = append(calls, "retry:"+id)
					return true, nil
				},
				mark: func(_ context.Context, id string, _ time.Time) (bool, error) {
					calls = append(calls, "mark:"+id)
					return true, nil
				},
			}
			publisher := outboxPublisherStub(func(_ context.Context, e model.OutboxEvent) error {
				calls = append(calls, "publish:"+e.EventID)
				if e.EventID == events[0].EventID {
					return fmt.Errorf("publish: %w: %w", orderserrors.ErrPublishFailed, cause)
				}
				return nil
			})
			s := NewOutboxService(repo, publisher, nil, slog.New(slog.NewJSONHandler(&logs, nil)))
			if err := s.PublishBatch(context.Background()); err != nil {
				t.Fatal(err)
			}
			want := []string{"publish:" + events[0].EventID, "retry:" + events[0].EventID, "publish:" + events[1].EventID, "mark:" + events[1].EventID}
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("calls=%v", calls)
			}
			if retryCtx.Err() != context.Canceled {
				t.Fatal("retry context leaked")
			}
			for _, fragment := range []string{"retry scheduled", `"attempts":3`, events[0].EventID, cause.Error()} {
				if !strings.Contains(logs.String(), fragment) {
					t.Fatalf("missing %q in log=%s", fragment, logs.String())
				}
			}
		})
	}
}

// Проверяет остановку пачки и сохранение причины ошибки БД.
func TestOutboxPublishBatchDatabaseErrors(t *testing.T) {
	for _, operation := range []string{"list", "mark", "retry"} {
		t.Run(operation, func(t *testing.T) {
			cause := fmt.Errorf("database operation: %w", orderserrors.ErrDatabaseUnavailable)
			events := []model.OutboxEvent{outboxFixture(1), outboxFixture(2)}
			var databaseCtx context.Context
			published := 0
			repo := &outboxRepositoryStub{
				list: func(ctx context.Context, _ int) ([]model.OutboxEvent, error) {
					databaseCtx = ctx
					if operation == "list" {
						return events, cause
					}
					return events, nil
				},
				mark:  func(ctx context.Context, _ string, _ time.Time) (bool, error) { databaseCtx = ctx; return false, cause },
				retry: func(ctx context.Context, _ string, _ time.Time) (bool, error) { databaseCtx = ctx; return false, cause },
			}
			publisher := outboxPublisherStub(func(context.Context, model.OutboxEvent) error {
				published++
				if operation == "retry" {
					return orderserrors.ErrPublishFailed
				}
				return nil
			})
			s := NewOutboxService(repo, publisher, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
			err := s.PublishBatch(context.Background())
			if !errors.Is(err, cause) || !errors.Is(err, orderserrors.ErrDatabaseUnavailable) {
				t.Fatalf("err=%v", err)
			}
			want := 1
			if operation == "list" {
				want = 0
			}
			if published != want {
				t.Fatalf("published=%d want=%d", published, want)
			}
			if databaseCtx.Err() != context.Canceled {
				t.Fatal("database context leaked")
			}
		})
	}
}

// Проверяет пустую пачку и отсутствие повторной записи уже обработанного события.
func TestOutboxPublishBatchEmptyAndUnchanged(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		repo := &outboxRepositoryStub{list: func(context.Context, int) ([]model.OutboxEvent, error) { return nil, nil }}
		if err := NewOutboxService(repo, nil, nil, nil).PublishBatch(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
	for _, operation := range []string{"mark_published", "schedule_retry"} {
		t.Run(operation, func(t *testing.T) {
			event := outboxFixture(1)
			var logs bytes.Buffer
			writes := 0
			unchanged := func(context.Context, string, time.Time) (bool, error) { writes++; return false, nil }
			repo := &outboxRepositoryStub{
				list: func(context.Context, int) ([]model.OutboxEvent, error) { return []model.OutboxEvent{event}, nil },
				mark: unchanged, retry: unchanged,
			}
			publisher := outboxPublisherStub(func(context.Context, model.OutboxEvent) error {
				if operation == "schedule_retry" {
					return orderserrors.ErrPublishFailed
				}
				return nil
			})
			s := NewOutboxService(repo, publisher, nil, slog.New(slog.NewJSONHandler(&logs, nil)))
			if err := s.PublishBatch(context.Background()); err != nil {
				t.Fatal(err)
			}
			if writes != 1 || !strings.Contains(logs.String(), operation) || !strings.Contains(logs.String(), "was not updated") || strings.Contains(logs.String(), "retry scheduled") {
				t.Fatalf("writes=%d log=%s", writes, logs.String())
			}
		})
	}
}

// Проверяет отмену до чтения, во время отправки и между событиями.
func TestOutboxPublishBatchCancellation(t *testing.T) {
	t.Run("before read", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := (&OutboxService{}).PublishBatch(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	})
	for _, mode := range []string{"after read", "during successful publish", "during failed publish", "after mark"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			events := []model.OutboxEvent{outboxFixture(1), outboxFixture(2)}
			publishes, marks := 0, 0
			repo := &outboxRepositoryStub{
				list: func(context.Context, int) ([]model.OutboxEvent, error) {
					if mode == "after read" {
						cancel()
					}
					return events, nil
				},
				mark: func(context.Context, string, time.Time) (bool, error) { marks++; cancel(); return true, nil },
			}
			publisher := outboxPublisherStub(func(context.Context, model.OutboxEvent) error {
				publishes++
				if mode != "after mark" {
					cancel()
				}
				if mode == "during failed publish" {
					return orderserrors.ErrPublishFailed
				}
				return nil
			})
			s := NewOutboxService(repo, publisher, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err := s.PublishBatch(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("err=%v", err)
			}
			wantPublish, wantMark := 1, 0
			if mode == "after read" {
				wantPublish = 0
			}
			if mode == "after mark" {
				wantMark = 1
			}
			if publishes != wantPublish || marks != wantMark {
				t.Fatalf("publishes=%d marks=%d", publishes, marks)
			}
		})
	}
}

// Проверяет наследование короткого deadline при чтении и записи результата.
func TestOutboxPublishBatchParentDeadline(t *testing.T) {
	for _, operation := range []string{"list", "mark", "retry"} {
		t.Run(operation, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
			defer cancel()
			parentDeadline, _ := ctx.Deadline()
			wait := func(child context.Context) error {
				t.Helper()
				deadline, ok := child.Deadline()
				if !ok || !deadline.Equal(parentDeadline) {
					t.Fatalf("deadline=%v parent=%v", deadline, parentDeadline)
				}
				<-child.Done()
				return child.Err()
			}
			repo := &outboxRepositoryStub{
				list: func(child context.Context, _ int) ([]model.OutboxEvent, error) {
					if operation == "list" {
						return nil, wait(child)
					}
					return []model.OutboxEvent{outboxFixture(1)}, nil
				},
				mark:  func(child context.Context, _ string, _ time.Time) (bool, error) { return false, wait(child) },
				retry: func(child context.Context, _ string, _ time.Time) (bool, error) { return false, wait(child) },
			}
			publisher := outboxPublisherStub(func(context.Context, model.OutboxEvent) error {
				if operation == "retry" {
					return orderserrors.ErrPublishFailed
				}
				return nil
			})
			s := NewOutboxService(repo, publisher, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err := s.PublishBatch(ctx); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

// Проверяет диапазоны задержки и защиту сдвига от переполнения.
func TestRetryDelayBounds(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	for _, tc := range []struct {
		attempts int
		min, max time.Duration
	}{
		{-maxInt - 1, 500 * time.Millisecond, time.Second}, {-1, 500 * time.Millisecond, time.Second},
		{0, 500 * time.Millisecond, time.Second}, {1, time.Second, 2 * time.Second},
		{2, 2 * time.Second, 4 * time.Second}, {3, 4 * time.Second, 8 * time.Second},
		{4, 8 * time.Second, 16 * time.Second}, {5, 15 * time.Second, 30 * time.Second},
		{6, 15 * time.Second, 30 * time.Second}, {maxInt, 15 * time.Second, 30 * time.Second},
	} {
		t.Run(fmt.Sprint(tc.attempts), func(t *testing.T) {
			for i := 0; i < 100; i++ {
				got := retryDelay(tc.attempts)
				if got < tc.min || got > tc.max {
					t.Fatalf("delay=%v want=[%v,%v]", got, tc.min, tc.max)
				}
			}
		})
	}
}

// Передаёт чтение статистики тестовому сценарию.
func (r *outboxRepositoryStub) GetOutboxStats(ctx context.Context) (model.OutboxStats, error) {
	return r.stats(ctx)
}
