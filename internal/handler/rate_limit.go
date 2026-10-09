package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
)

// Проверяет лимит и при отказе отправляет HTTP-ответ.
func (h *Handler) allowCreateOrder(ctx context.Context, w http.ResponseWriter, userID string) bool {
	if ctx.Err() != nil {
		h.handleError(w, ctx.Err())
		return false
	}
	decision, err := h.limiter.Allow(ctx, userID)
	if ctx.Err() != nil {
		h.handleError(w, ctx.Err())
		return false
	}
	if err != nil {
		if errors.Is(err, orderserrors.ErrInvalidInput) {
			h.handleError(w, err)
			return false
		}
		// Ошибка Redis не блокирует создание заказа.
		h.logger.Warn("order rate limiter unavailable", "request_id", w.Header().Get("X-Request-ID"))
		return true
	}
	if decision.Allowed {
		return true
	}
	// Retry-After задаётся в целых секундах с округлением вверх.
	seconds := int64(decision.RetryAfter / time.Second)
	if decision.RetryAfter%time.Second != 0 {
		seconds++
	}
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
	h.writeError(w, http.StatusTooManyRequests, "rate_limit_exceeded", "too many order creation requests")
	return false
}
