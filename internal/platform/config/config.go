package config

import (
	"errors"
	"log/slog"
	"os"
	"strings"
)

// Настройки HTTP, БД, каталога, Kafka и логирования заказов.
type OrdersConfig struct {
	HTTPAddr          string
	DatabaseURL       string
	DatabasePassword  string
	CatalogGRPCAddr   string
	LogLevel          slog.Level
	KafkaBrokers      []string
	OrderCreatedTopic string
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

	catalogGRPCAddr, exists := os.LookupEnv("CATALOG_GRPC_ADDR")
	if !exists {
		catalogGRPCAddr = "127.0.0.1:9082"
	}
	catalogGRPCAddr = strings.TrimSpace(catalogGRPCAddr)
	if catalogGRPCAddr == "" {
		return OrdersConfig{}, errors.New("CATALOG_GRPC_ADDR must not be empty")
	}
	brokersRaw, exists := os.LookupEnv("KAFKA_BROKERS")
	if !exists {
		brokersRaw = "127.0.0.1:19092"
	}
	brokers := strings.Split(brokersRaw, ",")
	for i := range brokers {
		brokers[i] = strings.TrimSpace(brokers[i])
		if brokers[i] == "" {
			return OrdersConfig{}, errors.New("KAFKA_BROKERS must contain non-empty broker addresses")
		}
	}
	topic, exists := os.LookupEnv("ORDER_CREATED_TOPIC")
	if !exists {
		topic = "food.order-created.v1"
	}
	topic = strings.TrimSpace(topic)
	if topic == "" {
		return OrdersConfig{}, errors.New("ORDER_CREATED_TOPIC must not be empty")
	}

	return OrdersConfig{
		HTTPAddr:          addr,
		DatabaseURL:       dsn,
		DatabasePassword:  databasePass,
		LogLevel:          level,
		CatalogGRPCAddr:   catalogGRPCAddr,
		KafkaBrokers:      brokers,
		OrderCreatedTopic: topic,
	}, nil
}
