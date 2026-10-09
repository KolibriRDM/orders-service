//go:build integration

package repository_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/KolibriRDM/orders-service/internal/model"
	"github.com/KolibriRDM/orders-service/internal/repository"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Проверяет отбор, публикацию и повторы outbox на PostgreSQL.
func testOutboxPostgres(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	repo := repository.NewRepository(pool)
	const orderID = "00000000-0000-4000-8000-000000000001"
	const missingID = "60000000-0000-4000-8000-999999999999"
	past := time.Date(2000, 1, 1, 12, 0, 0, 0, time.FixedZone("MSK", 3*60*60))
	future := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Microsecond)
	eventID := func(n int) string { return fmt.Sprintf("60000000-0000-4000-8000-%012d", n) }
	reset := func(t *testing.T) {
		t.Helper()
		if _, err := pool.Exec(ctx, `DELETE FROM outbox_events WHERE event_id::text LIKE '60000000-%'`); err != nil {
			t.Fatal(err)
		}
	}
	defer reset(t)
	seed := func(t *testing.T, n int, created, next time.Time, published *time.Time, attempts int) {
		t.Helper()
		_, err := pool.Exec(ctx, `INSERT INTO outbox_events(event_id,order_id,type,payload,created_at,next_attempt_at,published_at,attempts)
   VALUES ($1::uuid,$2::uuid,$3,$4::jsonb,$5,$6,$7,$8)`, eventID(n), orderID, model.OrderCreatedEventType,
			fmt.Sprintf(`{"event_id":%q,"order_id":%q,"total_kopecks":79900}`, eventID(n), orderID), created, next, published, attempts)
		if err != nil {
			t.Fatal(err)
		}
	}
	ids := func(events []model.OutboxEvent) []string {
		result := make([]string, 0, len(events))
		for _, event := range events {
			result = append(result, event.EventID)
		}
		return result
	}

	t.Run("statistics include delayed retries but exclude published events", func(t *testing.T) {
		reset(t)
		stats, err := repo.GetOutboxStats(ctx)
		if err != nil || stats != (model.OutboxStats{}) {
			t.Fatalf("empty stats=%+v err=%v", stats, err)
		}
		oldest := time.Now().UTC().Add(-2 * time.Minute)
		seed(t, 1, oldest, future, nil, 3)
		seed(t, 2, oldest.Add(time.Minute), past, nil, 0)
		seed(t, 3, past, past, &past, 0)
		started := time.Now()
		stats, err = repo.GetOutboxStats(ctx)
		if err != nil || stats.PendingEvents != 2 || stats.OldestPendingAgeSeconds < started.Sub(oldest).Seconds()-0.01 || stats.OldestPendingAgeSeconds > time.Since(oldest).Seconds()+0.01 {
			t.Fatalf("stats=%+v err=%v", stats, err)
		}
		if _, err := repo.MarkEventPublished(ctx, eventID(1), time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		stats, err = repo.GetOutboxStats(ctx)
		if err != nil || stats.PendingEvents != 1 || stats.OldestPendingAgeSeconds < 59 || stats.OldestPendingAgeSeconds > 61 {
			t.Fatalf("remaining stats=%+v err=%v", stats, err)
		}
		reset(t)
		seed(t, 1, future, future, nil, 0)
		stats, err = repo.GetOutboxStats(ctx)
		if err != nil || stats.PendingEvents != 1 || stats.OldestPendingAgeSeconds != 0 {
			t.Fatalf("future stats=%+v err=%v", stats, err)
		}
		if _, err := repo.MarkEventPublished(ctx, eventID(1), time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		stats, err = repo.GetOutboxStats(ctx)
		if err != nil || stats != (model.OutboxStats{}) {
			t.Fatalf("published stats=%+v err=%v", stats, err)
		}
	})
	t.Run("filters orders limits and maps fields", func(t *testing.T) {
		reset(t)
		// Вставляем в обратном порядке, чтобы проверить сортировку SQL.
		seed(t, 4, past.Add(time.Minute), past, nil, 0)
		seed(t, 3, past, past, nil, 2)
		seed(t, 2, past, past, nil, 0)
		seed(t, 1, past.Add(time.Hour), past.Add(-time.Minute), nil, 0)
		seed(t, 5, past, future, nil, 0)
		seed(t, 6, past, past, &past, 0)
		for _, limit := range []int{1, 2, 100} {
			events, err := repo.ListPendingEvents(ctx, limit)
			want := []string{eventID(1), eventID(2), eventID(3), eventID(4)}
			if limit < len(want) {
				want = want[:limit]
			}
			if err != nil || !reflect.DeepEqual(ids(events), want) {
				t.Fatalf("limit=%d ids=%v err=%v want=%v", limit, ids(events), err, want)
			}
			for _, event := range events {
				expectedTime := past
				expectedAttempts := 0
				switch event.EventID {
				case eventID(1):
					expectedTime = past.Add(time.Hour)
				case eventID(3):
					expectedAttempts = 2
				case eventID(4):
					expectedTime = past.Add(time.Minute)
				}
				if event.OrderID != orderID || event.Type != model.OrderCreatedEventType || event.Attempts != expectedAttempts || !event.CreatedAt.Equal(expectedTime) || event.CreatedAt.Location() != time.UTC {
					t.Fatalf("invalid event: %+v", event)
				}
				var payload map[string]any
				if err := json.Unmarshal(event.Payload, &payload); err != nil || payload["event_id"] != event.EventID || payload["order_id"] != orderID || payload["total_kopecks"] != float64(79900) {
					t.Fatalf("payload=%s err=%v", event.Payload, err)
				}
			}
		}
	})
	t.Run("empty result is nonnil", func(t *testing.T) {
		reset(t)
		seed(t, 1, past, future, nil, 0)
		seed(t, 2, past, past, &past, 0)
		events, err := repo.ListPendingEvents(ctx, 100)
		if err != nil || events == nil || len(events) != 0 {
			t.Fatalf("events=%v err=%v", events, err)
		}
	})
	t.Run("retry preserves payload and publication is idempotent", func(t *testing.T) {
		reset(t)
		seed(t, 1, past, past, nil, 0)
		original, err := repo.ListPendingEvents(ctx, 1)
		if err != nil || len(original) != 1 {
			t.Fatalf("events=%v err=%v", original, err)
		}
		changed, err := repo.ScheduleEventRetry(ctx, eventID(1), future)
		if err != nil || !changed {
			t.Fatalf("changed=%v err=%v", changed, err)
		}
		events, err := repo.ListPendingEvents(ctx, 100)
		if err != nil || len(events) != 0 {
			t.Fatalf("events=%v err=%v", events, err)
		}
		var attempts int
		var next time.Time
		var unpublished bool
		if err := pool.QueryRow(ctx, `SELECT attempts,next_attempt_at,published_at IS NULL FROM outbox_events WHERE event_id=$1::uuid`, eventID(1)).Scan(&attempts, &next, &unpublished); err != nil || attempts != 1 || !next.Equal(future) || !unpublished {
			t.Fatalf("attempts=%d next=%v unpublished=%v err=%v", attempts, next, unpublished, err)
		}
		changed, err = repo.ScheduleEventRetry(ctx, eventID(1), past)
		if err != nil || !changed {
			t.Fatalf("changed=%v err=%v", changed, err)
		}
		events, err = repo.ListPendingEvents(ctx, 1)
		expected := original[0]
		expected.Attempts = 2
		if err != nil || len(events) != 1 || !reflect.DeepEqual(events[0], expected) {
			t.Fatalf("events=%v err=%v", events, err)
		}
		publishedAt := past.Add(2 * time.Hour)
		changed, err = repo.MarkEventPublished(ctx, eventID(1), publishedAt)
		if err != nil || !changed {
			t.Fatalf("changed=%v err=%v", changed, err)
		}
		changed, err = repo.MarkEventPublished(ctx, eventID(1), future)
		if err != nil || changed {
			t.Fatalf("repeat changed=%v err=%v", changed, err)
		}
		changed, err = repo.ScheduleEventRetry(ctx, eventID(1), future)
		if err != nil || changed {
			t.Fatalf("published retry changed=%v err=%v", changed, err)
		}
		var published time.Time
		var payload []byte
		if err := pool.QueryRow(ctx, `SELECT published_at,attempts,next_attempt_at,payload FROM outbox_events WHERE event_id=$1::uuid`, eventID(1)).Scan(&published, &attempts, &next, &payload); err != nil || !published.Equal(publishedAt) || attempts != 2 || !next.Equal(past) || string(payload) != string(original[0].Payload) {
			t.Fatalf("published=%v attempts=%d next=%v payload=%s err=%v", published, attempts, next, payload, err)
		}
		events, err = repo.ListPendingEvents(ctx, 100)
		if err != nil || len(events) != 0 {
			t.Fatalf("events=%v err=%v", events, err)
		}
	})
	for name, call := range map[string]func(context.Context, string, time.Time) (bool, error){"publish": repo.MarkEventPublished, "retry": repo.ScheduleEventRetry} {
		t.Run(name+" missing and invalid UUID", func(t *testing.T) {
			changed, err := call(ctx, missingID, past)
			if err != nil || changed {
				t.Fatalf("changed=%v err=%v", changed, err)
			}
			changed, err = call(ctx, "invalid", past)
			var pgErr *pgconn.PgError
			if changed || !errors.As(err, &pgErr) || pgErr.Code != "22P02" {
				t.Fatalf("changed=%v err=%v", changed, err)
			}
		})
	}
	t.Run("deadline while waiting for pool", func(t *testing.T) {
		held, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer held.Release()
		calls := map[string]func(context.Context) error{
			"stats": func(ctx context.Context) error {
				stats, err := repo.GetOutboxStats(ctx)
				if stats != (model.OutboxStats{}) {
					t.Errorf("partial stats=%+v", stats)
				}
				return err
			},
			"list": func(ctx context.Context) error {
				events, err := repo.ListPendingEvents(ctx, 1)
				if events != nil {
					t.Errorf("partial events=%v", events)
				}
				return err
			},
			"publish": func(ctx context.Context) error {
				changed, err := repo.MarkEventPublished(ctx, eventID(1), past)
				if changed {
					t.Error("unexpected update")
				}
				return err
			},
			"retry": func(ctx context.Context) error {
				changed, err := repo.ScheduleEventRetry(ctx, eventID(1), past)
				if changed {
					t.Error("unexpected update")
				}
				return err
			},
		}
		for name, call := range calls {
			t.Run(name, func(t *testing.T) {
				limited, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
				defer cancel()
				if err := call(limited); !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("err=%v", err)
				}
			})
		}
	})
}
