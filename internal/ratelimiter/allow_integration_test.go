//go:build integration

package ratelimiter

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KolibriRDM/orders-service/internal/model"
	redisplatform "github.com/KolibriRDM/orders-service/internal/platform/redis"
)

// Проверяет Go-метод вместе с Lua на Redis и удаляет только свои ключи.
func TestLimiterAllowRedis(t *testing.T) {
	addr := os.Getenv("ORDERS_TEST_REDIS_ADDR")
	if addr == "" {
		t.Fatal("set ORDERS_TEST_REDIS_ADDR to the local test Redis address")
	}
	client := redisplatform.NewClient(addr)
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	limiter := NewLimiter(client)
	newUser := func(t *testing.T) string {
		t.Helper()
		var bytes [16]byte
		if _, err := rand.Read(bytes[:]); err != nil {
			t.Fatal(err)
		}
		// Битовые маски задают UUID v4 и стандартный вариант.
		bytes[6] = (bytes[6] & 0x0f) | 0x40
		bytes[8] = (bytes[8] & 0x3f) | 0x80
		id := fmt.Sprintf("%x-%x-%x-%x-%x", bytes[:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:])
		t.Cleanup(func() {
			cleanupCtx, stop := context.WithTimeout(context.Background(), time.Second)
			defer stop()
			if err := client.Del(cleanupCtx, rateLimitKey(id)).Err(); err != nil {
				t.Errorf("cleanup: %v", err)
			}
		})
		return id
	}
	t.Run("boundary and canonical user key", func(t *testing.T) {
		user := newUser(t)
		for i := 1; i <= 31; i++ {
			input := user
			if i%2 == 0 {
				input = strings.ReplaceAll(strings.ToUpper(user), "-", "")
			}
			got, err := limiter.Allow(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			if i <= maxRequests {
				if got != (model.RateLimitDecision{Allowed: true}) {
					t.Fatalf("request=%d decision=%+v", i, got)
				}
			} else if got.Allowed || got.RetryAfter <= 0 || got.RetryAfter > window {
				t.Fatalf("request=%d decision=%+v", i, got)
			}
		}
		count, err := client.Get(ctx, rateLimitKey(user)).Int64()
		if err != nil || count != 31 {
			t.Fatalf("counter=%d err=%v", count, err)
		}
		if got, err := limiter.Allow(ctx, newUser(t)); err != nil || !got.Allowed {
			t.Fatalf("independent user: %+v %v", got, err)
		}
	})
	t.Run("concurrent allowance", func(t *testing.T) {
		user := newUser(t)
		type result struct {
			decision model.RateLimitDecision
			err      error
		}
		const workers, attempts = 8, 10
		results := make(chan result, workers*attempts)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				for range attempts {
					decision, err := limiter.Allow(ctx, user)
					results <- result{decision, err}
				}
			}()
		}
		close(start)
		wg.Wait()
		close(results)
		allowed := 0
		for res := range results {
			if res.err != nil {
				t.Error(res.err)
				continue
			}
			if res.decision.Allowed {
				allowed++
				if res.decision.RetryAfter != 0 {
					t.Error("allowed response has RetryAfter")
				}
			} else if res.decision.RetryAfter <= 0 || res.decision.RetryAfter > window {
				t.Errorf("invalid wait: %v", res.decision.RetryAfter)
			}
		}
		if allowed != maxRequests {
			t.Fatalf("allowed=%d want=%d", allowed, maxRequests)
		}
		count, err := client.Get(ctx, rateLimitKey(user)).Int64()
		if err != nil || count != workers*attempts {
			t.Fatalf("counter=%d err=%v", count, err)
		}
	})
}
