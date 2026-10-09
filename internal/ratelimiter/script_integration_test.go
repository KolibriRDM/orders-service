//go:build integration

package ratelimiter

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	redisplatform "github.com/KolibriRDM/orders-service/internal/platform/redis"
)

// Проверяет Lua на Redis, удаляя только уникальные ключи текущего теста.
func TestAllowScriptRedis(t *testing.T) {
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
	newKey := func(t *testing.T) string {
		t.Helper()
		key := rateLimitKey("test-" + rand.Text())
		t.Cleanup(func() {
			cleanupCtx, stop := context.WithTimeout(context.Background(), time.Second)
			defer stop()
			if err := client.Del(cleanupCtx, key).Err(); err != nil {
				t.Errorf("cleanup: %v", err)
			}
		})
		return key
	}
	// Декодирует два целочисленных значения ответа скрипта.
	run := func(key string, period time.Duration) (int64, int64, error) {
		values, err := allowScript.Run(ctx, client, []string{key}, maxRequests, period.Milliseconds()).Slice()
		if err != nil {
			return 0, 0, err
		}
		if len(values) != 2 {
			return 0, 0, fmt.Errorf("unexpected result: %v", values)
		}
		allowed, ok := values[0].(int64)
		if !ok || allowed < 0 || allowed > 1 {
			return 0, 0, fmt.Errorf("invalid allowed: %v", values[0])
		}
		ttl, ok := values[1].(int64)
		if !ok || ttl < 0 || ttl > period.Milliseconds() {
			return 0, 0, fmt.Errorf("invalid TTL: %v", values[1])
		}
		return allowed, ttl, nil
	}
	t.Run("thirty allowed then denied", func(t *testing.T) {
		key := newKey(t)
		for i := 1; i <= 32; i++ {
			allowed, _, err := run(key, window)
			want := int64(1)
			if i > maxRequests {
				want = 0
			}
			if err != nil || allowed != want {
				t.Fatalf("request=%d allowed=%d err=%v", i, allowed, err)
			}
		}
		count, err := client.Get(ctx, key).Int64()
		if err != nil || count != 32 {
			t.Fatalf("count=%d err=%v", count, err)
		}
		ttl, err := client.PTTL(ctx, key).Result()
		if err != nil || ttl <= 0 || ttl > window {
			t.Fatalf("TTL=%v err=%v", ttl, err)
		}
		if allowed, _, err := run(newKey(t), window); err != nil || allowed != 1 {
			t.Fatalf("independent user: allowed=%d err=%v", allowed, err)
		}
	})
	t.Run("window is not extended", func(t *testing.T) {
		key := newKey(t)
		if _, _, err := run(key, window); err != nil {
			t.Fatal(err)
		}
		if err := client.PExpire(ctx, key, 5*time.Second).Err(); err != nil {
			t.Fatal(err)
		}
		// Проверяем как разрешённые, так и отклонённые обращения.
		for i := 0; i < 32; i++ {
			_, ttl, err := run(key, window)
			if err != nil || ttl > 5000 {
				t.Fatalf("TTL extended: %d err=%v", ttl, err)
			}
		}
	})
	t.Run("missing expiration repaired", func(t *testing.T) {
		key := newKey(t)
		if err := client.Set(ctx, key, 7, 0).Err(); err != nil {
			t.Fatal(err)
		}
		allowed, ttl, err := run(key, window)
		if err != nil || allowed != 1 || ttl <= 0 {
			t.Fatalf("allowed=%d TTL=%d err=%v", allowed, ttl, err)
		}
		actual, err := client.PTTL(ctx, key).Result()
		if err != nil || actual <= 0 || actual > window {
			t.Fatalf("actual TTL=%v err=%v", actual, err)
		}
		count, err := client.Get(ctx, key).Int64()
		if err != nil || count != 8 {
			t.Fatalf("count reset: %d err=%v", count, err)
		}
	})
	t.Run("new window after expiration", func(t *testing.T) {
		key := newKey(t)
		if err := client.Set(ctx, key, maxRequests, 150*time.Millisecond).Err(); err != nil {
			t.Fatal(err)
		}
		if allowed, _, err := run(key, window); err != nil || allowed != 0 {
			t.Fatalf("full window: allowed=%d err=%v", allowed, err)
		}
		deadline := time.Now().Add(2 * time.Second)
		for {
			exists, err := client.Exists(ctx, key).Result()
			if err != nil {
				t.Fatal(err)
			}
			if exists == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("key did not expire")
			}
			time.Sleep(10 * time.Millisecond)
		}
		allowed, ttl, err := run(key, window)
		if err != nil || allowed != 1 || ttl <= 0 {
			t.Fatalf("new window: allowed=%d TTL=%d err=%v", allowed, ttl, err)
		}
		count, err := client.Get(ctx, key).Int64()
		if err != nil || count != 1 {
			t.Fatalf("new count=%d err=%v", count, err)
		}
	})
	t.Run("concurrent requests", func(t *testing.T) {
		key := newKey(t)
		type result struct {
			allowed int64
			err     error
		}
		const workers, perWorker = 8, 10
		results := make(chan result, workers*perWorker)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				for range perWorker {
					allowed, _, err := run(key, window)
					results <- result{allowed, err}
				}
			}()
		}
		close(start)
		wg.Wait()
		close(results)
		var allowedCount int64
		for result := range results {
			if result.err != nil {
				t.Error(result.err)
			}
			allowedCount += result.allowed
		}
		if allowedCount != maxRequests {
			t.Fatalf("allowed=%d want=%d", allowedCount, maxRequests)
		}
		count, err := client.Get(ctx, key).Int64()
		if err != nil || count != workers*perWorker {
			t.Fatalf("lost increments: count=%d err=%v", count, err)
		}
	})
	t.Run("invalid counter returns error", func(t *testing.T) {
		key := newKey(t)
		if err := client.Set(ctx, key, "invalid", window).Err(); err != nil {
			t.Fatal(err)
		}
		if _, _, err := run(key, window); err == nil {
			t.Fatal("expected invalid counter error")
		}
		value, err := client.Get(ctx, key).Result()
		if err != nil || value != "invalid" {
			t.Fatalf("counter overwritten: %q err=%v", value, err)
		}
	})
}
