package catalogclient

import (
	"fmt"
	"strings"

	"github.com/KolibriRDM/orders-service/internal/model"
	"github.com/KolibriRDM/orders-service/internal/transport/grpc/catalogv1"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
)

// Проверяет protobuf-ответ и возвращает блюда в порядке запроса.
func toMenuItems(
	restaurantID string,
	itemIDs []string,
	response *catalogv1.ResolveItemsResponse,
) ([]model.MenuItem, error) {
	if response == nil {
		return nil, fmt.Errorf("catalog response does not match request: %w", orderserrors.ErrCatalogUnavailable)
	}
	if response.GetRestaurantId() != restaurantID {
		return nil, fmt.Errorf("catalog response does not match request: %w", orderserrors.ErrCatalogUnavailable)
	}
	if len(response.GetItems()) != len(itemIDs) {
		return nil, fmt.Errorf("catalog response does not match request: %w", orderserrors.ErrCatalogUnavailable)
	}
	// Множество позволяет проверить принадлежность блюда запросу.
	requested := make(map[string]struct{}, len(itemIDs))

	for _, id := range itemIDs {
		requested[id] = struct{}{}
	}
	resolvedItems := response.GetItems()
	byID := make(map[string]model.MenuItem, len(resolvedItems))

	for _, item := range resolvedItems {
		if item == nil {
			return nil, fmt.Errorf("nil catalog item: %w", orderserrors.ErrCatalogUnavailable)
		}
		id := item.GetId()
		if _, ok := requested[id]; !ok {
			return nil, fmt.Errorf("unexpected catalog item: %w", orderserrors.ErrCatalogUnavailable)
		}
		if _, ok := byID[id]; ok {
			return nil, fmt.Errorf("duplicate catalog item: %w", orderserrors.ErrCatalogUnavailable)
		}
		if strings.TrimSpace(item.GetName()) == "" {
			return nil, fmt.Errorf("invalid catalog item values: %w", orderserrors.ErrCatalogUnavailable)
		}
		price := item.GetPriceKopecks()
		if price < 1 || price > int64(model.MaxUnitPrice) {
			return nil, fmt.Errorf("invalid catalog item values: %w", orderserrors.ErrCatalogUnavailable)
		}
		// Проверенную цену переводим из protobuf int64 в денежный тип orders.
		byID[id] = model.MenuItem{
			ID:           id,
			RestaurantID: restaurantID,
			Name:         item.GetName(),
			PriceKopecks: model.Money(price),
			IsAvailable:  true,
		}
	}

	items := make([]model.MenuItem, 0, len(itemIDs))
	// Возвращаем блюда в порядке ID исходного запроса.
	for _, id := range itemIDs {
		if item, exists := byID[id]; exists {
			items = append(items, item)
		} else {
			return nil, fmt.Errorf("item is not exists in resolved items: %w", orderserrors.ErrCatalogUnavailable)
		}
	}
	return items, nil
}
