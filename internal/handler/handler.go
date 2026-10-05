package handler

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/KolibriRDM/orders-service/internal/handler/dto"
	"github.com/KolibriRDM/orders-service/internal/model"
)

// Контракт бизнес-операций для HTTP-обработчиков.
type Service interface {
	CreateOrder(ctx context.Context, input model.CreateOrderInput) (model.CreateOrderResult, error)
	GetOrder(ctx context.Context, userID string, orderID string) (model.Order, error)
}

// HTTP-обработчики с зависимостями сервиса и логирования.
type Handler struct {
	service Service
	logger  *slog.Logger
}

// Связывает HTTP-обработчики с сервисом и логгером.
func NewHandler(service Service, logger *slog.Logger) *Handler {
	return &Handler{service: service, logger: logger}
}

// Создаёт заказ по HTTP или возвращает сохранённый повтор.
func (h *Handler) CreateOrder(w http.ResponseWriter, r *http.Request) {
	request, ok := h.decodeCreateOrderRequest(w, r)
	if !ok {
		return
	}
	userID := r.Header.Get("X-Demo-User-ID")
	idempotencyKey := r.Header.Get("Idempotency-Key")
	input := dto.ToCreateOrderInput(request, userID, idempotencyKey)
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	result, err := h.service.CreateOrder(ctx, input)
	if err != nil {
		h.handleError(w, err)
		return
	}
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
		w.Header().Set("Location", "/v1/orders/"+result.Order.ID)
	}
	h.writeJSON(w, status, dto.ToOrderResponse(result.Order))
}

// Возвращает по HTTP заказ запрашивающего пользователя.
func (h *Handler) GetOrder(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-Demo-User-ID")
	orderID := r.PathValue("id")
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	order, err := h.service.GetOrder(ctx, userID, orderID)
	if err != nil {
		h.handleError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, dto.ToOrderResponse(order))
}
