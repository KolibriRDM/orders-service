package config

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
)

// Проверяет адрес HTTP-сервера и диапазон порта.
func httpAddr(key, fallback string) (string, error) {
	raw, ok := os.LookupEnv(key)
	if !ok {
		return fallback, nil
	}
	addr := strings.TrimSpace(raw)
	if addr == "" {
		return "", fmt.Errorf("%s must not be empty", key)
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("%s: expected host:port, got %q: %w", key, addr, err)
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return "", fmt.Errorf("%s: port must be an integer from 1 to 65535, got %q", key, port)
	}
	return addr, nil
}

// Преобразует LOG_LEVEL в уровень логирования.
func logLevel() (slog.Level, error) {
	raw, ok := os.LookupEnv("LOG_LEVEL")
	if !ok {
		return slog.LevelInfo, nil
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("LOG_LEVEL: expected debug, info, warn or error, got %q", raw)
	}
}
