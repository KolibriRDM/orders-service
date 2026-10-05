package catalogclient

// Тело HTTP-запроса orders к каталогу.
type resolveItemsRequest struct {
	RestaurantID string   `json:"restaurant_id"`
	ItemIDs      []string `json:"item_ids"`
}

// Тело успешного ответа каталога для orders.
type resolveItemsResponse struct {
	RestaurantID string                 `json:"restaurant_id"`
	Items        []resolvedItemResponse `json:"items"`
}

// Название и цена блюда из HTTP-ответа каталога.
type resolvedItemResponse struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	PriceKopecks int64  `json:"price_kopecks"`
}

// Ошибка HTTP-каталога с кодом и request ID.
type errorResponse struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}
