package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
	"github.com/KolibriRDM/orders-service/internal/handler/dto"
	"github.com/KolibriRDM/orders-service/internal/model"
	"github.com/KolibriRDM/orders-service/internal/ratelimiter"
)

var _ RateLimiter = (*ratelimiter.Limiter)(nil)

// Подменяет решение лимитера в HTTP-тестах.
type rateLimiterFunc func(context.Context, string) (model.RateLimitDecision, error)

// Передаёт вызов тестовой функции.
func (f rateLimiterFunc) Allow(ctx context.Context, userID string) (model.RateLimitDecision, error) {
	return f(ctx, userID)
}

// Разрешает запросы в тестах остальных HTTP-операций.
func allowAllOrders(context.Context, string) (model.RateLimitDecision, error) {
	return model.RateLimitDecision{Allowed: true}, nil
}

// Создаёт HTTP-запрос с корректными заголовками.
func rateLimitRequest() *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/v1/orders", strings.NewReader(createBody))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Demo-User-ID", orderUserID)
	r.Header.Set("Idempotency-Key", "same-order")
	return r
}

// Проверяет JSON 429, округление Retry-After и отсутствие вызова сервиса.
func TestCreateOrderRateLimitExceeded(t *testing.T) {
	for _, tc := range []struct {
		name  string
		retry time.Duration
		want  string
	}{
		{"minute", time.Minute, "60"}, {"fraction", 1250 * time.Millisecond, "2"},
		{"subsecond", 300 * time.Millisecond, "1"}, {"exact", 2 * time.Second, "2"},
		{"zero", 0, "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			calls := 0
			h := NewHandler(nil, rateLimiterFunc(func(_ context.Context, id string) (model.RateLimitDecision, error) {
				calls++
				if id != orderUserID {
					t.Fatalf("user=%q", id)
				}
				return model.RateLimitDecision{RetryAfter: tc.retry}, nil
			}), logger)
			w := httptest.NewRecorder()
			NewRouter(logger, nil, h, newHTTPMetricsForTest()).ServeHTTP(w, rateLimitRequest())
			var body dto.ErrorResponse
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if w.Code != 429 || calls != 1 || w.Header().Get("Retry-After") != tc.want || body.Code != "rate_limit_exceeded" || body.Message != "too many order creation requests" || body.RequestID == "" || body.RequestID != w.Header().Get("X-Request-ID") {
				t.Fatalf("status=%d headers=%v calls=%d body=%+v", w.Code, w.Header(), calls, body)
			}
			if w.Header().Get("Content-Type") != "application/json" || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Location") != "" {
				t.Fatalf("headers=%v", w.Header())
			}
		})
	}
}

// Проверяет допуск и fail-open без утечки причины сбоя Redis.
func TestCreateOrderRateLimitAllowsAndFailsOpen(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"allowed", nil}, {"Redis failure", errors.New("private Redis details")},
		{"Redis timeout", fmt.Errorf("Redis operation: %w", context.DeadlineExceeded)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs strings.Builder
			logger := slog.New(slog.NewTextHandler(&logs, nil))
			limiterCalls, serviceCalls := 0, 0
			var limiterCtx context.Context
			limiter := rateLimiterFunc(func(ctx context.Context, id string) (model.RateLimitDecision, error) {
				limiterCalls++
				limiterCtx = ctx
				deadline, ok := ctx.Deadline()
				if id != orderUserID || !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 2*time.Second {
					t.Fatalf("id=%q deadline=%v", id, deadline)
				}
				return model.RateLimitDecision{Allowed: tc.err == nil}, tc.err
			})
			svc := orderServiceStub{create: func(ctx context.Context, input model.CreateOrderInput) (model.CreateOrderResult, error) {
				serviceCalls++
				if limiterCalls != 1 || ctx != limiterCtx || ctx.Err() != nil || input.UserID != orderUserID {
					t.Fatal("incorrect service call after limiter")
				}
				return model.CreateOrderResult{Order: handlerOrderFixture(), Created: true}, nil
			}}
			r := rateLimitRequest()
			r.Header.Set("X-Demo-User-ID", strings.ReplaceAll(strings.ToUpper(orderUserID), "-", ""))
			w := httptest.NewRecorder()
			NewRouter(logger, nil, NewHandler(svc, limiter, logger), newHTTPMetricsForTest()).ServeHTTP(w, r)
			if w.Code != 201 || serviceCalls != 1 || limiterCalls != 1 || w.Header().Get("Retry-After") != "" {
				t.Fatalf("status=%d service=%d limiter=%d", w.Code, serviceCalls, limiterCalls)
			}
			warned := strings.Contains(logs.String(), "order rate limiter unavailable")
			if warned != (tc.err != nil) || strings.Contains(logs.String()+w.Body.String(), "private Redis details") {
				t.Fatalf("logs=%s", logs.String())
			}
			if tc.err != nil && !strings.Contains(logs.String(), "request_id="+w.Header().Get("X-Request-ID")) {
				t.Fatal("warning missing request ID")
			}
		})
	}
}

