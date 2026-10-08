package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/KolibriRDM/orders-service/internal/handler/dto"
)

// Отменяет по HTTP заказ запрашивающего пользователя.
func (h *Handler) CancelOrder(w http.ResponseWriter, r *http.Request) {
	request, ok := h.decodeCancelOrderRequest(w, r)
	if !ok {
		return
	}
	userID := r.Header.Get("X-Demo-User-ID")
	orderID := r.PathValue("id")
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	order, err := h.service.CancelOrder(ctx, userID, orderID, request.ExpectedVersion)
	if err != nil {
		h.handleError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, dto.ToOrderResponse(order))
}
