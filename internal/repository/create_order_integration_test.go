//go:build integration

package repository_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
	"github.com/KolibriRDM/orders-service/internal/model"
	"github.com/KolibriRDM/orders-service/internal/repository"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Проверяет транзакцию, повторы и гонки создания в PostgreSQL.
func testCreateIdempotentPostgres(t *testing.T, ctx context.Context, pool *pgxpool.Pool, cfg *pgxpool.Config) {
	repo := repository.NewRepository(pool)
	sequence := 1000
	newParams := func(key string) model.CreateOrderParams {
		sequence++
		id := fmt.Sprintf("10000000-0000-4000-8000-%012d", sequence)
		now := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
		return model.CreateOrderParams{
			Order: model.Order{
				ID: id, UserID: "20000000-0000-4000-8000-000000000001", RestaurantID: "30000000-0000-4000-8000-000000000001",
				Status: model.StatusCreated, Version: 1, TotalKopecks: 79900, DeliveryAddress: "Москва, Тестовая, 10", CreatedAt: now, UpdatedAt: now,
				Items: []model.OrderItem{
					{MenuItemID: "40000000-0000-4000-8000-000000000001", NameSnapshot: "Пицца", UnitPriceKopecks: 25000, Quantity: 2},
					{MenuItemID: "40000000-0000-4000-8000-000000000002", NameSnapshot: "Морс", UnitPriceKopecks: 10000, Quantity: 1},
				},
			},
			IdempotencyKey: key, RequestHash: strings.Repeat("a", 64),
			Event: model.OutboxEvent{EventID: fmt.Sprintf("50000000-0000-4000-8000-%012d", sequence), OrderID: id, Type: model.OrderCreatedEventType,
				Payload: []byte(fmt.Sprintf(`{"order_id":%q,"total_kopecks":79900}`, id)), CreatedAt: now.Add(time.Second)},
		}
	}
	counts := func() [4]int {
		t.Helper()
		var n [4]int
		if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM orders), (SELECT count(*) FROM order_items), (SELECT count(*) FROM idempotency_keys), (SELECT count(*) FROM outbox_events)`).Scan(&n[0], &n[1], &n[2], &n[3]); err != nil {
			t.Fatal(err)
		}
		return n
	}
	checkDelta := func(before [4]int, orders int) {
		t.Helper()
		want := before
		for i, delta := range [4]int{orders, 2 * orders, orders, orders} {
			want[i] += delta
		}
		if got := counts(); got != want {
			t.Fatalf("table counts=%v want=%v", got, want)
		}
	}
	create := func(p model.CreateOrderParams) model.CreateOrderResult {
		t.Helper()
		got, err := repo.CreateIdempotent(ctx, p)
		if err != nil || !got.Created || !reflect.DeepEqual(got.Order, p.Order) {
			t.Fatalf("create got=%+v err=%v", got, err)
		}
		return got
	}
	zeroResult := func(got model.CreateOrderResult) {
		t.Helper()
		if !reflect.DeepEqual(got, model.CreateOrderResult{}) {
			t.Fatalf("partial result on error: %+v", got)
		}
	}

	t.Run("persists all fields and outbox defaults", func(t *testing.T) {
		before := counts()
		p := newParams("new-order")
		create(p)
		got, err := repo.GetByID(ctx, p.Order.ID)
		if err != nil || !reflect.DeepEqual(got, p.Order) {
			t.Fatalf("persisted order=%+v err=%v", got, err)
		}
		var orderID, hash string
		var keyTime time.Time
		err = pool.QueryRow(ctx, `SELECT order_id::text,request_hash,created_at FROM idempotency_keys WHERE user_id=$1::uuid AND key=$2`, p.Order.UserID, p.IdempotencyKey).Scan(&orderID, &hash, &keyTime)
		if err != nil || orderID != p.Order.ID || hash != p.RequestHash || !keyTime.Equal(p.Order.CreatedAt) {
			t.Fatalf("key: order=%s hash=%s time=%v err=%v", orderID, hash, keyTime, err)
		}
		var eventID, eventType string
		var payload []byte
		var createdAt, nextAttempt time.Time
		var unpublished bool
		var attempts int
		err = pool.QueryRow(ctx, `SELECT event_id::text,order_id::text,type,payload,created_at,published_at IS NULL,attempts,next_attempt_at FROM outbox_events WHERE order_id=$1::uuid`, p.Order.ID).Scan(&eventID, &orderID, &eventType, &payload, &createdAt, &unpublished, &attempts, &nextAttempt)
		if err != nil || eventID != p.Event.EventID || orderID != p.Order.ID || eventType != p.Event.Type || !createdAt.Equal(p.Event.CreatedAt) || !unpublished || attempts != 0 || nextAttempt.IsZero() {
			t.Fatalf("invalid persisted event: err=%v", err)
		}
		var actual, expected any
		if err := json.Unmarshal(payload, &actual); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(p.Event.Payload, &expected); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(actual, expected) {
			t.Fatalf("payload=%s want=%s", payload, p.Event.Payload)
		}
		checkDelta(before, 1)
	})
	t.Run("replays original order with a single connection", func(t *testing.T) {
		p := newParams("repeat")
		create(p)
		before := counts()
		for _, candidate := range []model.CreateOrderParams{p, newParams("repeat")} {
			limited, cancel := context.WithTimeout(ctx, time.Second)
			got, err := repo.CreateIdempotent(limited, candidate)
			cancel()
			if err != nil || got.Created || !reflect.DeepEqual(got.Order, p.Order) {
				t.Fatalf("replay=%+v err=%v", got, err)
			}
		}
		checkDelta(before, 0)
	})
	t.Run("different hash conflicts with same or new order ID", func(t *testing.T) {
		p := newParams("hash-conflict")
		create(p)
		before := counts()
		for _, candidate := range []model.CreateOrderParams{p, newParams("hash-conflict")} {
			candidate.RequestHash = strings.Repeat("b", 64)
			got, err := repo.CreateIdempotent(ctx, candidate)
			if !errors.Is(err, orderserrors.ErrIdempotencyConflict) {
				t.Fatalf("err=%v", err)
			}
			zeroResult(got)
		}
		checkDelta(before, 0)
	})
	t.Run("same key belongs independently to each user", func(t *testing.T) {
		before := counts()
		p, other := newParams("per-user"), newParams("per-user")
		other.Order.UserID = "20000000-0000-4000-8000-000000000002"
		create(p)
		create(other)
		checkDelta(before, 2)
	})
	for _, tc := range []struct {
		name, code, constraint string
		mutate                 func(*model.CreateOrderParams)
	}{
		{"invalid order", "23514", "orders_status_valid", func(p *model.CreateOrderParams) { p.Order.Status = "invalid" }},
		{"invalid key", "23514", "idempotency_keys_key_length", func(p *model.CreateOrderParams) { p.IdempotencyKey = strings.Repeat("x", 129) }},
		{"invalid second item", "23514", "order_items_quantity_range", func(p *model.CreateOrderParams) { p.Order.Items[1].Quantity = 0 }},
		{"duplicate item", "23505", "order_items_pkey", func(p *model.CreateOrderParams) { p.Order.Items[1].MenuItemID = p.Order.Items[0].MenuItemID }},
		{"invalid event JSON", "22P02", "", func(p *model.CreateOrderParams) { p.Event.Payload = []byte("{") }},
		{"event must be object", "23514", "outbox_events_payload_object", func(p *model.CreateOrderParams) { p.Event.Payload = []byte("[]") }},
	} {
		t.Run("rollback "+tc.name, func(t *testing.T) {
			before := counts()
			p := newParams(tc.name)
			tc.mutate(&p)
			got, err := repo.CreateIdempotent(ctx, p)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != tc.code || pgErr.ConstraintName != tc.constraint {
				t.Fatalf("error=%v want code=%s constraint=%s", err, tc.code, tc.constraint)
			}
			zeroResult(got)
			checkDelta(before, 0)
		})
	}
	t.Run("unrelated order ID collision is not a replay", func(t *testing.T) {
		p := newParams("original-id")
		create(p)
		before := counts()
		p.IdempotencyKey = "unrelated-key"
		got, err := repo.CreateIdempotent(ctx, p)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.ConstraintName != "orders_pkey" {
			t.Fatalf("err=%v", err)
		}
		zeroResult(got)
		checkDelta(before, 0)
	})
	t.Run("event ID collision rolls back instead of replaying", func(t *testing.T) {
		p := newParams("original-event")
		create(p)
		before := counts()
		candidate := newParams("event-collision")
		candidate.Event.EventID = p.Event.EventID
		got, err := repo.CreateIdempotent(ctx, candidate)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.ConstraintName != "outbox_events_pkey" {
			t.Fatalf("err=%v", err)
		}
		zeroResult(got)
		checkDelta(before, 0)
	})
	t.Run("deadline while waiting for pool", func(t *testing.T) {
		before := counts()
		held, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer held.Release()
		limited, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		defer cancel()
		got, err := repo.CreateIdempotent(limited, newParams("pool-timeout"))
		held.Release()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err=%v", err)
		}
		zeroResult(got)
		checkDelta(before, 0)
	})
	t.Run("deadline during final insert rolls back earlier writes", func(t *testing.T) {
		before := counts()
		blocker, err := pgx.ConnectConfig(ctx, cfg.ConnConfig.Copy())
		if err != nil {
			t.Fatal(err)
		}
		defer blocker.Close(context.Background())
		tx, err := blocker.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(context.Background())
		if _, err := tx.Exec(ctx, "LOCK TABLE outbox_events IN ACCESS EXCLUSIVE MODE"); err != nil {
			t.Fatal(err)
		}
		limited, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
		defer cancel()
		got, err := repo.CreateIdempotent(limited, newParams("write-timeout"))
		if err := tx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err=%v", err)
		}
		zeroResult(got)
		checkDelta(before, 0)
	})
	for _, mode := range []string{"different IDs", "identical parameters", "different hashes"} {
		t.Run("concurrent "+mode, func(t *testing.T) {
			before := counts()
			concurrentCfg := cfg.Copy()
			concurrentCfg.MaxConns = 8
			concurrentPool, err := pgxpool.NewWithConfig(ctx, concurrentCfg)
			if err != nil {
				t.Fatal(err)
			}
			defer concurrentPool.Close()
			concurrentRepo := repository.NewRepository(concurrentPool)
			limited, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			candidates := make([]model.CreateOrderParams, 8)
			for i := range candidates {
				candidates[i] = newParams("concurrent " + mode)
				if mode == "identical parameters" && i > 0 {
					candidates[i] = candidates[0]
				}
				if mode == "different hashes" {
					candidates[i].RequestHash = fmt.Sprintf("%064x", i+1)
				}
			}
			type outcome struct {
				result model.CreateOrderResult
				err    error
			}
			results := make(chan outcome, len(candidates))
			start := make(chan struct{})
			for _, candidate := range candidates {
				go func(p model.CreateOrderParams) {
					<-start
					result, err := concurrentRepo.CreateIdempotent(limited, p)
					results <- outcome{result, err}
				}(candidate)
			}
			// Закрытие канала одновременно разрешает всем горутинам начать работу.
			close(start)
			created, conflicts := 0, 0
			var winner string
			for range candidates {
				got := <-results
				if errors.Is(got.err, orderserrors.ErrIdempotencyConflict) && mode == "different hashes" {
					conflicts++
					zeroResult(got.result)
					continue
				}
				if got.err != nil {
					t.Errorf("concurrent error=%v", got.err)
					continue
				}
				if got.result.Created {
					created++
				}
				if winner == "" {
					winner = got.result.Order.ID
				}
				if got.result.Order.ID != winner {
					t.Errorf("orders differ: %s and %s", winner, got.result.Order.ID)
				}
			}
			if created != 1 {
				t.Fatalf("created=%d want=1", created)
			}
			if mode == "different hashes" && conflicts != len(candidates)-1 {
				t.Fatalf("conflicts=%d want=7", conflicts)
			}
			checkDelta(before, 1)
			stored, err := repo.GetByIdempotencyKey(ctx, candidates[0].Order.UserID, candidates[0].IdempotencyKey)
			if err != nil || stored.Order.ID != winner {
				t.Fatalf("lookup=%+v err=%v", stored, err)
			}
		})
	}
}
