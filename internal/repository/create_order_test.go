package repository_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
	"github.com/KolibriRDM/orders-service/internal/model"
	"github.com/KolibriRDM/orders-service/internal/repository"
	"github.com/KolibriRDM/orders-service/internal/service"
)

var _ service.Repository = (*repository.Repository)(nil)

// Проверяет отказ до БД при отмене и несвязанном событии.
func TestCreateIdempotentRejectsBeforeDatabase(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cancelled bool
		want      error
	}{
		{"cancelled context takes precedence", true, context.Canceled},
		{"event belongs to another order", false, orderserrors.ErrInvalidInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancelled {
				cancel()
			}
			params := model.CreateOrderParams{Order: model.Order{ID: "order"}, Event: model.OutboxEvent{OrderID: "another-order"}}
			got, err := repository.NewRepository(nil).CreateIdempotent(ctx, params)
			if !errors.Is(err, tc.want) || !reflect.DeepEqual(got, model.CreateOrderResult{}) {
				t.Fatalf("got=%+v err=%v want=%v", got, err, tc.want)
			}
		})
	}
}
