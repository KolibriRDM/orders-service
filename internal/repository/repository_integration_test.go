//go:build integration

package repository_test

import (
	"context"
	"errors"
	"os"
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

// Проверяет чтение и запись заказов на настоящем PostgreSQL.
func TestOrdersRepositoryPostgres(t *testing.T) {
	dsn, password := os.Getenv("ORDERS_TEST_DATABASE_URL"), os.Getenv("ORDERS_TEST_DB_PASSWORD")
	if dsn == "" || password == "" {
		t.Fatal("use scripts/test-orders-postgres.sh to create an isolated database")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid test DSN")
	}
	if !strings.HasPrefix(cfg.ConnConfig.Database, "orders_test_") {
		t.Fatal("tests require an isolated orders_test_ database")
	}
	cfg.ConnConfig.Password = password
	cfg.MaxConns = 1
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `
 INSERT INTO orders(id,user_id,restaurant_id,status,version,total_kopecks,delivery_address,created_at,updated_at) VALUES
 ('00000000-0000-4000-8000-000000000001','00000000-0000-4000-8000-000000000010','00000000-0000-4000-8000-000000000020','created',1,79900,'Москва, Тестовая улица, 1','2026-09-29 12:00:00+03','2026-09-29 12:01:00+03'),
 ('00000000-0000-4000-8000-000000000002','00000000-0000-4000-8000-000000000011','00000000-0000-4000-8000-000000000020','created',1,19900,'Без позиций','2026-09-29 12:00:00+03','2026-09-29 12:00:00+03');
 INSERT INTO order_items VALUES
 ('00000000-0000-4000-8000-000000000001','00000000-0000-4000-8000-000000000103','Морс',10000,1),
 ('00000000-0000-4000-8000-000000000001','00000000-0000-4000-8000-000000000101','Маргарита',25000,2);`)
	if err != nil {
		t.Fatal(err)
	}
	repo := repository.NewRepository(pool)
	const id = "00000000-0000-4000-8000-000000000001"
	want := model.Order{ID: id, UserID: "00000000-0000-4000-8000-000000000010", RestaurantID: "00000000-0000-4000-8000-000000000020", Status: model.StatusCreated, Version: 1, TotalKopecks: 79900, DeliveryAddress: "Москва, Тестовая улица, 1", CreatedAt: time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 9, 29, 9, 1, 0, 0, time.UTC), Items: []model.OrderItem{
		{MenuItemID: "00000000-0000-4000-8000-000000000101", NameSnapshot: "Маргарита", UnitPriceKopecks: 25000, Quantity: 2},
		{MenuItemID: "00000000-0000-4000-8000-000000000103", NameSnapshot: "Морс", UnitPriceKopecks: 10000, Quantity: 1},
	}}
	t.Run("reads all fields and ordered snapshots", func(t *testing.T) {
		got, err := repo.GetByID(ctx, id)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("got=%+v err=%v want=%+v", got, err, want)
		}
	})
	t.Run("missing order", func(t *testing.T) {
		got, err := repo.GetByID(ctx, "ffffffff-ffff-4fff-8fff-ffffffffffff")
		if !errors.Is(err, orderserrors.ErrNotFound) || !reflect.DeepEqual(got, model.Order{}) {
			t.Fatalf("got=%+v err=%v", got, err)
		}
	})
	t.Run("empty items are a nonnil slice", func(t *testing.T) {
		got, err := repo.GetByID(ctx, "00000000-0000-4000-8000-000000000002")
		if err != nil || got.Items == nil || len(got.Items) != 0 {
			t.Fatalf("got=%+v err=%v", got, err)
		}
	})
	t.Run("repeated reads release the single connection", func(t *testing.T) {
		for i := 0; i < 5; i++ {
			got, err := repo.GetByID(ctx, id)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("got=%+v err=%v", got, err)
			}
		}
	})
	t.Run("cancelled request", func(t *testing.T) {
		cancelled, stop := context.WithCancel(ctx)
		stop()
		got, err := repo.GetByID(cancelled, id)
		if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, model.Order{}) {
			t.Fatalf("got=%+v err=%v", got, err)
		}
	})
	t.Run("deadline waiting for pool", func(t *testing.T) {
		held, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer held.Release()
		limited, stop := context.WithTimeout(ctx, 50*time.Millisecond)
		defer stop()
		got, err := repo.GetByID(limited, id)
		if !errors.Is(err, context.DeadlineExceeded) || !reflect.DeepEqual(got, model.Order{}) {
			t.Fatalf("got=%+v err=%v", got, err)
		}
	})
	t.Run("timeout loading items returns no partial order", func(t *testing.T) {
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
		if _, err := tx.Exec(ctx, "LOCK TABLE order_items IN ACCESS EXCLUSIVE MODE"); err != nil {
			t.Fatal(err)
		}
		limited, stop := context.WithTimeout(ctx, 100*time.Millisecond)
		defer stop()
		got, err := repo.GetByID(limited, id)
		if !errors.Is(err, context.DeadlineExceeded) || !reflect.DeepEqual(got, model.Order{}) {
			t.Fatalf("got=%+v err=%v", got, err)
		}
	})
	t.Run("reads current status and preserves snapshots", func(t *testing.T) {
		if _, err := pool.Exec(ctx, "UPDATE orders SET status='cooking',version=2 WHERE id=$1::uuid", id); err != nil {
			t.Fatal(err)
		}
		got, err := repo.GetByID(ctx, id)
		expected := want
		expected.Status = model.StatusCooking
		expected.Version = 2
		if err != nil || !reflect.DeepEqual(got, expected) {
			t.Fatalf("got=%+v err=%v", got, err)
		}
	})
	t.Run("idempotency lookup", func(t *testing.T) {
		const user1 = "00000000-0000-4000-8000-000000000010"
		const user2 = "00000000-0000-4000-8000-000000000011"
		const order2 = "00000000-0000-4000-8000-000000000002"
		_, err := pool.Exec(ctx, `INSERT INTO idempotency_keys(user_id,key,request_hash,order_id,created_at) VALUES
   ($1::uuid,'shared',repeat('a',64),$3::uuid,now()),
   ($2::uuid,'shared',repeat('b',64),$4::uuid,now()),
   ($1::uuid,'private',repeat('c',64),$3::uuid,now())`, user1, user2, id, order2)
		if err != nil {
			t.Fatal(err)
		}
		t.Run("same key scoped by user and returns hash", func(t *testing.T) {
			for _, tc := range []struct{ user, order, hash string }{{user1, id, strings.Repeat("a", 64)}, {user2, order2, strings.Repeat("b", 64)}} {
				expected, err := repo.GetByID(ctx, tc.order)
				if err != nil {
					t.Fatal(err)
				}
				for i := 0; i < 3; i++ {
					got, err := repo.GetByIdempotencyKey(ctx, tc.user, "shared")
					if err != nil || got.RequestHash != tc.hash || !reflect.DeepEqual(got.Order, expected) {
						t.Fatalf("got=%+v err=%v expected=%+v", got, err, expected)
					}
				}
			}
		})
		for _, tc := range []struct{ name, user, key string }{
			{"unknown key", user1, "missing"},
			{"another user's key", user2, "private"},
			{"unknown user", "ffffffff-ffff-4fff-8fff-ffffffffffff", "shared"},
			{"key is not trimmed", user1, " shared "},
		} {
			t.Run(tc.name, func(t *testing.T) {
				got, err := repo.GetByIdempotencyKey(ctx, tc.user, tc.key)
				if !errors.Is(err, orderserrors.ErrNotFound) || !reflect.DeepEqual(got, model.IdempotentOrder{}) {
					t.Fatalf("got=%+v err=%v", got, err)
				}
			})
		}
		t.Run("cancelled context", func(t *testing.T) {
			cancelled, stop := context.WithCancel(ctx)
			stop()
			got, err := repo.GetByIdempotencyKey(cancelled, user1, "shared")
			if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, model.IdempotentOrder{}) {
				t.Fatalf("got=%+v err=%v", got, err)
			}
		})
		t.Run("deadline waiting for connection", func(t *testing.T) {
			held, err := pool.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer held.Release()
			limited, stop := context.WithTimeout(ctx, 50*time.Millisecond)
			defer stop()
			got, err := repo.GetByIdempotencyKey(limited, user1, "shared")
			if !errors.Is(err, context.DeadlineExceeded) || !reflect.DeepEqual(got, model.IdempotentOrder{}) {
				t.Fatalf("got=%+v err=%v", got, err)
			}
		})
		t.Run("order read error returns neither hash nor partial order", func(t *testing.T) {
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
			if _, err := tx.Exec(ctx, "LOCK TABLE order_items IN ACCESS EXCLUSIVE MODE"); err != nil {
				t.Fatal(err)
			}
			limited, stop := context.WithTimeout(ctx, 100*time.Millisecond)
			defer stop()
			got, err := repo.GetByIdempotencyKey(limited, user1, "shared")
			if !errors.Is(err, context.DeadlineExceeded) || !reflect.DeepEqual(got, model.IdempotentOrder{}) {
				t.Fatalf("got=%+v err=%v", got, err)
			}
		})
	})

	t.Run("outbox relay repository", func(t *testing.T) {
		testOutboxPostgres(t, ctx, pool)
	})

	t.Run("create idempotent", func(t *testing.T) {
		testCreateIdempotentPostgres(t, ctx, pool, cfg)
	})

	t.Run("update status", func(t *testing.T) {
		testUpdateStatusPostgres(t, ctx, cfg)
	})

}
