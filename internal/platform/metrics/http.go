package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Записывает результат HTTP-запроса и его длительность.
func (m *Metrics) ObserveHTTP(method, route string, status int, duration time.Duration) {
	if route == "" {
		route = "unmatched"
	}
	statusStr := strconv.Itoa(status)
	// Неизвестные методы объединяем, чтобы ограничить число меток.
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace, http.MethodConnect, http.MethodDelete, http.MethodPatch, http.MethodPost, http.MethodPut:
	default:
		method = "OTHER"
	}
	m.httpRequests.WithLabelValues(method, route, statusStr).Inc()
	m.httpDuration.WithLabelValues(method, route).Observe(duration.Seconds())
}

// Возвращает HTTP-обработчик экспорта метрик.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}
