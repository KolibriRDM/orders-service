package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
	"github.com/KolibriRDM/orders-service/internal/model"
)

// Тестовая подмена поиска ключа и записи заказа.
type createOrderRepositoryStub struct {
	lookup func(context.Context, string, string) (model.IdempotentOrder, error)
	create func(context.Context, model.CreateOrderParams) (model.CreateOrderResult, error)
}

// Вызывает тестовую подмену операции.
func (r *createOrderRepositoryStub) GetByIdempotencyKey(ctx context.Context, userID, key string) (model.IdempotentOrder, error) {
	return r.lookup(ctx, userID, key)
}

// Вызывает тестовую подмену операции.
func (r *createOrderRepositoryStub) CreateIdempotent(ctx context.Context, params model.CreateOrderParams) (model.CreateOrderResult, error) {
	return r.create(ctx, params)
}

// Выявляет неожиданный вызов чтения в сценарии создания.
func (r *createOrderRepositoryStub) GetByID(context.Context, string) (model.Order, error) {
	panic("CreateOrder must not call GetByID")
}

// Тестовая подмена получения блюд каталога.
type createOrderCatalogStub func(context.Context, string, []string) ([]model.MenuItem, error)

// Вызывает тестовую подмену операции.
func (f createOrderCatalogStub) ResolveItems(ctx context.Context, restaurantID string, ids []string) ([]model.MenuItem, error) {
	return f(ctx, restaurantID, ids)
}

// Готовит цены каталога в обратном порядке позиций.
func createOrderMenu() []model.MenuItem {
	input := validCreateOrderInput()
	// Другой порядок ответа проверяет сопоставление блюд по ID.
	return []model.MenuItem{
		{ID: input.Items[1].MenuItemID, RestaurantID: input.RestaurantID, Name: "Морс", PriceKopecks: 10000, IsAvailable: true},
		{ID: input.Items[0].MenuItemID, RestaurantID: input.RestaurantID, Name: "Пицца", PriceKopecks: 25000, IsAvailable: true},
	}
}

// Имитирует отсутствие сохранённого ключа.
func missingCreateOrder(context.Context, string, string) (model.IdempotentOrder, error) {
	return model.IdempotentOrder{}, fmt.Errorf("lookup: %w", orderserrors.ErrNotFound)
}

// Возвращает корректные тестовые блюда каталога.
func resolvedCreateOrder(context.Context, string, []string) ([]model.MenuItem, error) {
	return createOrderMenu(), nil
}

