package dto

// JSON-запрос отмены заказа с ожидаемой версией.
type CancelOrderRequest struct {
	ExpectedVersion int64 `json:"expected_version"`
}

// JSON-запрос смены статуса заказа с ожидаемой версией.
type UpdateOrderStatusRequest struct {
	ExpectedVersion int64  `json:"expected_version"`
	Status          string `json:"status"`
}
