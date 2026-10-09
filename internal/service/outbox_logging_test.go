package service

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
	"github.com/KolibriRDM/orders-service/internal/model"
)

// Публикация журналируется только после отметки БД и содержит идентификаторы.
func TestOutboxPublicationJSONLog(t *testing.T) {
	for _, mode := range []string{"published", "unchanged", "database error", "retry"} {
		t.Run(mode, func(t *testing.T) {
			var logs bytes.Buffer
			event := outboxFixture(1)
			event.OrderID = outboxFixture(2).OrderID
			event.Payload = []byte(`{"private":"payload-canary"}`)
			repo := &outboxRepositoryStub{
				list: func(context.Context, int) ([]model.OutboxEvent, error) { return []model.OutboxEvent{event}, nil },
				mark: func(context.Context, string, time.Time) (bool, error) {
					if mode == "database error" {
						return false, orderserrors.ErrDatabaseUnavailable
					}
					return mode != "unchanged", nil
				},
				retry: func(context.Context, string, time.Time) (bool, error) { return true, nil },
			}
			publisher := outboxPublisherStub(func(context.Context, model.OutboxEvent) error {
				if mode == "retry" {
					return orderserrors.ErrPublishFailed
				}
				return nil
			})
			logger := slog.New(slog.NewJSONHandler(&logs, nil)).With("service", "orders-service")
			err := NewOutboxService(repo, publisher, nil, logger).PublishBatch(context.Background())
			if mode == "database error" {
				if err == nil || !strings.Contains(err.Error(), event.EventID) || !strings.Contains(err.Error(), event.OrderID) {
					t.Fatalf("missing failure identifiers: %v", err)
				}
				if logs.Len() != 0 {
					t.Fatalf("false success: %s", &logs)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var entry map[string]any
			if err := json.Unmarshal(logs.Bytes(), &entry); err != nil {
				t.Fatal(err)
			}
			if entry["event_id"] != event.EventID || entry["order_id"] != event.OrderID || entry["service"] != "orders-service" {
				t.Fatalf("entry=%v", entry)
			}
			if (entry["msg"] == "outbox event published") != (mode == "published") {
				t.Fatalf("false publication: %v", entry)
			}
			if strings.Contains(logs.String(), "payload-canary") {
				t.Fatal("payload leaked")
			}
		})
	}
}