// Проверяет расчёт заказа, снимки и контракт события.
func TestCreateOrderPersistsSnapshotsAndEvent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	input := alternateCreateOrderInput()
	before := input
	before.Items = slices.Clone(input.Items)
	normalized := validCreateOrderInput()
	var calls []string
	var saved model.CreateOrderParams
	repo := &createOrderRepositoryStub{
		lookup: func(gotCtx context.Context, userID, key string) (model.IdempotentOrder, error) {
			calls = append(calls, "lookup")
			if gotCtx != ctx || userID != normalized.UserID || key != normalized.IdempotencyKey {
				t.Fatalf("lookup arguments: %s %q", userID, key)
			}
			return model.IdempotentOrder{}, orderserrors.ErrNotFound
		},
		create: func(gotCtx context.Context, p model.CreateOrderParams) (model.CreateOrderResult, error) {
			calls = append(calls, "create")
			if gotCtx != ctx {
				t.Fatal("lost request context")
			}
			saved = p
			return model.CreateOrderResult{Order: p.Order, Created: true}, nil
		},
	}
	catalog := createOrderCatalogStub(func(gotCtx context.Context, restaurantID string, ids []string) ([]model.MenuItem, error) {
		calls = append(calls, "catalog")
		want := []string{normalized.Items[0].MenuItemID, normalized.Items[1].MenuItemID}
		if gotCtx != ctx || restaurantID != normalized.RestaurantID || !slices.Equal(ids, want) {
			t.Fatalf("catalog arguments: %s %v", restaurantID, ids)
		}
		return createOrderMenu(), nil
	})
	started := time.Now().UTC().Truncate(time.Microsecond)
	got, err := NewService(repo, catalog).CreateOrder(ctx, input)
	if err != nil || !got.Created || !reflect.DeepEqual(got.Order, saved.Order) {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if !slices.Equal(calls, []string{"lookup", "catalog", "create"}) {
		t.Fatalf("call order=%v", calls)
	}
	if !reflect.DeepEqual(input, before) {
		t.Fatal("caller input changed")
	}
	order := saved.Order
	if err := order.Validate(); err != nil {
		t.Fatal(err)
	}
	if order.UserID != normalized.UserID || order.RestaurantID != normalized.RestaurantID || order.DeliveryAddress != normalized.DeliveryAddress || order.Status != model.StatusCreated || order.Version != 1 || order.TotalKopecks != 64900 {
		t.Fatalf("order=%+v", order)
	}
	wantItems := []model.OrderItem{
		{MenuItemID: normalized.Items[0].MenuItemID, NameSnapshot: "Пицца", UnitPriceKopecks: 25000, Quantity: 1},
		{MenuItemID: normalized.Items[1].MenuItemID, NameSnapshot: "Морс", UnitPriceKopecks: 10000, Quantity: 2},
	}
	if !reflect.DeepEqual(order.Items, wantItems) {
		t.Fatalf("snapshots=%+v", order.Items)
	}
	if order.CreatedAt.Before(started) || order.CreatedAt.After(time.Now()) || order.CreatedAt.Location() != time.UTC || order.CreatedAt.Nanosecond()%1000 != 0 || !order.CreatedAt.Equal(order.UpdatedAt) {
		t.Fatalf("timestamps=%v / %v", order.CreatedAt, order.UpdatedAt)
	}
	for _, id := range []string{order.ID, saved.Event.EventID} {
		canonical, err := model.NormalizeID(id)
		if err != nil || id != canonical || id[14] != '4' || !strings.ContainsRune("89ab", rune(id[19])) {
			t.Fatalf("invalid UUID v4=%s err=%v", id, err)
		}
	}
	if order.ID == saved.Event.EventID || saved.Event.OrderID != order.ID || saved.Event.Type != model.OrderCreatedEventType || !saved.Event.CreatedAt.Equal(order.CreatedAt) {
		t.Fatalf("event=%+v", saved.Event)
	}
	if saved.IdempotencyKey != normalized.IdempotencyKey || saved.RequestHash != "3d08aa6e249475861205df88e3bdc27aebc8059e2687a587e3a02170666e0191" {
		t.Fatalf("key/hash=%q / %q", saved.IdempotencyKey, saved.RequestHash)
	}
	// Независимый список JSON-ключей выявляет опечатки в тегах.
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(saved.Event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	wantPayload := map[string]any{
		"event_id": saved.Event.EventID, "type": "order.created.v1", "schema_version": 1,
		"occurred_at": order.CreatedAt, "order_id": order.ID, "user_id": order.UserID, "total_kopecks": int64(64900),
	}
	if len(payload) != len(wantPayload) {
		t.Fatalf("unexpected event fields: %s", saved.Event.Payload)
	}
	for key, value := range wantPayload {
		want, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if string(payload[key]) != string(want) {
			t.Errorf("event[%s]=%s want=%s", key, payload[key], want)
		}
	}
}

// Проверяет обработку сохранённого ключа без каталога.
func TestCreateOrderReplaySkipsCatalog(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprintf("conflict=%v", conflict), func(t *testing.T) {
			existing := model.Order{ID: "saved-order", Status: model.StatusCooking, Version: 2, TotalKopecks: 12345}
			hash := "3d08aa6e249475861205df88e3bdc27aebc8059e2687a587e3a02170666e0191"
			if conflict {
				hash = strings.Repeat("b", 64)
			}
			repo := &createOrderRepositoryStub{lookup: func(context.Context, string, string) (model.IdempotentOrder, error) {
				return model.IdempotentOrder{Order: existing, RequestHash: hash}, nil
			}}
			// Nil-зависимости доказывают отсутствие вызовов при повторе.
			got, err := NewService(repo, nil).CreateOrder(context.Background(), alternateCreateOrderInput())
			if conflict {
				if !errors.Is(err, orderserrors.ErrIdempotencyConflict) || !reflect.DeepEqual(got, model.CreateOrderResult{}) {
					t.Fatalf("got=%+v err=%v", got, err)
				}
			} else if err != nil || got.Created || !reflect.DeepEqual(got.Order, existing) {
				t.Fatalf("got=%+v err=%v", got, err)
			}
		})
	}
}

