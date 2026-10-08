package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// Описывает HTTP-операцию изменения заказа для общих проверок.
type statusEndpoint struct {
	name, method, suffix, body string
	status                     model.OrderStatus
}

var statusEndpoints = []statusEndpoint{
	{"cancel", http.MethodPost, "cancel", `{"expected_version":7}`, model.StatusCancelled},
	{"status", http.MethodPatch, "status", `{"expected_version":7,"status":"cooking"}`, model.StatusCooking},
}

// Готовит запрос с заголовками владельца и сотрудника.
func statusRequest(ep statusEndpoint, body string) *http.Request {
	r := httptest.NewRequest(ep.method, "/v1/orders/"+handlerOrderFixture().ID+"/"+ep.suffix, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Demo-User-ID", orderUserID)
	r.Header.Set("X-Demo-Role", "staff")
	return r
}

// Проверяет безопасный формат JSON-ошибки и обязательные заголовки.
func assertStatusHTTPError(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var body dto.ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("status=%d body=%s: %v", w.Code, w.Body.String(), err)
	}
	if w.Code != status || body.Code != code || body.Message == "" || body.RequestID == "" || body.RequestID != w.Header().Get("X-Request-ID") {
		t.Fatalf("status=%d body=%+v want=%d/%s", w.Code, body, status, code)
	}
	if w.Header().Get("Content-Type") != "application/json" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("missing JSON headers")
	}
	if strings.Contains(w.Body.String(), "private SQL") || strings.Contains(w.Body.String(), handlerOrderFixture().ID) || w.Header().Get("Location") != "" {
		t.Fatalf("leaked response: %s", w.Body.String())
	}
}

// Проверяет аргументы, ограничение deadline и возврат обновлённого заказа.
func TestOrderStatusHTTPSuccess(t *testing.T) {
	for _, ep := range statusEndpoints {
		for _, shortDeadline := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/short deadline=%t", ep.name, shortDeadline), func(t *testing.T) {
				parent := context.Background()
				if shortDeadline {
					var cancel context.CancelFunc
					parent, cancel = context.WithTimeout(parent, 500*time.Millisecond)
					defer cancel()
				}
				order := handlerOrderFixture()
				order.Status = ep.status
				order.Version = 8
				order.UpdatedAt = order.UpdatedAt.Add(time.Minute)
				calls := 0
				var calledCtx context.Context
				check := func(ctx context.Context, id string, version int64) (model.Order, error) {
					calls++
					calledCtx = ctx
					if id != order.ID || version != 7 {
						t.Fatalf("id=%s version=%d", id, version)
					}
					deadline, ok := ctx.Deadline()
					if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 2*time.Second {
						t.Fatal("missing bounded context")
					}
					if want, ok := parent.Deadline(); ok && !deadline.Equal(want) {
						t.Fatal("parent deadline was extended")
					}
					return order, nil
				}
				stub := orderServiceStub{
					cancel: func(ctx context.Context, userID, id string, version int64) (model.Order, error) {
						if ep.name != "cancel" || userID != order.UserID {
							t.Fatalf("wrong cancel arguments: %s", userID)
						}
						return check(ctx, id, version)
					},
					updateStatus: func(ctx context.Context, isStaff bool, id string, version int64, status model.OrderStatus) (model.Order, error) {
						if ep.name != "status" || !isStaff || status != ep.status {
							t.Fatalf("wrong update arguments: staff=%t status=%s", isStaff, status)
						}
						return check(ctx, id, version)
					},
				}
				r := statusRequest(ep, ep.body).WithContext(parent)
				// Отмене не нужна роль сотрудника, сотруднику не нужен заголовок владельца.
				if ep.name == "cancel" {
					r.Header.Del("X-Demo-Role")
				} else {
					r.Header.Del("X-Demo-User-ID")
				}
				w := httptest.NewRecorder()
				orderHandlerForTest(stub).ServeHTTP(w, r)
				var got dto.OrderResponse
				if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if w.Code != 200 || calls != 1 || !reflect.DeepEqual(got, dto.ToOrderResponse(order)) {
					t.Fatalf("status=%d calls=%d got=%+v", w.Code, calls, got)
				}
				if w.Header().Get("Content-Type") != "application/json" || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Request-ID") == "" || w.Header().Get("Location") != "" {
					t.Fatal("wrong response headers")
				}
				if !errors.Is(calledCtx.Err(), context.Canceled) {
					t.Fatal("handler did not release context")
				}
			})
		}
	}
}

