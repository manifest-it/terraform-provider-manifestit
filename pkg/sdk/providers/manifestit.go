package providers

import (
	"net/http"

	"terraform-provider-manifestit/pkg/sdk"
	"terraform-provider-manifestit/pkg/sdk/auth"
	"terraform-provider-manifestit/pkg/sdk/providers/observer"

	"github.com/rs/zerolog"
)

// Config holds everything needed to build the ManifestIT SDK clients.
type Config struct {
	APIKey                  string
	BaseURL                 string
	OrgID                   string
	OrgKey                  string
	ProviderID              string
	ProviderConfigurationID string
	HTTPClient              *http.Client
	Debug                   bool
	Logger                  zerolog.Logger
	MaxRetries              int
}

// ProviderClient holds per-resource API clients.
type ProviderClient struct {
	Observer observer.Client
	OrgID    string
}

// NewProviderClient builds a ProviderClient from the given Config.
func NewProviderClient(cfg Config) (*ProviderClient, error) {
	var authStrategy auth.AuthStrategy
	if cfg.APIKey != "" {
		var err error
		authStrategy, err = auth.NewAPIKeyAuth(cfg.APIKey)
		if err != nil {
			return nil, err
		}
	} else {
		authStrategy = auth.NewNoAuth()
	}

	executor := sdk.NewHTTPExecutor(sdk.HTTPExecutorConfig{
		Client:     cfg.HTTPClient,
		Auth:       authStrategy,
		Debug:      cfg.Debug,
		Logger:     cfg.Logger,
		MaxRetries: cfg.MaxRetries,
	})

	api := sdk.NewAPIClient(sdk.APIClient{
		Executor:                executor,
		BaseURL:                 cfg.BaseURL,
		OrgID:                   cfg.OrgID,
		OrgKey:                  cfg.OrgKey,
		ProviderID:              cfg.ProviderID,
		ProviderConfigurationID: cfg.ProviderConfigurationID,
		ApiKey:                  cfg.APIKey,
		Logger:                  cfg.Logger,
	})

	return &ProviderClient{
		Observer: observer.New(api),
		OrgID:    cfg.OrgID,
	}, nil
}
