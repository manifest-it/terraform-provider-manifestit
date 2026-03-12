package auth

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

var (
	ErrEmptyCredentials   = errors.New("credentials cannot be empty")
	ErrInvalidTokenFormat = errors.New("invalid token format")
	ErrTokenExpired       = errors.New("token has expired")
)

// AuthStrategy defines the interface for applying authentication to HTTP requests
type AuthStrategy interface {
	Apply(req *http.Request) error
	Validate() error
}

// APIKeyAuth implements API key authentication
type APIKeyAuth struct {
	Key    string
	Prefix string // e.g., "Api-Token", "Bearer", "X-API-Key"
	Header string // Header name, defaults to "Authorization"
}

// NewAPIKeyAuth creates a new API key auth with default settings
func NewAPIKeyAuth(key string) (*APIKeyAuth, error) {
	if strings.TrimSpace(key) == "" {
		return nil, ErrEmptyCredentials
	}

	return &APIKeyAuth{
		Key:    key,
		Prefix: "Api-Token",
		Header: "Authorization",
	}, nil
}

// NewAPIKeyAuthWithPrefix creates API key auth with custom prefix
func NewAPIKeyAuthWithPrefix(key, prefix string) (*APIKeyAuth, error) {
	if strings.TrimSpace(key) == "" {
		return nil, ErrEmptyCredentials
	}

	if strings.TrimSpace(prefix) == "" {
		return nil, errors.New("prefix cannot be empty")
	}

	return &APIKeyAuth{
		Key:    key,
		Prefix: prefix,
		Header: "Authorization",
	}, nil
}

// NewCustomHeaderAPIKeyAuth creates API key auth with custom header name
func NewCustomHeaderAPIKeyAuth(key, headerName string) (*APIKeyAuth, error) {
	if strings.TrimSpace(key) == "" {
		return nil, ErrEmptyCredentials
	}

	if strings.TrimSpace(headerName) == "" {
		return nil, errors.New("header name cannot be empty")
	}

	return &APIKeyAuth{
		Key:    key,
		Prefix: "", // No prefix for custom headers
		Header: headerName,
	}, nil
}

func (a *APIKeyAuth) Validate() error {
	if strings.TrimSpace(a.Key) == "" {
		return ErrEmptyCredentials
	}
	if strings.TrimSpace(a.Header) == "" {
		return errors.New("header name cannot be empty")
	}
	return nil
}

func (a *APIKeyAuth) Apply(req *http.Request) error {
	if err := a.Validate(); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	value := a.Key
	if a.Prefix != "" {
		value = a.Prefix + " " + a.Key
	}

	req.Header.Set(a.Header, value)
	return nil
}

// BearerTokenAuth implements Bearer token authentication
type BearerTokenAuth struct {
	Token string
}

func NewBearerTokenAuth(token string) (*BearerTokenAuth, error) {
	if strings.TrimSpace(token) == "" {
		return nil, ErrEmptyCredentials
	}

	return &BearerTokenAuth{Token: token}, nil
}

func (b *BearerTokenAuth) Validate() error {
	if strings.TrimSpace(b.Token) == "" {
		return ErrEmptyCredentials
	}
	return nil
}

func (b *BearerTokenAuth) Apply(req *http.Request) error {
	if err := b.Validate(); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+b.Token)
	return nil
}

// BasicAuth implements HTTP Basic authentication
type BasicAuth struct {
	Username string
	Password string
}

func NewBasicAuth(username, password string) (*BasicAuth, error) {
	if strings.TrimSpace(username) == "" || strings.TrimSpace(password) == "" {
		return nil, ErrEmptyCredentials
	}

	return &BasicAuth{
		Username: username,
		Password: password,
	}, nil
}

func (b *BasicAuth) Validate() error {
	if strings.TrimSpace(b.Username) == "" || strings.TrimSpace(b.Password) == "" {
		return ErrEmptyCredentials
	}
	return nil
}

func (b *BasicAuth) Apply(req *http.Request) error {
	if err := b.Validate(); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	// Use standard library's SetBasicAuth for proper encoding
	req.SetBasicAuth(b.Username, b.Password)
	return nil
}

// OAuth2TokenAuth implements OAuth2 token authentication with refresh capability
type OAuth2TokenAuth struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	TokenRefresh TokenRefreshFunc
	mu           sync.RWMutex
}

// TokenRefreshFunc is called when the access token needs to be refreshed
type TokenRefreshFunc func(refreshToken string) (accessToken string, expiresAt time.Time, err error)

func NewOAuth2TokenAuth(accessToken string, expiresAt time.Time) (*OAuth2TokenAuth, error) {
	if strings.TrimSpace(accessToken) == "" {
		return nil, ErrEmptyCredentials
	}

	return &OAuth2TokenAuth{
		AccessToken: accessToken,
		ExpiresAt:   expiresAt,
	}, nil
}

func NewOAuth2TokenAuthWithRefresh(accessToken, refreshToken string, expiresAt time.Time, refreshFunc TokenRefreshFunc) (*OAuth2TokenAuth, error) {
	if strings.TrimSpace(accessToken) == "" {
		return nil, ErrEmptyCredentials
	}

	if refreshFunc != nil && strings.TrimSpace(refreshToken) == "" {
		return nil, errors.New("refresh token required when refresh function is provided")
	}

	return &OAuth2TokenAuth{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    expiresAt,
		TokenRefresh: refreshFunc,
	}, nil
}

func (o *OAuth2TokenAuth) Validate() error {
	o.mu.RLock()
	defer o.mu.RUnlock()

	if strings.TrimSpace(o.AccessToken) == "" {
		return ErrEmptyCredentials
	}

	// Check if token is expired
	if !o.ExpiresAt.IsZero() && time.Now().After(o.ExpiresAt) {
		return ErrTokenExpired
	}

	return nil
}

