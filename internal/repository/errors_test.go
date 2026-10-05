package repository

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
	"github.com/jackc/pgx/v5/pgconn"
)

// Проверяет классификацию ошибок БД и приоритет отмены.
func TestDatabaseErrorClassification(t *testing.T) {
	for _, original := range []error{
		io.EOF, io.ErrUnexpectedEOF,
		&net.OpError{Op: "read", Net: "tcp", Err: io.EOF},
		&pgconn.PgError{Code: "08006"}, &pgconn.PgError{Code: "57P01"},
		&pgconn.PgError{Code: "57P02"}, &pgconn.PgError{Code: "57P03"}, &pgconn.PgError{Code: "53300"},
	} {
		if err := databaseError(context.Background(), "query", original); !errors.Is(err, orderserrors.ErrDatabaseUnavailable) {
			t.Fatalf("%T classified as %v", original, err)
		}
	}
	original := &pgconn.PgError{Code: "42601", Message: "syntax error"}
	err := databaseError(context.Background(), "query", original)
	if !errors.Is(err, original) || errors.Is(err, orderserrors.ErrDatabaseUnavailable) {
		t.Fatalf("SQL error misclassified: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := databaseError(ctx, "query", io.EOF); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

// Проверяет отказ отменённого чтения до доступа к пулу.
func TestGetByIDAlreadyCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := NewRepository(nil).GetByID(ctx, "unused")
	if !errors.Is(err, context.Canceled) || got.ID != "" || got.Items != nil {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}
