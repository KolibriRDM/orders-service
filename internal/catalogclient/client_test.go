package catalogclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
	"github.com/KolibriRDM/orders-service/internal/service"
)

var _ service.CatalogProvider = (*Client)(nil)

const restaurantID = "00000000-0000-4000-8000-000000000001"
const firstID = "00000000-0000-4000-8000-000000000101"
const secondID = "00000000-0000-4000-8000-000000000103"

// Готовит корректный ответ каталога для тестов.
func validResponse() resolveItemsResponse {
	return resolveItemsResponse{RestaurantID: restaurantID, Items: []resolvedItemResponse{
		{ID: secondID, Name: "Морс", PriceKopecks: 10000},
		{ID: firstID, Name: "Маргарита", PriceKopecks: 25000},
	}}
}

// Сериализует тестовый ответ каталога в JSON.
func responseJSON(t *testing.T, response resolveItemsResponse) string {
	t.Helper()
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// Создаёт HTTP-клиент и локальный тестовый сервер.
func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// Проверяет допустимые адреса HTTP-клиента каталога.
func TestNewClient(t *testing.T) {
	for _, raw := range []string{" http://localhost:8082/ ", "https://example.com", "http://[::1]:8082"} {
		t.Run(raw, func(t *testing.T) {
			client, err := NewClient(raw)
			if err != nil {
				t.Fatal(err)
			}
			if client.httpClient.Timeout != 500*time.Millisecond || !strings.HasSuffix(client.resolveURL, "/internal/v1/menu/resolve") {
				t.Fatal("incorrect client settings")
			}
		})
	}
	for _, raw := range []string{"", "localhost:8082", "ftp://host", "http:///path", "http://user:password-canary@host", "http://user:password-canary@host:invalid", "http://host?x=1", "http://host?", "http://host#part", "http://host#", "http://host/path", "http://host:0", "http://host:65536", "http://host:", "http://host:%"} {
		t.Run("invalid/"+raw, func(t *testing.T) {
			client, err := NewClient(raw)
			if err == nil || client != nil {
				t.Fatal("expected invalid URL error")
			}
			if strings.Contains(err.Error(), "password-canary") {
				t.Fatal("credentials leaked")
			}
		})
	}
}

// Проверяет HTTP-запрос и преобразование ответа каталога.
func TestResolveItemsRequestAndResult(t *testing.T) {
	type receivedRequest struct {
		method, path, contentType, accept string
		body                              resolveItemsRequest
		err                               error
	}
	received := make(chan receivedRequest, 1)
	response := responseJSON(t, validResponse())
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body resolveItemsRequest
		err := json.NewDecoder(r.Body).Decode(&body)
		received <- receivedRequest{r.Method, r.URL.Path, r.Header.Get("Content-Type"), r.Header.Get("Accept"), body, err}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		io.WriteString(w, response)
	})
	ids := []string{firstID, secondID}
	items, err := client.ResolveItems(context.Background(), restaurantID, ids)
	if err != nil {
		t.Fatal(err)
	}
	request := <-received
	if request.err != nil || request.method != "POST" || request.path != "/internal/v1/menu/resolve" || request.contentType != "application/json" || request.accept != "application/json" || request.body.RestaurantID != restaurantID || !reflect.DeepEqual(request.body.ItemIDs, ids) {
		t.Fatalf("request: %+v", request)
	}
	if len(items) != 2 || items[0].ID != firstID || items[0].Name != "Маргарита" || items[0].PriceKopecks != 25000 || items[1].ID != secondID || items[1].PriceKopecks != 10000 {
		t.Fatalf("items: %+v", items)
	}
	for _, item := range items {
		if item.RestaurantID != restaurantID || !item.IsAvailable {
			t.Fatalf("item: %+v", item)
		}
	}
	if !reflect.DeepEqual(ids, []string{firstID, secondID}) {
		t.Fatal("input mutated")
	}
}

