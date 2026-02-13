package common

import "errors"

var (
	// ErrRetryable indicates a transient error that should be retried (e.g. network glitch)
	ErrRetryable = errors.New("retryable error")

	// ErrNonRetryable indicates a permanent error that should NOT be retried (e.g. invalid data)
	ErrNonRetryable = errors.New("non-retryable error")
)
