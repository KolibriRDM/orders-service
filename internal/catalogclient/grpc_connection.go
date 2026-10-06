package catalogclient

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Ограничивает размер одного сообщения каталога до 64 КиБ.
const maxGRPCMessageBytes = 64 << 10

// Проверяет адрес и создаёт переиспользуемое gRPC-соединение.
func newGRPCConnection(address string) (*grpc.ClientConn, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return nil, fmt.Errorf("catalog gRPC address is required")
	}
	if strings.ContainsAny(address, "/?#@\\ \t\r\n") {
		return nil, fmt.Errorf("catalog gRPC address must be host:port")
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("catalog gRPC address must be host:port")
	}
	if host == "" {
		return nil, fmt.Errorf("catalog gRPC host is required")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return nil, fmt.Errorf("catalog gRPC port must be between 1 and 65535")
	}
	// Собираем адрес с корректными скобками для IPv6.
	endpoint := net.JoinHostPort(host, strconv.Itoa(portNumber))

	target := "passthrough:///" + endpoint

	// Локальный каталог использует gRPC без TLS и сообщения до 64 КиБ.
	conn, err := grpc.NewClient(
		target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDisableRetry(),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(maxGRPCMessageBytes),
			grpc.MaxCallSendMsgSize(maxGRPCMessageBytes),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("create catalog gRPC connection: %w", err)
	}
	return conn, nil
}
