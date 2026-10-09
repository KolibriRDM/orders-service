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

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
	"github.com/KolibriRDM/orders-service/internal/model"
	platformmetrics "github.com/KolibriRDM/orders-service/internal/platform/metrics"
)

var _ HTTPMetrics = (*platformmetrics.Metrics)(nil)

// Создаёт независимый реестр для существующих HTTP-тестов.
func newHTTPMetricsForTest() HTTPMetrics { return platformmetrics.NewMetrics() }

// Сохраняет одно наблюдение middleware для проверки меток и длительности.
type httpObservation struct {
	method, route string
	status        int
	duration      time.Duration
}

// Записывает обращения к метрикам без глобального состояния.
type httpMetricsRecorder struct{ observations []httpObservation }

// Запоминает результат обработанного запроса.
func (m *httpMetricsRecorder) ObserveHTTP(method, route string, status int, duration time.Duration) {
	m.observations = append(m.observations, httpObservation{method, route, status, duration})
}

// Возвращает простой обработчик экспорта для тестов middleware.
func (m *httpMetricsRecorder) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
}

// Проверяет совпадение нормализованного маршрута и длительности в метриках и JSON-логе.
func TestHTTPMetricsMatchAccessLog(t *testing.T) {
	var logs strings.Builder
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	metrics := &httpMetricsRecorder{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
	w := httptest.NewRecorder()
	requestMiddleware(logger, metrics, mux).ServeHTTP(w, httptest.NewRequest("GET", "/private-id?address=private-address", nil))
	if len(metrics.observations) != 1 {
		t.Fatalf("observations=%v", metrics.observations)
	}
	got := metrics.observations[0]
	var entry struct {
		Route     string `json:"route"`
		Status    int    `json:"status"`
		Duration  int64  `json:"duration"`
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal([]byte(logs.String()), &entry); err != nil {
		t.Fatal(err)
	}
	if got.route != "unmatched" || got.status != 404 || entry.Route != got.route || entry.Status != got.status || entry.Duration != int64(got.duration) || entry.RequestID == "" || entry.RequestID != w.Header().Get("X-Request-ID") {
		t.Fatalf("observation=%+v log=%+v", got, entry)
	}
	if strings.Contains(logs.String(), "private-") {
		t.Fatalf("URL leaked: %s", logs.String())
	}
}

// Проверяет диагностические статусы отмены без отправки нового HTTP-ответа.
func TestHTTPMetricsContextStatus(t *testing.T) {
	for _, tc := range []struct {
		name           string
		contextKind    string
		explicit, want int
	}{
		{"implicit success", "active", 0, 200}, {"cancelled", "cancelled", 0, 499},
		{"expired", "expired", 0, 504}, {"sent response wins", "cancelled", 503, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.contextKind == "cancelled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if tc.contextKind == "expired" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
			}
			metrics := &httpMetricsRecorder{}
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.explicit != 0 {
					w.WriteHeader(tc.explicit)
				}
			})
			// Внешняя обёртка отличает отсутствие записи статуса от неявного 200 recorder.
			w := &responseWriter{ResponseWriter: httptest.NewRecorder()}
			requestMiddleware(logger, metrics, next).ServeHTTP(w, httptest.NewRequest("GET", "/", nil).WithContext(ctx))
			if len(metrics.observations) != 1 || metrics.observations[0].status != tc.want || w.status != tc.explicit {
				t.Fatalf("observations=%v wireStatus=%d", metrics.observations, w.status)
			}
		})
	}
}

// Исключает служебные маршруты из RPS, сохраняя журнал и ответы 405.
func TestHTTPMetricsExcludeOperationalRoutes(t *testing.T) {
	var logs strings.Builder
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	metrics := &httpMetricsRecorder{}
	h := NewHandler(nil, nil, logger)
	router := NewRouter(logger, func(context.Context) error { return nil }, h, metrics)
	for _, path := range []string{"/metrics", "/healthz", "/readyz"} {
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost} {
			logs.Reset()
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(method, path, nil))
			want := 200
			if method == http.MethodPost {
				want = 405
			}
			if w.Code != want || len(metrics.observations) != 0 || !strings.Contains(logs.String(), "HTTP request") {
				t.Fatalf("%s %s: status=%d metrics=%v logs=%s", method, path, w.Code, metrics.observations, logs.String())
			}
			if method == http.MethodPost && w.Header().Get("Allow") != "GET, HEAD" {
				t.Fatalf("allow=%q", w.Header().Get("Allow"))
			}
		}
	}
}

