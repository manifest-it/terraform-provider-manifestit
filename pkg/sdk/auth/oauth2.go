package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// OAuth2Config holds OAuth2 client credentials configuration
type OAuth2Config struct {
	ClientID     string
	ClientSecret string
	TokenURL     string
	Scopes       []string
	Resource     string // Optional resource parameter (e.g., for Dynatrace account URN)
}

// OAuth2TokenResponse represents the OAuth2 token response
type OAuth2TokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	Scope       string `json:"scope"`
}

// OAuth2Auth implements OAuth2 client credentials authentication with automatic token refresh
type OAuth2Auth struct {
	config        OAuth2Config
	client        *http.Client
	token         string
	expiresAt     time.Time
	mu            sync.RWMutex
	refreshBuffer time.Duration // Refresh token this much before expiry
}

// NewOAuth2Auth creates a new OAuth2 authentication strategy
func NewOAuth2Auth(config OAuth2Config, client *http.Client) (*OAuth2Auth, error) {
	if strings.TrimSpace(config.ClientID) == "" {
		return nil, fmt.Errorf("client ID cannot be empty")
	}
	if strings.TrimSpace(config.ClientSecret) == "" {
		return nil, fmt.Errorf("client secret cannot be empty")
	}
	if strings.TrimSpace(config.TokenURL) == "" {
		return nil, fmt.Errorf("token URL cannot be empty")
	}

	if client == nil {
		client = http.DefaultClient
	}

	auth := &OAuth2Auth{
		config:        config,
		client:        client,
		refreshBuffer: 30 * time.Second, // Refresh 30s before expiry
	}

	// Get initial token
	if err := auth.refreshToken(context.Background()); err != nil {
		return nil, fmt.Errorf("failed to get initial token: %w", err)
	}

	return auth, nil
}

// Apply applies the OAuth2 token to the request
func (o *OAuth2Auth) Apply(req *http.Request) error {
	token, err := o.getValidToken(req.Context())
	if err != nil {
		return fmt.Errorf("failed to get valid token: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	return nil
}

// Validate validates the OAuth2 configuration
func (o *OAuth2Auth) Validate() error {
	if strings.TrimSpace(o.config.ClientID) == "" {
		return fmt.Errorf("client ID cannot be empty")
	}
	if strings.TrimSpace(o.config.ClientSecret) == "" {
		return fmt.Errorf("client secret cannot be empty")
	}
	if strings.TrimSpace(o.config.TokenURL) == "" {
		return fmt.Errorf("token URL cannot be empty")
	}
	return nil
}

// getValidToken returns a valid token, refreshing if necessary
func (o *OAuth2Auth) getValidToken(ctx context.Context) (string, error) {
	o.mu.RLock()
	if o.isTokenValid() {
		token := o.token
		o.mu.RUnlock()
		return token, nil
	}
	o.mu.RUnlock()

	// Token needs refresh
	o.mu.Lock()
	defer o.mu.Unlock()

	// Double-check after acquiring write lock
	if o.isTokenValid() {
		return o.token, nil
	}

	if err := o.refreshToken(ctx); err != nil {
		return "", err
	}

	return o.token, nil
}

// isTokenValid checks if the current token is still valid (must hold read or write lock)
func (o *OAuth2Auth) isTokenValid() bool {
	if o.token == "" {
		return false
	}
	// Consider token invalid if it expires within the refresh buffer
	return time.Now().Add(o.refreshBuffer).Before(o.expiresAt)
}

// refreshToken fetches a new token from the OAuth2 server (must hold write lock)
func (o *OAuth2Auth) refreshToken(ctx context.Context) error {
	// Build request body
	data := url.Values{}
	data.Set("grant_type", "client_credentials")
	data.Set("client_id", o.config.ClientID)
	data.Set("client_secret", o.config.ClientSecret)
	if len(o.config.Scopes) > 0 {
		data.Set("scope", strings.Join(o.config.Scopes, " "))
	}
	if o.config.Resource != "" {
		data.Set("resource", o.config.Resource)
	}

	// Create request
	req, err := http.NewRequestWithContext(ctx, "POST", o.config.TokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return fmt.Errorf("failed to create token request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	// Execute request
	resp, err := o.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to request token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errorBody map[string]interface{}
		err = json.NewDecoder(resp.Body).Decode(&errorBody)
		if err != nil {
			return fmt.Errorf("token request failed with status %d and unreadable body", resp.StatusCode)
		}
		errorJSON, _ := json.Marshal(errorBody)
		return fmt.Errorf("token request failed with status %d: %s", resp.StatusCode, string(errorJSON))
	}

	// Parse response
	var tokenResp OAuth2TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return fmt.Errorf("failed to decode token response: %w", err)
	}

	if tokenResp.AccessToken == "" {
		return fmt.Errorf("received empty access token")
	}

	// Update token
	o.token = tokenResp.AccessToken
	if tokenResp.ExpiresIn > 0 {
		o.expiresAt = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	} else {
		// Default to 5 minutes if not specified (Dynatrace logs tokens expire in 5 mins)
		o.expiresAt = time.Now().Add(5 * time.Minute)
	}

	return nil
}

// ForceRefresh forces a token refresh regardless of expiry
func (o *OAuth2Auth) ForceRefresh(ctx context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.refreshToken(ctx)
}
