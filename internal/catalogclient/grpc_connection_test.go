package catalogclient

import (
	"strings"
	"testing"

	"google.golang.org/grpc/connectivity"
)

// Проверяет нормализацию адреса и создание канала без сетевого подключения.
func TestNewGRPCConnection(t *testing.T) {
	for _, tc := range []struct {
		name       string
		address    string
		wantTarget string
	}{
		{"IPv4", "127.0.0.1:9082", "passthrough:///127.0.0.1:9082"},
		{"hostname", "localhost:9082", "passthrough:///localhost:9082"},
		{"service name", "catalog-service:9082", "passthrough:///catalog-service:9082"},
		{"unresolved host", "catalog.invalid:9082", "passthrough:///catalog.invalid:9082"},
		{"IPv6", "[::1]:9082", "passthrough:///[::1]:9082"},
		{"trimmed", " \tlocalhost:9082\r\n", "passthrough:///localhost:9082"},
		{"trimmed IPv6", " [::1]:9082 ", "passthrough:///[::1]:9082"},
		{"minimum port", "localhost:1", "passthrough:///localhost:1"},
		{"maximum port", "localhost:65535", "passthrough:///localhost:65535"},
		{"normalized port", "localhost:09082", "passthrough:///localhost:9082"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn, err := newGRPCConnection(tc.address)
			if conn != nil {
				t.Cleanup(func() { _ = conn.Close() })
			}
			if err != nil {
				t.Fatal(err)
			}
			if conn == nil {
				t.Fatal("expected non-nil connection")
			}
			if got := conn.Target(); got != tc.wantTarget {
				t.Errorf("target = %q, want %q", got, tc.wantTarget)
			}
			// Конструктор оставляет канал открытым и не начинает подключение к каталогу.
			if got := conn.GetState(); got != connectivity.Idle {
				t.Errorf("state = %v, want IDLE", got)
			}
		})
	}
}

// Проверяет ошибки конфигурации и отсутствие адреса с паролем в сообщении.
func TestNewGRPCConnectionInvalidAddress(t *testing.T) {
	for _, tc := range []struct {
		name    string
		address string
	}{
		{"empty", ""},
		{"blank", " \t\r\n"},
		{"missing host", ":9082"},
		{"empty bracketed host", "[]:9082"},
		{"missing port", "localhost"},
		{"empty port", "localhost:"},
		{"zero port", "localhost:0"},
		{"negative port", "localhost:-1"},
		{"excessive port", "localhost:65536"},
		{"overflowing port", "localhost:99999999999999999999999999999999"},
		{"text port", "localhost:grpc"},
		{"HTTP URL", "http://localhost:9082"},
		{"HTTPS URL", "https://localhost:9082"},
		{"resolver URL", "passthrough:///localhost:9082"},
		{"credentials", "user:password-canary@localhost:9082"},
		{"path", "localhost:9082/menu"},
		{"query", "localhost:9082?x=1"},
		{"fragment", "localhost:9082#part"},
		{"backslash", "localhost:9082\\menu"},
		{"embedded space", "local host:9082"},
		{"embedded tab", "local\thost:9082"},
		{"embedded newline", "local\nhost:9082"},
		{"embedded carriage return", "local\rhost:9082"},
		{"unbracketed IPv6", "::1:9082"},
		{"unclosed bracket", "[::1:9082"},
		{"extra port", "localhost:9082:9083"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn, err := newGRPCConnection(tc.address)
			if conn != nil {
				t.Cleanup(func() { _ = conn.Close() })
				t.Error("invalid address returned a connection")
			}
			if err == nil {
				t.Fatal("expected configuration error")
			}
			if !strings.Contains(err.Error(), "catalog gRPC") {
				t.Errorf("error does not identify catalog configuration: %v", err)
			}
			if strings.Contains(err.Error(), "password-canary") {
				t.Fatal("credentials leaked in configuration error")
			}
		})
	}
}