// Проверяет ошибочные тела, запрет лишних полей и лимит до вызова сервиса.
func TestOrderStatusHTTPInvalidBody(t *testing.T) {
	for _, ep := range statusEndpoints {
		for _, tc := range []struct {
			name, body, contentType string
			status                  int
			code                    string
		}{
			{"empty", "", "application/json", 400, "invalid_input"},
			{"null", "null", "application/json", 400, "invalid_input"},
			{"array", "[]", "application/json", 400, "invalid_input"},
			{"truncated", "{", "application/json", 400, "invalid_input"},
			{"string version", `{"expected_version":"7"}`, "application/json", 400, "invalid_input"},
			{"fractional version", `{"expected_version":1.5}`, "application/json", 400, "invalid_input"},
			{"overflow", `{"expected_version":9223372036854775808}`, "application/json", 400, "invalid_input"},
			{"unknown field", `{"expected_version":7,"is_staff":true}`, "application/json", 400, "invalid_input"},
			{"second object", ep.body + " {}", "application/json", 400, "invalid_input"},
			{"second null", ep.body + " null", "application/json", 400, "invalid_input"},
			{"trailing garbage", ep.body + " garbage", "application/json", 400, "invalid_input"},
			{"missing content type", ep.body, "", 415, "unsupported_media_type"},
			{"wrong content type", ep.body, "text/plain", 415, "unsupported_media_type"},
			{"invalid content type", ep.body, "application/json; charset", 415, "unsupported_media_type"},
			{"one byte over limit", ep.body + strings.Repeat(" ", maxRequestBytes-len(ep.body)+1), "application/json", 413, "body_too_large"},
		} {
			t.Run(ep.name+"/"+tc.name, func(t *testing.T) {
				r := statusRequest(ep, tc.body)
				r.Header.Set("Content-Type", tc.contentType)
				body := &trackedBody{Reader: strings.NewReader(tc.body)}
				r.Body = body
				w := httptest.NewRecorder()
				orderHandlerForTest(nil).ServeHTTP(w, r)
				assertStatusHTTPError(t, w, tc.status, tc.code)
				if !body.closed {
					t.Fatal("body not closed")
				}
			})
		}
	}
}

// Проверяет допустимость тела ровно в 64 KiB и параметра charset.
func TestOrderStatusHTTPBodyAtLimit(t *testing.T) {
	for _, ep := range statusEndpoints {
		t.Run(ep.name, func(t *testing.T) {
			calls := 0
			result := func() (model.Order, error) { calls++; return handlerOrderFixture(), nil }
			stub := orderServiceStub{
				cancel:       func(context.Context, string, string, int64) (model.Order, error) { return result() },
				updateStatus: func(context.Context, bool, string, int64, model.OrderStatus) (model.Order, error) { return result() },
			}
			r := statusRequest(ep, ep.body+strings.Repeat(" ", maxRequestBytes-len(ep.body)))
			r.Header.Set("Content-Type", "application/json; charset=utf-8")
			w := httptest.NewRecorder()
			orderHandlerForTest(stub).ServeHTTP(w, r)
			if w.Code != 200 || calls != 1 {
				t.Fatalf("status=%d calls=%d body=%s", w.Code, calls, w.Body.String())
			}
		})
	}
}

// Проверяет отказ без demo-роли до разбора некорректного тела и вызова сервиса.
func TestUpdateOrderStatusHTTPForbidden(t *testing.T) {
	for _, role := range []string{"", "user", "admin", "STAFF", " staff "} {
		t.Run(role, func(t *testing.T) {
			r := statusRequest(statusEndpoints[1], "{")
			r.Header.Set("X-Demo-Role", role)
			r.Header.Del("Content-Type")
			w := httptest.NewRecorder()
			orderHandlerForTest(nil).ServeHTTP(w, r)
			assertStatusHTTPError(t, w, 403, "forbidden")
		})
	}
}

// Проверяет перевод ошибок сервиса без возврата частичного заказа.
func TestOrderStatusHTTPServiceErrors(t *testing.T) {
	for _, ep := range statusEndpoints {
		for _, tc := range []struct {
			cause  error
			status int
			code   string
		}{
			{orderserrors.ErrInvalidInput, 400, "invalid_input"}, {orderserrors.ErrForbidden, 403, "forbidden"},
			{orderserrors.ErrNotFound, 404, "not_found"}, {orderserrors.ErrVersionConflict, 409, "version_conflict"},
			{orderserrors.ErrInvalidTransition, 409, "invalid_transition"}, {orderserrors.ErrDatabaseUnavailable, 503, "unavailable"},
			{context.DeadlineExceeded, 503, "unavailable"}, {errors.New("private SQL"), 500, "internal_error"},
		} {
			t.Run(ep.name+"/"+tc.cause.Error(), func(t *testing.T) {
				calls := 0
				result := func() (model.Order, error) {
					calls++
					return handlerOrderFixture(), fmt.Errorf("private SQL: %w", tc.cause)
				}
				stub := orderServiceStub{
					cancel:       func(context.Context, string, string, int64) (model.Order, error) { return result() },
					updateStatus: func(context.Context, bool, string, int64, model.OrderStatus) (model.Order, error) { return result() },
				}
				w := httptest.NewRecorder()
				orderHandlerForTest(stub).ServeHTTP(w, statusRequest(ep, ep.body))
				assertStatusHTTPError(t, w, tc.status, tc.code)
				if calls != 1 {
					t.Fatalf("calls=%d", calls)
				}
			})
		}
	}
}

