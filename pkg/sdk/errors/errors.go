package errors

import (
	"errors"
	"fmt"
	"net/http"
	"time"
)

// HTTPError represents an HTTP error response with rich context
type HTTPError struct {
	Status     int
	Body       string
	Method     string
	URL        string
	RetryCount int
	Duration   time.Duration
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("HTTP %d %s %s (retries: %d, duration: %v): %s",
		e.Status, e.Method, e.URL, e.RetryCount, e.Duration, e.Body)
}

func (e *HTTPError) Is(target error) bool {
	switch target {
	case ErrUnauthorized:
		return e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden
	case ErrRateLimited:
		return e.Status == http.StatusTooManyRequests
	case ErrNotFound:
		return e.Status == http.StatusNotFound
	case ErrBadRequest:
		return e.Status == http.StatusBadRequest
	case ErrServerError:
		return e.Status >= 500 && e.Status < 600
	default:
		return false
	}
}

var (
	ErrUnauthorized = errors.New("unauthorized: invalid credentials")
	ErrRateLimited  = errors.New("rate limit exceeded")
	ErrNotFound     = errors.New("resource not found")
	ErrBadRequest   = errors.New("bad request")
	ErrServerError  = errors.New("server error")
	ErrBodyTooLarge = errors.New("response body exceeds maximum size")
	ErrEmptyBody    = errors.New("unexpected empty response body")
)

// Sentinel errors for validation and request errors
var (
	ErrMissingTimeRange = errors.New("missing required time range parameters")
	ErrInvalidTimeRange = errors.New("invalid time range: 'from' must be before 'to'")
	ErrInvalidParams    = errors.New("invalid parameters")
	ErrStreamChannelNil = errors.New("stream channel cannot be nil")
	ErrMaxPagesExceeded = errors.New("maximum pages limit exceeded")
	ErrEmptyResponse    = errors.New("empty response body")
	ErrInvalidJSON      = errors.New("invalid JSON response")
)

// Handle checks HTTP status code and returns appropriate error
func Handle(status int, body []byte) error {
	if status < 300 {
		return nil
	}

	bodyStr := string(body)
	if len(bodyStr) > 500 {
		bodyStr = bodyStr[:500] + "... (truncated)"
	}

	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w: %s", ErrUnauthorized, bodyStr)
	case http.StatusNotFound:
		return fmt.Errorf("%w: %s", ErrNotFound, bodyStr)
	case http.StatusBadRequest:
		return fmt.Errorf("%w: %s", ErrBadRequest, bodyStr)
	case http.StatusTooManyRequests:
		return fmt.Errorf("%w: %s", ErrRateLimited, bodyStr)
	default:
		if status >= 500 {
			return &HTTPError{Status: status, Body: bodyStr}
		}
		return &HTTPError{Status: status, Body: bodyStr}
	}
}
