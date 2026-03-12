package observer

import (
	"context"
	"encoding/json"
	"fmt"

	"terraform-provider-manifestit/pkg/sdk"
	sdkErrors "terraform-provider-manifestit/pkg/sdk/errors"
)

// Client defines the interface for observer API operations.
type Client interface {
	Post(ctx context.Context, input ObserverPayload) (*ObserverResponse, error)
}

// ObserverPayload is the full payload posted to the observer endpoint.
// It wraps the collected identity, git, and cloud context from a Terraform run.
type ObserverPayload struct {
	Identity    any    `json:"identity"`
	Git         any    `json:"git"`
	Cloud       any    `json:"cloud,omitempty"`
	CollectedAt string `json:"collected_at"`
	Action      string `json:"action"`           // "apply", "plan", "destroy"
	ResourceID  string `json:"resource_id"`       // Terraform resource ID
	OrgID       string `json:"org_id,omitempty"`
}

// ObserverResponse is the API response from posting an observation.
type ObserverResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

const basePath = "/api/v1/observer"

// client implements Client using the SDK's APIClient.
type client struct {
	api *sdk.APIClient
}

// New creates an observer Client backed by the given APIClient.
func New(api *sdk.APIClient) Client {
	return &client{api: api}
}

func (c *client) Post(ctx context.Context, input ObserverPayload) (*ObserverResponse, error) {
	body, status, err := c.api.Post(ctx, basePath, input)
	if err != nil {
		return nil, fmt.Errorf("observer post failed: %w", err)
	}
	if err := sdkErrors.Handle(status, body); err != nil {
		return nil, err
	}

	var result ObserverResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse observer response: %w", err)
	}
	return &result, nil
}
