package redis

import (
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// Создаёт ленивый Redis-клиент; закрывает его вызывающий код.
func NewClient(addr string) *goredis.Client {
	return goredis.NewClient(&goredis.Options{
		Addr:       addr,
		MaxRetries: -1,
		// В этой версии одна попытка подключения задаётся значением 1.
		DialerRetries:         1,
		DialTimeout:           100 * time.Millisecond,
		ReadTimeout:           100 * time.Millisecond,
		WriteTimeout:          100 * time.Millisecond,
		PoolTimeout:           100 * time.Millisecond,
		ContextTimeoutEnabled: true,
		PoolSize:              10,
		MaxActiveConns:        10,
		MinIdleConns:          0,
	})
}
