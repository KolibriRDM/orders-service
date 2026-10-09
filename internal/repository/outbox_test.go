package repository_test

import (
	"context"
	"errors"
	"testing"
	"time"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
	"github.com/KolibriRDM/orders-service/internal/repository"
)

// Проверяет отклонение неверных параметров до обращения к БД.
func TestOutboxInvalidInput(t *testing.T) {
	repo := repository.NewRepository(nil)
	for _, limit := range []int{-1, 0, 101} {
		events, err := repo.ListPendingEvents(context.Background(), limit)
		if events != nil || !errors.Is(err, orderserrors.ErrInvalidInput) {
			t.Fatalf("limit=%d events=%v err=%v", limit, events, err)
		}
	}
	for name, call := range map[string]func(context.Context, string, time.Time) (bool, error){
		"publish": repo.MarkEventPublished,
		"retry":   repo.ScheduleEventRetry,
	} {
		t.Run(name, func(t *testing.T) {
			changed, err := call(context.Background(), "unused", time.Time{})
			if changed || !errors.Is(err, orderserrors.ErrInvalidInput) {
				t.Fatalf("changed=%v err=%v", changed, err)
			}
		})
	}
}

// Проверяет приоритет отмены контекста над валидацией и SQL.
func TestOutboxCancelledContext(t *testing.T) {
	repo := repository.NewRepository(nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	events, err := repo.ListPendingEvents(ctx, 0)
	if events != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("events=%v err=%v", events, err)
	}
	for name, call := range map[string]func(context.Context, string, time.Time) (bool, error){
		"publish": repo.MarkEventPublished,
		"retry":   repo.ScheduleEventRetry,
	} {
		t.Run(name, func(t *testing.T) {
			changed, err := call(ctx, "unused", time.Time{})
			if changed || !errors.Is(err, context.Canceled) {
				t.Fatalf("changed=%v err=%v", changed, err)
			}
		})
	}
}

// Отменённая статистика возвращает нули без обращения к БД.
func TestOutboxStatsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stats, err := repository.NewRepository(nil).GetOutboxStats(ctx)
	if !errors.Is(err, context.Canceled) || stats.PendingEvents != 0 || stats.OldestPendingAgeSeconds != 0 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
}
