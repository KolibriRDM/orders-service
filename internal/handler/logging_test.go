package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
	"github.com/KolibriRDM/orders-service/internal/model"
)

// Связывает запрос с заказом и отличает создание от повтора без утечки данных.
func TestCreateOrderJSONLog(t *testing.T) {
	for _, mode := range []string{"created", "duplicate", "error"} {
		t.Run(mode, func(t *testing.T) {
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil)).With("service", "orders-service")
			svc := orderServiceStub{create: func(context.Context, model.CreateOrderInput) (model.CreateOrderResult, error) {
				if mode == "error" {
					return model.CreateOrderResult{}, orderserrors.ErrDatabaseUnavailable
				}
				return model.CreateOrderResult{Order: handlerOrderFixture(), Created: mode == "created"}, nil
			}}
			router := NewRouter(logger, nil, NewHandler(svc, rateLimiterFunc(allowAllOrders), logger), newHTTPMetricsForTest())
			req := rateLimitRequest()
			req.Header.Set("Idempotency-Key", "private-key-canary")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			matches := 0
			for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
				var entry map[string]any
				if err := json.Unmarshal(line, &entry); err != nil {
					t.Fatal(err)
				}
				if entry["msg"] != "order creation handled" {
					continue
				}
				matches++
				if entry["service"] != "orders-service" || entry["order_id"] != handlerOrderFixture().ID || entry["request_id"] != w.Header().Get("X-Request-ID") || entry["request_id"] == "" || entry["created"] != (mode == "created") {
					t.Fatalf("entry=%v", entry)
				}
			}
			want := 1
			if mode == "error" {
				want = 0
			}
			if matches != want {
				t.Fatalf("matches=%d logs=%s", matches, &logs)
			}
			for _, private := range []string{"private-key-canary", "delivery_address", handlerOrderFixture().DeliveryAddress} {
				if strings.Contains(logs.String(), private) {
					t.Fatalf("private data in log: %s", &logs)
				}
			}
		})
	}
}
