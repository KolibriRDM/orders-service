package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
	"github.com/KolibriRDM/orders-service/internal/model"
)

// Проверяет запрос, нормализует UUID и сортирует копию позиций.
func normalizeCreateOrderInput(input model.CreateOrderInput) (model.CreateOrderInput, error) {
	userID, err := model.NormalizeID(input.UserID)
	if err != nil {
		return model.CreateOrderInput{}, fmt.Errorf("user ID must be a UUID: %w", orderserrors.ErrInvalidInput)
	}
	input.UserID = userID
	restaurantID, err := model.NormalizeID(input.RestaurantID)
	if err != nil {
		return model.CreateOrderInput{}, fmt.Errorf("restaurant ID must be a UUID: %w", orderserrors.ErrInvalidInput)
	}
	input.RestaurantID = restaurantID
	input.DeliveryAddress = strings.TrimSpace(input.DeliveryAddress)
	if !utf8.ValidString(input.DeliveryAddress) || strings.ContainsRune(input.DeliveryAddress, '\x00') {
		return model.CreateOrderInput{}, fmt.Errorf("delivery address must be valid UTF-8 without NUL: %w", orderserrors.ErrInvalidInput)
	}
	if input.DeliveryAddress == "" || utf8.RuneCountInString(input.DeliveryAddress) > 300 {
		return model.CreateOrderInput{}, fmt.Errorf("delivery address must contain 1 to 300 characters: %w", orderserrors.ErrInvalidInput)
	}
	if !utf8.ValidString(input.IdempotencyKey) || strings.ContainsRune(input.IdempotencyKey, '\x00') {
		return model.CreateOrderInput{}, fmt.Errorf("idempotency key must be valid UTF-8 without NUL: %w", orderserrors.ErrInvalidInput)
	}
	if strings.TrimSpace(input.IdempotencyKey) == "" || utf8.RuneCountInString(input.IdempotencyKey) > 128 {
		return model.CreateOrderInput{}, fmt.Errorf("idempotency key must contain 1 to 128 characters and not be blank: %w", orderserrors.ErrInvalidInput)
	}
	if len(input.Items) < 1 || len(input.Items) > 20 {
		return model.CreateOrderInput{}, fmt.Errorf("order must contain 1 to 20 items: %w", orderserrors.ErrInvalidInput)
	}
	// Копия позволяет нормализовать позиции без изменения исходного среза.
	input.Items = slices.Clone(input.Items)
	seen := make(map[string]struct{}, len(input.Items))
	for i := range input.Items {
		itemID, err := model.NormalizeID(input.Items[i].MenuItemID)
		if err != nil {
			return model.CreateOrderInput{}, fmt.Errorf("item %d ID must be a UUID: %w", i, orderserrors.ErrInvalidInput)
		}
		if input.Items[i].Quantity < 1 || input.Items[i].Quantity > 20 {
			return model.CreateOrderInput{}, fmt.Errorf("item %d quantity must be between 1 and 20: %w", i, orderserrors.ErrInvalidInput)
		}
		if _, exists := seen[itemID]; exists {
			return model.CreateOrderInput{}, fmt.Errorf("duplicate menu item ID %q: %w", itemID, orderserrors.ErrInvalidInput)
		}
		seen[itemID] = struct{}{}
		input.Items[i].MenuItemID = itemID
	}
	// Порядок блюд в запросе не должен влиять на хеш.
	slices.SortFunc(input.Items, func(a, b model.CreateOrderItemInput) int {
		return strings.Compare(a.MenuItemID, b.MenuItemID)
	})
	return input, nil
}

// Поля запроса, определяющие совпадение повторных заказов.
type createOrderHashInput struct {
	RestaurantID    string
	DeliveryAddress string
	Items           []model.CreateOrderItemInput
}

// Вычисляет стабильный хеш нормализованного запроса.
func hashCreateOrderInput(input model.CreateOrderInput) (string, error) {
	// Формат JSON должен оставаться стабильным для сохранённых ключей.
	payload := createOrderHashInput{
		RestaurantID:    input.RestaurantID,
		DeliveryAddress: input.DeliveryAddress,
		Items:           input.Items,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal order request hash: %w", err)
	}
	sum := sha256.Sum256(data)
	// 32 байта SHA-256 превращаются в 64 шестнадцатеричных символа.
	return hex.EncodeToString(sum[:]), nil
}
