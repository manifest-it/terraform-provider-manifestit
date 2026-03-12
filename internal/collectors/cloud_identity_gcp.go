package collectors

import (
	"context"
	"encoding/json"
	"os"
	"strings"

	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// gcpCredentialFile represents the common fields in a GCP credential JSON file.
type gcpCredentialFile struct {
	Type                           string `json:"type"`
	ProjectID                      string `json:"project_id"`
	PrivateKeyID                   string `json:"private_key_id"`
	ClientEmail                    string `json:"client_email"`
	ClientID                       string `json:"client_id"`
	ServiceAccountImpersonationURL string `json:"service_account_impersonation_url"`
	WorkforcePoolUserProject       string `json:"workforce_pool_user_project"`
}

// collectGCP detects GCP credentials and extracts identity from credential files or env vars.
func (c *Collector) collectGCP(ctx context.Context) *CloudIdentity {
	credJSON, authType := c.findGCPCredentials(ctx)
	if authType == "" {
		return nil
	}

	gcpID := &GCPIdentity{
		AuthType: authType,
	}

	if credJSON != nil {
		cred, err := parseGCPCredentialFile(credJSON)
		if err != nil {
			tflog.Warn(ctx, "failed to parse GCP credentials", map[string]interface{}{"error": err.Error()})
		} else {
			c.enrichGCPFromCredFile(gcpID, cred)
		}
	}

	principalID := gcpID.ClientEmail
	if principalID == "" {
		principalID = gcpID.ClientID
	}

	return &CloudIdentity{
		Provider:    "gcp",
		AccountID:   gcpID.ProjectID,
		PrincipalID: principalID,
		AuthType:    gcpID.AuthType,
		GCP:         gcpID,
	}
}

// findGCPCredentials locates GCP credentials and returns the raw JSON + auth type.
func (c *Collector) findGCPCredentials(ctx context.Context) ([]byte, string) {
	// 1. GOOGLE_APPLICATION_CREDENTIALS points to a key file
	if path := c.Env.Getenv("GOOGLE_APPLICATION_CREDENTIALS"); path != "" {
		data, err := c.FS.ReadFile(path)
		if err != nil {
			tflog.Debug(ctx, "failed to read GOOGLE_APPLICATION_CREDENTIALS", map[string]interface{}{
				"path": path, "error": err.Error(),
			})
			return nil, "service-account" // file unreadable but env var is set
		}
		// Determine type from file contents
		authType := gcpAuthTypeFromJSON(data)
		return data, authType
	}

	// 2. GOOGLE_CREDENTIALS contains JSON directly
	if creds := c.Env.Getenv("GOOGLE_CREDENTIALS"); creds != "" {
		data := []byte(creds)
		return data, gcpAuthTypeFromJSON(data)
	}

	// 3. gcloud CLI access token
	if c.Env.Getenv("CLOUDSDK_AUTH_ACCESS_TOKEN") != "" {
		return nil, "user-adc"
	}

	// 4. Application Default Credentials file
	home, err := os.UserHomeDir()
	if err == nil {
		adcPath := home + "/.config/gcloud/application_default_credentials.json"
		if data, err := c.FS.ReadFile(adcPath); err == nil {
			return data, gcpAuthTypeFromJSON(data)
		}
	}

	return nil, ""
}

// gcpAuthTypeFromJSON determines the auth type from the "type" field in a GCP credential JSON.
func gcpAuthTypeFromJSON(data []byte) string {
	var peek struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &peek); err != nil {
		return "service-account" // default assumption
	}
	switch peek.Type {
	case "service_account":
		return "service-account"
	case "authorized_user":
		return "user-adc"
	case "external_account":
		return "workload-identity"
	default:
		return peek.Type
	}
}

// parseGCPCredentialFile parses a GCP credential JSON file. Never exposes private key content.
func parseGCPCredentialFile(data []byte) (*gcpCredentialFile, error) {
	var cred gcpCredentialFile
	if err := json.Unmarshal(data, &cred); err != nil {
		return nil, err
	}
	return &cred, nil
}

func (c *Collector) enrichGCPFromCredFile(id *GCPIdentity, cred *gcpCredentialFile) {
	if cred.ProjectID != "" {
		id.ProjectID = cred.ProjectID
	}
	if cred.ClientEmail != "" {
		id.ClientEmail = cred.ClientEmail
	}
	if cred.ClientID != "" {
		id.ClientID = cred.ClientID
	}
	if cred.PrivateKeyID != "" {
		id.KeyID = cred.PrivateKeyID
	}

	// For workload identity federation, extract the impersonated SA email
	if cred.ServiceAccountImpersonationURL != "" {
		id.ImpersonatedEmail = extractSAEmailFromURL(cred.ServiceAccountImpersonationURL)
	}

	// Refine auth type based on credential content
	switch cred.Type {
	case "service_account":
		id.AuthType = "service-account"
	case "authorized_user":
		id.AuthType = "user-adc"
	case "external_account":
		id.AuthType = "workload-identity"
	}
}

// extractSAEmailFromURL extracts a service account email from an impersonation URL.
// Example URL: https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/sa@proj.iam.gserviceaccount.com:generateAccessToken
func extractSAEmailFromURL(url string) string {
	const marker = "serviceAccounts/"
	idx := strings.Index(url, marker)
	if idx < 0 {
		return ""
	}
	rest := url[idx+len(marker):]
	// Email ends at ':' (for :generateAccessToken) or end of string
	if colonIdx := strings.Index(rest, ":"); colonIdx >= 0 {
		return rest[:colonIdx]
	}
	return rest
}
