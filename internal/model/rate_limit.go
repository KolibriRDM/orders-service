package model

import "time"

// Решение лимитера и время до следующей разрешённой попытки.
type RateLimitDecision struct {
	Allowed    bool
	RetryAfter time.Duration
}
