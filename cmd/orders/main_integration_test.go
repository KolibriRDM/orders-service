//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/KolibriRDM/orders-service/internal/model"
	"github.com/KolibriRDM/orders-service/internal/repository"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
)

// Запускает настоящий main в отдельном тестовом процессе.
func TestOrdersProcessHelper(t *testing.T) {
	if os.Getenv("ORDERS_PROCESS_HELPER") != "1" {
		return
	}
	main()
	os.Exit(0)
}

// Защищает журнал дочернего процесса от конкурентного чтения.
type processLog struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

// Записывает очередную часть вывода процесса.
func (b *processLog) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(data)
}

// Возвращает согласованный снимок журнала.
func (b *processLog) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.buffer.String() }

// Хранит процесс и результат его завершения.
type runningProcess struct {
	command *exec.Cmd
	logs    processLog
	done    chan struct{}
	err     error
}

// Запускает процесс с изолированной БД и гарантирует его очистку.
func startOrders(t *testing.T, ctx context.Context, address, dsn, password string) *runningProcess {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	settings := map[string]string{
		"ORDERS_PROCESS_HELPER": "1", "ORDERS_HTTP_ADDR": address,
		"ORDERS_DATABASE_URL": dsn, "ORDERS_DB_PASSWORD": password, "LOG_LEVEL": "info",
		"KAFKA_BROKERS": os.Getenv("ORDERS_RELAY_TEST_BROKER"), "ORDER_CREATED_TOPIC": "orders-relay-test",
		"CATALOG_GRPC_ADDR": "127.0.0.1:1",
	}
	env := make([]string, 0, len(os.Environ())+len(settings))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, replace := settings[key]; !replace {
			env = append(env, entry)
		}
	}
	for key, value := range settings {
		env = append(env, key+"="+value)
	}
	p := &runningProcess{command: exec.CommandContext(ctx, executable, "-test.run=^TestOrdersProcessHelper$"), done: make(chan struct{})}
	p.command.Env = env
	p.command.Stdout = &p.logs
	p.command.Stderr = &p.logs
	if err := p.command.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { p.err = p.command.Wait(); close(p.done) }()
	t.Cleanup(func() { _ = p.command.Process.Kill(); <-p.done })
	return p
}

// Ждёт готовности HTTP с ограниченным временем и отслеживает ранний выход.
func waitOrdersReady(t *testing.T, p *runningProcess, client *http.Client, url string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/readyz", nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if response.StatusCode == 200 {
				return
			}
		}
		select {
		case <-p.done:
			t.Fatalf("process exited before readiness: %v\n%s", p.err, p.logs.String())
		case <-ctx.Done():
			t.Fatalf("readiness timed out\n%s", p.logs.String())
		case <-ticker.C:
		}
	}
}

// Проверяет HTTP-запрос и возвращает JSON-ответ.
func processRequest(t *testing.T, client *http.Client, url, path, user string, status int) map[string]json.RawMessage {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if user != "" {
		req.Header.Set("X-Demo-User-ID", user)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != status {
		t.Fatalf("status=%d want=%d body=%s", resp.StatusCode, status, data)
	}
	if resp.Header.Get("X-Request-ID") == "" {
		t.Fatal("missing request ID")
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatal(err)
	}
	return body
}

// Запускает Docker только для выделенного брокера тестового сценария.
func relayBrokerCommand(t *testing.T, operation string) {
	t.Helper()
	name := os.Getenv("ORDERS_RELAY_TEST_CONTAINER")
	if !strings.HasPrefix(name, "food-orders-relay-test-") {
		t.Fatal("dedicated relay test container required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	args := []string{operation, name}
	if operation == "stop" {
		args = []string{"stop", "-t", "3", name}
	}
	output, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v %s", operation, err, output)
	}
}

// Ждёт проверяемого состояния с ограничением по времени.
func waitRelayState(t *testing.T, ctx context.Context, description string, check func() bool) {
	t.Helper()
	timer := time.NewTicker(50 * time.Millisecond)
	defer timer.Stop()
	for {
		if check() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timeout: %s", description)
		case <-timer.C:
		}
	}
}

// Завершает процесс сигналом и проверяет завершение обеих горутин.
func stopOrdersProcess(t *testing.T, p *runningProcess, signal os.Signal) {
	t.Helper()
	if err := p.command.Process.Signal(signal); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.done:
	case <-time.After(8 * time.Second):
		t.Fatalf("shutdown timeout: %s", p.logs.String())
	}
	if p.err != nil {
		t.Fatalf("exit=%v logs=%s", p.err, p.logs.String())
	}
	for _, fragment := range []string{"outbox relay stopped", "HTTP server stopped"} {
		if !strings.Contains(p.logs.String(), fragment) {
			t.Fatalf("missing %q: %s", fragment, p.logs.String())
		}
	}
}