// Проверяет отмену и валидацию до обращения к зависимостям.
func TestCreateOrderEarlyExit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := NewService(nil, nil).CreateOrder(ctx, model.CreateOrderInput{})
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, model.CreateOrderResult{}) {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	got, err = NewService(nil, nil).CreateOrder(context.Background(), model.CreateOrderInput{})
	if !errors.Is(err, orderserrors.ErrInvalidInput) || !reflect.DeepEqual(got, model.CreateOrderResult{}) {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

// Проверяет сохранение ошибок зависимостей без частичного заказа.
func TestCreateOrderDependencyErrors(t *testing.T) {
	for _, tc := range []struct {
		stage string
		cause error
	}{
		{"lookup", orderserrors.ErrDatabaseUnavailable},
		{"lookup", context.DeadlineExceeded},
		{"catalog", orderserrors.ErrCatalogUnavailable},
		{"catalog", orderserrors.ErrInvalidInput},
		{"catalog", orderserrors.ErrNotFound},
		{"catalog", context.DeadlineExceeded},
		{"create", orderserrors.ErrDatabaseUnavailable},
		{"create", orderserrors.ErrIdempotencyConflict},
		{"create", context.DeadlineExceeded},
		{"create", errors.New("unexpected storage error")},
	} {
		t.Run(tc.stage+"/"+tc.cause.Error(), func(t *testing.T) {
			repo := &createOrderRepositoryStub{lookup: missingCreateOrder}
			var catalog createOrderCatalogStub
			switch tc.stage {
			case "lookup":
				repo.lookup = func(context.Context, string, string) (model.IdempotentOrder, error) {
					return model.IdempotentOrder{}, fmt.Errorf("dependency: %w", tc.cause)
				}
			case "catalog":
				catalog = func(context.Context, string, []string) ([]model.MenuItem, error) {
					return createOrderMenu(), fmt.Errorf("dependency: %w", tc.cause)
				}
			case "create":
				catalog = resolvedCreateOrder
				repo.create = func(context.Context, model.CreateOrderParams) (model.CreateOrderResult, error) {
					return model.CreateOrderResult{Created: true}, fmt.Errorf("dependency: %w", tc.cause)
				}
			}
			got, err := NewService(repo, catalog).CreateOrder(context.Background(), validCreateOrderInput())
			if !errors.Is(err, tc.cause) || !reflect.DeepEqual(got, model.CreateOrderResult{}) {
				t.Fatalf("got=%+v err=%v", got, err)
			}
		})
	}
}

// Проверяет отказ при нарушении контракта каталога.
func TestCreateOrderRejectsMalformedCatalogResults(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func([]model.MenuItem) []model.MenuItem
	}{
		{"missing item", func(m []model.MenuItem) []model.MenuItem { return m[:1] }},
		{"extra item", func(m []model.MenuItem) []model.MenuItem { return append(m, m[0]) }},
		{"duplicate ID", func(m []model.MenuItem) []model.MenuItem { m[1].ID = m[0].ID; return m }},
		{"unknown ID", func(m []model.MenuItem) []model.MenuItem { m[0].ID = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"; return m }},
		{"wrong restaurant", func(m []model.MenuItem) []model.MenuItem { m[0].RestaurantID = "other"; return m }},
		{"unavailable", func(m []model.MenuItem) []model.MenuItem { m[0].IsAvailable = false; return m }},
		{"blank name", func(m []model.MenuItem) []model.MenuItem { m[0].Name = " \t"; return m }},
		{"NUL name", func(m []model.MenuItem) []model.MenuItem { m[0].Name = "x\x00"; return m }},
		{"invalid UTF8 name", func(m []model.MenuItem) []model.MenuItem { m[0].Name = string([]byte{0xff}); return m }},
		{"zero price", func(m []model.MenuItem) []model.MenuItem { m[0].PriceKopecks = 0; return m }},
		{"negative price", func(m []model.MenuItem) []model.MenuItem { m[0].PriceKopecks = -1; return m }},
		{"price above limit", func(m []model.MenuItem) []model.MenuItem { m[0].PriceKopecks = model.MaxUnitPrice + 1; return m }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &createOrderRepositoryStub{lookup: missingCreateOrder}
			catalog := createOrderCatalogStub(func(context.Context, string, []string) ([]model.MenuItem, error) {
				return tc.mutate(createOrderMenu()), nil
			})
			got, err := NewService(repo, catalog).CreateOrder(context.Background(), validCreateOrderInput())
			if !errors.Is(err, orderserrors.ErrCatalogUnavailable) || !reflect.DeepEqual(got, model.CreateOrderResult{}) {
				t.Fatalf("got=%+v err=%v", got, err)
			}
		})
	}
}

// Проверяет остановку сценария после отмены контекста.
func TestCreateOrderStopsAfterCancellation(t *testing.T) {
	for _, stage := range []string{"lookup", "catalog"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			repo := &createOrderRepositoryStub{lookup: missingCreateOrder}
			var catalog createOrderCatalogStub
			if stage == "lookup" {
				repo.lookup = func(context.Context, string, string) (model.IdempotentOrder, error) {
					cancel()
					return model.IdempotentOrder{}, orderserrors.ErrNotFound
				}
			} else {
				catalog = func(context.Context, string, []string) ([]model.MenuItem, error) {
					cancel()
					return createOrderMenu(), nil
				}
			}
			got, err := NewService(repo, catalog).CreateOrder(ctx, validCreateOrderInput())
			if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, model.CreateOrderResult{}) {
				t.Fatalf("got=%+v err=%v", got, err)
			}
		})
	}
}

