package dto

import "github.com/KolibriRDM/orders-service/internal/model"

// Объединяет JSON и заголовки во входную модель сервиса.
func ToCreateOrderInput(request CreateOrderRequest, userID string, idempotencyKey string) model.CreateOrderInput {
	input := model.CreateOrderInput{
		UserID: userID, IdempotencyKey: idempotencyKey,
		RestaurantID: request.RestaurantID, DeliveryAddress: request.DeliveryAddress,
		Items: make([]model.CreateOrderItemInput, 0, len(request.Items)),
	}
	for _, item := range request.Items {
		input.Items = append(input.Items, model.CreateOrderItemInput{MenuItemID: item.MenuItemID, Quantity: item.Quantity})
	}
	return input
}

// Преобразует заказ и снимки позиций в JSON-ответ.
func ToOrderResponse(order model.Order) OrderResponse {
	response := OrderResponse{
		ID: order.ID, UserID: order.UserID, RestaurantID: order.RestaurantID,
		Items:  make([]OrderItemResponse, 0, len(order.Items)),
		Status: string(order.Status), Version: order.Version, TotalKopecks: int64(order.TotalKopecks),
		DeliveryAddress: order.DeliveryAddress, CreatedAt: order.CreatedAt, UpdatedAt: order.UpdatedAt,
	}
	for _, item := range order.Items {
		response.Items = append(response.Items, OrderItemResponse{
			MenuItemID: item.MenuItemID, NameSnapshot: item.NameSnapshot,
			UnitPriceKopecks: int64(item.UnitPriceKopecks), Quantity: item.Quantity,
		})
	}
	return response
}
