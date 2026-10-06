package catalogclient

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
	"github.com/KolibriRDM/orders-service/internal/service"
	"github.com/KolibriRDM/orders-service/internal/transport/grpc/catalogv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/status"
)

// Проверяет соответствие клиента контракту бизнес-сервиса.
var _ service.CatalogProvider = (*Client)(nil)

const restaurantID = "00000000-0000-4000-8000-000000000001"
const firstID = "00000000-0000-4000-8000-000000000101"
const secondID = "00000000-0000-4000-8000-000000000103"

// Подменяет RPC-вызов каталога управляемой функцией.
type catalogRPCStub func(context.Context, *catalogv1.ResolveItemsRequest, ...grpc.CallOption) (*catalogv1.ResolveItemsResponse, error)

// Передаёт RPC тестовой функции.
func (f catalogRPCStub) ResolveItems(ctx context.Context, req *catalogv1.ResolveItemsRequest, opts ...grpc.CallOption) (*catalogv1.ResolveItemsResponse, error) {
	return f(ctx, req, opts...)
}

// Проверяет сборку клиента, ошибку конфигурации и закрытие соединения.
func TestNewClient(t *testing.T) {
	client, err := NewClient("127.0.0.1:9082")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if client.conn == nil || client.api == nil {
		t.Fatal("client dependencies are missing")
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if got := client.conn.GetState(); got != connectivity.Shutdown {
		t.Fatalf("connection state=%v, want SHUTDOWN", got)
	}
	items, err := client.ResolveItems(context.Background(), restaurantID, []string{firstID})
	if items != nil || !errors.Is(err, orderserrors.ErrCatalogUnavailable) {
		t.Fatalf("closed client: items=%v error=%v", items, err)
	}
	invalid, err := NewClient("http://localhost:9082")
	if invalid != nil {
		_ = invalid.Close()
		t.Fatal("invalid address returned a client")
	}
	if err == nil {
		t.Fatal("expected configuration error")
	}
}

// Проверяет параметры RPC, deadline, порядок ответа и освобождение контекста.
func TestResolveItemsRequestAndResult(t *testing.T) {
	ids := []string{firstID, secondID}
	calls := 0
	var rpcCtx context.Context
	client := &Client{api: catalogRPCStub(func(ctx context.Context, request *catalogv1.ResolveItemsRequest, _ ...grpc.CallOption) (*catalogv1.ResolveItemsResponse, error) {
		calls++
		rpcCtx = ctx
		if request.GetRestaurantId() != restaurantID || !reflect.DeepEqual(request.GetItemIds(), ids) {
			t.Fatalf("unexpected request: %v", request)
		}
		deadline, ok := ctx.Deadline()
		if remaining := time.Until(deadline); !ok || remaining <= 0 || remaining > catalogRequestTimeout {
			t.Fatalf("unexpected RPC deadline: present=%v remaining=%v", ok, remaining)
		}
		return validGRPCResponse(), nil
	})}
	items, err := client.ResolveItems(context.Background(), restaurantID, ids)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(items) != 2 || items[0].ID != firstID || items[1].ID != secondID ||
		items[0].Name != "Маргарита" || items[0].PriceKopecks != 25000 || items[1].PriceKopecks != 10000 {
		t.Fatalf("calls=%d items=%v", calls, items)
	}
	for _, item := range items {
		if item.RestaurantID != restaurantID || !item.IsAvailable {
			t.Fatalf("incorrect item: %v", item)
		}
	}
	if !reflect.DeepEqual(ids, []string{firstID, secondID}) {
		t.Fatal("input IDs were modified")
	}
	if !errors.Is(rpcCtx.Err(), context.Canceled) {
		t.Fatal("RPC context was not released after completion")
	}
}

// Проверяет отклонение неверных параметров до RPC.
func TestResolveItemsInvalidInputDoesNotSend(t *testing.T) {
	tooMany := make([]string, 21)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("item-%d", i)
	}
	for _, tc := range []struct {
		name, restaurant string
		ids              []string
	}{
		{"empty restaurant", "", []string{firstID}},
		{"no items", restaurantID, nil},
		{"empty ID", restaurantID, []string{firstID, ""}},
		{"duplicates", restaurantID, []string{firstID, firstID}},
		{"too many items", restaurantID, tooMany},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &Client{api: catalogRPCStub(func(context.Context, *catalogv1.ResolveItemsRequest, ...grpc.CallOption) (*catalogv1.ResolveItemsResponse, error) {
				t.Fatal("unexpected RPC call")
				return nil, nil
			})}
			items, err := client.ResolveItems(context.Background(), tc.restaurant, tc.ids)
			if items != nil || !errors.Is(err, orderserrors.ErrInvalidInput) {
				t.Fatalf("items=%v error=%v", items, err)
			}
		})
	}
}

