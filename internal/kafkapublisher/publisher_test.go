package kafkapublisher

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
	"github.com/KolibriRDM/orders-service/internal/model"
	"github.com/twmb/franz-go/pkg/kgo"
)

// Подменяет только отправку и закрытие Kafka-клиента в тестах.
type producerStub struct {
	produce func(context.Context, *kgo.Record, func(*kgo.Record, error))
	close   func()
}

// Передаёт сообщение тестовому сценарию.
func (s *producerStub) TryProduce(ctx context.Context, r *kgo.Record, done func(*kgo.Record, error)) {
	s.produce(ctx, r, done)
}

// Фиксирует закрытие тестового клиента.
func (s *producerStub) Close() {
	if s.close != nil {
		s.close()
	}
}

// Создаёт сохранённое событие с неизменяемым идентификатором.
func testEvent() model.OutboxEvent {
	return model.OutboxEvent{
		EventID: "00000000-0000-4000-8000-000000000001",
		OrderID: "00000000-0000-4000-8000-000000000002",
		Type:    model.OrderCreatedEventType,
		Payload: []byte(`{"event_id":"00000000-0000-4000-8000-000000000001","order_id":"00000000-0000-4000-8000-000000000002","type":"order.created.v1","schema_version":1,"occurred_at":"2026-10-07T12:00:00Z","user_id":"00000000-0000-4000-8000-000000000003","total_kopecks":79900}`),
	}
}

// Проверяет пустые настройки и нормализацию без изменения входного среза.
func TestNewPublisher(t *testing.T) {
	for _, tc := range []struct {
		name        string
		brokers     []string
		topic, want string
	}{
		{"no brokers", nil, "topic", "brokers must not be empty"},
		{"empty broker", []string{"127.0.0.1:19092", "  "}, "topic", "broker at index 1 must not be empty"},
		{"empty topic", []string{"127.0.0.1:19092"}, " \t", "topic must not be empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := NewPublisher(tc.brokers, tc.topic)
			if p != nil {
				p.Close()
				t.Fatal("unexpected publisher")
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v", err)
			}
		})
	}
	brokers := []string{" 127.0.0.1:19092 "}
	p, err := NewPublisher(brokers, " food.order-created.v1 ")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if brokers[0] != " 127.0.0.1:19092 " || p.topic != "food.order-created.v1" {
		t.Fatalf("brokers=%v topic=%q", brokers, p.topic)
	}
	client := p.client.(*kgo.Client)
	if got := client.OptValue(kgo.SeedBrokers); !reflect.DeepEqual(got, []string{"127.0.0.1:19092"}) {
		t.Fatalf("brokers=%v", got)
	}
}

// Проверяет ключ, исходный payload и ожидание подтверждения.
func TestPublishAcknowledgement(t *testing.T) {
	event := testEvent()
	var callback func(*kgo.Record, error)
	var record *kgo.Record
	var sendCtx context.Context
	started := make(chan struct{})
	p := &Publisher{topic: "food.order-created.v1", client: &producerStub{produce: func(ctx context.Context, r *kgo.Record, done func(*kgo.Record, error)) {
		sendCtx, record, callback = ctx, r, done
		close(started)
	}}}
	result := make(chan error, 1)
	go func() { result <- p.Publish(context.Background(), event) }()
	<-started
	if record.Topic != p.topic || string(record.Key) != event.OrderID || string(record.Value) != string(event.Payload) {
		t.Errorf("record=%+v", record)
	}
	if deadline, ok := sendCtx.Deadline(); !ok || time.Until(deadline) > publishTimeout {
		t.Error("missing bounded deadline")
	}
	select {
	case err := <-result:
		t.Fatalf("returned before acknowledgement: %v", err)
	default:
	}
	callback(record, nil)
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Publish did not finish")
	}
	if sendCtx.Err() != context.Canceled {
		t.Fatalf("context not released: %v", sendCtx.Err())
	}
}

