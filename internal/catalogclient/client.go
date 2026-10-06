package catalogclient

import (
	"context"
	"fmt"
	"time"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
	"github.com/KolibriRDM/orders-service/internal/model"
	"github.com/KolibriRDM/orders-service/internal/transport/grpc/catalogv1"
	"google.golang.org/grpc"
)

// Переиспользуемый gRPC-клиент внутреннего API каталога.
type Client struct {
	conn *grpc.ClientConn
	api  catalogv1.CatalogServiceClient
}

// Создаёт соединение и клиент внутреннего API каталога.
func NewClient(address string) (*Client, error) {
	conn, err := newGRPCConnection(address)
	if err != nil {
		return nil, err
	}
	api := catalogv1.NewCatalogServiceClient(conn)
	return &Client{
		conn: conn,
		api:  api,
	}, nil
}

// Ограничивает время одного обращения к каталогу.
const catalogRequestTimeout = 500 * time.Millisecond

// Получает проверенные блюда каталога через gRPC.
func (c *Client) ResolveItems(ctx context.Context, restaurantID string, itemIDs []string) ([]model.MenuItem, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("resolve items: %w", err)
	}
	// Уникальные ID позволяют однозначно сопоставить запрос и ответ.
	if restaurantID == "" || len(itemIDs) < 1 || len(itemIDs) > 20 {
		return nil, fmt.Errorf("invalid catalog resolve parameters: %w", orderserrors.ErrInvalidInput)
	}
	requested := make(map[string]struct{}, len(itemIDs))
	for _, id := range itemIDs {
		if _, exists := requested[id]; exists || id == "" {
			return nil, fmt.Errorf("invalid catalog item IDs: %w", orderserrors.ErrInvalidInput)
		}
		requested[id] = struct{}{}
	}
	// Ограничиваем RPC, сохраняя отмену исходного запроса.
	rpcCtx, cancel := context.WithTimeout(ctx, catalogRequestTimeout)
	defer cancel()
	request := &catalogv1.ResolveItemsRequest{
		RestaurantId: restaurantID,
		ItemIds:      itemIDs,
	}
	response, err := c.api.ResolveItems(rpcCtx, request)
	if ctx.Err() != nil {
		return nil, fmt.Errorf("resolve items context error: %w", ctx.Err())
	}
	if err != nil {
		return nil, mapGRPCError(ctx, err)
	}
	if rpcCtx.Err() != nil {
		return nil, mapGRPCError(ctx, rpcCtx.Err())
	}
	return toMenuItems(restaurantID, itemIDs, response)
}

// Закрывает соединение с каталогом при остановке orders.
func (c *Client) Close() error {
	return c.conn.Close()
}
