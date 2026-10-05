package model

// Сохранённый заказ и хеш запроса для проверки повтора.
type IdempotentOrder struct {
	Order       Order
	RequestHash string
}
