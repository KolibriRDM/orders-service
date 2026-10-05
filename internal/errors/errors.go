package errors

import "errors"

// Ошибки сервиса для проверки через errors.Is.
var (
	ErrInvalidInput      = errors.New("invalid input")
	ErrNotFound          = errors.New("not found")
	ErrForbidden         = errors.New("forbidden")
	ErrVersionConflict   = errors.New("version conflict")
	ErrInvalidTransition = errors.New("invalid status transition")

	ErrAlreadyExists = errors.New("already exists")

	ErrCatalogUnavailable  = errors.New("catalog unavailable")
	ErrDatabaseUnavailable = errors.New("orders database unavailable")

	ErrIdempotencyConflict = errors.New("idempotency key already used for another request")
)