// Проверяет отмену и истёкший deadline до обращения к каталогу.
func TestResolveItemsAlreadyCanceled(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelExpired()
	for _, ctx := range []context.Context{canceled, expired} {
		client := &Client{api: catalogRPCStub(func(context.Context, *catalogv1.ResolveItemsRequest, ...grpc.CallOption) (*catalogv1.ResolveItemsResponse, error) {
			t.Fatal("unexpected RPC call")
			return nil, nil
		})}
		items, err := client.ResolveItems(ctx, restaurantID, []string{firstID})
		if items != nil || !errors.Is(err, ctx.Err()) {
			t.Fatalf("items=%v error=%v, want %v", items, err, ctx.Err())
		}
	}
}

// Проверяет приоритет отмены исходного запроса над поздним ответом RPC.
func TestResolveItemsCanceledDuringRPC(t *testing.T) {
	for _, rpcErr := range []error{nil, status.Error(codes.InvalidArgument, "rejected")} {
		ctx, cancel := context.WithCancel(context.Background())
		client := &Client{api: catalogRPCStub(func(context.Context, *catalogv1.ResolveItemsRequest, ...grpc.CallOption) (*catalogv1.ResolveItemsResponse, error) {
			cancel()
			return validGRPCResponse(), rpcErr
		})}
		items, err := client.ResolveItems(ctx, restaurantID, []string{firstID, secondID})
		cancel()
		if items != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("items=%v error=%v", items, err)
		}
	}
}

// Проверяет сохранение более раннего deadline исходного запроса.
func TestResolveItemsPreservesCallerDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	wantDeadline, _ := ctx.Deadline()
	client := &Client{api: catalogRPCStub(func(rpcCtx context.Context, _ *catalogv1.ResolveItemsRequest, _ ...grpc.CallOption) (*catalogv1.ResolveItemsResponse, error) {
		if got, ok := rpcCtx.Deadline(); !ok || !got.Equal(wantDeadline) {
			t.Fatalf("deadline=%v, want %v", got, wantDeadline)
		}
		return validGRPCResponse(), nil
	})}
	if _, err := client.ResolveItems(ctx, restaurantID, []string{firstID, secondID}); err != nil {
		t.Fatal(err)
	}
}

// Проверяет ограничение ожидания каталога и отмену по deadline вызывающего кода.
func TestResolveItemsRPCTimeout(t *testing.T) {
	for _, tc := range []struct {
		name    string
		timeout time.Duration
		want    error
	}{
		{"catalog timeout", 3 * time.Second, orderserrors.ErrCatalogUnavailable},
		{"caller timeout", 50 * time.Millisecond, context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), tc.timeout)
			defer cancel()
			client := &Client{api: catalogRPCStub(func(rpcCtx context.Context, _ *catalogv1.ResolveItemsRequest, _ ...grpc.CallOption) (*catalogv1.ResolveItemsResponse, error) {
				<-rpcCtx.Done()
				return nil, status.FromContextError(rpcCtx.Err()).Err()
			})}
			items, err := client.ResolveItems(ctx, restaurantID, []string{firstID})
			if items != nil || !errors.Is(err, tc.want) {
				t.Fatalf("items=%v error=%v, want %v", items, err, tc.want)
			}
		})
	}
}

// Проверяет перевод ошибок без повторного вызова или частичного результата.
func TestResolveItemsRPCErrors(t *testing.T) {
	for _, tc := range []struct {
		code codes.Code
		want error
	}{
		{codes.InvalidArgument, orderserrors.ErrInvalidInput},
		{codes.NotFound, orderserrors.ErrNotFound},
		{codes.Unavailable, orderserrors.ErrCatalogUnavailable},
		{codes.Internal, orderserrors.ErrCatalogUnavailable},
		{codes.DeadlineExceeded, orderserrors.ErrCatalogUnavailable},
	} {
		t.Run(tc.code.String(), func(t *testing.T) {
			calls := 0
			client := &Client{api: catalogRPCStub(func(context.Context, *catalogv1.ResolveItemsRequest, ...grpc.CallOption) (*catalogv1.ResolveItemsResponse, error) {
				calls++
				return validGRPCResponse(), status.Error(tc.code, "secret-canary")
			})}
			items, err := client.ResolveItems(context.Background(), restaurantID, []string{firstID, secondID})
			if items != nil || !errors.Is(err, tc.want) || calls != 1 {
				t.Fatalf("items=%v error=%v calls=%d", items, err, calls)
			}
			if strings.Contains(err.Error(), "secret-canary") {
				t.Fatal("remote error details leaked")
			}
		})
	}
}

// Проверяет передачу protobuf-ответа в валидатор блюд.
func TestResolveItemsRejectsInvalidResponse(t *testing.T) {
	invalid := validGRPCResponse()
	invalid.Items[1].Id = invalid.Items[0].Id
	for _, response := range []*catalogv1.ResolveItemsResponse{nil, invalid} {
		client := &Client{api: catalogRPCStub(func(context.Context, *catalogv1.ResolveItemsRequest, ...grpc.CallOption) (*catalogv1.ResolveItemsResponse, error) {
			return response, nil
		})}
		items, err := client.ResolveItems(context.Background(), restaurantID, []string{firstID, secondID})
		if items != nil || !errors.Is(err, orderserrors.ErrCatalogUnavailable) {
			t.Fatalf("items=%v error=%v", items, err)
		}
	}
}
