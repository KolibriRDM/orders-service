package catalogclient

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
	"github.com/KolibriRDM/orders-service/internal/transport/grpc/catalogv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// Подменяет сервер каталога для проверки настоящего gRPC-транспорта.
type catalogServerStub struct {
	catalogv1.UnimplementedCatalogServiceServer
	resolve func(context.Context, *catalogv1.ResolveItemsRequest) (*catalogv1.ResolveItemsResponse, error)
}

// Передаёт сетевой RPC обработчику тестового сценария.
func (s *catalogServerStub) ResolveItems(ctx context.Context, req *catalogv1.ResolveItemsRequest) (*catalogv1.ResolveItemsResponse, error) {
	return s.resolve(ctx, req)
}

// Запускает локальный gRPC-сервер и создаёт клиент с рабочими настройками.
func newGRPCTestClient(t *testing.T, resolve func(context.Context, *catalogv1.ResolveItemsRequest) (*catalogv1.ResolveItemsResponse, error)) *Client {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	catalogv1.RegisterCatalogServiceServer(server, &catalogServerStub{resolve: resolve})
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
		if err := <-serveDone; err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			t.Error(err)
		}
	})
	client, err := NewClient(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// Проверяет совместное использование соединения несколькими горутинами.
func TestResolveItemsConcurrentUse(t *testing.T) {
	var calls atomic.Int32
	client := newGRPCTestClient(t, func(_ context.Context, req *catalogv1.ResolveItemsRequest) (*catalogv1.ResolveItemsResponse, error) {
		calls.Add(1)
		if req.GetRestaurantId() != restaurantID || len(req.GetItemIds()) != 2 || req.ItemIds[0] != firstID || req.ItemIds[1] != secondID {
			return nil, status.Error(codes.InvalidArgument, "unexpected request")
		}
		return validGRPCResponse(), nil
	})
	results := make(chan error, 8)
	for i := 0; i < cap(results); i++ {
		go func() {
			items, err := client.ResolveItems(context.Background(), restaurantID, []string{firstID, secondID})
			if err == nil && (len(items) != 2 || items[0].ID != firstID || items[1].ID != secondID) {
				err = fmt.Errorf("unexpected response: %v", items)
			}
			results <- err
		}()
	}
	for i := 0; i < cap(results); i++ {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
	if calls.Load() != 8 {
		t.Fatalf("calls=%d, want 8", calls.Load())
	}
}

// Проверяет лимит ответа на границе 64 КиБ и при превышении на один байт.
func TestResolveItemsGRPCResponseSize(t *testing.T) {
	for _, size := range []int{maxGRPCMessageBytes, maxGRPCMessageBytes + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			response := &catalogv1.ResolveItemsResponse{
				RestaurantId: restaurantID,
				Items:        []*catalogv1.ResolvedItem{{Id: firstID, Name: strings.Repeat("x", size), PriceKopecks: 1}},
			}
			// Учитываем служебные поля protobuf при подборе точного размера сообщения.
			nameLength := size - (proto.Size(response) - size)
			response.Items[0].Name = response.Items[0].Name[:nameLength]
			if got := proto.Size(response); got != size {
				t.Fatalf("test response size=%d, want %d", got, size)
			}
			client := newGRPCTestClient(t, func(context.Context, *catalogv1.ResolveItemsRequest) (*catalogv1.ResolveItemsResponse, error) {
				return response, nil
			})
			items, err := client.ResolveItems(context.Background(), restaurantID, []string{firstID})
			if size > maxGRPCMessageBytes {
				if items != nil || !errors.Is(err, orderserrors.ErrCatalogUnavailable) {
					t.Fatalf("items=%v error=%v, want catalog unavailable", items, err)
				}
				return
			}
			if err != nil || len(items) != 1 || len(items[0].Name) != nameLength {
				t.Fatalf("boundary response: count=%d error=%v", len(items), err)
			}
		})
	}
}

// Проверяет отклонение слишком большого запроса до серверного обработчика.
func TestResolveItemsGRPCRequestSize(t *testing.T) {
	var calls atomic.Int32
	client := newGRPCTestClient(t, func(context.Context, *catalogv1.ResolveItemsRequest) (*catalogv1.ResolveItemsResponse, error) {
		calls.Add(1)
		return nil, status.Error(codes.InvalidArgument, "unexpected call")
	})
	items, err := client.ResolveItems(context.Background(), restaurantID, []string{strings.Repeat("x", maxGRPCMessageBytes)})
	if items != nil || !errors.Is(err, orderserrors.ErrCatalogUnavailable) || calls.Load() != 0 {
		t.Fatalf("items=%v error=%v calls=%d", items, err, calls.Load())
	}
}

// Проверяет отсутствие повторной обработки RPC после ответа Unavailable.
func TestResolveItemsGRPCUnavailableDoesNotRetry(t *testing.T) {
	var calls atomic.Int32
	client := newGRPCTestClient(t, func(context.Context, *catalogv1.ResolveItemsRequest) (*catalogv1.ResolveItemsResponse, error) {
		calls.Add(1)
		return nil, status.Error(codes.Unavailable, "secret-canary")
	})
	items, err := client.ResolveItems(context.Background(), restaurantID, []string{firstID})
	if items != nil || !errors.Is(err, orderserrors.ErrCatalogUnavailable) || calls.Load() != 1 {
		t.Fatalf("items=%v error=%v calls=%d", items, err, calls.Load())
	}
	if strings.Contains(err.Error(), "secret-canary") {
		t.Fatal("remote error details leaked")
	}
}

// Проверяет доставку отмены запроса до серверного gRPC-обработчика.
func TestResolveItemsGRPCCancellation(t *testing.T) {
	started, stopped := make(chan struct{}), make(chan struct{})
	client := newGRPCTestClient(t, func(ctx context.Context, _ *catalogv1.ResolveItemsRequest) (*catalogv1.ResolveItemsResponse, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return nil, status.FromContextError(ctx.Err()).Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := client.ResolveItems(ctx, restaurantID, []string{firstID})
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("RPC did not start")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v, want cancellation", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("client did not finish")
	}
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("server did not receive cancellation")
	}
}
