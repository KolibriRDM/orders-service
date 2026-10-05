package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Создаёт пул и проверяет соединение; закрывает его вызывающий код.
func NewPool(ctx context.Context, dsn, password, applicationName string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		// Ошибка разбора DSN может содержать пароль, поэтому скрываем её текст.
		return nil, errors.New("database URL: invalid PostgreSQL connection string")
	}
	cfg.ConnConfig.Password = password
	cfg.ConnConfig.ConnectTimeout = 3 * time.Second
	cfg.ConnConfig.RuntimeParams["application_name"] = applicationName
	cfg.MaxConns = 10
	cfg.MinConns = 0
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 5 * time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, errors.New("create database pool failed")
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		if pingCtx.Err() != nil {
			return nil, fmt.Errorf("connect database: %w", pingCtx.Err())
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			return nil, fmt.Errorf("connect database: PostgreSQL SQLSTATE %s", pgErr.Code)
		}
		return nil, errors.New("connect database failed: check address, credentials and database availability")
	}
	return pool, nil
}
