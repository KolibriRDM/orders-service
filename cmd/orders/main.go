package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/KolibriRDM/orders-service/internal/catalogclient"
	"github.com/KolibriRDM/orders-service/internal/handler"
	"github.com/KolibriRDM/orders-service/internal/kafkapublisher"
	"github.com/KolibriRDM/orders-service/internal/platform/config"
	"github.com/KolibriRDM/orders-service/internal/platform/metrics"
	"github.com/KolibriRDM/orders-service/internal/platform/postgres"
	redisplatform "github.com/KolibriRDM/orders-service/internal/platform/redis"
	"github.com/KolibriRDM/orders-service/internal/ratelimiter"
	"github.com/KolibriRDM/orders-service/internal/repository"
	"github.com/KolibriRDM/orders-service/internal/service"
)

// Запускает сервис и завершает процесс при ошибке.
func main() {
	if err := run(); err != nil {
		logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
		logger.Error("service failed", "service", "orders-service", "error", err)
		os.Exit(1)
	}
}

// Собирает зависимости, запускает HTTP и управляет остановкой.
func run() error {
	cfg, err := config.LoadOrders()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logHandler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel})

	logger := slog.New(logHandler).With("service", "orders-service")

	// Один реестр используется для записи и экспорта метрик процесса.
	serviceMetrics := metrics.NewMetrics()

	catalogClient, err := catalogclient.NewClient(cfg.CatalogGRPCAddr)
	if err != nil {
		return fmt.Errorf("create catalog client: %w", err)
	}
	defer catalogClient.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL, cfg.DatabasePassword, "orders-service")
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("database create pool: %w", err)
	}
	defer pool.Close()
	publisher, err := kafkapublisher.NewPublisher(cfg.KafkaBrokers, cfg.OrderCreatedTopic)
	if err != nil {
		return fmt.Errorf("create Kafka publisher: %w", err)
	}
	defer publisher.Close()
	ordersRepository := repository.NewRepository(pool)
	outboxService := service.NewOutboxService(ordersRepository, publisher, serviceMetrics, logger)
	ordersService := service.NewService(ordersRepository, catalogClient)
	// Ленивое подключение позволяет запуститься при недоступном Redis.
	redisClient := redisplatform.NewClient(cfg.RedisAddr)
	defer redisClient.Close()
	orderLimiter := ratelimiter.NewLimiter(redisClient)
	ordersHandler := handler.NewHandler(ordersService, orderLimiter, logger)

	// Штатная остановка ждёт запросы; принудительная отменяет их контексты.
	requestsCtx, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler.NewRouter(logger, pool.Ping, ordersHandler, serviceMetrics),
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
		BaseContext:       func(net.Listener) context.Context { return requestsCtx },
	}
	listener, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen HTTP: %w", err)
	}
	defer listener.Close()
	defer srv.Close()

	relayCtx, cancelRelay := context.WithCancel(ctx)
	relayDone := make(chan struct{})
	metricsDone := make(chan struct{})

	// Канал закрывается только после полного завершения relay.
	go func() {
		defer close(relayDone)
		outboxService.Run(relayCtx)
	}()

	// Статистика обновляется даже при задержках публикации в Kafka.
	go func() {
		defer close(metricsDone)
		outboxService.RunMetrics(relayCtx)
	}()
	// Ждём остановку relay и измерений до закрытия зависимостей.
	defer func() {
		cancelRelay()
		<-relayDone
		<-metricsDone
	}()

	// Буфер позволяет серверу сообщить об остановке, не ожидая читателя.
	serverErr := make(chan error, 1)

	go func() {
		serverErr <- srv.Serve(listener)
	}()

	logger.Info("HTTP server started", "addr", listener.Addr().String())

	select {
	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve HTTP: %w", err)
		}
		return nil
	case <-ctx.Done():
		stop()
		logger.Info("HTTP server stopping")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = srv.Shutdown(shutdownCtx)
	if err != nil {
		cancelRequests()
		_ = srv.Close()
		<-serverErr
		return fmt.Errorf("shutdown HTTP: %w", err)
	}

	err = <-serverErr
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve HTTP: %w", err)
	}
	cancelRequests()
	logger.Info("HTTP server stopped")
	return nil
}
