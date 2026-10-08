package handler

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/KolibriRDM/orders-service/internal/handler/dto"
)

// 64 << 10 задаёт лимит тела в 64 KiB.
const maxRequestBytes = 64 << 10

// Читает один JSON-объект заданного типа с проверкой размера и полей.
func decodeJSONRequest[T any](h *Handler, w http.ResponseWriter, r *http.Request) (T, bool) {
	var zero T
	defer r.Body.Close()
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		h.writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
		return zero, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	// Указатель позволяет отличить JSON null от пустого объекта.
	var request *T
	if err := decoder.Decode(&request); err != nil {
		h.decodeError(w, err)
		return zero, false
	}
	if request == nil {
		h.writeError(w, http.StatusBadRequest, "invalid_input", "expected a JSON object")
		return zero, false
	}
	// Второе чтение должно найти EOF, а не ещё одно JSON-значение.
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			h.writeError(w, http.StatusBadRequest, "invalid_input", "expected exactly one JSON object")
		} else {
			h.decodeError(w, err)
		}
		return zero, false
	}
	return *request, true
}

// Читает JSON-запрос создания заказа.
func (h *Handler) decodeCreateOrderRequest(w http.ResponseWriter, r *http.Request) (dto.CreateOrderRequest, bool) {
	return decodeJSONRequest[dto.CreateOrderRequest](h, w, r)
}

// Читает JSON-запрос отмены заказа.
func (h *Handler) decodeCancelOrderRequest(w http.ResponseWriter, r *http.Request) (dto.CancelOrderRequest, bool) {
	return decodeJSONRequest[dto.CancelOrderRequest](h, w, r)
}

// Читает JSON-запрос изменения статуса.
func (h *Handler) decodeUpdateOrderStatusRequest(w http.ResponseWriter, r *http.Request) (dto.UpdateOrderStatusRequest, bool) {
	return decodeJSONRequest[dto.UpdateOrderStatusRequest](h, w, r)
}

// Переводит ошибку чтения JSON в ответ 400 или 413.
func (h *Handler) decodeError(w http.ResponseWriter, err error) {
	var limit *http.MaxBytesError
	if errors.As(err, &limit) {
		h.writeError(w, http.StatusRequestEntityTooLarge, "body_too_large", "request body exceeds 64 KiB")
		return
	}
	h.writeError(w, http.StatusBadRequest, "invalid_input", "invalid JSON request")
}
