package config

import (
	"strings"
	"testing"
)

// Проверяет адрес Redis и отклонение ошибочной конфигурации.
func TestOrdersRedisAddress(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		valid       bool
	}{
		{"local", "127.0.0.1:16379", true},
		{"compose", "redis:6379", true},
		{"trimmed", " redis:6379 ", true},
		{"IPv6", "[::1]:6379", true},
		{"empty", "", false},
		{"blank", "  ", false},
		{"missing host", ":6379", false},
		{"missing port", "redis", false},
		{"zero port", "redis:0", false},
		{"large port", "redis:65536", false},
		{"negative port", "redis:-1", false},
		{"text port", "redis:redis", false},
		{"URL", "redis://redis:6379", false},
		{"credentials", "user@redis:6379", false},
		{"space in host", "red is:6379", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearEnvironment(t)
			t.Setenv("ORDERS_DATABASE_URL", "postgres://localhost/orders_db")
			t.Setenv("ORDERS_DB_PASSWORD", "test")
			t.Setenv("ORDERS_REDIS_ADDR", tc.value)
			cfg, err := LoadOrders()
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
				if cfg.RedisAddr != strings.TrimSpace(tc.value) {
					t.Fatalf("address=%q", cfg.RedisAddr)
				}
			} else if err == nil || !strings.Contains(err.Error(), "ORDERS_REDIS_ADDR") {
				t.Fatalf("expected Redis configuration error, got %v", err)
			}
		})
	}
}
