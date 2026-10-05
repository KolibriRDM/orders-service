package handler

import (
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

// Добавляет request ID и журналирует результат запроса.
func requestMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := rand.Text()
		w.Header().Set("X-Request-ID", id)
		response := &responseWriter{ResponseWriter: w}
		started := time.Now()
		next.ServeHTTP(response, r)
		status := response.status
		if status == 0 {
			status = http.StatusOK
		}
		logger.Info("HTTP request", "method", r.Method, "route", r.Pattern, "status", status, "duration", time.Since(started), "request_id", id)
	})
}
