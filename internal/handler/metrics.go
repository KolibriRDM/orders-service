package handler

import (
	"net/http"
	"time"
)

// Контракт записи HTTP-метрик и их экспорта.
type HTTPMetrics interface {
	ObserveHTTP(method, route string, status int, duration time.Duration)
	Handler() http.Handler
}
