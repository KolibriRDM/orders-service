package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Проверяет отсутствие пароля в ошибке строки подключения.
func TestNewPoolInvalidDSNDoesNotExposeCredentials(t *testing.T) {
	pool, err := NewPool(context.Background(), "postgres://user:secret-canary@localhost:invalid/db", "password-canary", "pool-test")
	if pool != nil || err == nil {
		t.Fatalf("pool=%v err=%v", pool, err)
	}
	if strings.Contains(err.Error(), "canary") {
		t.Fatalf("credentials leaked: %v", err)
	}
}

// Проверяет создание пула с отменённым контекстом.
func TestNewPoolCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pool, err := NewPool(ctx, "postgres://catalog_app@127.0.0.1:1/catalog_db?sslmode=disable", "test", "pool-test")
	if pool != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("pool=%v err=%v", pool, err)
	}
}
