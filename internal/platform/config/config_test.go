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
	for _, key := range []string{"ORDERS_HTTP_ADDR", "ORDERS_DATABASE_URL", "ORDERS_DB_PASSWORD", "LOG_LEVEL", "CATALOG_GRPC_ADDR", "KAFKA_BROKERS", "ORDER_CREATED_TOPIC", "ORDERS_REDIS_ADDR"} {
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
	if cfg.RedisAddr != "127.0.0.1:16379" || cfg.HTTPAddr != ":8080" || cfg.LogLevel != slog.LevelInfo || cfg.DatabaseURL != "postgres://orders_app@localhost/orders_db" || cfg.DatabasePassword != " secret with spaces " {
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

// Проверяет defaults, пробелы и пустые элементы настроек Kafka.
func TestOrdersKafkaConfig(t *testing.T) {
	for _, tc := range []struct {
		name, brokers, topic string
		defaults, valid      bool
		want                 string
	}{
		{name: "defaults", defaults: true, valid: true},
		{name: "multiple trimmed", brokers: " localhost:19092, kafka:9092 ", topic: " custom.events ", valid: true},
		{name: "empty", brokers: "", topic: "topic", want: "KAFKA_BROKERS"},
		{name: "whitespace", brokers: " \t ", topic: "topic", want: "KAFKA_BROKERS"},
		{name: "empty middle", brokers: "host:9092, ,other:9092", topic: "topic", want: "KAFKA_BROKERS"},
		{name: "trailing comma", brokers: "host:9092,", topic: "topic", want: "KAFKA_BROKERS"},
		{name: "empty topic", brokers: "host:9092", want: "ORDER_CREATED_TOPIC"},
		{name: "blank topic", brokers: "host:9092", topic: " \t ", want: "ORDER_CREATED_TOPIC"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearEnvironment(t)
			t.Setenv("ORDERS_DATABASE_URL", "postgres://orders_app@localhost/orders_db")
			t.Setenv("ORDERS_DB_PASSWORD", "test")
			if !tc.defaults {
				t.Setenv("KAFKA_BROKERS", tc.brokers)
				t.Setenv("ORDER_CREATED_TOPIC", tc.topic)
			}
			cfg, err := LoadOrders()
			if !tc.valid {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("err=%v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.defaults {
				if len(cfg.KafkaBrokers) != 1 || cfg.KafkaBrokers[0] != "127.0.0.1:19092" || cfg.OrderCreatedTopic != "food.order-created.v1" {
					t.Fatalf("Kafka defaults=%v %q", cfg.KafkaBrokers, cfg.OrderCreatedTopic)
				}
			} else {
				if strings.Join(cfg.KafkaBrokers, ",") != "localhost:19092,kafka:9092" || cfg.OrderCreatedTopic != "custom.events" {
					t.Fatalf("Kafka config=%v %q", cfg.KafkaBrokers, cfg.OrderCreatedTopic)
				}
			}
		})
	}
}
