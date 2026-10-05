package model

// Локальное представление ресторана для модели orders.
type Restaurant struct {
	ID       string
	Name     string
	IsActive bool
}

// Проверенное блюдо, полученное orders от каталога.
type MenuItem struct {
	ID           string
	RestaurantID string
	Name         string
	PriceKopecks Money
	IsAvailable  bool
}
