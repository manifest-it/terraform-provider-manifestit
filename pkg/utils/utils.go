package utils

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"reflect"
	"time"
)

// Options configures retry behavior. Pass nil or empty struct for defaults.
type Options struct {
	MaxRetries         int           // default: 5
	BaseBackoff        time.Duration // default: 100ms
	MaxBackoff         time.Duration // default: 5s
	MinJitter          time.Duration // default: 10ms
	MaxJitter          time.Duration // default: 50ms
	RetryOnEmptyResult bool          // default: false
	IsRetryable        func(error) bool
	OnAttempt          func(attempt int, err error)
}

func (o *Options) normalize() {
	if o.MaxRetries <= 0 {
		o.MaxRetries = 5
	}
	if o.BaseBackoff <= 0 {
		o.BaseBackoff = 100 * time.Millisecond
	}
	if o.MaxBackoff <= 0 {
		o.MaxBackoff = 5 * time.Second
	}
	if o.MinJitter <= 0 {
		o.MinJitter = 10 * time.Millisecond
	}
	if o.MaxJitter <= 0 || o.MaxJitter < o.MinJitter {
		o.MaxJitter = 50 * time.Millisecond
	}
}

func Retry[T any](ctx context.Context, fn func(ctx context.Context) (T, error), opts *Options) (T, error) {
	var zero T
	var o Options
	if opts != nil {
		o = *opts
	}
	o.normalize()

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	totalAttempts := o.MaxRetries + 1
	var lastErr error
	for attempt := 0; attempt < totalAttempts; attempt++ {
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}

		val, err := fn(ctx)

		if err == nil && !(o.RetryOnEmptyResult && IsZero(val)) {
			return val, nil
		}

		shouldRetry := true
		if err != nil && o.IsRetryable != nil {
			shouldRetry = o.IsRetryable(err)
		}
		if !shouldRetry {
			return zero, err
		}

		lastErr = err

		if attempt == totalAttempts-1 {
			if err != nil {
				return zero, err
			}
			if IsZero(val) {
				return zero, fmt.Errorf("empty result after %d retries", o.MaxRetries)
			}
			return zero, errors.New("max retries exceeded")
		}

		if o.OnAttempt != nil {
			o.OnAttempt(attempt+1, err)
		}

		// Exponential backoff with cap and jitter
		backoff := o.BaseBackoff * time.Duration(1<<uint(attempt))
		if backoff > o.MaxBackoff {
			backoff = o.MaxBackoff
		}
		jitterRange := o.MaxJitter - o.MinJitter
		jitter := o.MinJitter
		if jitterRange > 0 {
			jitter += time.Duration(rng.Int63n(jitterRange.Nanoseconds()))
		}

		timer := time.NewTimer(backoff + jitter)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return zero, ctx.Err()
		case <-timer.C:
		}
	}

	if lastErr != nil {
		return zero, lastErr
	}
	return zero, errors.New("retry exhausted")
}

func IsZero[T any](val T) bool {
	return reflect.ValueOf(val).IsZero()
}

// GetMultiEnvVar returns first matching env var
func GetMultiEnvVar(envVars ...string) (string, error) {
	for _, value := range envVars {
		if v := os.Getenv(value); v != "" {
			return v, nil
		}
	}
	return "", fmt.Errorf("unable to retrieve any env vars from list: %v", envVars)
}

// MITAPIKey name of env var for API key
const MITAPIKey = "MIT_API_KEY"

// MITHTTPRetryEnabled name of env var for retry enabled
const MITHTTPRetryEnabled = "MIT_HTTP_CLIENT_RETRY_ENABLED"

// MITHTTPRetryMaxRetries name of env var for max retries
const MITHTTPRetryMaxRetries = "MIT_HTTP_CLIENT_RETRY_MAX_RETRIES"
