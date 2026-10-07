package kafkapublisher

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/KolibriRDM/orders-service/internal/model"
	"github.com/twmb/franz-go/pkg/kgo"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
)

const publishTimeout = 5 * time.Second

// Операции Kafka-клиента, необходимые для публикации.
type producer interface {
	TryProduce(context.Context, *kgo.Record, func(*kgo.Record, error))
	Close()
}

// Отправляет события outbox в Kafka.
type Publisher struct {
	client producer
	topic  string
}

// Создаёт Kafka-клиент для публикации событий.
func NewPublisher(brokers []string, topic string) (*Publisher, error) {
	if len(brokers) == 0 {
		return nil, fmt.Errorf("create Kafka publisher: brokers must not be empty")
	}

	topic = strings.TrimSpace(topic)
	if topic == "" {
		return nil, fmt.Errorf("create Kafka publisher: topic must not be empty")
	}

	addresses := make([]string, len(brokers))

	for i, broker := range brokers {
		addresses[i] = strings.TrimSpace(broker)
		if addresses[i] == "" {
			return nil, fmt.Errorf("create Kafka publisher: broker at index %d must not be empty", i)
		}
	}

	// Ограничиваем буфер и ждём подтверждения всех синхронных реплик.
	client, err := kgo.NewClient(
		kgo.SeedBrokers(addresses...),
		kgo.ClientID("orders-service"),
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.DialTimeout(2*time.Second),
		kgo.ProduceRequestTimeout(2*time.Second),
		kgo.RecordDeliveryTimeout(publishTimeout),
		kgo.MaxBufferedRecords(100),
		kgo.MaxBufferedBytes(1<<20),
	)
	if err != nil {
		return nil, fmt.Errorf("create Kafka publisher: %w", err)
	}
	return &Publisher{client: client, topic: topic}, nil
}

// Отправляет событие и ожидает подтверждения либо отмены контекста.
func (p *Publisher) Publish(ctx context.Context, event model.OutboxEvent) error {
	err := ctx.Err()
	if err != nil {
		return err
	}
	if event.Type != model.OrderCreatedEventType {
		return fmt.Errorf("publish event: unsupported event type: %w", orderserrors.ErrInvalidInput)
	}
	if event.EventID == "" || event.OrderID == "" {
		return fmt.Errorf("publish event: event_id and order_id are required: %w", orderserrors.ErrInvalidInput)
	}
	if len(event.Payload) == 0 {
		return fmt.Errorf("publish event: payload must not be empty: %w", orderserrors.ErrInvalidInput)
	}
	publishCtx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()

	// Ключ заказа определяет партицию; payload отправляем без изменений.
	record := &kgo.Record{
		Topic: p.topic,
		Key:   []byte(event.OrderID),
		Value: event.Payload,
	}

	// Буфер позволяет callback завершиться после выхода по таймауту.
	result := make(chan error, 1)
	p.client.TryProduce(
		publishCtx,
		record,
		func(_ *kgo.Record, err error) {
			result <- err
		},
	)
	select {
	case err := <-result:
		if err == nil {
			return nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("publish event: %w: %w", orderserrors.ErrPublishFailed, err)
	case <-publishCtx.Done():
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("publish event: %w: %w", orderserrors.ErrPublishFailed, publishCtx.Err())
	}
}

// Закрывает соединения и останавливает Kafka-клиент.
func (p *Publisher) Close() {
	p.client.Close()
}
