package service

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
	"github.com/KolibriRDM/orders-service/internal/model"
)

// Генерирует случайный UUID v4 для заказа или события.
func newID() (string, error) {
	var value [16]byte
	_, err := rand.Read(value[:])
	if err != nil {
		return "", fmt.Errorf("generate UUID: %w", err)
	}
	// Маски задают версию UUID v4 и стандартный вариант.
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80

	// Группы UUID содержат 8-4-4-4-12 шестнадцатеричных цифр.
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[:4], value[4:6], value[6:8], value[8:10], value[10:]), nil
}

// JSON-контракт события order.created.v1 для уведомлений.
type orderCreatedPayload struct {
	EventID       string      `json:"event_id"`
	Type          string      `json:"type"`
	SchemaVersion int         `json:"schema_version"`
	OccurredAt    time.Time   `json:"occurred_at"`
	OrderID       string      `json:"order_id"`
	UserID        string      `json:"user_id"`
	TotalKopecks  model.Money `json:"total_kopecks"`
}

// Проверяет повтор, получает цены и сохраняет заказ с событием.
func (s *Service) CreateOrder(ctx context.Context, input model.CreateOrderInput) (model.CreateOrderResult, error) {
	if err := ctx.Err(); err != nil {
		return model.CreateOrderResult{}, fmt.Errorf("create order ctx error: %w", err)
	}
	normalized, err := normalizeCreateOrderInput(input)
	if err != nil {
		return model.CreateOrderResult{}, fmt.Errorf("create order normalize error: %w", err)
	}
	requestHash, err := hashCreateOrderInput(normalized)
	if err != nil {
		return model.CreateOrderResult{}, fmt.Errorf("create order hash error: %w", err)
	}

	// Сохранённый повтор не зависит от доступности и новых цен каталога.
	existing, err := s.repo.GetByIdempotencyKey(ctx, normalized.UserID, normalized.IdempotencyKey)
	if ctx.Err() != nil {
		return model.CreateOrderResult{}, fmt.Errorf("get existing order: %w", ctx.Err())
	}
	if err == nil {
		if existing.RequestHash != requestHash {
			return model.CreateOrderResult{}, orderserrors.ErrIdempotencyConflict
		}
		return model.CreateOrderResult{Order: existing.Order, Created: false}, nil
	}
	if !errors.Is(err, orderserrors.ErrNotFound) {
		return model.CreateOrderResult{}, fmt.Errorf("get existing order: %w", err)
	}

	itemIDs := make([]string, len(normalized.Items))
	for i, item := range normalized.Items {
		itemIDs[i] = item.MenuItemID
	}
	menuItems, err := s.catalog.ResolveItems(ctx, normalized.RestaurantID, itemIDs)
	if ctx.Err() != nil {
		return model.CreateOrderResult{}, fmt.Errorf("resolve order items: %w", ctx.Err())
	}
	if err != nil {
		return model.CreateOrderResult{}, fmt.Errorf("resolve order items: %w", err)
	}
	if len(menuItems) != len(normalized.Items) {
		return model.CreateOrderResult{}, fmt.Errorf("incomplete resolved menu: %w", orderserrors.ErrCatalogUnavailable)
	}
	byID := make(map[string]model.MenuItem, len(menuItems))
	for _, item := range menuItems {
		if _, exists := byID[item.ID]; exists {
			return model.CreateOrderResult{}, fmt.Errorf("duplicate resolved menu item: %w", orderserrors.ErrCatalogUnavailable)
		}
		if item.RestaurantID != normalized.RestaurantID || !item.IsAvailable ||
			strings.TrimSpace(item.Name) == "" || !utf8.ValidString(item.Name) || strings.ContainsRune(item.Name, '\x00') ||
			item.PriceKopecks < 1 || item.PriceKopecks > model.MaxUnitPrice {
			return model.CreateOrderResult{}, fmt.Errorf("invalid resolved menu item: %w", orderserrors.ErrCatalogUnavailable)
		}
		byID[item.ID] = item
	}
	orderItems := make([]model.OrderItem, 0, len(normalized.Items))
	for _, requested := range normalized.Items {
		menuItem, exists := byID[requested.MenuItemID]
		if !exists {
			return model.CreateOrderResult{}, fmt.Errorf("missing resolved menu item: %w", orderserrors.ErrCatalogUnavailable)
		}
		orderItems = append(orderItems, model.OrderItem{
			MenuItemID:       requested.MenuItemID,
			NameSnapshot:     menuItem.Name,
			UnitPriceKopecks: menuItem.PriceKopecks,
			Quantity:         requested.Quantity,
		})
	}
	total, err := model.CalculateTotal(orderItems, model.DeliveryFee)
	if err != nil {
		return model.CreateOrderResult{}, fmt.Errorf("calculate order total: %w", err)
	}
	orderID, err := newID()
	if err != nil {
		return model.CreateOrderResult{}, err
	}
	eventID, err := newID()
	if err != nil {
		return model.CreateOrderResult{}, err
	}
	// Микросекунды совпадают с точностью времени PostgreSQL.
	now := time.Now().UTC().Truncate(time.Microsecond)
	order := model.Order{
		ID: orderID, UserID: normalized.UserID, RestaurantID: normalized.RestaurantID,
		Items: orderItems, Status: model.StatusCreated, Version: 1, TotalKopecks: total,
		DeliveryAddress: normalized.DeliveryAddress, CreatedAt: now, UpdatedAt: now,
	}
	if err := order.Validate(); err != nil {
		return model.CreateOrderResult{}, fmt.Errorf("validate created order: %w", err)
	}
	payload := orderCreatedPayload{
		EventID: eventID, Type: model.OrderCreatedEventType, SchemaVersion: 1,
		OccurredAt: now, OrderID: order.ID, UserID: order.UserID, TotalKopecks: order.TotalKopecks,
	}
	eventPayload, err := json.Marshal(payload)
	if err != nil {
		return model.CreateOrderResult{}, fmt.Errorf("marshal order created event: %w", err)
	}
	params := model.CreateOrderParams{
		Order: order, IdempotencyKey: normalized.IdempotencyKey, RequestHash: requestHash,
		Event: model.OutboxEvent{
			EventID: eventID, OrderID: order.ID, Type: model.OrderCreatedEventType,
			Payload: eventPayload, CreatedAt: now,
		},
	}
	if err := ctx.Err(); err != nil {
		return model.CreateOrderResult{}, fmt.Errorf("create order: %w", err)
	}
	result, err := s.repo.CreateIdempotent(ctx, params)
	if err != nil {
		return model.CreateOrderResult{}, fmt.Errorf("create order: %w", err)
	}
	return result, nil
}