// Проверяет отклонение некорректных ответов каталога.
func TestResolveItemsRejectsInvalidResponses(t *testing.T) {
	valid := responseJSON(t, validResponse())
	cases := []struct {
		name              string
		status            int
		contentType, body string
		want              error
	}{
		{"bad input", 400, "application/json", `{"code":"invalid_input","message":"secret-canary"}`, orderserrors.ErrInvalidInput},
		{"not found", 404, "application/json", `{"code":"not_found","message":"secret-canary"}`, orderserrors.ErrNotFound},
		{"unexpected error code", 400, "application/json", `{"code":"other"}`, orderserrors.ErrCatalogUnavailable},
		{"malformed error", 400, "application/json", `{`, orderserrors.ErrCatalogUnavailable},
		{"null error", 404, "application/json", `null`, orderserrors.ErrCatalogUnavailable},
		{"server error", 500, "application/json", `{"code":"internal_error"}`, orderserrors.ErrCatalogUnavailable},
		{"unavailable", 503, "application/json", `{"code":"unavailable"}`, orderserrors.ErrCatalogUnavailable},
		{"unexpected success", 201, "application/json", valid, orderserrors.ErrCatalogUnavailable},
		{"wrong content type", 200, "text/html", valid, orderserrors.ErrCatalogUnavailable},
		{"missing content type", 200, "", valid, orderserrors.ErrCatalogUnavailable},
		{"invalid content type", 200, "application/json;broken", valid, orderserrors.ErrCatalogUnavailable},
		{"malformed JSON", 200, "application/json", "{", orderserrors.ErrCatalogUnavailable},
		{"extra JSON", 200, "application/json", valid + " {}", orderserrors.ErrCatalogUnavailable},
		{"null JSON", 200, "application/json", "null", orderserrors.ErrCatalogUnavailable},
		{"array JSON", 200, "application/json", "[]", orderserrors.ErrCatalogUnavailable},
		{"empty object", 200, "application/json", "{}", orderserrors.ErrCatalogUnavailable},
		{"oversized response", 200, "application/json", valid + strings.Repeat(" ", maxResponseBytes), orderserrors.ErrCatalogUnavailable},
	}
	for _, mutate := range []struct {
		name string
		fn   func(*resolveItemsResponse)
	}{
		{"wrong restaurant", func(r *resolveItemsResponse) { r.RestaurantID = firstID }},
		{"missing item", func(r *resolveItemsResponse) { r.Items = r.Items[:1] }},
		{"extra item", func(r *resolveItemsResponse) { r.Items = append(r.Items, r.Items[0]) }},
		{"foreign item", func(r *resolveItemsResponse) { r.Items[0].ID = restaurantID }},
		{"duplicate item", func(r *resolveItemsResponse) { r.Items[1].ID = r.Items[0].ID }},
		{"blank name", func(r *resolveItemsResponse) { r.Items[0].Name = " \t\n" }},
		{"zero price", func(r *resolveItemsResponse) { r.Items[0].PriceKopecks = 0 }},
		{"negative price", func(r *resolveItemsResponse) { r.Items[0].PriceKopecks = -1 }},
		{"excessive price", func(r *resolveItemsResponse) { r.Items[0].PriceKopecks = 1000001 }},
	} {
		response := validResponse()
		mutate.fn(&response)
		cases = append(cases, struct {
			name              string
			status            int
			contentType, body string
			want              error
		}{mutate.name, 200, "application/json", responseJSON(t, response), orderserrors.ErrCatalogUnavailable})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header()["Content-Type"] = []string{tc.contentType}
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			})
			items, err := client.ResolveItems(context.Background(), restaurantID, []string{firstID, secondID})
			if !errors.Is(err, tc.want) || items != nil {
				t.Fatalf("items=%v err=%v", items, err)
			}
			if strings.Contains(err.Error(), "secret-canary") {
				t.Fatal("remote error details leaked")
			}
		})
	}
}

// Проверяет границы значений и дополнительные поля ответа.
func TestResolveItemsResponseBoundariesAndExtensions(t *testing.T) {
	response := validResponse()
	response.Items[0].PriceKopecks = 1
	response.Items[1].PriceKopecks = 1000000
	body := responseJSON(t, response)
	body = strings.TrimSuffix(body, "}") + `,"future_field":true}`
	body += strings.Repeat(" ", maxResponseBytes-len(body))
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body)
	})
	items, err := client.ResolveItems(context.Background(), restaurantID, []string{firstID, secondID})
	if err != nil || len(items) != 2 {
		t.Fatalf("items=%v err=%v", items, err)
	}
}