// Проверяет категорию ошибки и сохранение исходной причины Kafka.
func TestPublishPreservesCause(t *testing.T) {
	for _, cause := range []error{errors.New("broker rejected record"), kgo.ErrMaxBuffered, kgo.ErrClientClosed} {
		t.Run(cause.Error(), func(t *testing.T) {
			p := &Publisher{client: &producerStub{produce: func(_ context.Context, r *kgo.Record, done func(*kgo.Record, error)) { done(r, cause) }}}
			err := p.Publish(context.Background(), testEvent())
			if !errors.Is(err, orderserrors.ErrPublishFailed) || !errors.Is(err, cause) {
				t.Fatalf("err=%v lost cause=%v", err, cause)
			}
		})
	}
}

// Проверяет входные данные без обращения к Kafka.
func TestPublishInvalidInput(t *testing.T) {
	for name, mutate := range map[string]func(*model.OutboxEvent){
		"type":     func(e *model.OutboxEvent) { e.Type = "unknown" },
		"event ID": func(e *model.OutboxEvent) { e.EventID = "" },
		"order ID": func(e *model.OutboxEvent) { e.OrderID = "" },
		"payload":  func(e *model.OutboxEvent) { e.Payload = nil },
	} {
		t.Run(name, func(t *testing.T) {
			event := testEvent()
			mutate(&event)
			err := (&Publisher{}).Publish(context.Background(), event)
			if !errors.Is(err, orderserrors.ErrInvalidInput) {
				t.Fatalf("err=%v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (&Publisher{}).Publish(ctx, model.OutboxEvent{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

// Проверяет отмену родителя, внутренний таймаут и поздний callback.
func TestPublishCancellationAndTimeout(t *testing.T) {
	for _, mode := range []string{"cancel", "parent deadline", "internal timeout"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			if mode == "parent deadline" {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 40*time.Millisecond)
			}
			defer cancel()
			var callback func(*kgo.Record, error)
			var record *kgo.Record
			p := &Publisher{client: &producerStub{produce: func(_ context.Context, r *kgo.Record, done func(*kgo.Record, error)) {
				record, callback = r, done
				if mode == "cancel" {
					cancel()
				}
			}}}
			started := time.Now()
			err := p.Publish(ctx, testEvent())
			want := context.DeadlineExceeded
			if mode == "cancel" {
				want = context.Canceled
			}
			if !errors.Is(err, want) {
				t.Fatalf("err=%v want=%v", err, want)
			}
			if errors.Is(err, orderserrors.ErrPublishFailed) != (mode == "internal timeout") {
				t.Fatalf("wrong category: %v", err)
			}
			if time.Since(started) > publishTimeout+2*time.Second {
				t.Fatal("timeout not respected")
			}
			// Позднее подтверждение не должно блокировать поток callback клиента.
			finished := make(chan struct{})
			go func() { callback(record, nil); close(finished) }()
			select {
			case <-finished:
			case <-time.After(time.Second):
				t.Fatal("late callback blocked")
			}
		})
	}
}

// Проверяет безопасность одновременных публикаций и делегирование Close.
func TestPublisherConcurrentUseAndClose(t *testing.T) {
	closed := false
	p := &Publisher{client: &producerStub{
		produce: func(_ context.Context, r *kgo.Record, done func(*kgo.Record, error)) { done(r, nil) },
		close:   func() { closed = true },
	}}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Go(func() {
			if err := p.Publish(context.Background(), testEvent()); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	p.Close()
	if !closed {
		t.Fatal("client not closed")
	}
}

// Проверяет ошибку настоящего закрытого клиента без подключения к брокеру.
func TestPublishClosedClient(t *testing.T) {
	p, err := NewPublisher([]string{"127.0.0.1:19092"}, "food.order-created.v1")
	if err != nil {
		t.Fatal(err)
	}
	p.Close()
	err = p.Publish(context.Background(), testEvent())
	if !errors.Is(err, orderserrors.ErrPublishFailed) || !errors.Is(err, kgo.ErrClientClosed) {
		t.Fatalf("err=%v", err)
	}
}
