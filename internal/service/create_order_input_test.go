package service

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
	"github.com/KolibriRDM/orders-service/internal/model"
)

// Готовит корректный запрос создания заказа.
func validCreateOrderInput() model.CreateOrderInput {
	return model.CreateOrderInput{
		UserID:          "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		RestaurantID:    "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
		DeliveryAddress: "Москва, улица 1", IdempotencyKey: " key-A ",
		Items: []model.CreateOrderItemInput{
			{MenuItemID: "cccccccc-cccc-4ccc-8ccc-ccccccccccc1", Quantity: 1},
			{MenuItemID: "cccccccc-cccc-4ccc-8ccc-ccccccccccc2", Quantity: 2},
		},
	}
}

// Меняет форму UUID и порядок позиций без изменения заказа.
func alternateCreateOrderInput() model.CreateOrderInput {
	input := validCreateOrderInput()
	input.UserID = strings.ToUpper(strings.ReplaceAll(input.UserID, "-", ""))
	input.RestaurantID = strings.ToUpper(input.RestaurantID)
	input.DeliveryAddress = " \t" + input.DeliveryAddress + "\n"
	slices.Reverse(input.Items)
	for i := range input.Items {
		input.Items[i].MenuItemID = strings.ToUpper(strings.ReplaceAll(input.Items[i].MenuItemID, "-", ""))
	}
	return input
}

// Проверяет нормализацию без изменения исходного среза.
func TestNormalizeCreateOrderInputPreservesCaller(t *testing.T) {
	input := alternateCreateOrderInput()
	before := input
	before.Items = slices.Clone(input.Items)
	got, err := normalizeCreateOrderInput(input)
	if err != nil || !reflect.DeepEqual(got, validCreateOrderInput()) {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if !reflect.DeepEqual(input, before) {
		t.Fatalf("input changed: %+v", input)
	}
	got.Items[0].Quantity = 20
	if !reflect.DeepEqual(input, before) {
		t.Fatal("result aliases caller's items")
	}
	if got.IdempotencyKey != " key-A " {
		t.Fatalf("key changed: %q", got.IdempotencyKey)
	}
}

// Проверяет ограничения входных данных заказа.
func TestNormalizeCreateOrderInputRejectsInvalidValues(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*model.CreateOrderInput)
	}{
		{"invalid user", func(p *model.CreateOrderInput) { p.UserID = "user" }},
		{"invalid restaurant", func(p *model.CreateOrderInput) { p.RestaurantID = "restaurant" }},
		{"blank address", func(p *model.CreateOrderInput) { p.DeliveryAddress = " \t\n\u2003" }},
		{"long address", func(p *model.CreateOrderInput) { p.DeliveryAddress = strings.Repeat("я", 301) }},
		{"invalid UTF8 address", func(p *model.CreateOrderInput) { p.DeliveryAddress = string([]byte{0xff}) }},
		{"NUL address", func(p *model.CreateOrderInput) { p.DeliveryAddress = "street\x00" }},
		{"empty key", func(p *model.CreateOrderInput) { p.IdempotencyKey = "" }},
		{"blank key", func(p *model.CreateOrderInput) { p.IdempotencyKey = " \t\u2003" }},
		{"long key", func(p *model.CreateOrderInput) { p.IdempotencyKey = strings.Repeat("я", 129) }},
		{"invalid UTF8 key", func(p *model.CreateOrderInput) { p.IdempotencyKey = string([]byte{0xff}) }},
		{"NUL key", func(p *model.CreateOrderInput) { p.IdempotencyKey = "key\x00" }},
		{"nil items", func(p *model.CreateOrderInput) { p.Items = nil }},
		{"empty items", func(p *model.CreateOrderInput) { p.Items = []model.CreateOrderItemInput{} }},
		{"too many items", func(p *model.CreateOrderInput) { p.Items = make([]model.CreateOrderItemInput, 21) }},
		{"invalid item", func(p *model.CreateOrderInput) { p.Items[1].MenuItemID = "item" }},
		{"zero quantity", func(p *model.CreateOrderInput) { p.Items[1].Quantity = 0 }},
		{"negative quantity", func(p *model.CreateOrderInput) { p.Items[1].Quantity = -1 }},
		{"large quantity", func(p *model.CreateOrderInput) { p.Items[1].Quantity = 21 }},
		{"duplicate ID", func(p *model.CreateOrderInput) { p.Items[1].MenuItemID = p.Items[0].MenuItemID }},
		{"duplicate normalized ID", func(p *model.CreateOrderInput) {
			p.Items[1].MenuItemID = strings.ToLower(strings.ReplaceAll(p.Items[0].MenuItemID, "-", ""))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := alternateCreateOrderInput()
			tc.mutate(&input)
			before := input
			before.Items = slices.Clone(input.Items)
			got, err := normalizeCreateOrderInput(input)
			if !errors.Is(err, orderserrors.ErrInvalidInput) || !reflect.DeepEqual(got, model.CreateOrderInput{}) {
				t.Fatalf("got=%+v err=%v", got, err)
			}
			if !reflect.DeepEqual(input, before) {
				t.Fatal("failed validation changed caller's input")
			}
		})
	}
}

