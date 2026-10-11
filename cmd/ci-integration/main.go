// Команда запускает интеграционные тесты на собственных временных контейнерах.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

const service = "orders-service"

// environment хранит окружение тестов и только созданные текущим запуском контейнеры.
type environment struct {
	ctx        context.Context
	env        []string
	containers []string
}

// main обрабатывает сигналы и возвращает код ошибки после удаления временных ресурсов.
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "integration:", err)
		os.Exit(1)
	}
}

// command выполняет внешний процесс с таймаутом, сохраняя его диагностику.
func (e *environment) command(ctx context.Context, timeout time.Duration, input string, quiet bool, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Env = e.env
	cmd.Stdin = strings.NewReader(input)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err := cmd.Run()
	if !quiet {
		fmt.Print(out.String())
		fmt.Fprint(os.Stderr, stderr.String())
	}
	if err != nil {
		return "", fmt.Errorf("%s: %w: %s", strings.Join(args[:min(len(args), 3)], " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// set обновляет переменную без дублирования значений в окружении дочерних процессов.
func (e *environment) set(key, value string) {
	prefix := key + "="
	for i, s := range e.env {
		if strings.HasPrefix(s, prefix) {
			e.env[i] = prefix + value
			return
		}
	}
	e.env = append(e.env, prefix+value)
}

// create регистрирует контейнер до старта, чтобы ошибка запуска не оставила ресурс.
func (e *environment) create(args ...string) (string, error) {
	id, err := e.command(e.ctx, 240*time.Second, "", true, append([]string{"docker", "create"}, args...)...)
	if err != nil {
		return "", err
	}
	e.containers = append(e.containers, id)
	_, err = e.command(e.ctx, 30*time.Second, "", true, "docker", "start", id)
	return id, err
}

// cleanup удаляет все свои контейнеры, продолжая очистку после отдельной ошибки.
func (e *environment) cleanup() error {
	var errs []error
	for i := len(e.containers) - 1; i >= 0; i-- {
		_, err := e.command(context.Background(), 30*time.Second, "", true, "docker", "rm", "-f", "-v", e.containers[i])
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// pause завершает ожидание сразу при отмене контекста.
func pause(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// waitReady опрашивает зависимость по TCP до истечения общего срока.
func (e *environment) waitReady(args ...string) error {
	ctx, cancel := context.WithTimeout(e.ctx, 120*time.Second)
	defer cancel()
	var last error
	for {
		_, last = e.command(ctx, 10*time.Second, "", true, args...)
		if last == nil {
			return nil
		}
		if err := pause(ctx, time.Second); err != nil {
			return fmt.Errorf("dependency not ready: %w; last probe: %v", err, last)
		}
	}
}

// address читает порт, который Docker назначил контейнеру на loopback.
func (e *environment) address(id, port string) (string, error) {
	addr, err := e.command(e.ctx, 10*time.Second, "", true, "docker", "port", id, port)
	if err != nil {
		return "", err
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host != "127.0.0.1" {
		return "", fmt.Errorf("unexpected Docker port %q", addr)
	}
	return addr, nil
}

// freePort резервирует свободный порт до настройки advertised.listeners Kafka.
func freePort() (int, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	return port, listener.Close()
}

// test запускает пакет с race detector и отдельным сроком завершения.
func (e *environment) test(timeout, pkg, filter string) error {
	args := []string{"go", "test", "-tags=integration", "-race", "-count=1", "-timeout=" + timeout}
	if filter != "" {
		args = append(args, "-run", filter)
	}
	args = append(args, pkg)
	_, err := e.command(e.ctx, 240*time.Second, "", false, args...)
	return err
}

// run подготавливает только зависимости, необходимые данному сервису.
func run(ctx context.Context) (err error) {
	if _, err = os.Stat("migrations"); err != nil {
		return fmt.Errorf("run from the %s repository root: %w", service, err)
	}
	e := &environment{ctx: ctx, env: os.Environ()}
	e.set("GOWORK", "off")
	defer func() { err = errors.Join(err, e.cleanup()) }()
	var random [8]byte
	if _, err = rand.Read(random[:]); err != nil {
		return err
	}
	suffix := hex.EncodeToString(random[:])
	prefix := strings.TrimSuffix(service, "-service")
	db := prefix + "_test_ci_" + suffix
	pg, err := e.create("--name", "food-ci-postgres-"+suffix, "-p", "127.0.0.1::5432", "-e", "POSTGRES_USER=test_app", "-e", "POSTGRES_PASSWORD=test_password", "-e", "POSTGRES_DB="+db, "postgres:17.11-bookworm")
	if err != nil {
		return err
	}
	if err = e.waitReady("docker", "exec", pg, "pg_isready", "-h", "127.0.0.1", "-U", "test_app", "-d", db); err != nil {
		return err
	}
	files, err := filepath.Glob("migrations/*.up.sql")
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return errors.New("no migrations found")
	}
	sort.Strings(files)
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		if _, err = e.command(ctx, 60*time.Second, string(data), false, "docker", "exec", "-i", pg, "psql", "-U", "test_app", "-d", db, "-v", "ON_ERROR_STOP=1"); err != nil {
			return err
		}
	}
	pgAddr, err := e.address(pg, "5432/tcp")
	if err != nil {
		return err
	}
	e.set(strings.ToUpper(prefix)+"_TEST_DATABASE_URL", "postgres://test_app@"+pgAddr+"/"+db+"?sslmode=disable")
	e.set(strings.ToUpper(prefix)+"_TEST_DB_PASSWORD", "test_password")
	if err = e.test("90s", "./internal/repository", ""); err != nil {
		return err
	}
	if prefix == "catalog" || prefix == "orders" {
		redis, err := e.create("--name", "food-ci-redis-"+suffix, "-p", "127.0.0.1::6379", "redis:8.2.9", "redis-server", "--save", "", "--appendonly", "no")
		if err != nil {
			return err
		}
		if err = e.waitReady("docker", "exec", redis, "redis-cli", "ping"); err != nil {
			return err
		}
		addr, err := e.address(redis, "6379/tcp")
		if err != nil {
			return err
		}
		e.set(strings.ToUpper(prefix)+"_TEST_REDIS_ADDR", addr)
		pkg := "./internal/repository/menucache"
		if prefix == "orders" {
			pkg = "./internal/ratelimiter"
		}
		if err = e.test("90s", pkg, ""); err != nil {
			return err
		}
	}
	if prefix != "orders" && prefix != "notifications" {
		return nil
	}
	kafka := "food-ci-kafka-" + suffix
	if prefix == "orders" {
		kafka = "food-orders-relay-test-" + suffix
	}
	port, err := freePort()
	if err != nil {
		return err
	}
	broker := fmt.Sprintf("127.0.0.1:%d", port)
	settings := []string{
		"CLUSTER_ID=4L6g3nShT-eMCtK--X86sw",
		"KAFKA_NODE_ID=1",
		"KAFKA_PROCESS_ROLES=broker,controller",
		"KAFKA_LISTENERS=INTERNAL://:9092,EXTERNAL://:19092,CONTROLLER://:9093",
		"KAFKA_ADVERTISED_LISTENERS=INTERNAL://127.0.0.1:9092,EXTERNAL://" + broker,
		"KAFKA_LISTENER_SECURITY_PROTOCOL_MAP=INTERNAL:PLAINTEXT,EXTERNAL:PLAINTEXT,CONTROLLER:PLAINTEXT",
		"KAFKA_CONTROLLER_LISTENER_NAMES=CONTROLLER",
		"KAFKA_INTER_BROKER_LISTENER_NAME=INTERNAL",
		"KAFKA_CONTROLLER_QUORUM_VOTERS=1@localhost:9093",
		"KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR=1",
		"KAFKA_TRANSACTION_STATE_LOG_REPLICATION_FACTOR=1",
		"KAFKA_TRANSACTION_STATE_LOG_MIN_ISR=1",
		"KAFKA_AUTO_CREATE_TOPICS_ENABLE=false",
		"KAFKA_LOG_DIRS=/tmp/kraft-test-logs",
	}
	args := []string{"--name", kafka, "-p", broker + ":19092"}
	for _, setting := range settings {
		args = append(args, "-e", setting)
	}
	args = append(args, "apache/kafka:4.1.2")
	if _, err = e.create(args...); err != nil {
		return err
	}
	if err = e.waitReady("docker", "exec", kafka, "/opt/kafka/bin/kafka-topics.sh", "--bootstrap-server", "127.0.0.1:9092", "--list"); err != nil {
		return err
	}
	e.set("KAFKA_TEST_BROKERS", broker)
	if prefix == "orders" {
		if err = e.test("120s", "./internal/kafkapublisher", ""); err != nil {
			return err
		}
		e.set("ORDERS_RELAY_TEST_CONTAINER", kafka)
		e.set("ORDERS_RELAY_TEST_BROKER", broker)
		return e.test("150s", "./cmd/orders", "^TestOrdersRelayProcess$")
	}
	if err = e.test("180s", "./internal/kafkaconsumer", ""); err != nil {
		return err
	}
	return e.test("90s", "./cmd/notifications", "^TestNotificationsProcess$")
}
