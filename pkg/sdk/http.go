package sdk

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"terraform-provider-manifestit/pkg/utils"
	"time"

	"terraform-provider-manifestit/pkg/sdk/auth"
	sdkErrors "terraform-provider-manifestit/pkg/sdk/errors"

	"github.com/rs/zerolog"
)

// HTTPExecutor handles HTTP requests with retry logic, authentication, and observability
type HTTPExecutor struct {
	Client      *http.Client
	Auth        auth.AuthStrategy
	Logger      zerolog.Logger
	Debug       bool
	MaxRetries  int
	BaseBackoff time.Duration
	MaxBodySize int64 // Maximum response body size in bytes
}

// HTTPExecutorConfig provides configuration for HTTPExecutor
type HTTPExecutorConfig struct {
	Client      *http.Client
	Auth        auth.AuthStrategy
	Debug       bool
	Logger      zerolog.Logger
	MaxRetries  int
	BaseBackoff time.Duration
	MaxBodySize int64 // 0 means unlimited
}

// httpResult contains the response data
type httpResult struct {
	body   []byte
	status int
	header http.Header
}

// NewHTTPExecutor creates a new HTTPExecutor with sensible defaults
func NewHTTPExecutor(config HTTPExecutorConfig) *HTTPExecutor {
	if config.Client == nil {
		config.Client = &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
				DisableCompression:  false,
			},
		}
	}

	if config.MaxRetries == 0 {
		config.MaxRetries = 3
	}

	if config.BaseBackoff == 0 {
		config.BaseBackoff = 300 * time.Millisecond
	}

	if config.MaxBodySize == 0 {
		config.MaxBodySize = 100 * 1024 * 1024 // 100MB default
	}

	return &HTTPExecutor{
		Client:      config.Client,
		Auth:        config.Auth,
		Debug:       config.Debug,
		Logger:      config.Logger,
		MaxRetries:  config.MaxRetries,
		BaseBackoff: config.BaseBackoff,
		MaxBodySize: config.MaxBodySize,
	}
}

// Do execute an HTTP request with retry logic and comprehensive error handling
func (h *HTTPExecutor) Do(ctx context.Context, req *http.Request) ([]byte, int, error) {
	startTime := time.Now()
	var lastErr error
	var retryCount int

	// Apply authentication
	if h.Auth != nil {
		if err := h.Auth.Apply(req); err != nil {
			return nil, 0, fmt.Errorf("auth application failed: %w", err)
		}
	}

	// Add tracing if debug is enabled
	if h.Debug {
		req = h.addTracing(ctx, req)
	}

	// Log initial request
	h.logRequest(req)

	result, err := utils.Retry[httpResult](ctx,
		func(ctx context.Context) (httpResult, error) {
			retryCount++

			// Clone request for retry safety
			reqClone := req.Clone(ctx)

			resp, err := h.Client.Do(reqClone)
			if err != nil {
				lastErr = fmt.Errorf("request failed: %w", err)

				// Network errors are retryable
				return httpResult{}, lastErr
			}

			defer func() {
				// Ensure body is fully consumed and closed
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}()

			// Read body with size limit
			body, err := h.readBody(resp.Body)
			if err != nil {
				lastErr = err
				h.logResponse(resp.StatusCode, 0, err)

				if errors.Is(err, sdkErrors.ErrBodyTooLarge) {
					// Don't retry body too large errors
					return httpResult{}, fmt.Errorf("non-retryable: %w", err)
				}

				return httpResult{}, lastErr
			}

			// Log response
			h.logResponse(resp.StatusCode, len(body), nil)

			// Check for retryable status codes
			if h.isRetryable(resp.StatusCode) {
				lastErr = &sdkErrors.HTTPError{
					Status:     resp.StatusCode,
					Body:       truncate(string(body), 500),
					Method:     req.Method,
					URL:        req.URL.String(),
					RetryCount: retryCount - 1,
					Duration:   time.Since(startTime),
				}

				return httpResult{}, lastErr
			}

			// Return result for non-retryable responses (including errors like 400, 404)
			return httpResult{
				body:   body,
				status: resp.StatusCode,
				header: resp.Header,
			}, nil
		},
		&utils.Options{
			MaxRetries:  h.MaxRetries,
			BaseBackoff: h.BaseBackoff,
		},
	)

	duration := time.Since(startTime)

	if err != nil {
		// Enhance error with context
		if lastErr != nil {
			var httpErr *sdkErrors.HTTPError
			if errors.As(lastErr, &httpErr) {
				httpErr.RetryCount = retryCount - 1
				httpErr.Duration = duration
				return nil, 0, httpErr
			}
			return nil, 0, fmt.Errorf("after %d retries: %w", retryCount-1, lastErr)
		}
		return nil, 0, fmt.Errorf("request failed after %d retries: %w", retryCount-1, err)
	}

	// Check for HTTP errors on successful completion
	if result.status >= 400 {
		return result.body, result.status, &sdkErrors.HTTPError{
			Status:     result.status,
			Body:       truncate(string(result.body), 500),
			Method:     req.Method,
			URL:        req.URL.String(),
			RetryCount: retryCount - 1,
			Duration:   duration,
		}
	}

	return result.body, result.status, nil
}

