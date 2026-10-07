//go:build integration

package kafkapublisher

import (
	"context"
	"crypto/rand"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
)

// Проверяет публикацию и ключ партиционирования в отдельном временном топике.
func TestPublisherKafka(t *testing.T) {
	raw := strings.TrimSpace(os.Getenv("KAFKA_TEST_BROKERS"))
	if raw == "" {
		t.Fatal("set KAFKA_TEST_BROKERS to the local test Kafka cluster")
	}
	brokers := strings.Split(raw, ",")
	for i := range brokers {
		brokers[i] = strings.TrimSpace(brokers[i])
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.DialTimeout(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	topic := "orders-publisher-test-" + strings.ToLower(rand.Text())
	create := kmsg.NewPtrCreateTopicsRequest()
	create.TimeoutMillis = 10000
	create.Topics = []kmsg.CreateTopicsRequestTopic{{Topic: topic, NumPartitions: 3, ReplicationFactor: 1}}
	response, err := create.RequestWith(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Topics) != 1 || response.Topics[0].ErrorCode != 0 {
		t.Fatalf("create topic response=%+v", response.Topics)
	}
	// Удаляем только созданный этим тестом топик даже при провале проверок.
	defer func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		request := kmsg.NewPtrDeleteTopicsRequest()
		request.TopicNames = []string{topic}
		request.Topics = []kmsg.DeleteTopicsRequestTopic{{Topic: &topic}}
		result, err := request.RequestWith(cleanupCtx, admin)
		if err != nil {
			t.Errorf("delete test topic %s: %v", topic, err)
			return
		}
		if len(result.Topics) != 1 {
			t.Errorf("unexpected delete response: %+v", result)
			return
		}
		if err := kerr.ErrorForCode(result.Topics[0].ErrorCode); err != nil {
			t.Errorf("delete test topic %s: %v", topic, err)
		}
	}()
	t.Logf("temporary topic: %s", topic)
	publisher, err := NewPublisher(brokers, topic)
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Close()
	event := testEvent()
	// Повтор outbox сохраняет event_id; Kafka не дедуплицирует отдельные вызовы по нему.
	for i := 0; i < 3; i++ {
		if err := publisher.Publish(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	consumer, err := kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{
		topic: {0: kgo.NewOffset().AtStart(), 1: kgo.NewOffset().AtStart(), 2: kgo.NewOffset().AtStart()},
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()
	var records []*kgo.Record
	for len(records) < 3 {
		fetches := consumer.PollRecords(ctx, 3-len(records))
		if errs := fetches.Errors(); len(errs) != 0 {
			t.Fatalf("fetch errors=%v", errs)
		}
		records = append(records, fetches.Records()...)
	}
	for i, record := range records {
		if record.Topic != topic || string(record.Key) != event.OrderID || string(record.Value) != string(event.Payload) {
			t.Fatalf("unexpected record=%+v", record)
		}
		if record.Partition != records[0].Partition || record.Offset != int64(i) {
			t.Fatalf("partition=%d offset=%d index=%d", record.Partition, record.Offset, i)
		}
	}
}
