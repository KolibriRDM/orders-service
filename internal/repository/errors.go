package repository

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
	"github.com/jackc/pgx/v5/pgconn"
)

// Отделяет отмену и недоступность БД от остальных SQL-ошибок.
func databaseError(ctx context.Context, operation string, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("%s: %w", operation, ctx.Err())
	}
	var networkError net.Error
	var connectError *pgconn.ConnectError
	var pgError *pgconn.PgError
	unavailable := errors.As(err, &networkError) || errors.As(err, &connectError) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
	if errors.As(err, &pgError) {
		unavailable = unavailable || strings.HasPrefix(pgError.Code, "08") || pgError.Code == "57P01" || pgError.Code == "57P02" || pgError.Code == "57P03" || pgError.Code == "53300"
	}
	if unavailable {
		return fmt.Errorf("%s: %w", operation, orderserrors.ErrDatabaseUnavailable)
	}
	return fmt.Errorf("%s: %w", operation, err)
}
