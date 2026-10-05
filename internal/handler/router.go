package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// Регистрирует бизнес-маршруты и ответы на неверный метод.
func RegisterRoutes(mux *http.ServeMux, h *Handler) {
	mux.HandleFunc("POST /v1/orders", h.CreateOrder)
	mux.HandleFunc("GET /v1/orders/{id}", h.GetOrder)
	mux.HandleFunc("/v1/orders", h.methodNotAllowed("POST"))
	mux.HandleFunc("/v1/orders/{id}", h.methodNotAllowed("GET, HEAD"))
}

// Объединяет маршруты, проверки работоспособности и журналирование.
func NewRouter(logger *slog.Logger, checkDatabase func(context.Context) error, h *Handler) http.Handler {
	mux := http.NewServeMux()
	RegisterRoutes(mux, h)

	health := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")

		err := json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		if err != nil {
			logger.Error("write health response", "error", err)
		}
	}

	mux.HandleFunc("GET /healthz", health)

	ready := func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		err := checkDatabase(ctx)
		if err != nil {
			logger.Warn("database readiness check failed")
			w.WriteHeader(http.StatusServiceUnavailable)
			err = json.NewEncoder(w).Encode(map[string]string{"status": "not_ready"})
			if err != nil {
				logger.Error("write ready response", "error", err)
			}
			return
		}

		err = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		if err != nil {
			logger.Error("write ready response", "error", err)
		}
	}

	mux.HandleFunc("GET /readyz", ready)

	mux.HandleFunc("/healthz", h.methodNotAllowed("GET, HEAD"))
	mux.HandleFunc("/readyz", h.methodNotAllowed("GET, HEAD"))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		h.writeError(w, http.StatusNotFound, "not_found", "route not found")
	})
	return requestMiddleware(logger, mux)
}