// Проверяет передачу отмены запроса без записи HTTP-ответа.
func TestOrderStatusHTTPCancellation(t *testing.T) {
	for _, ep := range statusEndpoints {
		t.Run(ep.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			calls := 0
			check := func(ctx context.Context) (model.Order, error) {
				calls++
				if !errors.Is(ctx.Err(), context.Canceled) {
					t.Fatal("cancellation lost")
				}
				return model.Order{}, ctx.Err()
			}
			stub := orderServiceStub{
				cancel: func(ctx context.Context, _ string, _ string, _ int64) (model.Order, error) { return check(ctx) },
				updateStatus: func(ctx context.Context, _ bool, _ string, _ int64, _ model.OrderStatus) (model.Order, error) {
					return check(ctx)
				},
			}
			w := &responseWriter{ResponseWriter: httptest.NewRecorder()}
			orderHandlerForTest(stub).ServeHTTP(w, statusRequest(ep, ep.body).WithContext(ctx))
			if calls != 1 || w.status != 0 {
				t.Fatalf("calls=%d written status=%d", calls, w.status)
			}
		})
	}
}

// Проверяет JSON-ответ 405 и Allow для новых маршрутов.
func TestOrderStatusHTTPMethodNotAllowed(t *testing.T) {
	for _, ep := range statusEndpoints {
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete, http.MethodOptions} {
			if method == ep.method {
				continue
			}
			t.Run(ep.name+"/"+method, func(t *testing.T) {
				r := statusRequest(ep, ep.body)
				r.Method = method
				w := httptest.NewRecorder()
				orderHandlerForTest(nil).ServeHTTP(w, r)
				assertStatusHTTPError(t, w, 405, "method_not_allowed")
				if w.Header().Get("Allow") != ep.method {
					t.Fatalf("Allow=%q", w.Header().Get("Allow"))
				}
			})
		}
	}
}

// Подменяет только чтение для проверки сокрытия чужого заказа настоящим сервисом.
type statusHTTPRepositoryStub struct {
	ordersservice.Repository
	order model.Order
}

// Возвращает заказ другого владельца; изменение через встроенный nil-интерфейс недопустимо.
func (r statusHTTPRepositoryStub) GetByID(context.Context, string) (model.Order, error) {
	return r.order, nil
}

// Проверяет связь обработчиков с валидацией и правами настоящего сервиса.
func TestOrderStatusHTTPRealService(t *testing.T) {
	for _, ep := range statusEndpoints {
		for _, body := range []string{`{}`, `{"expected_version":0}`, `{"expected_version":-1}`, `{"expected_version":null}`, `{"expected_version":9223372036854775807}`, `{"expected_version":1,"status":"unknown"}`} {
			t.Run(ep.name+"/"+body, func(t *testing.T) {
				w := httptest.NewRecorder()
				orderHandlerForTest(ordersservice.NewService(nil, nil)).ServeHTTP(w, statusRequest(ep, body))
				assertStatusHTTPError(t, w, 400, "invalid_input")
			})
		}
	}
	t.Run("missing owner", func(t *testing.T) {
		r := statusRequest(statusEndpoints[0], statusEndpoints[0].body)
		r.Header.Del("X-Demo-User-ID")
		w := httptest.NewRecorder()
		orderHandlerForTest(ordersservice.NewService(nil, nil)).ServeHTTP(w, r)
		assertStatusHTTPError(t, w, 400, "invalid_input")
	})
	t.Run("foreign order", func(t *testing.T) {
		order := handlerOrderFixture()
		order.UserID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
		service := ordersservice.NewService(statusHTTPRepositoryStub{order: order}, nil)
		w := httptest.NewRecorder()
		orderHandlerForTest(service).ServeHTTP(w, statusRequest(statusEndpoints[0], statusEndpoints[0].body))
		assertStatusHTTPError(t, w, 404, "not_found")
	})
}
