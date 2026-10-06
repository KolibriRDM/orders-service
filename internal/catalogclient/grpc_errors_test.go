package catalogclient

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Проверяет перевод статусов и скрытие внутренних сообщений каталога.
func TestMapGRPCError(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want error
	}{
		{"success", nil, nil},
		{"invalid input", status.Error(codes.InvalidArgument, "secret-canary"), orderserrors.ErrInvalidInput},
		{"not found", status.Error(codes.NotFound, "secret-canary"), orderserrors.ErrNotFound},
		{"wrapped invalid input", fmt.Errorf("transport: %w", status.Error(codes.InvalidArgument, "secret-canary")), orderserrors.ErrInvalidInput},
		{"wrapped not found", fmt.Errorf("transport: %w", status.Error(codes.NotFound, "secret-canary")), orderserrors.ErrNotFound},
		{"unavailable", status.Error(codes.Unavailable, "secret-canary"), orderserrors.ErrCatalogUnavailable},
		{"internal", status.Error(codes.Internal, "secret-canary"), orderserrors.ErrCatalogUnavailable},
		{"RPC deadline", status.Error(codes.DeadlineExceeded, "secret-canary"), orderserrors.ErrCatalogUnavailable},
		{"remote cancellation", status.Error(codes.Canceled, "secret-canary"), orderserrors.ErrCatalogUnavailable},
		{"message limit", status.Error(codes.ResourceExhausted, "secret-canary"), orderserrors.ErrCatalogUnavailable},
		{"unknown error", errors.New("secret-canary"), orderserrors.ErrCatalogUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := mapGRPCError(context.Background(), tc.err)
			if !errors.Is(got, tc.want) {
				t.Fatalf("error = %v, want %v", got, tc.want)
			}
			if got != nil && strings.Contains(got.Error(), "secret-canary") {
				t.Fatal("catalog error details leaked")
			}
		})
	}
}

// Проверяет приоритет отмены исходного запроса над статусом RPC.
func TestMapGRPCErrorRequestContext(t *testing.T) {
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	deadlineCtx, cancelDeadline := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelDeadline()
	for _, tc := range []struct {
		name string
		ctx  context.Context
		want error
	}{
		{"canceled", canceledCtx, context.Canceled},
		{"deadline", deadlineCtx, context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, code := range []codes.Code{codes.InvalidArgument, codes.NotFound, codes.Unavailable} {
				got := mapGRPCError(tc.ctx, status.Error(code, "secret-canary"))
				if !errors.Is(got, tc.want) {
					t.Errorf("code=%v: error=%v, want %v", code, got, tc.want)
				}
			}
		})
	}
}

// Отличает таймаут вызова каталога от отмены исходного запроса.
func TestMapGRPCErrorRPCTimeout(t *testing.T) {
	requestCtx := context.Background()
	rpcCtx, cancel := context.WithDeadline(requestCtx, time.Now().Add(-time.Second))
	defer cancel()
	err := status.FromContextError(rpcCtx.Err()).Err()
	got := mapGRPCError(requestCtx, err)
	if !errors.Is(got, orderserrors.ErrCatalogUnavailable) || errors.Is(got, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want catalog unavailable", got)
	}
}
