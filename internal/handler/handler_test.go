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
	"reflect"
	"strings"
	"testing"
	"time"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
	"github.com/KolibriRDM/orders-service/internal/handler/dto"
	"github.com/KolibriRDM/orders-service/internal/model"
	ordersservice "github.com/KolibriRDM/orders-service/internal/service"
)

// Тестовая подмена бизнес-операций над заказами.
type orderServiceStub struct {
	create       func(context.Context, model.CreateOrderInput) (model.CreateOrderResult, error)
	get          func(context.Context, string, string) (model.Order, error)
	cancel       func(context.Context, string, string, int64) (model.Order, error)
	updateStatus func(context.Context, bool, string, int64, model.OrderStatus) (model.Order, error)
}

// Вызывает тестовую подмену операции.
func (s orderServiceStub) CreateOrder(ctx context.Context, input model.CreateOrderInput) (model.CreateOrderResult, error) {
	return s.create(ctx, input)
}

// Вызывает тестовую подмену операции.
func (s orderServiceStub) GetOrder(ctx context.Context, userID, orderID string) (model.Order, error) {
	return s.get(ctx, userID, orderID)
}

// Вызывает тестовую подмену отмены заказа.
func (s orderServiceStub) CancelOrder(ctx context.Context, userID, orderID string, version int64) (model.Order, error) {
	return s.cancel(ctx, userID, orderID, version)
}

// Вызывает тестовую подмену изменения статуса.
func (s orderServiceStub) UpdateOrderStatus(ctx context.Context, isStaff bool, orderID string, version int64, status model.OrderStatus) (model.Order, error) {
	return s.updateStatus(ctx, isStaff, orderID, version, status)
}

// Собирает настоящий роутер с тестовым сервисом.
func orderHandlerForTest(service Service) http.Handler {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := NewHandler(service, rateLimiterFunc(allowAllOrders), logger)
	return NewRouter(logger, func(context.Context) error { return nil }, h, newHTTPMetricsForTest())
}

const createBody = `{"restaurant_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","delivery_address":" Москва, 1 ","items":[{"menu_item_id":"cccccccc-cccc-4ccc-8ccc-cccccccccccc","quantity":2}]}`
const orderUserID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"

// Готовит заказ для проверки HTTP-ответов.
func handlerOrderFixture() model.Order {
	now := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	return model.Order{
		ID: "dddddddd-dddd-4ddd-8ddd-dddddddddddd", UserID: orderUserID,
		RestaurantID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", DeliveryAddress: "Москва, 1",
		Status: model.StatusCreated, Version: 1, TotalKopecks: 69900, CreatedAt: now, UpdatedAt: now,
		Items: []model.OrderItem{{MenuItemID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", Quantity: 2, NameSnapshot: "Пицца", UnitPriceKopecks: 25000}},
	}
}

// Проверяет создание и повтор через HTTP-обработчик.
func TestCreateOrderHTTP(t *testing.T) {
	for _, created := range []bool{true, false} {
		t.Run(fmt.Sprintf("created=%v", created), func(t *testing.T) {
			order := handlerOrderFixture()
			calls := 0
			var calledCtx context.Context
			h := orderHandlerForTest(orderServiceStub{create: func(ctx context.Context, input model.CreateOrderInput) (model.CreateOrderResult, error) {
				calls++
				calledCtx = ctx
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 2*time.Second {
					t.Fatal("missing bounded context")
				}
				want := model.CreateOrderInput{UserID: orderUserID, IdempotencyKey: " key ", RestaurantID: order.RestaurantID, DeliveryAddress: " Москва, 1 ", Items: []model.CreateOrderItemInput{{MenuItemID: order.Items[0].MenuItemID, Quantity: 2}}}
				if !reflect.DeepEqual(input, want) {
					t.Fatalf("input=%+v want=%+v", input, want)
				}
				return model.CreateOrderResult{Order: order, Created: created}, nil
			}})
			r := httptest.NewRequest(http.MethodPost, "/v1/orders", strings.NewReader(createBody))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-Demo-User-ID", orderUserID)
			r.Header.Set("Idempotency-Key", " key ")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			status, location := http.StatusOK, ""
			if created {
				status, location = http.StatusCreated, "/v1/orders/"+order.ID
			}
			if w.Code != status || w.Header().Get("Location") != location || calls != 1 {
				t.Fatalf("status=%d location=%q calls=%d", w.Code, w.Header().Get("Location"), calls)
			}
			var response dto.OrderResponse
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(response, dto.ToOrderResponse(order)) {
				t.Fatalf("response=%+v", response)
			}
			if w.Header().Get("Content-Type") != "application/json" || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Request-ID") == "" {
				t.Fatal("missing response headers")
			}
			if !errors.Is(calledCtx.Err(), context.Canceled) {
				t.Fatal("handler did not release context")
			}
		})
	}
}

// Проверяет чтение заказа и передачу контекста через HTTP.
func TestGetOrderHTTP(t *testing.T) {
	order := handlerOrderFixture()
	var calledCtx context.Context
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	parentDeadline, _ := ctx.Deadline()
	h := orderHandlerForTest(orderServiceStub{get: func(child context.Context, userID, orderID string) (model.Order, error) {
		calledCtx = child
		if userID != order.UserID || orderID != order.ID {
			t.Fatalf("arguments=%s / %s", userID, orderID)
		}
		deadline, ok := child.Deadline()
		if !ok || !deadline.Equal(parentDeadline) {
			t.Fatal("parent deadline not preserved")
		}
		return order, nil
	}})
	r := httptest.NewRequest(http.MethodGet, "/v1/orders/"+order.ID, nil).WithContext(ctx)
	r.Header.Set("X-Demo-User-ID", order.UserID)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var response dto.OrderResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || !reflect.DeepEqual(response, dto.ToOrderResponse(order)) {
		t.Fatalf("status=%d response=%+v", w.Code, response)
	}
	if !errors.Is(calledCtx.Err(), context.Canceled) {
		t.Fatal("handler did not release context")
	}
}

// Проверяет ответы при ошибках сервиса без утечки данных.
func TestOrderHandlersReturnServiceErrors(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		for _, tc := range []struct {
			cause  error
			status int
			code   string
		}{
			{orderserrors.ErrInvalidInput, 400, "invalid_input"},
			{orderserrors.ErrNotFound, 404, "not_found"},
			{orderserrors.ErrIdempotencyConflict, 409, "idempotency_conflict"},
			{orderserrors.ErrCatalogUnavailable, 503, "unavailable"},
			{orderserrors.ErrDatabaseUnavailable, 503, "unavailable"},
			{context.DeadlineExceeded, 503, "unavailable"},
			{errors.New("private SQL details"), 500, "internal_error"},
		} {
			t.Run(method+"/"+tc.code+"/"+tc.cause.Error(), func(t *testing.T) {
				order := handlerOrderFixture()
				err := fmt.Errorf("private SQL details: %w", tc.cause)
				h := orderHandlerForTest(orderServiceStub{
					create: func(context.Context, model.CreateOrderInput) (model.CreateOrderResult, error) {
						return model.CreateOrderResult{Order: order, Created: true}, err
					},
					get: func(context.Context, string, string) (model.Order, error) { return order, err },
				})
				path := "/v1/orders"
				if method == http.MethodGet {
					path += "/" + order.ID
				}
				r := httptest.NewRequest(method, path, strings.NewReader(createBody))
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("X-Demo-User-ID", orderUserID)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				var body dto.ErrorResponse
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if w.Code != tc.status || body.Code != tc.code || body.RequestID == "" || body.RequestID != w.Header().Get("X-Request-ID") {
					t.Fatalf("status=%d body=%+v", w.Code, body)
				}
				if strings.Contains(w.Body.String(), order.ID) || strings.Contains(w.Body.String(), "SQL") || w.Header().Get("Location") != "" {
					t.Fatalf("partial result or internal details leaked: %s", w.Body.String())
				}
			})
		}
	}
}

