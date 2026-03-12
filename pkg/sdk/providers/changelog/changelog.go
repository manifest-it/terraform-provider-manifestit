package changelog

import (
	"context"
	"encoding/json"
	"fmt"

	"terraform-provider-manifestit/pkg/sdk"
	sdkErrors "terraform-provider-manifestit/pkg/sdk/errors"
)

// Client defines the interface for changelog API operations.
// Each Terraform resource interacts with the API through this interface,
// making it easy to mock in tests or swap implementations.
type Client interface {
	Create(ctx context.Context, input CreateInput) (*Changelog, error)
	Read(ctx context.Context, id string) (*Changelog, error)
	Update(ctx context.Context, id string, input UpdateInput) (*Changelog, error)
	Delete(ctx context.Context, id string) error
}

// Changelog is the API representation of a changelog resource.
type Changelog struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// CreateInput holds the fields for creating a changelog.
type CreateInput struct {
	Name string `json:"name,omitempty"`
}

// UpdateInput holds the fields for updating a changelog.
type UpdateInput struct {
	Name string `json:"name,omitempty"`
}

const basePath = "/api/v1/changelogs"

// client implements Client using the SDK's APIClient.
type client struct {
	api *sdk.APIClient
}

// New creates a changelog Client backed by the given APIClient.
func New(api *sdk.APIClient) Client {
	return &client{api: api}
}

func (c *client) Create(ctx context.Context, input CreateInput) (*Changelog, error) {
	body, status, err := c.api.Post(ctx, basePath, input)
	if err != nil {
		return nil, fmt.Errorf("changelog create failed: %w", err)
	}
	if err := sdkErrors.Handle(status, body); err != nil {
		return nil, err
	}

	var result Changelog
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse create response: %w", err)
	}
	return &result, nil
}

func (c *client) Read(ctx context.Context, id string) (*Changelog, error) {
	body, status, err := c.api.Get(ctx, fmt.Sprintf("%s/%s", basePath, id))
	if err != nil {
		return nil, fmt.Errorf("changelog read failed: %w", err)
	}
	if err := sdkErrors.Handle(status, body); err != nil {
		return nil, err
	}

	var result Changelog
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse read response: %w", err)
	}
	return &result, nil
}

func (c *client) Update(ctx context.Context, id string, input UpdateInput) (*Changelog, error) {
	body, status, err := c.api.Put(ctx, fmt.Sprintf("%s/%s", basePath, id), input)
	if err != nil {
		return nil, fmt.Errorf("changelog update failed: %w", err)
	}
	if err := sdkErrors.Handle(status, body); err != nil {
		return nil, err
	}

	var result Changelog
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse update response: %w", err)
	}
	return &result, nil
}

func (c *client) Delete(ctx context.Context, id string) error {
	body, status, err := c.api.Delete(ctx, fmt.Sprintf("%s/%s", basePath, id))
	if err != nil {
		return fmt.Errorf("changelog delete failed: %w", err)
	}
	if err := sdkErrors.Handle(status, body); err != nil {
		return err
	}
	return nil
}
