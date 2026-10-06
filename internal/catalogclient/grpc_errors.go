package catalogclient

import (
	"context"
	"fmt"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Преобразует ошибку RPC в ошибку сервиса заказов.
func mapGRPCError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	switch status.Code(err) {
	case codes.InvalidArgument:
		return fmt.Errorf("catalog rejected items: %w", orderserrors.ErrInvalidInput)
	case codes.NotFound:
		return fmt.Errorf("catalog restaurant not found: %w", orderserrors.ErrNotFound)
	default:
		return fmt.Errorf("resolve catalog items: %w", orderserrors.ErrCatalogUnavailable)
	}
}
