package model

// Состояние заказа в процессе приготовления и доставки.
type OrderStatus string

const (
	StatusCreated    OrderStatus = "created"
	StatusCooking    OrderStatus = "cooking"
	StatusReady      OrderStatus = "ready"
	StatusDelivering OrderStatus = "delivering"
	StatusDelivered  OrderStatus = "delivered"
	StatusCancelled  OrderStatus = "cancelled"
)

// Проверяет допустимость перехода в следующий статус.
func (s OrderStatus) CanTransitionTo(next OrderStatus) bool {
	switch s {
	case StatusCreated:
		return next == StatusCooking || next == StatusCancelled
	case StatusCooking:
		return next == StatusReady
	case StatusReady:
		return next == StatusDelivering
	case StatusDelivering:
		return next == StatusDelivered
	default:
		return false
	}
}

// Проверяет, входит ли статус в разрешённый набор.
func (s OrderStatus) IsValid() bool {
	switch s {
	case StatusCreated, StatusCooking, StatusReady, StatusDelivering, StatusDelivered, StatusCancelled:
		return true
	default:
		return false
	}
}
