package model

import "time"

const OrderCreatedEventType = "order.created.v1"

// Событие, сохраняемое вместе с заказом для будущей публикации.
type OutboxEvent struct {
	EventID   string
	OrderID   string
	Type      string
	Payload   []byte
	CreatedAt time.Time
}
