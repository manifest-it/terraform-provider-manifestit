package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/rs/zerolog"
)

// APIClient provides convenience methods on top of HTTPExecutor for JSON REST APIs.
// Resource-specific clients hold a reference to this for making HTTP calls.
type APIClient struct {
	Executor                *HTTPExecutor
	BaseURL                 string
	OrgID                   string
	OrgKey                  string
	ProviderID              string
	ProviderConfigurationID string
	UserAgent               string
	Logger                  zerolog.Logger
}

// APIClientConfig holds configuration for building an APIClient.
type APIClientConfig struct {
	Executor                *HTTPExecutor
	BaseURL                 string
	OrgID                   string
	OrgKey                  string
	ProviderID              string
	ProviderConfigurationID string
	UserAgent               string
	Logger                  zerolog.Logger
}

// NewAPIClient creates a new APIClient.
func NewAPIClient(cfg APIClientConfig) *APIClient {
	if cfg.UserAgent == "" {
		cfg.UserAgent = "terraform-provider-manifestit/1.0"
	}
	return &APIClient{
		Executor:                cfg.Executor,
		BaseURL:                 strings.TrimRight(cfg.BaseURL, "/"),
		OrgID:                   cfg.OrgID,
		OrgKey:                  cfg.OrgKey,
		ProviderID:              cfg.ProviderID,
		ProviderConfigurationID: cfg.ProviderConfigurationID,
		UserAgent:               cfg.UserAgent,
		Logger:                  cfg.Logger,
	}
}

// Get performs a GET request to baseURL + path and returns the raw response body.
func (c *APIClient) Get(ctx context.Context, path string) ([]byte, int, error) {
	req, err := c.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, 0, err
	}
	return c.Executor.Do(ctx, req)
}

// Post performs a POST request with a JSON body.
func (c *APIClient) Post(ctx context.Context, path string, body any) ([]byte, int, error) {
	req, err := c.newRequest(ctx, http.MethodPost, path, body)
	if err != nil {
		return nil, 0, err
	}
	return c.Executor.Do(ctx, req)
}

// Put performs a PUT request with a JSON body.
func (c *APIClient) Put(ctx context.Context, path string, body any) ([]byte, int, error) {
	req, err := c.newRequest(ctx, http.MethodPut, path, body)
	if err != nil {
		return nil, 0, err
	}
	return c.Executor.Do(ctx, req)
}

// Patch performs a PATCH request with a JSON body.
func (c *APIClient) Patch(ctx context.Context, path string, body any) ([]byte, int, error) {
	req, err := c.newRequest(ctx, http.MethodPatch, path, body)
	if err != nil {
		return nil, 0, err
	}
	return c.Executor.Do(ctx, req)
}

// Delete performs a DELETE request.
func (c *APIClient) Delete(ctx context.Context, path string) ([]byte, int, error) {
	req, err := c.newRequest(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return nil, 0, err
	}
	return c.Executor.Do(ctx, req)
}

// newRequest builds an *http.Request with standard headers.
func (c *APIClient) newRequest(ctx context.Context, method, path string, body any) (*http.Request, error) {
	url := c.BaseURL + path

	var reqBody *bytes.Buffer
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		reqBody = bytes.NewBuffer(data)
	}

	var req *http.Request
	var err error
	if reqBody != nil {
		req, err = http.NewRequestWithContext(ctx, method, url, reqBody)
	} else {
		req, err = http.NewRequestWithContext(ctx, method, url, nil)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("User-Agent", c.UserAgent)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.OrgID != "" {
		req.Header.Set("Mit-Org-ID", c.OrgID)
	}
	if c.OrgKey != "" {
		req.Header.Set("Mit-Org-Key", c.OrgKey)
	}
	if c.ProviderID != "" {
		req.Header.Set("Mit-Provider-ID", c.ProviderID)
		req.Header.Set("X-Provider", "terraform")
	}
	if c.ProviderConfigurationID != "" {
		req.Header.Set("Mit-Provider-Config-ID", c.ProviderConfigurationID)
	}

	return req, nil
}
