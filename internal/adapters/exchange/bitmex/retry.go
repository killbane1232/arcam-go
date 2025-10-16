package bitmex

import (
	"context"
	"time"
)

// Retry с экспоненциальным backoff и дедлайном через context
func Retry(ctx context.Context, maxAttempts int, initialDelay time.Duration, fn func() error) error {
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	if initialDelay <= 0 {
		initialDelay = 100 * time.Millisecond
	}

	delay := initialDelay
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := fn(); err != nil {
			lastErr = err
		} else {
			return nil
		}
		// Ждём перед следующей попыткой
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		delay = delay * 2
		if delay > 5*time.Second {
			delay = 5 * time.Second
		}
	}
	return lastErr
}

// StopRetry оборачивает ошибку как не подлежащую повтору
type StopRetryError struct{ Err error }

func (e StopRetryError) Error() string { return e.Err.Error() }

func StopRetry(err error) error { return StopRetryError{Err: err} }
