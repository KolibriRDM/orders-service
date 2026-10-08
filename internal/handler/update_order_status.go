package handler

import (
	"context"
	"net/http"
	"time"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
	"github.com/KolibriRDM/orders-service/internal/handler/dto"
	"github.com/KolibriRDM/orders-service/internal/model"
)

// Меняет статус заказа по HTTP при наличии demo-роли сотрудника.
func (h *Handler) UpdateOrderStatus(w http.ResponseWriter, r *http.Request) {
	isStaff := r.Header.Get("X-Demo-Role") == "staff"
	if !isStaff {
		h.handleError(w, orderserrors.ErrForbidden)
		return
	}
	request, ok := h.decodeUpdateOrderStatusRequest(w, r)
	if !ok {
		return
	}
	orderID := r.PathValue("id")
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	// Преобразование строки в OrderStatus не заменяет проверку значения в сервисе.
	order, err := h.service.UpdateOrderStatus(ctx, isStaff, orderID, request.ExpectedVersion, model.OrderStatus(request.Status))
	if err != nil {
		h.handleError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, dto.ToOrderResponse(order))
}
