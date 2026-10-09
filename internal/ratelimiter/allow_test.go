package ratelimiter

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
	"github.com/KolibriRDM/orders-service/internal/model"
	"github.com/redis/go-redis/v9"
)

const testUserID = "abcdef01-2345-4678-9abc-def012345678"

// Подменяет выполнение команды Redis без сетевого подключения.
type limiterHook struct{ process redis.ProcessHook }

// Сохраняет стандартную настройку подключения клиента.
func (h limiterHook) DialHook(next redis.DialHook) redis.DialHook { return next }

// Передаёт команду тестовой функции.
func (h limiterHook) ProcessHook(redis.ProcessHook) redis.ProcessHook { return h.process }

// Сохраняет стандартную обработку pipeline.
func (h limiterHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

// Создаёт лимитер с управляемым ответом Redis.
func stubLimiter(t *testing.T, process redis.ProcessHook) *Limiter {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1})
	client.AddHook(limiterHook{process: process})
	t.Cleanup(func() { _ = client.Close() })
	return NewLimiter(client)
}

// Проверяет разрешение, отказ, единицы времени и нормализованный ключ.
func TestAllowValidResponse(t *testing.T) {
	for _, tc := range []struct {
		name      string
		flag, ttl int64
		want      model.RateLimitDecision
	}{
		{"allowed", 1, 60000, model.RateLimitDecision{Allowed: true}},
		{"allowed at boundary", 1, 0, model.RateLimitDecision{Allowed: true}},
		{"denied", 0, 1250, model.RateLimitDecision{RetryAfter: 1250 * time.Millisecond}},
		{"denied at boundary", 0, 0, model.RateLimitDecision{RetryAfter: time.Millisecond}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			limiter := stubLimiter(t, func(ctx context.Context, cmd redis.Cmder) error {
				calls++
				args := cmd.Args()
				if len(args) != 6 || args[0] != "evalsha" || !reflect.DeepEqual(args[2:], []any{1, rateLimitKey(testUserID), maxRequests, window.Milliseconds()}) {
					t.Fatalf("args=%v", args)
				}
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > operationTimeout {
					t.Fatal("missing operation deadline")
				}
				cmd.(*redis.Cmd).SetVal([]any{tc.flag, tc.ttl})
				return nil
			})
			rawID := strings.ReplaceAll(strings.ToUpper(testUserID), "-", "")
			got, err := limiter.Allow(context.Background(), rawID)
			if err != nil || got != tc.want || calls != 1 {
				t.Fatalf("decision=%+v err=%v calls=%d", got, err, calls)
			}
		})
	}
}

// Отклоняет неполные ответы и неверные типы без паники или разрешения запроса.
func TestAllowMalformedResponse(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
	}{
		{"nil", nil}, {"scalar", int64(1)}, {"empty", []any{}},
		{"one element", []any{int64(1)}}, {"extra element", []any{int64(1), int64(1000), int64(7)}},
		{"string flag", []any{"1", int64(1000)}}, {"bool flag", []any{true, int64(1000)}},
		{"negative flag", []any{int64(-1), int64(1000)}}, {"large flag", []any{int64(2), int64(1000)}},
		{"string TTL", []any{int64(0), "1000"}}, {"negative TTL", []any{int64(0), int64(-1)}},
		{"excessive TTL", []any{int64(0), int64(60001)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			limiter := stubLimiter(t, func(_ context.Context, cmd redis.Cmder) error { cmd.(*redis.Cmd).SetVal(tc.value); return nil })
			got, err := limiter.Allow(context.Background(), testUserID)
			if err == nil || got != (model.RateLimitDecision{}) || !strings.HasPrefix(err.Error(), "check order rate limit:") {
				t.Fatalf("decision=%+v err=%v", got, err)
			}
		})
	}
}

// Проверяет входные данные и отмену до обращения к Redis.
func TestAllowBeforeRedis(t *testing.T) {
	limiter := NewLimiter(nil)
	for _, id := range []string{"", "user", " abcdef01-2345-4678-9abc-def012345678 ", "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx"} {
		if got, err := limiter.Allow(context.Background(), id); !errors.Is(err, orderserrors.ErrInvalidInput) || got != (model.RateLimitDecision{}) {
			t.Fatalf("id=%q decision=%+v err=%v", id, got, err)
		}
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	for _, ctx := range []context.Context{cancelled, expired} {
		if got, err := limiter.Allow(ctx, ""); !errors.Is(err, ctx.Err()) || got != (model.RateLimitDecision{}) {
			t.Fatalf("decision=%+v err=%v", got, err)
		}
	}
}

// Возвращает причину ошибки Redis без повторного увеличения счётчика.
func TestAllowRedisError(t *testing.T) {
	cause := errors.New("connection lost")
	calls := 0
	limiter := stubLimiter(t, func(context.Context, redis.Cmder) error { calls++; return cause })
	got, err := limiter.Allow(context.Background(), testUserID)
	if !errors.Is(err, cause) || got != (model.RateLimitDecision{}) || calls != 1 {
		t.Fatalf("decision=%+v err=%v calls=%d", got, err, calls)
	}
}

// Отмена исходного запроса имеет приоритет над ответом Redis.
func TestAllowCancellationDuringRedis(t *testing.T) {
	for _, cause := range []error{nil, errors.New("connection lost")} {
		ctx, cancel := context.WithCancel(context.Background())
		limiter := stubLimiter(t, func(_ context.Context, cmd redis.Cmder) error {
			cancel()
			cmd.(*redis.Cmd).SetVal([]any{int64(1), int64(60000)})
			return cause
		})
		got, err := limiter.Allow(ctx, testUserID)
		cancel()
		if !errors.Is(err, context.Canceled) || got != (model.RateLimitDecision{}) {
			t.Fatalf("decision=%+v err=%v", got, err)
		}
	}
}

// Соблюдает таймаут операции и более короткий deadline клиента.
func TestAllowDeadline(t *testing.T) {
	for _, period := range []time.Duration{time.Second, 20 * time.Millisecond} {
		t.Run(period.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				limiter := stubLimiter(t, func(ctx context.Context, _ redis.Cmder) error { <-ctx.Done(); return ctx.Err() })
				ctx, cancel := context.WithTimeout(context.Background(), period)
				defer cancel()
				start := time.Now()
				got, err := limiter.Allow(ctx, testUserID)
				if !errors.Is(err, context.DeadlineExceeded) || got != (model.RateLimitDecision{}) || time.Since(start) != min(period, operationTimeout) {
					t.Fatalf("decision=%+v err=%v duration=%v", got, err, time.Since(start))
				}
			})
		})
	}
}
