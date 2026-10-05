package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
	"github.com/KolibriRDM/orders-service/internal/handler/dto"
)

// Отправляет JSON с указанным статусом и запретом кэширования.
func (h *Handler) writeJSON(w http.ResponseWriter, status int, body any) {
	data, err := json.Marshal(body)
	if err != nil {
		h.logger.Error("encode JSON response", "request_id", w.Header().Get("X-Request-ID"))
		status = http.StatusInternalServerError
		data, _ = json.Marshal(dto.ErrorResponse{Code: "internal_error", Message: "internal server error", RequestID: w.Header().Get("X-Request-ID")})
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if _, err := w.Write(append(data, '\n')); err != nil {
		h.logger.Error("write JSON response", "request_id", w.Header().Get("X-Request-ID"))
	}
}

// Формирует JSON-ошибку с request ID.
func (h *Handler) writeError(w http.ResponseWriter, status int, code, message string) {
	h.writeJSON(w, status, dto.ErrorResponse{Code: code, Message: message, RequestID: w.Header().Get("X-Request-ID")})
}

// Сопоставляет ошибку сервиса с безопасным HTTP-ответом.
func (h *Handler) handleError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, context.Canceled):
		return
	case errors.Is(err, orderserrors.ErrInvalidInput):
		h.writeError(w, http.StatusBadRequest, "invalid_input", "invalid request parameters")
	case errors.Is(err, orderserrors.ErrNotFound):
		h.writeError(w, http.StatusNotFound, "not_found", "requested resource not found")
	case errors.Is(err, orderserrors.ErrForbidden):
		h.writeError(w, http.StatusForbidden, "forbidden", "access denied")
	case errors.Is(err, orderserrors.ErrIdempotencyConflict):
		h.writeError(w, http.StatusConflict, "idempotency_conflict", "idempotency key already used for another request")
	case errors.Is(err, orderserrors.ErrVersionConflict):
		h.writeError(w, http.StatusConflict, "version_conflict", "order version has changed")
	case errors.Is(err, orderserrors.ErrInvalidTransition):
		h.writeError(w, http.StatusConflict, "invalid_transition", "order status transition is not allowed")
	case errors.Is(err, orderserrors.ErrAlreadyExists):
		h.writeError(w, http.StatusConflict, "already_exists", "resource already exists")
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, orderserrors.ErrCatalogUnavailable), errors.Is(err, orderserrors.ErrDatabaseUnavailable):
		h.writeError(w, http.StatusServiceUnavailable, "unavailable", "service temporarily unavailable")
	default:
		// SQL-ошибка может содержать значения запроса, включая адрес доставки.
		h.logger.Error("orders request failed", "request_id", w.Header().Get("X-Request-ID"))
		h.writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}

// Возвращает JSON-ошибку 405 и заголовок Allow.
func (h *Handler) methodNotAllowed(allow string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", allow)
		h.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}