// DoWithHeaders is like Do but also returns response headers
func (h *HTTPExecutor) DoWithHeaders(ctx context.Context, req *http.Request) ([]byte, int, http.Header, error) {
	startTime := time.Now()
	var lastErr error
	var retryCount int

	if h.Auth != nil {
		if err := h.Auth.Apply(req); err != nil {
			return nil, 0, nil, fmt.Errorf("auth application failed: %w", err)
		}
	}

	if h.Debug {
		req = h.addTracing(ctx, req)
	}

	h.logRequest(req)

	result, err := utils.Retry[httpResult](
		ctx,
		func(ctx context.Context) (httpResult, error) {
			retryCount++
			reqClone := req.Clone(ctx)

			resp, err := h.Client.Do(reqClone)
			if err != nil {
				lastErr = fmt.Errorf("request failed: %w", err)
				return httpResult{}, lastErr
			}

			defer func() {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}()

			body, err := h.readBody(resp.Body)
			if err != nil {
				lastErr = err
				h.logResponse(resp.StatusCode, 0, err)
				if errors.Is(err, sdkErrors.ErrBodyTooLarge) {
					return httpResult{}, fmt.Errorf("non-retryable: %w", err)
				}
				return httpResult{}, lastErr
			}

			h.logResponse(resp.StatusCode, len(body), nil)

			if h.isRetryable(resp.StatusCode) {
				lastErr = &sdkErrors.HTTPError{
					Status:     resp.StatusCode,
					Body:       truncate(string(body), 500),
					Method:     req.Method,
					URL:        req.URL.String(),
					RetryCount: retryCount - 1,
					Duration:   time.Since(startTime),
				}

				return httpResult{}, lastErr
			}

			return httpResult{
				body:   body,
				status: resp.StatusCode,
				header: resp.Header,
			}, nil
		},
		&utils.Options{
			MaxRetries:  h.MaxRetries,
			BaseBackoff: h.BaseBackoff,
		},
	)

	duration := time.Since(startTime)

	if err != nil {
		if lastErr != nil {
			if httpErr, ok := lastErr.(*sdkErrors.HTTPError); ok {
				httpErr.RetryCount = retryCount - 1
				httpErr.Duration = duration
				return nil, 0, nil, httpErr
			}
			return nil, 0, nil, fmt.Errorf("after %d retries: %w", retryCount-1, lastErr)
		}
		return nil, 0, nil, fmt.Errorf("request failed after %d retries: %w", retryCount-1, err)
	}

	if result.status >= 400 {
		return result.body, result.status, result.header, &sdkErrors.HTTPError{
			Status:     result.status,
			Body:       truncate(string(result.body), 500),
			Method:     req.Method,
			URL:        req.URL.String(),
			RetryCount: retryCount - 1,
			Duration:   duration,
		}
	}

	return result.body, result.status, result.header, nil
}

