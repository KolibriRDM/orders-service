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
	CancelOrder(ctx context.Context, userID string, orderID string, expectedVersion int64) (model.Order, error)
	UpdateOrderStatus(ctx context.Context, isStaff bool, orderID string, expectedVersion int64, nextStatus model.OrderStatus) (model.Order, error)
}

// Проверяет лимит попыток создания заказа.
type RateLimiter interface {
	Allow(ctx context.Context, userID string) (model.RateLimitDecision, error)
}

// HTTP-обработчики с зависимостями сервиса и логирования.
type Handler struct {
	service Service
	logger  *slog.Logger
	limiter RateLimiter
}

// Связывает HTTP-обработчики с сервисом, лимитером и логгером.
func NewHandler(service Service, limiter RateLimiter, logger *slog.Logger) *Handler {
	return &Handler{service: service, logger: logger, limiter: limiter}
}

// Создаёт заказ по HTTP или возвращает сохранённый повтор.
func (h *Handler) CreateOrder(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	userID, err := model.NormalizeID(r.Header.Get("X-Demo-User-ID"))
	if err != nil {
		h.handleError(w, err)
		return
	}
	// Лимит учитывает попытки создания, включая идемпотентные повторы.
	allow := h.allowCreateOrder(ctx, w, userID)
	if !allow {
		return
	}
	request, ok := h.decodeCreateOrderRequest(w, r)
	if !ok {
		return
	}
	idempotencyKey := r.Header.Get("Idempotency-Key")
	input := dto.ToCreateOrderInput(request, userID, idempotencyKey)
	result, err := h.service.CreateOrder(ctx, input)
	if err != nil {
		h.handleError(w, err)
		return
	}
	// Связываем HTTP-запрос с сохранённым заказом без записи его содержимого.
	h.logger.InfoContext(ctx, "order creation handled",
		"request_id", w.Header().Get("X-Request-ID"),
		"order_id", result.Order.ID,
		"created", result.Created,
	)
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
