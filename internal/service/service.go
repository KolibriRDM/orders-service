package service

import (
	"context"
	"time"

	//? "time"

	"github.com/KolibriRDM/orders-service/internal/model"
)

// Контракт хранения данных для бизнес-сценариев.
type Repository interface {
	//! Create(ctx context.Context, order model.Order) error
	// Атомарно сохраняет заказ или возвращает результат повтора по ключу.
	CreateIdempotent(ctx context.Context, params model.CreateOrderParams) (model.CreateOrderResult, error)
	// Находит заказ и хеш по пользователю и ключу.
	GetByIdempotencyKey(ctx context.Context, userID string, key string) (model.IdempotentOrder, error)
	GetByID(ctx context.Context, id string) (model.Order, error)
	// Атомарно меняет статус при совпадении ожидаемой версии.
	UpdateStatus(ctx context.Context, id string, expectedVersion int64, nextStatus model.OrderStatus, updatedAt time.Time) (model.Order, error)
}

// Контракт получения проверенных блюд из каталога.
type CatalogProvider interface {
	ResolveItems(ctx context.Context, restaurantID string, itemIDs []string) ([]model.MenuItem, error)
}

// Бизнес-сценарии сервиса и их зависимости.
type Service struct {
	repo    Repository
	catalog CatalogProvider
}

// Связывает бизнес-сценарии с их зависимостями.
func NewService(repo Repository, catalog CatalogProvider) *Service {
	return &Service{
		repo:    repo,
		catalog: catalog,
	}
}