// readBody reads the response body with size limit protection
func (h *HTTPExecutor) readBody(body io.Reader) ([]byte, error) {
	limitedReader := io.LimitReader(body, h.MaxBodySize+1)
	data, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, fmt.Errorf("failed to read body: %w", err)
	}

	if int64(len(data)) > h.MaxBodySize {
		return nil, sdkErrors.ErrBodyTooLarge
	}

	return data, nil
}

// isRetryable determines if an HTTP status code should trigger a retry
func (h *HTTPExecutor) isRetryable(status int) bool {
	return status == http.StatusTooManyRequests ||
		status == http.StatusRequestTimeout ||
		status == http.StatusServiceUnavailable ||
		status == http.StatusGatewayTimeout ||
		status == http.StatusBadGateway ||
		(status >= 500 && status < 600)
}

// logRequest logs the outgoing request
func (h *HTTPExecutor) logRequest(req *http.Request) {
	if !h.Debug {
		return
	}

	event := h.Logger.Debug().
		Str("method", req.Method).
		Str("url", req.URL.String()).
		Str("host", req.URL.Host).
		Str("path", req.URL.Path)

	if req.URL.RawQuery != "" {
		event.Str("query", req.URL.RawQuery)
	}

	// Log selected headers (avoid sensitive data)
	headers := make(map[string]string)
	for key, values := range req.Header {
		lowerKey := strings.ToLower(key)
		// Skip sensitive headers
		if lowerKey == "authorization" || lowerKey == "cookie" || lowerKey == "api-key" {
			headers[key] = "[REDACTED]"
		} else if len(values) > 0 {
			headers[key] = values[0]
		}
	}

	if len(headers) > 0 {
		event.Interface("headers", headers)
	}

	event.Msg("HTTP request")
}

// logResponse logs the HTTP response
func (h *HTTPExecutor) logResponse(status int, bodySize int, err error) {
	if !h.Debug {
		return
	}

	event := h.Logger.Debug().Int("status", status).Int("body_size", bodySize)

	if err != nil {
		event.Err(err)
	}

	// Add status category
	switch {
	case status >= 500:
		event.Str("category", "server_error")
	case status >= 400:
		event.Str("category", "client_error")
	case status >= 300:
		event.Str("category", "redirect")
	case status >= 200:
		event.Str("category", "success")
	default:
		event.Str("category", "informational")
	}

	event.Msg("HTTP response")
}

// addTracing adds HTTP tracing for detailed debugging
func (h *HTTPExecutor) addTracing(ctx context.Context, req *http.Request) *http.Request {
	trace := &httptrace.ClientTrace{
		DNSStart: func(info httptrace.DNSStartInfo) {
			h.Logger.Debug().Str("host", info.Host).Msg("DNS lookup started")
		},
		DNSDone: func(info httptrace.DNSDoneInfo) {
			if info.Err != nil {
				h.Logger.Debug().Err(info.Err).Msg("DNS lookup failed")
			} else {
				h.Logger.Debug().Interface("addrs", info.Addrs).Msg("DNS lookup completed")
			}
		},
		ConnectStart: func(network, addr string) {
			h.Logger.Debug().Str("network", network).Str("addr", addr).Msg("Connection started")
		},
		ConnectDone: func(network, addr string, err error) {
			if err != nil {
				h.Logger.Debug().Err(err).Msg("Connection failed")
			} else {
				h.Logger.Debug().Str("network", network).Str("addr", addr).Msg("Connection established")
			}
		},
		GotFirstResponseByte: func() {
			h.Logger.Debug().Msg("First response byte received")
		},
	}

	return req.WithContext(httptrace.WithClientTrace(ctx, trace))
}

// truncate truncates a string to maxLen characters
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "... (truncated)"
}
