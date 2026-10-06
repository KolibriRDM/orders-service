package config

import (
	"log/slog"
	"os"
	"strings"
	"testing"
)

// Изолирует тест от настроек окружения пользователя.
func clearEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range []string{"ORDERS_HTTP_ADDR", "ORDERS_DATABASE_URL", "ORDERS_DB_PASSWORD", "LOG_LEVEL", "CATALOG_GRPC_ADDR"} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
}

// Проверяет настройки по умолчанию и сохранность паролей.
func TestLoadDefaults(t *testing.T) {
	clearEnvironment(t)
	t.Setenv("ORDERS_DATABASE_URL", " postgres://orders_app@localhost/orders_db ")
	t.Setenv("ORDERS_DB_PASSWORD", " secret with spaces ")
	cfg, err := LoadOrders()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != ":8080" || cfg.LogLevel != slog.LevelInfo || cfg.DatabaseURL != "postgres://orders_app@localhost/orders_db" || cfg.DatabasePassword != " secret with spaces " {
		t.Fatal("unexpected defaults or modified credentials")
	}
	if cfg.CatalogGRPCAddr != "127.0.0.1:9082" {
		t.Fatal("unexpected catalog gRPC address")
	}
}

// Проверяет формат адреса и диапазон HTTP-порта.
func TestHTTPAddressValidation(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		valid       bool
	}{
		{"empty", "", false}, {"blank", " ", false}, {"missing port", "localhost", false}, {"zero", ":0", false}, {"negative", ":-1", false}, {"large", ":65536", false}, {"text", ":http", false}, {"normal", "127.0.0.1:8082", true}, {"IPv6", "[::1]:8082", true}, {"trimmed", " :65535 ", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearEnvironment(t)
			t.Setenv("ORDERS_DATABASE_URL", "postgres://orders_app@localhost/orders_db")
			t.Setenv("ORDERS_DB_PASSWORD", "test")
			for _, key := range []string{"ORDERS_HTTP_ADDR"} {
				t.Setenv(key, tc.value)
			}
			cfg, err := LoadOrders()
			if (err == nil) != tc.valid {
				t.Fatalf("error=%v", err)
			}
			if tc.valid && cfg.HTTPAddr != strings.TrimSpace(tc.value) {
				t.Fatal("address not preserved")
			}
		})
	}
}

// Проверяет разбор уровня логирования.
func TestLogLevelValidation(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		want  slog.Level
		valid bool
	}{
		{" DEBUG ", slog.LevelDebug, true}, {"info", slog.LevelInfo, true}, {"Warn", slog.LevelWarn, true}, {"error", slog.LevelError, true}, {"", 0, false}, {"trace", 0, false},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			clearEnvironment(t)
			t.Setenv("ORDERS_DATABASE_URL", "postgres://orders_app@localhost/orders_db")
			t.Setenv("ORDERS_DB_PASSWORD", "test")
			t.Setenv("LOG_LEVEL", tc.raw)
			cfg, err := LoadOrders()
			if (err == nil) != tc.valid {
				t.Fatalf("error=%v", err)
			}
			if tc.valid && cfg.LogLevel != tc.want {
				t.Fatal("incorrect log level")
			}
		})
	}
}

// Проверяет обязательные настройки заказов.
func TestOrdersRequiredEnvironment(t *testing.T) {
	for _, key := range []string{"ORDERS_DATABASE_URL", "ORDERS_DB_PASSWORD"} {
		for _, state := range []string{"missing", "empty"} {
			t.Run(key+"/"+state, func(t *testing.T) {
				clearEnvironment(t)
				t.Setenv("ORDERS_DATABASE_URL", "postgres://orders_app@localhost/orders_db")
				t.Setenv("ORDERS_DB_PASSWORD", "test")
				if state == "missing" {
					if err := os.Unsetenv(key); err != nil {
						t.Fatal(err)
					}
				} else {
					t.Setenv(key, "")
				}
				_, err := LoadOrders()
				if err == nil || !strings.Contains(err.Error(), key) {
					t.Fatalf("got %v", err)
				}
			})
		}
	}
	t.Run("blank URL", func(t *testing.T) {
		clearEnvironment(t)
		t.Setenv("ORDERS_DATABASE_URL", " \t ")
		t.Setenv("ORDERS_DB_PASSWORD", "test")
		_, err := LoadOrders()
		if err == nil || !strings.Contains(err.Error(), "ORDERS_DATABASE_URL") {
			t.Fatalf("got %v", err)
		}
	})
}

// Проверяет чтение и очистку gRPC-адреса каталога.
func TestOrdersCatalogGRPCAddress(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		valid bool
	}{
		{" localhost:9082 ", true}, {"", false}, {" \t ", false},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			clearEnvironment(t)
			t.Setenv("ORDERS_DATABASE_URL", "postgres://orders_app@localhost/orders_db")
			t.Setenv("ORDERS_DB_PASSWORD", "test")
			t.Setenv("CATALOG_GRPC_ADDR", tc.raw)
			cfg, err := LoadOrders()
			if (err == nil) != tc.valid {
				t.Fatalf("error=%v", err)
			}
			if tc.valid && cfg.CatalogGRPCAddr != strings.TrimSpace(tc.raw) {
				t.Fatalf("address=%q", cfg.CatalogGRPCAddr)
			}
			if !tc.valid && !strings.Contains(err.Error(), "CATALOG_GRPC_ADDR") {
				t.Fatalf("error=%v", err)
			}
		})
	}
}