// Проверяет допустимые крайние значения запроса.
func TestNormalizeCreateOrderInputAcceptsBoundaries(t *testing.T) {
	for _, count := range []int{1, 20} {
		t.Run(fmt.Sprintf("%d items", count), func(t *testing.T) {
			input := validCreateOrderInput()
			input.DeliveryAddress = " " + strings.Repeat("я", 300) + " "
			input.IdempotencyKey = strings.Repeat("я", 128)
			input.Items = make([]model.CreateOrderItemInput, count)
			for i := range input.Items {
				input.Items[i] = model.CreateOrderItemInput{MenuItemID: fmt.Sprintf("cccccccc-cccc-4ccc-8ccc-%012d", i), Quantity: 20}
			}
			got, err := normalizeCreateOrderInput(input)
			if err != nil || len(got.Items) != count || got.DeliveryAddress != strings.Repeat("я", 300) || got.IdempotencyKey != input.IdempotencyKey {
				t.Fatalf("got=%+v err=%v", got, err)
			}
		})
	}
}

// Проверяет стабильность хеша и значимые изменения заказа.
func TestHashCreateOrderInput(t *testing.T) {
	hash := func(input model.CreateOrderInput) string {
		t.Helper()
		normalized, err := normalizeCreateOrderInput(input)
		if err != nil {
			t.Fatal(err)
		}
		got, err := hashCreateOrderInput(normalized)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	baseline := hash(validCreateOrderInput())
	// Фиксируем формат хеша, чтобы рефакторинг не сломал ранее сохранённые ключи.
	const want = "3d08aa6e249475861205df88e3bdc27aebc8059e2687a587e3a02170666e0191"
	if baseline != want {
		t.Fatalf("hash=%s want=%s", baseline, want)
	}
	if got := hash(alternateCreateOrderInput()); got != baseline {
		t.Fatalf("equivalent request has different hash: %s", got)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*model.CreateOrderInput)
		same   bool
	}{
		{"another user", func(p *model.CreateOrderInput) { p.UserID = "dddddddd-dddd-4ddd-8ddd-dddddddddddd" }, true},
		{"another key", func(p *model.CreateOrderInput) { p.IdempotencyKey = "other" }, true},
		{"another restaurant", func(p *model.CreateOrderInput) { p.RestaurantID = "dddddddd-dddd-4ddd-8ddd-dddddddddddd" }, false},
		{"another address", func(p *model.CreateOrderInput) { p.DeliveryAddress += ", кв. 2" }, false},
		{"internal address spaces", func(p *model.CreateOrderInput) { p.DeliveryAddress = "Москва,  улица 1" }, false},
		{"another quantity", func(p *model.CreateOrderInput) { p.Items[0].Quantity++ }, false},
		{"another item", func(p *model.CreateOrderInput) { p.Items[0].MenuItemID = "dddddddd-dddd-4ddd-8ddd-dddddddddddd" }, false},
		{"fewer items", func(p *model.CreateOrderInput) { p.Items = p.Items[:1] }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := validCreateOrderInput()
			tc.mutate(&input)
			if same := hash(input) == baseline; same != tc.same {
				t.Fatalf("same hash=%v want=%v", same, tc.same)
			}
		})
	}
}
