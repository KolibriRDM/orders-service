package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

// Проверяет адрес Redis для исходящего подключения.
func redisAddr() (string, error) {
	const key = "ORDERS_REDIS_ADDR"
	raw, ok := os.LookupEnv(key)
	if !ok {
		return "127.0.0.1:16379", nil
	}
	addr := strings.TrimSpace(raw)
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("%s: expected host:port: %w", key, err)
	}
	if host == "" || strings.ContainsAny(host, " /?#@\t\r\n") {
		return "", fmt.Errorf("%s: host must not be empty or contain URL components or whitespace", key)
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return "", fmt.Errorf("%s: port must be an integer from 1 to 65535", key)
	}
	return addr, nil
}
