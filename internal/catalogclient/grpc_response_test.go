package catalogclient

import (
	"errors"
	"math"
	"reflect"
	"testing"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
	"github.com/KolibriRDM/orders-service/internal/model"
	"github.com/KolibriRDM/orders-service/internal/transport/grpc/catalogv1"
	"google.golang.org/protobuf/proto"
)

// Создаёт независимый protobuf-ответ с обратным порядком блюд.
func validGRPCResponse() *catalogv1.ResolveItemsResponse {
	return &catalogv1.ResolveItemsResponse{
		RestaurantId: restaurantID,
		Items: []*catalogv1.ResolvedItem{
			{Id: secondID, Name: "Морс", PriceKopecks: 10000},
			{Id: firstID, Name: "Маргарита", PriceKopecks: 25000},
		},
	}
}

// Проверяет поля, порядок блюд и сохранность входного ответа.
func TestToMenuItemsSuccess(t *testing.T) {
	response := validGRPCResponse()
	original := proto.Clone(response)
	itemIDs := []string{firstID, secondID}
	got, err := toMenuItems(restaurantID, itemIDs, response)
	if err != nil {
		t.Fatal(err)
	}
	want := []model.MenuItem{
		{ID: firstID, RestaurantID: restaurantID, Name: "Маргарита", PriceKopecks: 25000, IsAvailable: true},
		{ID: secondID, RestaurantID: restaurantID, Name: "Морс", PriceKopecks: 10000, IsAvailable: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("items = %#v, want %#v", got, want)
	}
	if !proto.Equal(response, original) || !reflect.DeepEqual(itemIDs, []string{firstID, secondID}) {
		t.Fatal("input data was modified")
	}
}

// Проверяет отклонение отсутствующего ответа каталога.
func TestToMenuItemsNilResponse(t *testing.T) {
	items, err := toMenuItems(restaurantID, []string{firstID}, nil)
	if !errors.Is(err, orderserrors.ErrCatalogUnavailable) || items != nil {
		t.Fatalf("items=%v error=%v, want nil and catalog unavailable", items, err)
	}
}

// Проверяет отклонение некорректных ответов без частичного результата.
func TestToMenuItemsMalformedResponse(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*catalogv1.ResolveItemsResponse)
	}{
		{"wrong restaurant", func(r *catalogv1.ResolveItemsResponse) { r.RestaurantId = "another-restaurant" }},
		{"missing restaurant", func(r *catalogv1.ResolveItemsResponse) { r.RestaurantId = "" }},
		{"no items", func(r *catalogv1.ResolveItemsResponse) { r.Items = nil }},
		{"missing item", func(r *catalogv1.ResolveItemsResponse) { r.Items = r.Items[:1] }},
		{"extra item", func(r *catalogv1.ResolveItemsResponse) { r.Items = append(r.Items, r.Items[0]) }},
		{"nil item", func(r *catalogv1.ResolveItemsResponse) { r.Items[1] = nil }},
		{"foreign item", func(r *catalogv1.ResolveItemsResponse) { r.Items[1].Id = "another-item" }},
		{"empty ID", func(r *catalogv1.ResolveItemsResponse) { r.Items[1].Id = "" }},
		{"duplicate item", func(r *catalogv1.ResolveItemsResponse) { r.Items[1].Id = r.Items[0].Id }},
		{"empty name", func(r *catalogv1.ResolveItemsResponse) { r.Items[1].Name = "" }},
		{"blank name", func(r *catalogv1.ResolveItemsResponse) { r.Items[1].Name = " \t\n" }},
		{"zero price", func(r *catalogv1.ResolveItemsResponse) { r.Items[1].PriceKopecks = 0 }},
		{"negative price", func(r *catalogv1.ResolveItemsResponse) { r.Items[1].PriceKopecks = -1 }},
		{"excessive price", func(r *catalogv1.ResolveItemsResponse) { r.Items[1].PriceKopecks = int64(model.MaxUnitPrice) + 1 }},
		{"maximum int64 price", func(r *catalogv1.ResolveItemsResponse) { r.Items[1].PriceKopecks = math.MaxInt64 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := validGRPCResponse()
			tc.change(response)
			items, err := toMenuItems(restaurantID, []string{firstID, secondID}, response)
			if !errors.Is(err, orderserrors.ErrCatalogUnavailable) || items != nil {
				t.Fatalf("items=%v error=%v, want nil and catalog unavailable", items, err)
			}
		})
	}
}

// Проверяет допустимые крайние значения цены без потери точности.
func TestToMenuItemsPriceBounds(t *testing.T) {
	for _, price := range []int64{1, int64(model.MaxUnitPrice)} {
		response := validGRPCResponse()
		response.Items[1].PriceKopecks = price
		items, err := toMenuItems(restaurantID, []string{firstID, secondID}, response)
		if err != nil {
			t.Fatalf("price=%d: %v", price, err)
		}
		if len(items) != 2 || items[0].PriceKopecks != model.Money(price) {
			t.Fatalf("price=%d: unexpected items %v", price, items)
		}
	}
}
