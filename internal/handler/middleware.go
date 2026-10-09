package handler

import (
	"context"
	"crypto/rand"
	"log/slog"
	"net/http"
	"time"
)

// Обёртка ответа, сохраняющая HTTP-статус для журнала.
type responseWriter struct {
	http.ResponseWriter
	status int
}

// Запоминает и отправляет первый статус ответа.
func (w *responseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

// Пишет тело, выставляя 200 при отсутствии статуса.
func (w *responseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}

// Возвращает исходный ResponseWriter для HTTP-инструментов.
func (w *responseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Добавляет request ID, записывает HTTP-метрики и журнал запроса.
func requestMiddleware(logger *slog.Logger, metrics HTTPMetrics, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := rand.Text()
		w.Header().Set("X-Request-ID", id)
		response := &responseWriter{ResponseWriter: w}
		started := time.Now()
		next.ServeHTTP(response, r)
		elapsed := time.Since(started)
		status := response.status
		if status == 0 {
			status = http.StatusOK
			// Без отправленного ответа отмену отмечаем как 499, дедлайн — как 504.
			if r.Context().Err() == context.Canceled {
				status = 499
			}
			if r.Context().Err() == context.DeadlineExceeded {
				status = http.StatusGatewayTimeout
			}
		}
		// Шаблон маршрута не содержит UUID и query-параметры.
		route := r.Pattern
		if route == "" || route == "/" {
			route = "unmatched"
		}
		// Служебные проверки и сбор метрик исключаем из прикладного RPS.
		switch route {
		case "GET /metrics", "/metrics",
			"GET /healthz", "/healthz",
			"GET /readyz", "/readyz":
		default:
			metrics.ObserveHTTP(r.Method, route, status, elapsed)
		}
		logger.Info("HTTP request", "method", r.Method, "route", route, "status", status, "duration", elapsed, "request_id", id)
	})
}
