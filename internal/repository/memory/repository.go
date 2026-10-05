package memory

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sync"
	"time"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
	"github.com/KolibriRDM/orders-service/internal/model"
)

// Учебное хранилище в памяти; в приложении не используется.
type Repository struct {
	mu     sync.RWMutex
	orders map[string]model.Order
}

// Создаёт учебное хранилище заказов в памяти.
func NewRepository() *Repository {
	return &Repository{
		orders: make(map[string]model.Order),
	}
}

// Копирует позиции, чтобы вызывающий код не изменил хранилище.
func cloneOrder(order model.Order) model.Order {
	order.Items = slices.Clone(order.Items)
	return order
}

// Сохраняет проверенный заказ в памяти под блокировкой.
func (s *Repository) Create(ctx context.Context, order model.Order) error {
	err := ctx.Err()
	if err != nil {
		return fmt.Errorf("ctx is expire %w", err)
	}

	err = order.Validate()
	if err != nil {
		return fmt.Errorf("create order: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	err = ctx.Err()
	if err != nil {
		return fmt.Errorf("ctx is expire %w", err)
	}

	if _, exists := s.orders[order.ID]; exists {
		return fmt.Errorf("%w: order %q", orderserrors.ErrAlreadyExists, order.ID)
	}

	s.orders[order.ID] = cloneOrder(order)
	return nil
}

// Читает независимую копию заказа под блокировкой.
func (s *Repository) GetByID(ctx context.Context, id string) (model.Order, error) {
	err := ctx.Err()
	if err != nil {
		return model.Order{}, fmt.Errorf("ctx is expire %w", err)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	err = ctx.Err()
	if err != nil {
		return model.Order{}, fmt.Errorf("ctx is expire %w", err)
	}
	order, exists := s.orders[id]
	if !exists {
		return model.Order{}, fmt.Errorf("%w: order %q", orderserrors.ErrNotFound, id)
	}
	return cloneOrder(order), nil
}

// Атомарно меняет статус при совпадении версии заказа.
func (s *Repository) UpdateStatus(
	ctx context.Context,
	id string,
	expectedVersion int64,
	nextStatus model.OrderStatus,
	updatedAt time.Time,
) (model.Order, error) {
	err := ctx.Err()
	if err != nil {
		return model.Order{}, fmt.Errorf("ctx is expire %w", err)
	}
	if expectedVersion < 1 || !nextStatus.IsValid() || updatedAt.IsZero() {
		return model.Order{}, orderserrors.ErrInvalidInput
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	err = ctx.Err()
	if err != nil {
		return model.Order{}, fmt.Errorf("ctx is expire %w", err)
	}

	order, exists := s.orders[id]
	if !exists {
		return model.Order{}, fmt.Errorf("%w: order %q", orderserrors.ErrNotFound, id)
	}

	// Проверка версии под блокировкой исключает потерю конкурентного изменения.
	if order.Version != expectedVersion {
		return model.Order{}, orderserrors.ErrVersionConflict
	}

	if !order.Status.CanTransitionTo(nextStatus) {
		return model.Order{}, orderserrors.ErrInvalidTransition
	}

	if updatedAt.Before(order.UpdatedAt) {
		return model.Order{}, orderserrors.ErrInvalidInput
	}

	if order.Version == math.MaxInt64 {
		return model.Order{}, orderserrors.ErrInvalidInput
	}

	order.Status = nextStatus
	order.Version++
	order.UpdatedAt = updatedAt.UTC()

	s.orders[id] = order

	return cloneOrder(order), nil
}
