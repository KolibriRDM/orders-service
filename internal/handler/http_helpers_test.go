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

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
	"github.com/KolibriRDM/orders-service/internal/handler/dto"
	"github.com/KolibriRDM/orders-service/internal/model"
	ordersservice "github.com/KolibriRDM/orders-service/internal/service"
)

var _ Service = (*ordersservice.Service)(nil)

// Тестовое тело с учётом вызова Close.
type trackedBody struct {
	io.Reader
	closed bool
}

// Отмечает закрытие тестового тела запроса или ответа.
func (b *trackedBody) Close() error { b.closed = true; return nil }

// Проверяет формат, размер и закрытие тела запроса.
func TestDecodeCreateOrderRequest(t *testing.T) {
	const valid = `{"restaurant_id":"restaurant","delivery_address":"Москва","items":[{"menu_item_id":"dish","quantity":2}]}`
	for _, tc := range []struct {
		name, contentType, body string
		status                  int
		code                    string
	}{
		{"valid", "application/json", valid, 0, ""},
		{"charset and whitespace", "application/json; charset=utf-8", valid + " \n\t", 0, ""},
		{"missing content type", "", valid, 415, "unsupported_media_type"},
		{"wrong content type", "text/plain", valid, 415, "unsupported_media_type"},
		{"invalid content type", "application/json; charset", valid, 415, "unsupported_media_type"},
		{"empty", "application/json", "", 400, "invalid_input"},
		{"null", "application/json", "null", 400, "invalid_input"},
		{"array", "application/json", "[]", 400, "invalid_input"},
		{"truncated", "application/json", "{", 400, "invalid_input"},
		{"user must come from header", "application/json", `{"user_id":"attacker"}`, 400, "invalid_input"},
		{"unknown item field", "application/json", `{"items":[{"price_kopecks":1}]}`, 400, "invalid_input"},
		{"wrong quantity type", "application/json", `{"items":[{"quantity":"2"}]}`, 400, "invalid_input"},
		{"second object", "application/json", valid + " {}", 400, "invalid_input"},
		{"second null", "application/json", valid + " null", 400, "invalid_input"},
		{"trailing garbage", "application/json", valid + " garbage", 400, "invalid_input"},
		{"large body", "application/json", `{"delivery_address":"` + strings.Repeat("x", maxRequestBytes) + `"}`, 413, "body_too_large"},
		{"large trailing whitespace", "application/json", valid + strings.Repeat(" ", maxRequestBytes), 413, "body_too_large"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			h := NewHandler(nil, nil, logger)
			body := &trackedBody{Reader: strings.NewReader(tc.body)}
			r := httptest.NewRequest(http.MethodPost, "/v1/orders", nil)
			r.Body = body
			r.Header.Set("Content-Type", tc.contentType)
			w := httptest.NewRecorder()
			w.Header().Set("X-Request-ID", "test-request")
			got, ok := h.decodeCreateOrderRequest(w, r)
			if !body.closed {
				t.Fatal("request body not closed")
			}
			if tc.status == 0 {
				want := dto.CreateOrderRequest{RestaurantID: "restaurant", DeliveryAddress: "Москва", Items: []dto.CreateOrderItemRequest{{MenuItemID: "dish", Quantity: 2}}}
				if !ok || !reflect.DeepEqual(got, want) || w.Body.Len() != 0 {
					t.Fatalf("got=%+v ok=%v response=%s", got, ok, w.Body.String())
				}
				return
			}
			if ok || !reflect.DeepEqual(got, dto.CreateOrderRequest{}) || w.Code != tc.status {
				t.Fatalf("got=%+v ok=%v status=%d", got, ok, w.Code)
			}
			var response dto.ErrorResponse
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Code != tc.code || response.RequestID != "test-request" || response.Message == "" {
				t.Fatalf("response=%+v", response)
			}
		})
	}
}

