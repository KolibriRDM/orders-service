package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KolibriRDM/orders-service/internal/handler/dto"
)

// Тестовая функция проверки подключения к БД.
type pingFunc = func(context.Context) error

// Проверяет независимость health от БД.
func TestHealthDoesNotPingDatabase(t *testing.T) {
	h := newProbeRouter(slog.New(slog.NewTextHandler(io.Discard, nil)), pingFunc(func(context.Context) error { t.Fatal("healthz touched database"); return nil }))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"status":"ok"}` {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

// Проверяет readiness при доступной и недоступной БД.
func TestReadiness(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code int
		body string
	}{
		{"connected", nil, 200, `{"status":"ok"}`},
		{"failed", errors.New("password=do-not-expose"), 503, `{"status":"not_ready"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs strings.Builder
			calls := 0
			h := newProbeRouter(slog.New(slog.NewTextHandler(&logs, nil)), pingFunc(func(ctx context.Context) error {
				calls++
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > time.Second || time.Until(deadline) <= 0 {
					t.Fatal("missing bounded readiness context")
				}
				return tc.err
			}))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil))
			if calls != 1 || w.Code != tc.code || strings.TrimSpace(w.Body.String()) != tc.body {
				t.Fatalf("calls=%d, response=%d %s", calls, w.Code, w.Body)
			}
			if w.Header().Get("Content-Type") != "application/json" || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("incorrect headers")
			}
			if strings.Contains(logs.String()+w.Body.String(), "do-not-expose") {
				t.Fatal("database details leaked")
			}
		})
	}
}

// Проверяет передачу отмены запроса в readiness.
func TestReadinessHonorsRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h := newProbeRouter(slog.New(slog.NewTextHandler(io.Discard, nil)), pingFunc(func(ctx context.Context) error {
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Fatalf("context error: %v", ctx.Err())
		}
		return ctx.Err()
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil).WithContext(ctx))
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
}

// Проверяет ограничение времени readiness.
func TestReadinessTimeout(t *testing.T) {
	h := newProbeRouter(slog.New(slog.NewTextHandler(io.Discard, nil)), pingFunc(func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }))
	start := time.Now()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil))
	if w.Code != 503 || time.Since(start) > 3*time.Second {
		t.Fatalf("status=%d elapsed=%v", w.Code, time.Since(start))
	}
}

// Проверяет допустимые HTTP-методы служебных маршрутов.
func TestProbeMethods(t *testing.T) {
	h := newProbeRouter(slog.New(slog.NewTextHandler(io.Discard, nil)), pingFunc(func(context.Context) error { t.Fatal("unexpected ping"); return nil }))
	for _, path := range []string{"/healthz", "/readyz"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, nil))
		if w.Code != http.StatusMethodNotAllowed {
			t.Fatal(w.Code)
		}
	}
}

// Собирает роутер для проверки health и readiness.
func newProbeRouter(logger *slog.Logger, checkDatabase func(context.Context) error) http.Handler {
	return NewRouter(logger, checkDatabase, NewHandler(nil, logger))
}

// Проверяет JSON-ответы для неизвестного пути и метода.
func TestRouterFallbackResponses(t *testing.T) {
	h := newProbeRouter(slog.New(slog.NewTextHandler(io.Discard, nil)), func(context.Context) error { t.Fatal("unexpected database call"); return nil })
	for _, tc := range []struct {
		method, path string
		status       int
		allow        string
	}{
		{"GET", "/missing", 404, ""},
		{"GET", "/v1/orders", 405, "POST"},
		{"DELETE", "/v1/orders/id", 405, "GET, HEAD"},
		{"POST", "/healthz", 405, "GET, HEAD"},
		{"POST", "/readyz", 405, "GET, HEAD"},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		var body dto.ErrorResponse
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if w.Code != tc.status || w.Header().Get("Allow") != tc.allow || body.RequestID == "" || body.RequestID != w.Header().Get("X-Request-ID") {
			t.Fatalf("%s %s: status=%d body=%+v", tc.method, tc.path, w.Code, body)
		}
	}
}
