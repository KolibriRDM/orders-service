package main

import (
	"strings"
	"testing"
)

// Проверяет возврат ошибки конфигурации без закрытия несуществующего клиента.
func TestRunRejectsInvalidCatalogAddress(t *testing.T) {
	t.Setenv("ORDERS_HTTP_ADDR", ":8080")
	t.Setenv("LOG_LEVEL", "info")
	t.Setenv("KAFKA_BROKERS", "127.0.0.1:19092")
	t.Setenv("ORDER_CREATED_TOPIC", "food.order-created.v1")
	t.Setenv("ORDERS_DATABASE_URL", "postgres://orders_app@127.0.0.1:1/orders_db?sslmode=disable")
	t.Setenv("ORDERS_DB_PASSWORD", "test")
	t.Setenv("CATALOG_GRPC_ADDR", "http://127.0.0.1:9082")
	if err := run(); err == nil || !strings.Contains(err.Error(), "create catalog client") {
		t.Fatalf("error = %v, want catalog configuration error", err)
	}
}