// Проверяет статусы ошибок и отсутствие внутренних данных.
func TestOrdersErrorResponses(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{orderserrors.ErrInvalidInput, 400, "invalid_input"},
		{orderserrors.ErrNotFound, 404, "not_found"},
		{orderserrors.ErrForbidden, 403, "forbidden"},
		{orderserrors.ErrIdempotencyConflict, 409, "idempotency_conflict"},
		{orderserrors.ErrVersionConflict, 409, "version_conflict"},
		{orderserrors.ErrInvalidTransition, 409, "invalid_transition"},
		{orderserrors.ErrAlreadyExists, 409, "already_exists"},
		{orderserrors.ErrCatalogUnavailable, 503, "unavailable"},
		{orderserrors.ErrDatabaseUnavailable, 503, "unavailable"},
		{context.DeadlineExceeded, 503, "unavailable"},
		{errors.New("SQL contains private-address"), 500, "internal_error"},
	} {
		t.Run(tc.err.Error(), func(t *testing.T) {
			var logs strings.Builder
			h := NewHandler(nil, nil, slog.New(slog.NewTextHandler(&logs, nil)))
			w := httptest.NewRecorder()
			w.Header().Set("X-Request-ID", "request-123")
			h.handleError(w, fmt.Errorf("private-address: %w", tc.err))
			var body dto.ErrorResponse
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if w.Code != tc.status || body.Code != tc.code || body.RequestID != "request-123" {
				t.Fatalf("status=%d body=%+v", w.Code, body)
			}
			if w.Header().Get("Content-Type") != "application/json" || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("missing response headers")
			}
			if strings.Contains(w.Body.String()+logs.String(), "private-address") {
				t.Fatal("request details leaked")
			}
		})
	}
}

// Проверяет ответ 500 при ошибке сериализации JSON.
func TestWriteJSONEncodingFailure(t *testing.T) {
	h := NewHandler(nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	w := httptest.NewRecorder()
	w.Header().Set("X-Request-ID", "encode-failure")
	h.writeJSON(w, http.StatusCreated, make(chan int))
	var body dto.ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != 500 || body.Code != "internal_error" || body.RequestID != "encode-failure" {
		t.Fatalf("status=%d body=%+v", w.Code, body)
	}
}

// Проверяет request ID и журнал без приватных URL-параметров.
func TestRequestIDAndSafeAccessLog(t *testing.T) {
	var logs strings.Builder
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	h := NewHandler(nil, nil, logger)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/orders/{id}", func(w http.ResponseWriter, r *http.Request) { h.handleError(w, orderserrors.ErrNotFound) })
	wrapped := requestMiddleware(logger, newHTTPMetricsForTest(), mux)
	seen := make(map[string]bool)
	for range 2 {
		r := httptest.NewRequest(http.MethodGet, "/v1/orders/private-id?address=private-address", nil)
		r.Header.Set("X-Request-ID", "untrusted-id")
		w := httptest.NewRecorder()
		wrapped.ServeHTTP(w, r)
		id := w.Header().Get("X-Request-ID")
		var body dto.ErrorResponse
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if id == "" || id == "untrusted-id" || seen[id] || body.RequestID != id {
			t.Fatalf("invalid request ID=%q body=%+v", id, body)
		}
		seen[id] = true
	}
	if strings.Contains(logs.String(), "private-") || !strings.Contains(logs.String(), "status=404") || !strings.Contains(logs.String(), "/v1/orders/{id}") {
		t.Fatalf("unexpected log=%s", logs.String())
	}
}

// Проверяет формат ответа с пустым списком позиций.
func TestOrderResponseEmptyItemsContract(t *testing.T) {
	response := dto.ToOrderResponse(model.Order{})
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["items"]) != "[]" || fields["total_kopecks"] == nil || fields["created_at"] == nil {
		t.Fatalf("response=%s", data)
	}
}

// Проверяет отсутствие ответа на отменённый запрос.
func TestCancelledRequestDoesNotWriteError(t *testing.T) {
	h := NewHandler(nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	w := &responseWriter{ResponseWriter: httptest.NewRecorder()}
	h.handleError(w, fmt.Errorf("request: %w", context.Canceled))
	if w.status != 0 {
		t.Fatalf("wrote HTTP status %d for cancelled request", w.status)
	}
}
