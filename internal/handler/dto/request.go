package dto

// Блюдо и количество из JSON-запроса клиента.
type CreateOrderItemRequest struct {
	MenuItemID string `json:"menu_item_id"`
	Quantity   int    `json:"quantity"`
}

// JSON-запрос заказа без цен и данных заголовков.
type CreateOrderRequest struct {
	RestaurantID    string                   `json:"restaurant_id"`
	DeliveryAddress string                   `json:"delivery_address"`
	Items           []CreateOrderItemRequest `json:"items"`
}
