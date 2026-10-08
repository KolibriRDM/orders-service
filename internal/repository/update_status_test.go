package repository_test

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
	"github.com/KolibriRDM/orders-service/internal/model"
	"github.com/KolibriRDM/orders-service/internal/repository"
)

// Проверяет отклонение некорректных аргументов до обращения к БД.
func TestUpdateStatusRejectsInvalidInput(t *testing.T) {
	const id = "30000000-0000-4000-8000-000000000001"
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		id      string
		version int64
		status  model.OrderStatus
		at      time.Time
	}{
		{"empty ID", "", 1, model.StatusCooking, now},
		{"invalid ID", "not-a-uuid", 1, model.StatusCooking, now},
		{"zero version", id, 0, model.StatusCooking, now},
		{"negative version", id, -1, model.StatusCooking, now},
		{"overflow version", id, math.MaxInt64, model.StatusCooking, now},
		{"empty status", id, 1, "", now},
		{"unknown status", id, 1, "paid", now},
		{"zero timestamp", id, 1, model.StatusCooking, time.Time{}},
	}
	// Отсутствующий пул выявляет случайное обращение к БД при валидации.
	repo := repository.NewRepository(nil)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := repo.UpdateStatus(context.Background(), tc.id, tc.version, tc.status, tc.at)
			if !errors.Is(err, orderserrors.ErrInvalidInput) || !reflect.DeepEqual(got, model.Order{}) {
				t.Fatalf("got=%+v err=%v", got, err)
			}
		})
	}
}

// Проверяет приоритет отмены контекста над валидацией аргументов.
func TestUpdateStatusCancelledContext(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	for _, tc := range []struct {
		name string
		ctx  context.Context
		want error
	}{
		{"cancelled", cancelled, context.Canceled},
		{"expired", expired, context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := repository.NewRepository(nil).UpdateStatus(tc.ctx, "", 0, "", time.Time{})
			if !errors.Is(err, tc.want) || !reflect.DeepEqual(got, model.Order{}) {
				t.Fatalf("got=%+v err=%v", got, err)
			}
		})
	}
}