// Проверяет реальный процесс, доставку outbox и восстановление после отказа Kafka.
func TestOrdersRelayProcess(t *testing.T) {
	dsn, password := os.Getenv("ORDERS_TEST_DATABASE_URL"), os.Getenv("ORDERS_TEST_DB_PASSWORD")
	cfg, err := pgxpool.ParseConfig(dsn)
	if dsn == "" || password == "" || err != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "orders_test_") {
		t.Fatal("use infrastructure/scripts/test-orders-relay.sh with isolated database")
	}
	broker := os.Getenv("ORDERS_RELAY_TEST_BROKER")
	if broker == "" || !strings.HasPrefix(os.Getenv("ORDERS_RELAY_TEST_CONTAINER"), "food-orders-relay-test-") {
		t.Fatal("dedicated test Kafka required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	cfg.ConnConfig.Password = password
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	admin, err := kgo.NewClient(kgo.SeedBrokers(broker), kgo.DialTimeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	waitRelayState(t, ctx, "test Kafka readiness", func() bool {
		probe, stop := context.WithTimeout(ctx, time.Second)
		defer stop()
		return admin.Ping(probe) == nil
	})
	create := kmsg.NewPtrCreateTopicsRequest()
	create.TimeoutMillis = 10000
	create.Topics = []kmsg.CreateTopicsRequestTopic{{Topic: "orders-relay-test", NumPartitions: 3, ReplicationFactor: 1}}
	response, err := create.RequestWith(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Topics) != 1 || response.Topics[0].ErrorCode != 0 {
		t.Fatalf("topic response=%+v", response.Topics)
	}
	repo := repository.NewRepository(pool)
	// Создание заказа с outbox проходит через настоящую SQL-транзакцию.
	seed := func(n int) (model.Order, model.OutboxEvent) {
		t.Helper()
		id := fmt.Sprintf("10000000-0000-4000-8000-%012d", n)
		eventID := fmt.Sprintf("20000000-0000-4000-8000-%012d", n)
		now := time.Now().UTC()
		order := model.Order{ID: id, UserID: "30000000-0000-4000-8000-000000000001", RestaurantID: "40000000-0000-4000-8000-000000000001", Status: model.StatusCreated, Version: 1, TotalKopecks: 29900, DeliveryAddress: "Тестовая улица, 1", CreatedAt: now, UpdatedAt: now, Items: []model.OrderItem{{MenuItemID: "50000000-0000-4000-8000-000000000001", NameSnapshot: "Пицца", UnitPriceKopecks: 10000, Quantity: 1}}}
		payload, _ := json.Marshal(map[string]any{"event_id": eventID, "order_id": id, "user_id": order.UserID, "type": model.OrderCreatedEventType, "schema_version": 1, "occurred_at": now, "total_kopecks": 29900})
		event := model.OutboxEvent{EventID: eventID, OrderID: id, Type: model.OrderCreatedEventType, Payload: payload, CreatedAt: now}
		result, err := repo.CreateIdempotent(ctx, model.CreateOrderParams{Order: order, Event: event, IdempotencyKey: fmt.Sprint(n), RequestHash: strings.Repeat("a", 64)})
		if err != nil || !result.Created {
			t.Fatalf("seed: created=%v err=%v", result.Created, err)
		}
		return order, event
	}
	state := func(id string) (bool, int) {
		t.Helper()
		var published bool
		var attempts int
		if err := pool.QueryRow(ctx, `SELECT published_at IS NOT NULL,attempts FROM outbox_events WHERE event_id=$1::uuid`, id).Scan(&published, &attempts); err != nil {
			t.Fatal(err)
		}
		return published, attempts
	}
	start := func() *runningProcess {
		t.Helper()
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		address := listener.Addr().String()
		listener.Close()
		p := startOrders(t, ctx, address, dsn, password)
		return p
	}
	// Адрес процесса читается из его окружения, чтобы не делить порт между тестами.
	processURL := func(p *runningProcess) string {
		for _, entry := range p.command.Env {
			if value, ok := strings.CutPrefix(entry, "ORDERS_HTTP_ADDR="); ok {
				return "http://" + value
			}
		}
		t.Fatal("missing child address")
		return ""
	}
	client := &http.Client{Timeout: time.Second}
	defer client.CloseIdleConnections()
	_, first := seed(1)
	p := start()
	url := processURL(p)
	waitOrdersReady(t, p, client, url)
	waitRelayState(t, ctx, "initial outbox publication", func() bool { published, _ := state(first.EventID); return published })
	t.Log("initial outbox publication confirmed")
	relayBrokerCommand(t, "stop")
	order, second := seed(2)
	waitRelayState(t, ctx, "retry while Kafka is stopped", func() bool {
		published, attempts := state(second.EventID)
		if published {
			t.Fatal("event published while Kafka stopped")
		}
		return attempts > 0
	})
	processRequest(t, client, url, "/healthz", "", 200)
	processRequest(t, client, url, "/readyz", "", 200)
	processRequest(t, client, url, "/v1/orders/"+order.ID, order.UserID, 200)
	t.Log("Kafka stopped: event remains pending, retry persisted, HTTP available")
	relayBrokerCommand(t, "start")
	waitRelayState(t, ctx, "backlog publication after Kafka restart", func() bool { published, _ := state(second.EventID); return published })
	t.Log("same orders process recovered and published backlog")
	consumer, err := kgo.NewClient(kgo.SeedBrokers(broker), kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{"orders-relay-test": {0: kgo.NewOffset().AtStart(), 1: kgo.NewOffset().AtStart(), 2: kgo.NewOffset().AtStart()}}))
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()
	want := map[string]model.OutboxEvent{first.EventID: first, second.EventID: second}
	for len(want) > 0 {
		fetches := consumer.PollRecords(ctx, 10)
		if errs := fetches.Errors(); len(errs) > 0 {
			t.Fatalf("fetch errors=%v", errs)
		}
		for _, record := range fetches.Records() {
			var payload map[string]any
			if err := json.Unmarshal(record.Value, &payload); err != nil {
				t.Fatal(err)
			}
			id, _ := payload["event_id"].(string)
			if expected, ok := want[id]; ok {
				var expectedPayload map[string]any
				_ = json.Unmarshal(expected.Payload, &expectedPayload)
				if string(record.Key) != expected.OrderID || !reflect.DeepEqual(payload, expectedPayload) {
					t.Fatalf("record key or payload differs: %s", record.Value)
				}
				delete(want, id)
			}
		}
	}
	stopOrdersProcess(t, p, syscall.SIGTERM)
	t.Log("SIGTERM: HTTP and relay stopped cleanly")
	relayBrokerCommand(t, "stop")
	_, third := seed(3)
	p = start()
	url = processURL(p)
	waitOrdersReady(t, p, client, url)
	waitRelayState(t, ctx, "retry after startup without Kafka", func() bool { published, attempts := state(third.EventID); return !published && attempts > 0 })
	stopOrdersProcess(t, p, os.Interrupt)
	t.Log("startup without Kafka and SIGINT shutdown succeeded")
	t.Run("occupied HTTP port does not start relay", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		child := startOrders(t, ctx, listener.Addr().String(), dsn, password)
		select {
		case <-child.done:
		case <-time.After(8 * time.Second):
			t.Fatal("startup did not fail")
		}
		if child.err == nil || !strings.Contains(child.logs.String(), "listen HTTP") || strings.Contains(child.logs.String(), "outbox relay started") {
			t.Fatalf("exit=%v logs=%s", child.err, child.logs.String())
		}
	})
}