// Отклоняет неверный UUID до Redis и ошибку входных данных от лимитера.
func TestCreateOrderRateLimitInvalidInput(t *testing.T) {
	for _, tc := range []struct {
		user      string
		wantCalls int
	}{{"", 0}, {"invalid", 0}, {orderUserID, 1}} {
		t.Run(tc.user, func(t *testing.T) {
			calls := 0
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			h := NewHandler(nil, rateLimiterFunc(func(context.Context, string) (model.RateLimitDecision, error) {
				calls++
				return model.RateLimitDecision{}, fmt.Errorf("invalid user: %w", orderserrors.ErrInvalidInput)
			}), logger)
			r := rateLimitRequest()
			r.Header.Set("X-Demo-User-ID", tc.user)
			w := httptest.NewRecorder()
			h.CreateOrder(w, r)
			if w.Code != 400 || calls != tc.wantCalls || w.Header().Get("Retry-After") != "" {
				t.Fatalf("status=%d calls=%d", w.Code, calls)
			}
		})
	}
}

// Учитывает неверное тело и каждый идемпотентный повтор как отдельную попытку.
func TestCreateOrderRateLimitCountsAttempts(t *testing.T) {
	calls, serviceCalls := 0, 0
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	limiter := rateLimiterFunc(func(context.Context, string) (model.RateLimitDecision, error) {
		calls++
		return model.RateLimitDecision{Allowed: true}, nil
	})
	svc := orderServiceStub{create: func(context.Context, model.CreateOrderInput) (model.CreateOrderResult, error) {
		serviceCalls++
		return model.CreateOrderResult{Order: handlerOrderFixture(), Created: serviceCalls == 1}, nil
	}}
	h := NewHandler(svc, limiter, logger)
	r := rateLimitRequest()
	r.Body = io.NopCloser(strings.NewReader("{"))
	w := httptest.NewRecorder()
	h.CreateOrder(w, r)
	if w.Code != 400 || calls != 1 || serviceCalls != 0 {
		t.Fatalf("invalid body: status=%d limiter=%d service=%d", w.Code, calls, serviceCalls)
	}
	for _, status := range []int{201, 200} {
		w = httptest.NewRecorder()
		h.CreateOrder(w, rateLimitRequest())
		if w.Code != status {
			t.Fatalf("status=%d want=%d", w.Code, status)
		}
	}
	if calls != 3 || serviceCalls != 2 {
		t.Fatalf("limiter=%d service=%d", calls, serviceCalls)
	}
}

// Не продолжает отменённый запрос ни до, ни после обращения к лимитеру.
func TestCreateOrderRateLimitCancellation(t *testing.T) {
	for _, during := range []bool{false, true} {
		t.Run(fmt.Sprint(during), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if !during {
				cancel()
			}
			calls := 0
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			h := NewHandler(nil, rateLimiterFunc(func(context.Context, string) (model.RateLimitDecision, error) {
				calls++
				cancel()
				return model.RateLimitDecision{}, errors.New("Redis failed during cancellation")
			}), logger)
			w := &responseWriter{ResponseWriter: httptest.NewRecorder()}
			h.CreateOrder(w, rateLimitRequest().WithContext(ctx))
			wantCalls := 0
			if during {
				wantCalls = 1
			}
			if calls != wantCalls || w.status != 0 {
				t.Fatalf("calls=%d status=%d", calls, w.status)
			}
		})
	}
}

// Сохраняет короткий дедлайн запроса и останавливает работу при его истечении.
func TestCreateOrderRateLimitDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		wantDeadline, _ := ctx.Deadline()
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		calls := 0
		h := NewHandler(nil, rateLimiterFunc(func(ctx context.Context, _ string) (model.RateLimitDecision, error) {
			calls++
			deadline, _ := ctx.Deadline()
			if !deadline.Equal(wantDeadline) {
				t.Fatal("parent deadline changed")
			}
			<-ctx.Done()
			return model.RateLimitDecision{}, ctx.Err()
		}), logger)
		w := httptest.NewRecorder()
		h.CreateOrder(w, rateLimitRequest().WithContext(ctx))
		if w.Code != 503 || calls != 1 || w.Header().Get("Retry-After") != "" {
			t.Fatalf("status=%d calls=%d", w.Code, calls)
		}
	})
}

// Чтение заказа и служебные маршруты не расходуют лимит создания.
func TestReadAndProbesBypassCreateRateLimit(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := orderServiceStub{get: func(context.Context, string, string) (model.Order, error) { return handlerOrderFixture(), nil }}
	limiter := rateLimiterFunc(func(context.Context, string) (model.RateLimitDecision, error) {
		t.Fatal("unexpected limiter call")
		return model.RateLimitDecision{}, nil
	})
	router := NewRouter(logger, func(context.Context) error { return nil }, NewHandler(svc, limiter, logger), newHTTPMetricsForTest())
	for _, path := range []string{"/healthz", "/readyz", "/v1/orders/" + handlerOrderFixture().ID} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("X-Demo-User-ID", orderUserID)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("path=%s status=%d", path, w.Code)
		}
	}
}