// Проверяет отклонение неверного тела до вызова сервиса.
func TestCreateOrderRejectsBodyBeforeService(t *testing.T) {
	for _, tc := range []struct {
		body, contentType string
		status            int
	}{
		{"null", "application/json", 400},
		{createBody + " {}", "application/json", 400},
		{`{"user_id":"other-user"}`, "application/json", 400},
		{`{"delivery_address":"` + strings.Repeat("x", maxRequestBytes) + `"}`, "application/json", 413},
		{createBody, "text/plain", 415},
	} {
		r := httptest.NewRequest(http.MethodPost, "/v1/orders", strings.NewReader(tc.body))
		r.Header.Set("Content-Type", tc.contentType)
		r.Header.Set("X-Demo-User-ID", orderUserID)
		w := httptest.NewRecorder()
		orderHandlerForTest(nil).ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("status=%d want=%d", w.Code, tc.status)
		}
	}
}

// Проверяет обязательность пользователя и ключа в заголовках.
func TestOrderHandlersRequiredHeaders(t *testing.T) {
	for _, tc := range []struct{ method, userID, key string }{
		{http.MethodPost, "", "key"},
		{http.MethodPost, orderUserID, ""},
		{http.MethodGet, "", ""},
	} {
		t.Run(tc.method+"/"+tc.userID, func(t *testing.T) {
			path := "/v1/orders"
			if tc.method == http.MethodGet {
				path += "/" + handlerOrderFixture().ID
			}
			r := httptest.NewRequest(tc.method, path, strings.NewReader(createBody))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-Demo-User-ID", tc.userID)
			r.Header.Set("Idempotency-Key", tc.key)
			w := httptest.NewRecorder()
			// Настоящий сервис проверяет заголовки до обращения к зависимостям.
			orderHandlerForTest(ordersservice.NewService(nil, nil)).ServeHTTP(w, r)
			if w.Code != 400 {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

// Проверяет остановку отменённого POST и передачу отмены при чтении.
func TestOrderHandlersPropagateCancellation(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			calls := 0
			check := func(got context.Context) error {
				calls++
				if !errors.Is(got.Err(), context.Canceled) {
					t.Fatal("cancellation lost")
				}
				return got.Err()
			}
			h := orderHandlerForTest(orderServiceStub{
				create: func(ctx context.Context, _ model.CreateOrderInput) (model.CreateOrderResult, error) {
					return model.CreateOrderResult{}, check(ctx)
				},
				get: func(ctx context.Context, _, _ string) (model.Order, error) { return model.Order{}, check(ctx) },
			})
			path := "/v1/orders"
			if method == http.MethodGet {
				path += "/" + handlerOrderFixture().ID
			}
			r := httptest.NewRequest(method, path, strings.NewReader(createBody)).WithContext(ctx)
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-Demo-User-ID", orderUserID)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			wantCalls := 1
			if method == http.MethodPost {
				wantCalls = 0
			}
			if calls != wantCalls || w.Body.Len() != 0 {
				t.Fatalf("calls=%d body=%s", calls, w.Body.String())
			}
		})
	}
}
