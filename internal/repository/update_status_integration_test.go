//go:build integration

package repository_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
	"github.com/KolibriRDM/orders-service/internal/model"
	"github.com/KolibriRDM/orders-service/internal/repository"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Проверяет переходы, неизменность снимков и конкурентное обновление в PostgreSQL.
func testUpdateStatusPostgres(t *testing.T, ctx context.Context, cfg *pgxpool.Config) {
	cfg = cfg.Copy()
	cfg.MaxConns = 4
	cfg.ConnConfig.RuntimeParams["application_name"] = "orders_update_status_test"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	repo := repository.NewRepository(pool)
	base := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	sequence := 0
	// Каждый сценарий получает отдельный заказ с двумя сохранёнными позициями.
	fixture := func(t *testing.T, status model.OrderStatus, version int64) model.Order {
		t.Helper()
		sequence++
		id := fmt.Sprintf("3abc0000-0000-4000-8000-%012d", sequence)
		_, err := pool.Exec(ctx, `INSERT INTO orders(id,user_id,restaurant_id,status,version,total_kopecks,delivery_address,created_at,updated_at)
   VALUES ($1::uuid,'30000000-0000-4000-8000-000000000100','30000000-0000-4000-8000-000000000200',$2,$3,60000,'Москва, Тестовая, 1',$4,$4)`, id, string(status), version, base)
		if err != nil {
			t.Fatal(err)
		}
		_, err = pool.Exec(ctx, `INSERT INTO order_items(order_id,menu_item_id,name_snapshot,unit_price_kopecks,quantity) VALUES
   ($1::uuid,'30000000-0000-4000-8000-000000000301','Пицца',25000,2),
   ($1::uuid,'30000000-0000-4000-8000-000000000302','Морс',10000,1)`, id)
		if err != nil {
			t.Fatal(err)
		}
		order, err := repo.GetByID(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return order
	}
	assertStored := func(t *testing.T, want model.Order) {
		t.Helper()
		got, err := repo.GetByID(ctx, want.ID)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("stored=%+v err=%v want=%+v", got, err, want)
		}
	}
	t.Run("full delivery chain preserves snapshots", func(t *testing.T) {
		want := fixture(t, model.StatusCreated, 1)
		for _, next := range []model.OrderStatus{model.StatusCooking, model.StatusReady, model.StatusDelivering, model.StatusDelivered} {
			at := want.UpdatedAt.Add(time.Minute).In(time.FixedZone("MSK", 3*60*60))
			got, err := repo.UpdateStatus(ctx, want.ID, want.Version, next, at)
			want.Status = next
			want.Version++
			want.UpdatedAt = at.UTC()
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("got=%+v err=%v want=%+v", got, err, want)
			}
			assertStored(t, want)
		}
	})
	t.Run("cancel with normalized ID and equal timestamp", func(t *testing.T) {
		want := fixture(t, model.StatusCreated, 1)
		got, err := repo.UpdateStatus(ctx, strings.ReplaceAll(strings.ToUpper(want.ID), "-", ""), 1, model.StatusCancelled, base)
		want.Status = model.StatusCancelled
		want.Version = 2
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("got=%+v err=%v want=%+v", got, err, want)
		}
		assertStored(t, want)
	})
	t.Run("last increment does not overflow", func(t *testing.T) {
		want := fixture(t, model.StatusCreated, math.MaxInt64-1)
		got, err := repo.UpdateStatus(ctx, want.ID, want.Version, model.StatusCooking, base)
		want.Status = model.StatusCooking
		want.Version = math.MaxInt64
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("got=%+v err=%v", got, err)
		}
		assertStored(t, want)
	})
	for _, tc := range []struct {
		name          string
		current, next model.OrderStatus
		expected      int64
		at            time.Time
		want          error
	}{
		{"stale version", model.StatusCreated, model.StatusCooking, 1, base, orderserrors.ErrVersionConflict},
		{"version checked before transition", model.StatusDelivered, model.StatusCooking, 1, base, orderserrors.ErrVersionConflict},
		{"same status", model.StatusCreated, model.StatusCreated, 2, base, orderserrors.ErrInvalidTransition},
		{"skipped status", model.StatusCreated, model.StatusReady, 2, base, orderserrors.ErrInvalidTransition},
		{"backward transition", model.StatusReady, model.StatusCooking, 2, base, orderserrors.ErrInvalidTransition},
		{"cancel after cooking", model.StatusCooking, model.StatusCancelled, 2, base, orderserrors.ErrInvalidTransition},
		{"delivered terminal", model.StatusDelivered, model.StatusCancelled, 2, base, orderserrors.ErrInvalidTransition},
		{"cancelled terminal", model.StatusCancelled, model.StatusCooking, 2, base, orderserrors.ErrInvalidTransition},
		{"older timestamp", model.StatusCreated, model.StatusCooking, 2, base.Add(-time.Microsecond), orderserrors.ErrInvalidInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := fixture(t, tc.current, 2)
			got, err := repo.UpdateStatus(ctx, before.ID, tc.expected, tc.next, tc.at)
			if !errors.Is(err, tc.want) || !reflect.DeepEqual(got, model.Order{}) {
				t.Fatalf("got=%+v err=%v want=%v", got, err, tc.want)
			}
			assertStored(t, before)
		})
	}
	t.Run("missing order", func(t *testing.T) {
		got, err := repo.UpdateStatus(ctx, "30000000-0000-4000-8000-999999999999", 1, model.StatusCooking, base)
		if !errors.Is(err, orderserrors.ErrNotFound) || !reflect.DeepEqual(got, model.Order{}) {
			t.Fatalf("got=%+v err=%v", got, err)
		}
	})
	t.Run("two checked transitions have one winner", func(t *testing.T) {
		before := fixture(t, model.StatusCreated, 1)
		raceCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		blocker := lockStatusOrder(t, raceCtx, pool, before.ID)
		defer blocker.Rollback(context.Background())
		type result struct {
			order model.Order
			err   error
		}
		results := make(chan result, 2)
		for _, next := range []model.OrderStatus{model.StatusCooking, model.StatusCancelled} {
			go func() {
				order, err := repo.UpdateStatus(raceCtx, before.ID, 1, next, base.Add(time.Minute))
				results <- result{order, err}
			}()
		}
		// Оба вызова должны пройти чтение и остановиться именно на условном UPDATE.
		waitStatusUpdates(t, raceCtx, pool, 2)
		if err := blocker.Rollback(raceCtx); err != nil {
			t.Fatal(err)
		}
		successes, conflicts := 0, 0
		var winner model.Order
		for range 2 {
			select {
			case got := <-results:
				switch {
				case got.err == nil:
					successes++
					winner = got.order
				case errors.Is(got.err, orderserrors.ErrVersionConflict):
					conflicts++
					if !reflect.DeepEqual(got.order, model.Order{}) {
						t.Errorf("conflict returned partial order: %+v", got.order)
					}
				default:
					t.Errorf("unexpected error: %v", got.err)
				}
			case <-raceCtx.Done():
				t.Fatal(raceCtx.Err())
			}
		}
		if successes != 1 || conflicts != 1 {
			t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
		}
		if winner.Status != model.StatusCooking && winner.Status != model.StatusCancelled {
			t.Fatalf("unexpected winner: %+v", winner)
		}
		want := before
		want.Status = winner.Status
		want.Version = 2
		want.UpdatedAt = base.Add(time.Minute)
		if !reflect.DeepEqual(winner, want) {
			t.Fatalf("winner=%+v want=%+v", winner, want)
		}
		assertStored(t, want)
	})
	t.Run("cancellation while update waits for row lock", func(t *testing.T) {
		before := fixture(t, model.StatusCreated, 1)
		blocker := lockStatusOrder(t, ctx, pool, before.ID)
		defer blocker.Rollback(context.Background())
		requestCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		done := make(chan error, 1)
		go func() {
			got, err := repo.UpdateStatus(requestCtx, before.ID, 1, model.StatusCooking, base.Add(time.Minute))
			if !reflect.DeepEqual(got, model.Order{}) {
				done <- fmt.Errorf("cancelled update returned order: %+v", got)
				return
			}
			done <- err
		}()
		waitCtx, stop := context.WithTimeout(ctx, 5*time.Second)
		defer stop()
		waitStatusUpdates(t, waitCtx, pool, 1)
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("err=%v", err)
			}
		case <-waitCtx.Done():
			t.Fatal(waitCtx.Err())
		}
		// Возврат из pgx опережает завершение отмены запроса на сервере.
		waitStatusUpdates(t, waitCtx, pool, 0)
		if err := blocker.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		assertStored(t, before)
	})
}

// Удерживает строку, сохраняя доступность её прежней версии для обычного SELECT.
func lockStatusOrder(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id string) pgx.Tx {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "SELECT id FROM orders WHERE id=$1::uuid FOR UPDATE", id); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatal(err)
	}
	return tx
}

// Ожидает блокировку UPDATE на сервере без предположений о скорости горутин.
func waitStatusUpdates(t *testing.T, ctx context.Context, pool *pgxpool.Pool, want int) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var count int
		err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
   WHERE datname=current_database() AND application_name='orders_update_status_test'
    AND pid<>pg_backend_pid() AND state='active' AND wait_event_type='Lock'
    AND query LIKE '%UPDATE orders SET status%'`).Scan(&count)
		if err != nil {
			t.Fatal(err)
		}
		if count == want {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting updates=%d want=%d: %v", count, want, ctx.Err())
		case <-ticker.C:
		}
	}
}
