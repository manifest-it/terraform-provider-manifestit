package collectors

import (
	"context"
	"encoding/json"
	"path/filepath"

	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// tfStateMetadata represents the structure of .terraform/terraform.tfstate,
// which contains backend configuration (not the actual state content).
type tfStateMetadata struct {
	Backend *tfBackendBlock `json:"backend"`
}

type tfBackendBlock struct {
	Type   string                 `json:"type"`
	Config map[string]interface{} `json:"config"`
}

// tfState represents the top-level fields of a Terraform state file.
// We only parse what we need for correlation (lineage, serial, version).
type tfState struct {
	Lineage          string `json:"lineage"`
	Serial           int64  `json:"serial"`
	TerraformVersion string `json:"terraform_version"`
}

// CollectStateMetadata reads backend config from .terraform/terraform.tfstate,
// then fetches the actual state from the remote backend to extract lineage and serial.
func (c *Collector) CollectStateMetadata(ctx context.Context) StateMetadata {
	meta := StateMetadata{}

	// Step A: Read .terraform/terraform.tfstate for backend config
	backendCfg, err := c.readBackendConfig(ctx)
	if err != nil {
		tflog.Debug(ctx, "no backend config found, trying local state", map[string]interface{}{"error": err.Error()})
		meta.Backend = &BackendConfig{Type: "local"}
		return c.readLocalState(ctx, meta)
	}
	meta.Backend = backendCfg

	// Local backend: read state directly from disk
	if backendCfg.Type == "local" {
		return c.readLocalState(ctx, meta)
	}

	// Step B: Read actual state from remote backend
	if c.StateR == nil {
		return meta
	}

	stateBytes, err := c.StateR.ReadState(ctx, backendCfg)
	if err != nil {
		tflog.Debug(ctx, "could not read remote state", map[string]interface{}{"error": err.Error()})
		return meta
	}

	return c.parseStateBytes(stateBytes, meta)
}

// readBackendConfig parses .terraform/terraform.tfstate to extract backend type and config.
func (c *Collector) readBackendConfig(ctx context.Context) (*BackendConfig, error) {
	data, err := c.FS.ReadFile(filepath.Join(".terraform", "terraform.tfstate"))
	if err != nil {
		return nil, err
	}

	var tfMeta tfStateMetadata
	if err := json.Unmarshal(data, &tfMeta); err != nil {
		return nil, err
	}

	if tfMeta.Backend == nil {
		// No backend block means local backend
		return &BackendConfig{Type: "local"}, nil
	}

	cfg := &BackendConfig{Type: tfMeta.Backend.Type}
	raw := tfMeta.Backend.Config

	// Extract backend-specific fields
	switch cfg.Type {
	case "s3":
		cfg.Bucket = stringFromMap(raw, "bucket")
		cfg.Key = stringFromMap(raw, "key")
		cfg.Region = stringFromMap(raw, "region")
	case "azurerm":
		cfg.StorageAccountName = stringFromMap(raw, "storage_account_name")
		cfg.ContainerName = stringFromMap(raw, "container_name")
		cfg.Key = stringFromMap(raw, "key")
	case "gcs":
		cfg.Bucket = stringFromMap(raw, "bucket")
		cfg.Prefix = stringFromMap(raw, "prefix")
	}

	return cfg, nil
}

// readLocalState reads terraform.tfstate from the working directory for local backends.
func (c *Collector) readLocalState(ctx context.Context, meta StateMetadata) StateMetadata {
	data, err := c.FS.ReadFile("terraform.tfstate")
	if err != nil {
		tflog.Debug(ctx, "could not read local state file", map[string]interface{}{"error": err.Error()})
		return meta
	}
	return c.parseStateBytes(data, meta)
}

// parseStateBytes extracts lineage, serial, and terraform_version from raw state JSON.
func (c *Collector) parseStateBytes(data []byte, meta StateMetadata) StateMetadata {
	var state tfState
	if err := json.Unmarshal(data, &state); err != nil {
		return meta
	}

	meta.Available = true
	meta.Lineage = state.Lineage
	meta.Serial = state.Serial
	meta.TerraformVersion = state.TerraformVersion
	return meta
}

// stringFromMap safely extracts a string value from a map[string]interface{}.
func stringFromMap(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