// Проверяет возврат заказа победившего конкурентного запроса.
func TestCreateOrderReturnsRepositoryWinner(t *testing.T) {
	winner := model.CreateOrderResult{Order: model.Order{ID: "concurrent-winner", Status: model.StatusCooking, Version: 2}, Created: false}
	repo := &createOrderRepositoryStub{lookup: missingCreateOrder, create: func(context.Context, model.CreateOrderParams) (model.CreateOrderResult, error) { return winner, nil }}
	got, err := NewService(repo, createOrderCatalogStub(resolvedCreateOrder)).CreateOrder(context.Background(), validCreateOrderInput())
	if err != nil || !reflect.DeepEqual(got, winner) {
		t.Fatalf("got=%+v err=%v want=%+v", got, err, winner)
	}
}

// Проверяет неизменность старых снимков при смене цены.
func TestCreateOrderSnapshotsSurviveMenuChange(t *testing.T) {
	menu := createOrderMenu()
	var saved []model.CreateOrderParams
	repo := &createOrderRepositoryStub{lookup: missingCreateOrder, create: func(_ context.Context, p model.CreateOrderParams) (model.CreateOrderResult, error) {
		saved = append(saved, p)
		return model.CreateOrderResult{Order: p.Order, Created: true}, nil
	}}
	catalog := createOrderCatalogStub(func(context.Context, string, []string) ([]model.MenuItem, error) { return menu, nil })
	svc := NewService(repo, catalog)
	input := validCreateOrderInput()
	first, err := svc.CreateOrder(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	menu[1].Name, menu[1].PriceKopecks = "Новая пицца", 30000
	input.IdempotencyKey = "new-order"
	second, err := svc.CreateOrder(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if first.Order.Items[0].NameSnapshot != "Пицца" || first.Order.Items[0].UnitPriceKopecks != 25000 || first.Order.TotalKopecks != 64900 {
		t.Fatalf("old snapshot changed: %+v", first)
	}
	if second.Order.Items[0].NameSnapshot != "Новая пицца" || second.Order.Items[0].UnitPriceKopecks != 30000 || second.Order.TotalKopecks != 69900 {
		t.Fatalf("new snapshot=%+v", second)
	}
	if saved[0].RequestHash != saved[1].RequestHash {
		t.Fatal("menu prices must not affect request hash")
	}
}
