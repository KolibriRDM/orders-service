package config

import (
	"errors"
	"log/slog"
	"os"
	"strings"
)

// Настройки HTTP, БД, каталога и логирования заказов.
type OrdersConfig struct {
	HTTPAddr         string
	DatabaseURL      string
	DatabasePassword string
	CatalogBaseURL   string
	LogLevel         slog.Level
}

// Читает и проверяет настройки заказов из окружения.
func LoadOrders() (OrdersConfig, error) {
	addr, err := httpAddr("ORDERS_HTTP_ADDR", ":8080")
	if err != nil {
		return OrdersConfig{}, err
	}
	databaseURL := os.Getenv("ORDERS_DATABASE_URL")
	dsn := strings.TrimSpace(databaseURL)
	if dsn == "" {
		return OrdersConfig{}, errors.New("ORDERS_DATABASE_URL is required")
	}
	databasePass := os.Getenv("ORDERS_DB_PASSWORD")
	if databasePass == "" {
		return OrdersConfig{}, errors.New("ORDERS_DB_PASSWORD is required")
	}
	level, err := logLevel()
	if err != nil {
		return OrdersConfig{}, err
	}

	catalogBaseURL, exists := os.LookupEnv("CATALOG_BASE_URL")
	if !exists {
		catalogBaseURL = "http://127.0.0.1:8082"
	}
	catalogBaseURL = strings.TrimSpace(catalogBaseURL)
	if catalogBaseURL == "" {
		return OrdersConfig{}, errors.New("CATALOG_BASE_URL must not be empty")
	}
	return OrdersConfig{
		HTTPAddr:         addr,
		DatabaseURL:      dsn,
		DatabasePassword: databasePass,
		LogLevel:         level,
		CatalogBaseURL:   catalogBaseURL,
	}, nil
}
