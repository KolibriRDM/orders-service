package ratelimiter

import (
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	maxRequests      = 30
	window           = time.Minute
	operationTimeout = 100 * time.Millisecond
)

// Ограничивает частоту создания заказов через общий Redis.
type Limiter struct {
	client *redis.Client
}

// Создаёт лимитер поверх готового Redis-клиента.
func NewLimiter(client *redis.Client) *Limiter {
	return &Limiter{client: client}
}

// Отделяет счётчики создания заказов от других ключей Redis.
func rateLimitKey(userID string) string {
	return "rate_limit:orders:create:v1:" + userID
}
