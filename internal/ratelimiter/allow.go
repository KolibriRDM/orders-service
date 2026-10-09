package ratelimiter

import (
	"context"
	"fmt"
	"time"

	"github.com/KolibriRDM/orders-service/internal/model"
	"github.com/redis/go-redis/v9"
)

// Атомарно увеличивает счётчик и задаёт срок жизни окна.
var allowScript = redis.NewScript(`
	local count = redis.call("INCR", KEYS[1])
	local ttl = redis.call("PTTL", KEYS[1])

	if count == 1 or ttl < 0 then
		redis.call("PEXPIRE", KEYS[1], ARGV[2])
		ttl = tonumber(ARGV[2])
	end
	local allowed = 0
	if count <= tonumber(ARGV[1]) then
		allowed = 1
	end
	return {allowed, ttl}
`)

// Проверяет лимит пользователя и возвращает время ожидания при отказе.
func (l *Limiter) Allow(ctx context.Context, userID string) (model.RateLimitDecision, error) {
	if ctx.Err() != nil {
		return model.RateLimitDecision{}, ctx.Err()
	}
	id, err := model.NormalizeID(userID)
	if err != nil {
		return model.RateLimitDecision{}, fmt.Errorf("rate limit user ID must be a UUID: %w", err)
	}
	operationCtx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	cmd := allowScript.Run(operationCtx, l.client, []string{rateLimitKey(id)}, maxRequests, window.Milliseconds())
	values, err := cmd.Slice()
	if ctx.Err() != nil {
		return model.RateLimitDecision{}, ctx.Err()
	}
	if err != nil {
		return model.RateLimitDecision{}, fmt.Errorf("check order rate limit: %w", err)
	}
	if len(values) != 2 {
		return model.RateLimitDecision{}, fmt.Errorf("check order rate limit: invalid script response")
	}
	allowed, ok := values[0].(int64)
	if !ok || (allowed != 0 && allowed != 1) {
		return model.RateLimitDecision{}, fmt.Errorf("check order rate limit: invalid script response")
	}
	ttlMS, ok := values[1].(int64)
	if !ok || ttlMS < 0 || ttlMS > window.Milliseconds() {
		return model.RateLimitDecision{}, fmt.Errorf("check order rate limit: invalid script response")
	}
	if allowed == 1 {
		return model.RateLimitDecision{Allowed: true}, nil
	}
	// На границе окна оставляем хотя бы миллисекунду ожидания.
	return model.RateLimitDecision{Allowed: false, RetryAfter: time.Duration(max(ttlMS, 1)) * time.Millisecond}, nil
}
