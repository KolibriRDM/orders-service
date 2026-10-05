package memory

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sync"
	"testing"
	"time"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
	"github.com/KolibriRDM/orders-service/internal/model"
	// "github.com/KolibriRDM/orders-service/internal/service"
)

// var _ service.Repository = (*Repository)(nil)

// Готовит корректный заказ для тестов.
func validOrder() model.Order {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	return model.Order{
		ID:           "order-1",
		UserID:       "user-1",
		RestaurantID: "restaurant-1",
		Items: []model.OrderItem{{
			MenuItemID:       "item-1",
			NameSnapshot:     "Пицца",
			UnitPriceKopecks: 25_000,
			Quantity:         2,
		}},
		Status:          model.StatusCreated,
		Version:         1,
		TotalKopecks:    69_900,
		DeliveryAddress: "Москва, Тестовая улица, 1",
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

// Создаёт хранилище в памяти с тестовым заказом.
func newStoreWithOrder(t *testing.T) (*Repository, model.Order) {
	t.Helper()

	store := NewRepository()
	order := validOrder()

	err := store.Create(context.Background(), order)
	if err != nil {
		t.Fatalf("prepare order: %v", err)
	}

	return store, order
}

// Проверяет сохранение и чтение заказа в памяти.
func TestRepositoryCreateAndGet(t *testing.T) {
	ctx := context.Background()
	store := NewRepository()
	want := validOrder()
	err := store.Create(ctx, want)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := store.GetByID(ctx, want.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// Проверяет отказ при повторном ID заказа в памяти.
func TestRepositoryCreateDuplicate(t *testing.T) {
	store, original := newStoreWithOrder(t)
	duplicate := original
	duplicate.DeliveryAddress = "Москва, Другой адрес, 2"
	err := store.Create(context.Background(), duplicate)
	if !errors.Is(err, orderserrors.ErrAlreadyExists) {
		t.Fatalf("expected ErrAlreadyExists, got %v", err)
	}
	order, err := store.GetByID(context.Background(), original.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !reflect.DeepEqual(original, order) {
		t.Fatalf("got %+v, want %+v", order, original)
	}
}

// Проверяет ошибку отсутствующего заказа в памяти.
func TestRepositoryGetMissing(t *testing.T) {
	store := NewRepository()
	_, err := store.GetByID(context.Background(), "missing-order")
	if !errors.Is(err, orderserrors.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// Проверяет отказ при сохранении неверной модели заказа.
func TestRepositoryCreateInvalid(t *testing.T) {
	store := NewRepository()
	order := validOrder()
	order.TotalKopecks++
	if err := store.Create(context.Background(), order); !errors.Is(err, orderserrors.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
	if _, err := store.GetByID(context.Background(), order.ID); !errors.Is(err, orderserrors.ErrNotFound) {
		t.Fatalf("invalid order was saved: %v", err)
	}
}

// Проверяет независимость копий позиций от хранилища.
func TestRepositoryCopiesItems(t *testing.T) {
	t.Run("Create copies the input", func(t *testing.T) {
		store, original := newStoreWithOrder(t)
		wantName := original.Items[0].NameSnapshot
		original.Items[0].NameSnapshot = "changed after Create"
		got, err := store.GetByID(context.Background(), original.ID)
		if err != nil {
			t.Fatalf("GetByID: %v", err)
		}
		if got.Items[0].NameSnapshot != wantName {
			t.Fatalf("input mutation changed stored name: got %q, want %q", got.Items[0].NameSnapshot, wantName)
		}
	})

	t.Run("GetByID copies its result", func(t *testing.T) {
		store, original := newStoreWithOrder(t)
		got, err := store.GetByID(context.Background(), original.ID)
		if err != nil {
			t.Fatalf("GetByID: %v", err)
		}
		got.Items[0].NameSnapshot = "changed after GetByID"
		stored, err := store.GetByID(context.Background(), original.ID)
		if err != nil {
			t.Fatalf("GetByID: %v", err)
		}
		if !reflect.DeepEqual(stored, original) {
			t.Fatalf("result mutation changed stored order: got %+v, want %+v", stored, original)
		}
	})

	t.Run("UpdateStatus copies its result", func(t *testing.T) {
		store, original := newStoreWithOrder(t)
		updatedAt := original.UpdatedAt.Add(time.Minute)
		got, err := store.UpdateStatus(context.Background(), original.ID, 1, model.StatusCooking, updatedAt)
		if err != nil {
			t.Fatalf("UpdateStatus: %v", err)
		}
		got.Items[0].NameSnapshot = "changed after UpdateStatus"
		stored, err := store.GetByID(context.Background(), original.ID)
		if err != nil {
			t.Fatalf("GetByID: %v", err)
		}
		want := original
		want.Status = model.StatusCooking
		want.Version = 2
		want.UpdatedAt = updatedAt
		if !reflect.DeepEqual(stored, want) {
			t.Fatalf("result mutation changed stored order: got %+v, want %+v", stored, want)
		}
	})
}

// Проверяет смену статуса и увеличение версии.
func TestRepositoryUpdateStatus(t *testing.T) {
	store, original := newStoreWithOrder(t)
	// Один момент времени из разных часовых поясов сохраняется в UTC.
	updatedAt := original.UpdatedAt.Add(time.Minute).In(time.FixedZone("UTC+3", 3*60*60))
	got, err := store.UpdateStatus(context.Background(), original.ID, 1, model.StatusCooking, updatedAt)
	if err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	want := original
	want.Status = model.StatusCooking
	want.Version = 2
	want.UpdatedAt = updatedAt.UTC()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	stored, err := store.GetByID(context.Background(), original.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !reflect.DeepEqual(stored, want) {
		t.Fatalf("stored %+v, want %+v", stored, want)
	}
}

// Проверяет отказ при недопустимом изменении заказа.
func TestRepositoryRejectsInvalidUpdates(t *testing.T) {
	type updateRequest struct {
		id      string
		version int64
		status  model.OrderStatus
		at      time.Time
	}
	tests := []struct {
		name    string
		change  func(*model.Order, *updateRequest)
		wantErr error
	}{
		{
			name:    "mismatched version",
			change:  func(_ *model.Order, r *updateRequest) { r.version = 2 },
			wantErr: orderserrors.ErrVersionConflict,
		},
		{
			name:    "stale version",
			change:  func(o *model.Order, _ *updateRequest) { o.Version = 2 },
			wantErr: orderserrors.ErrVersionConflict,
		},
		{
			name:    "invalid transition",
			change:  func(_ *model.Order, r *updateRequest) { r.status = model.StatusDelivered },
			wantErr: orderserrors.ErrInvalidTransition,
		},
		{
			name:    "same status",
			change:  func(_ *model.Order, r *updateRequest) { r.status = model.StatusCreated },
			wantErr: orderserrors.ErrInvalidTransition,
		},
		{
			name:    "terminal status",
			change:  func(o *model.Order, _ *updateRequest) { o.Status = model.StatusCancelled },
			wantErr: orderserrors.ErrInvalidTransition,
		},
		{
			name:    "missing order",
			change:  func(_ *model.Order, r *updateRequest) { r.id = "missing-order" },
			wantErr: orderserrors.ErrNotFound,
		},
		{
			name:    "zero expected version",
			change:  func(_ *model.Order, r *updateRequest) { r.version = 0 },
			wantErr: orderserrors.ErrInvalidInput,
		},
		{
			name:    "unknown status",
			change:  func(_ *model.Order, r *updateRequest) { r.status = "unknown" },
			wantErr: orderserrors.ErrInvalidInput,
		},
		{
			name:    "zero update time",
			change:  func(_ *model.Order, r *updateRequest) { r.at = time.Time{} },
			wantErr: orderserrors.ErrInvalidInput,
		},
		{
			name:    "time moves backwards",
			change:  func(o *model.Order, r *updateRequest) { r.at = o.UpdatedAt.Add(-time.Second) },
			wantErr: orderserrors.ErrInvalidInput,
		},
		{
			name:    "version overflow",
			change:  func(o *model.Order, r *updateRequest) { o.Version = math.MaxInt64; r.version = math.MaxInt64 },
			wantErr: orderserrors.ErrInvalidInput,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store := NewRepository()
			original := validOrder()
			req := updateRequest{original.ID, 1, model.StatusCooking, original.UpdatedAt.Add(time.Minute)}
			tc.change(&original, &req)
			if err := store.Create(ctx, original); err != nil {
				t.Fatalf("Create: %v", err)
			}
			if _, err := store.UpdateStatus(ctx, req.id, req.version, req.status, req.at); !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected %v, got %v", tc.wantErr, err)
			}
			stored, err := store.GetByID(ctx, original.ID)
			if err != nil {
				t.Fatalf("GetByID: %v", err)
			}
			if !reflect.DeepEqual(stored, original) {
				t.Fatalf("rejected update changed order: got %+v, want %+v", stored, original)
			}
		})
	}
}

// Проверяет отмену операций хранилища в памяти.
func TestRepositoryCancelledContext(t *testing.T) {
	for _, operation := range []string{"Create", "GetByID", "UpdateStatus"} {
		t.Run(operation, func(t *testing.T) {
			store, original := newStoreWithOrder(t)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			var err error
			switch operation {
			case "Create":
				another := validOrder()
				another.ID = "order-2"
				err = store.Create(ctx, another)
			case "GetByID":
				_, err = store.GetByID(ctx, original.ID)
			case "UpdateStatus":
				_, err = store.UpdateStatus(ctx, original.ID, 1, model.StatusCooking, original.UpdatedAt.Add(time.Minute))
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("expected context.Canceled, got %v", err)
			}
			stored, err := store.GetByID(context.Background(), original.ID)
			if err != nil {
				t.Fatalf("GetByID: %v", err)
			}
			if !reflect.DeepEqual(stored, original) {
				t.Fatalf("cancelled operation changed order: got %+v, want %+v", stored, original)
			}
			if _, err := store.GetByID(context.Background(), "order-2"); !errors.Is(err, orderserrors.ErrNotFound) {
				t.Fatalf("cancelled operation created order-2: %v", err)
			}
		})
	}
}

// Проверяет единственного победителя конкурентной смены статуса.
func TestRepositoryConcurrentUpdate(t *testing.T) {
	store, original := newStoreWithOrder(t)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	updatedAt := original.UpdatedAt.Add(time.Minute)
	for _, status := range []model.OrderStatus{model.StatusCooking, model.StatusCancelled} {
		go func(next model.OrderStatus) {
			defer wg.Done()
			<-start
			_, err := store.UpdateStatus(context.Background(), original.ID, 1, next, updatedAt)
			results <- err
		}(status)
	}
	// Закрытие канала одновременно разрешает всем горутинам начать работу.
	close(start)
	wg.Wait()
	close(results)

	successes, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, orderserrors.ErrVersionConflict):
			conflicts++
		default:
			t.Fatalf("unexpected update error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d, want 1/1", successes, conflicts)
	}
	stored, err := store.GetByID(context.Background(), original.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if stored.Version != 2 || (stored.Status != model.StatusCooking && stored.Status != model.StatusCancelled) {
		t.Fatalf("unexpected final state: version=%d status=%s", stored.Version, stored.Status)
	}
	if !stored.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("update time = %v, want %v", stored.UpdatedAt, updatedAt)
	}
}

// Проверяет единственную запись при конкурентном создании.
func TestRepositoryConcurrentCreate(t *testing.T) {
	const workers = 8
	type result struct {
		order model.Order
		err   error
	}
	store := NewRepository()
	start := make(chan struct{})
	results := make(chan result, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(index int) {
			defer wg.Done()
			order := validOrder()
			order.DeliveryAddress = fmt.Sprintf("Адрес %d", index)
			<-start
			results <- result{order, store.Create(context.Background(), order)}
		}(i)
	}
	// Закрытие канала одновременно разрешает всем горутинам начать работу.
	close(start)
	wg.Wait()
	close(results)

	successes, duplicates := 0, 0
	var winner model.Order
	for result := range results {
		switch {
		case result.err == nil:
			successes++
			winner = result.order
		case errors.Is(result.err, orderserrors.ErrAlreadyExists):
			duplicates++
		default:
			t.Fatalf("unexpected Create error: %v", result.err)
		}
	}
	if successes != 1 || duplicates != workers-1 {
		t.Fatalf("successes=%d duplicates=%d, want 1/%d", successes, duplicates, workers-1)
	}
	stored, err := store.GetByID(context.Background(), winner.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !reflect.DeepEqual(stored, winner) {
		t.Fatalf("stored %+v, want winning order %+v", stored, winner)
	}
}