func (o *OAuth2TokenAuth) Apply(req *http.Request) error {
	// Try to refresh if token is expired or about to expire
	if o.needsRefresh() {
		if err := o.refreshToken(); err != nil {
			return fmt.Errorf("token refresh failed: %w", err)
		}
	}

	if err := o.Validate(); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	o.mu.RLock()
	token := o.AccessToken
	o.mu.RUnlock()

	req.Header.Set("Authorization", "Bearer "+token)
	return nil
}

func (o *OAuth2TokenAuth) needsRefresh() bool {
	if o.TokenRefresh == nil {
		return false
	}

	o.mu.RLock()
	defer o.mu.RUnlock()

	if o.ExpiresAt.IsZero() {
		return false
	}

	// Refresh if token expires within 5 minutes
	return time.Now().Add(5 * time.Minute).After(o.ExpiresAt)
}

func (o *OAuth2TokenAuth) refreshToken() error {
	if o.TokenRefresh == nil {
		return ErrTokenExpired
	}

	o.mu.Lock()
	defer o.mu.Unlock()

	// Double-check still needs refresh after acquiring lock
	if !o.ExpiresAt.IsZero() && time.Now().Add(5*time.Minute).Before(o.ExpiresAt) {
		return nil
	}

	newToken, expiresAt, err := o.TokenRefresh(o.RefreshToken)
	if err != nil {
		return err
	}

	o.AccessToken = newToken
	o.ExpiresAt = expiresAt

	return nil
}

// MultiAuth applies multiple auth strategies in sequence
type MultiAuth struct {
	strategies []AuthStrategy
}

func NewMultiAuth(strategies ...AuthStrategy) (*MultiAuth, error) {
	if len(strategies) == 0 {
		return nil, errors.New("at least one auth strategy required")
	}

	return &MultiAuth{strategies: strategies}, nil
}

func (m *MultiAuth) Validate() error {
	for i, strategy := range m.strategies {
		if err := strategy.Validate(); err != nil {
			return fmt.Errorf("strategy %d validation failed: %w", i, err)
		}
	}
	return nil
}

func (m *MultiAuth) Apply(req *http.Request) error {
	for i, strategy := range m.strategies {
		if err := strategy.Apply(req); err != nil {
			return fmt.Errorf("strategy %d application failed: %w", i, err)
		}
	}
	return nil
}

// NoAuth is a no-op auth strategy (for testing or public endpoints)
type NoAuth struct{}

func NewNoAuth() *NoAuth {
	return &NoAuth{}
}

func (n *NoAuth) Validate() error {
	return nil
}

func (n *NoAuth) Apply(req *http.Request) error {
	return nil
}

// CustomHeaderAuth allows setting arbitrary headers
type CustomHeaderAuth struct {
	Headers map[string]string
}

func NewCustomHeaderAuth(headers map[string]string) (*CustomHeaderAuth, error) {
	if len(headers) == 0 {
		return nil, errors.New("at least one header required")
	}

	// Validate headers
	for key, value := range headers {
		if strings.TrimSpace(key) == "" {
			return nil, errors.New("header key cannot be empty")
		}
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("header value for '%s' cannot be empty", key)
		}
	}

	return &CustomHeaderAuth{Headers: headers}, nil
}

func (c *CustomHeaderAuth) Validate() error {
	if len(c.Headers) == 0 {
		return errors.New("no headers configured")
	}

	for key, value := range c.Headers {
		if strings.TrimSpace(key) == "" {
			return errors.New("header key cannot be empty")
		}
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("header value for '%s' cannot be empty", key)
		}
	}

	return nil
}

func (c *CustomHeaderAuth) Apply(req *http.Request) error {
	if err := c.Validate(); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	for key, value := range c.Headers {
		req.Header.Set(key, value)
	}

	return nil
}

// AWSSignatureAuth is a placeholder for AWS Signature V4 authentication
// For production use, integrate with aws-sdk-go-v2/aws/signer/v4
type AWSSignatureAuth struct {
	AccessKeyID     string
	SecretAccessKey string
	Region          string
	Service         string
}

func NewAWSSignatureAuth(accessKeyID, secretAccessKey, region, service string) (*AWSSignatureAuth, error) {
	if strings.TrimSpace(accessKeyID) == "" || strings.TrimSpace(secretAccessKey) == "" {
		return nil, ErrEmptyCredentials
	}

	if strings.TrimSpace(region) == "" || strings.TrimSpace(service) == "" {
		return nil, errors.New("region and service are required")
	}

	return &AWSSignatureAuth{
		AccessKeyID:     accessKeyID,
		SecretAccessKey: secretAccessKey,
		Region:          region,
		Service:         service,
	}, nil
}

func (a *AWSSignatureAuth) Validate() error {
	if strings.TrimSpace(a.AccessKeyID) == "" || strings.TrimSpace(a.SecretAccessKey) == "" {
		return ErrEmptyCredentials
	}
	if strings.TrimSpace(a.Region) == "" || strings.TrimSpace(a.Service) == "" {
		return errors.New("region and service are required")
	}
	return nil
}

func (a *AWSSignatureAuth) Apply(req *http.Request) error {
	// This is a placeholder - in production, use aws-sdk-go-v2/aws/signer/v4
	return fmt.Errorf("AWS Signature V4 not implemented - use aws-sdk-go-v2/aws/signer/v4")
}

// Helper function to decode and validate base64 encoded credentials
func validateBase64Credentials(encoded string) error {
	if strings.TrimSpace(encoded) == "" {
		return ErrEmptyCredentials
	}

	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidTokenFormat, err)
	}

	if len(decoded) == 0 {
		return ErrEmptyCredentials
	}

	return nil
}