// Проверяет реальные серии экспорта: UUID объединены, ошибки и 429 учитываются.
func TestRouterExportsHTTPMetrics(t *testing.T) {
	metrics := platformmetrics.NewMetrics()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := orderServiceStub{get: func(_ context.Context, _, id string) (model.Order, error) {
		if id == "ffffffff-ffff-4fff-8fff-ffffffffffff" {
			return model.Order{}, errors.New("private SQL error")
		}
		return model.Order{}, orderserrors.ErrNotFound
	}}
	limiter := rateLimiterFunc(func(_ context.Context, user string) (model.RateLimitDecision, error) {
		return model.RateLimitDecision{Allowed: user != orderUserID, RetryAfter: time.Second}, nil
	})
	router := NewRouter(logger, func(context.Context) error { return nil }, NewHandler(svc, limiter, logger), metrics)
	for _, tc := range []struct {
		method, path, user string
		status             int
	}{
		{"GET", "/v1/orders/11111111-1111-4111-8111-111111111111?secret=hidden", orderUserID, 404},
		{"GET", "/v1/orders/22222222-2222-4222-8222-222222222222", orderUserID, 404},
		{"GET", "/v1/orders/ffffffff-ffff-4fff-8fff-ffffffffffff", orderUserID, 500},
		{"GET", "/missing-one", "", 404}, {"GET", "/missing-two", "", 404},
		{"DELETE", "/v1/orders/id", orderUserID, 405},
		{"POST", "/v1/orders", orderUserID, 429},
		{"POST", "/v1/orders", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", 400},
	} {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader("{"))
		r.Header.Set("X-Demo-User-ID", tc.user)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s %s: status=%d body=%s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
	var previous string
	for range 2 {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/metrics", nil)
		r.Header.Set("Accept", "text/plain; version=0.0.4")
		router.ServeHTTP(w, r)
		if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
			t.Fatalf("export status=%d headers=%v", w.Code, w.Header())
		}
		var custom []string
		for _, line := range strings.Split(w.Body.String(), "\n") {
			if strings.HasPrefix(line, "food_http_") {
				custom = append(custom, line)
			}
		}
		exported := strings.Join(custom, "\n")
		for _, want := range []string{
			`food_http_requests_total{method="GET",route="GET /v1/orders/{id}",service="orders-service",status="404"} 2`,
			`food_http_requests_total{method="GET",route="GET /v1/orders/{id}",service="orders-service",status="500"} 1`,
			`food_http_requests_total{method="GET",route="unmatched",service="orders-service",status="404"} 2`,
			`food_http_requests_total{method="DELETE",route="/v1/orders/{id}",service="orders-service",status="405"} 1`,
			`food_http_requests_total{method="POST",route="POST /v1/orders",service="orders-service",status="429"} 1`,
			`food_http_requests_total{method="POST",route="POST /v1/orders",service="orders-service",status="400"} 1`,
			`food_http_request_duration_seconds_count{method="POST",route="POST /v1/orders",service="orders-service"} 2`,
		} {
			if !strings.Contains(exported, want) {
				t.Fatalf("missing %q in:\n%s", want, exported)
			}
		}
		for _, forbidden := range []string{"11111111", "22222222", "ffffffff", orderUserID, "missing-one", "missing-two", "secret", "hidden", "/metrics", "request_id"} {
			if strings.Contains(exported, forbidden) {
				t.Fatalf("unexpected label %q", forbidden)
			}
		}
		if previous != "" && previous != exported {
			t.Fatal("scraping changed application metrics")
		}
		previous = exported
	}
}
