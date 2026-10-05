package model

// Подготовленные заказ, ключ, хеш и событие для записи.
type CreateOrderParams struct {
	Order          Order
	IdempotencyKey string
	RequestHash    string
	Event          OutboxEvent
}

// Результат создания с признаком нового заказа или повтора.
type CreateOrderResult struct {
	Order   Order
	Created bool
}

// ID блюда и количество до получения цен из каталога.
type CreateOrderItemInput struct {
	MenuItemID string
	Quantity   int
}

// Входные данные сценария создания заказа.
type CreateOrderInput struct {
	UserID          string
	RestaurantID    string
	DeliveryAddress string
	IdempotencyKey  string
	Items           []CreateOrderItemInput
}
