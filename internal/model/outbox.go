package model

import "time"

const OrderCreatedEventType = "order.created.v1"

// Событие, сохраняемое вместе с заказом для будущей публикации.
type OutboxEvent struct {
	EventID   string
	OrderID   string
	Type      string
	Payload   []byte
	Attempts  int
	CreatedAt time.Time
}

// Снимок количества и возраста неопубликованных событий.
type OutboxStats struct {
	PendingEvents           int64
	OldestPendingAgeSeconds float64
}