// Проверяет отмену и таймаут запроса к каталогу.
func TestResolveItemsCancellationAndTimeout(t *testing.T) {
	t.Run("already cancelled", func(t *testing.T) {
		var calls atomic.Int32
		client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := client.ResolveItems(ctx, restaurantID, []string{firstID})
		if !errors.Is(err, context.Canceled) || calls.Load() != 0 {
			t.Fatalf("err=%v calls=%d", err, calls.Load())
		}
	})
	for _, duringBody := range []bool{false, true} {
		t.Run(fmt.Sprintf("client timeout/body=%v", duringBody), func(t *testing.T) {
			client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				if duringBody {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(200)
					w.(http.Flusher).Flush()
				}
				select {
				case <-r.Context().Done():
				case <-time.After(3 * time.Second):
				}
			})
			started := time.Now()
			_, err := client.ResolveItems(context.Background(), restaurantID, []string{firstID})
			if !errors.Is(err, orderserrors.ErrCatalogUnavailable) || time.Since(started) > 3*time.Second {
				t.Fatalf("err=%v elapsed=%v", err, time.Since(started))
			}
		})
	}
	t.Run("caller cancellation in flight", func(t *testing.T) {
		started := make(chan struct{})
		client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body)
			close(started)
			select {
			case <-r.Context().Done():
			case <-time.After(3 * time.Second):
			}
		})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { _, err := client.ResolveItems(ctx, restaurantID, []string{firstID}); done <- err }()
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("request did not start")
		}
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
	t.Run("caller deadline", func(t *testing.T) {
		client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body)
			select {
			case <-r.Context().Done():
			case <-time.After(3 * time.Second):
			}
		})
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		_, err := client.ResolveItems(ctx, restaurantID, []string{firstID})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	})
}

// Проверяет отсутствие редиректов и автоматических повторов.
func TestResolveItemsDoesNotFollowRedirectsOrRetry(t *testing.T) {
	var requests, redirected atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
	defer destination.Close()
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	})
	_, err := client.ResolveItems(context.Background(), restaurantID, []string{firstID})
	if !errors.Is(err, orderserrors.ErrCatalogUnavailable) || requests.Load() != 1 || redirected.Load() != 0 {
		t.Fatalf("err=%v requests=%d redirected=%d", err, requests.Load(), redirected.Load())
	}
}

// Проверяет отказ до HTTP-вызова при неверных параметрах.
func TestResolveItemsInvalidInputDoesNotSend(t *testing.T) {
	var calls atomic.Int32
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	for _, ids := range [][]string{nil, {""}, {firstID, firstID}, make([]string, 21)} {
		items, err := client.ResolveItems(context.Background(), restaurantID, ids)
		if !errors.Is(err, orderserrors.ErrInvalidInput) || items != nil {
			t.Fatalf("items=%v err=%v", items, err)
		}
	}
	_, err := client.ResolveItems(context.Background(), "", []string{firstID})
	if !errors.Is(err, orderserrors.ErrInvalidInput) || calls.Load() != 0 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}

// Тестовая функция вместо HTTP-транспорта.
type roundTripFunc func(*http.Request) (*http.Response, error)

// Передаёт HTTP-запрос тестовой функции транспорта.
func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Тестовое тело с учётом вызова Close.
type trackedBody struct {
	io.Reader
	closed bool
}

// Отмечает закрытие тестового тела запроса или ответа.
func (b *trackedBody) Close() error { b.closed = true; return nil }

// Тестовый источник, имитирующий обрыв чтения.
type brokenReader struct{}

// Имитирует обрыв чтения для проверки обработки ошибки.
func (brokenReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

// Проверяет закрытие тела ответа при успехе и ошибке.
func TestResolveItemsClosesBody(t *testing.T) {
	for _, broken := range []bool{false, true} {
		t.Run(fmt.Sprint(broken), func(t *testing.T) {
			var reader io.Reader = strings.NewReader("invalid JSON")
			if broken {
				reader = brokenReader{}
			}
			body := &trackedBody{Reader: reader}
			client, _ := NewClient("http://catalog.test")
			client.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: body}, nil
			})
			_, err := client.ResolveItems(context.Background(), restaurantID, []string{firstID})
			if !errors.Is(err, orderserrors.ErrCatalogUnavailable) || !body.closed {
				t.Fatalf("err=%v closed=%v", err, body.closed)
			}
		})
	}
}

// Проверяет совместное использование клиента горутинами.
func TestResolveItemsConcurrentUse(t *testing.T) {
	body := responseJSON(t, validResponse())
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body)
	})
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() {
			items, err := client.ResolveItems(context.Background(), restaurantID, []string{firstID, secondID})
			if err == nil && (len(items) != 2 || items[0].ID != firstID) {
				err = errors.New("incorrect result")
			}
			results <- err
		}()
	}
	for i := 0; i < 8; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}
