package collectors

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// azureJWTClaims holds the relevant claims from an Azure AD JWT access token.
type azureJWTClaims struct {
	TenantID    string `json:"tid"`
	ObjectID    string `json:"oid"`
	AppID       string `json:"appid"`
	UPN         string `json:"upn"`
	DisplayName string `json:"name"`
	Subject     string `json:"sub"`
}

// azureAccountInfo holds fields from `az account show --output json`.
type azureAccountInfo struct {
	ID       string `json:"id"`       // subscription ID
	TenantID string `json:"tenantId"`
	Name     string `json:"name"`     // subscription name
	User     struct {
		Name string `json:"name"`
		Type string `json:"type"` // "user", "servicePrincipal"
	} `json:"user"`
}

// collectAzure detects Azure credentials and extracts identity from JWT token claims, env vars, or az CLI.
func (c *Collector) collectAzure(ctx context.Context) *CloudIdentity {
	authType := c.detectAzureAuthType(ctx)
	if authType == "" {
		return nil
	}

	azureID := &AzureIdentity{
		AuthType: authType,
		TenantID: c.Env.Getenv("ARM_TENANT_ID"),
		ClientID: c.Env.Getenv("ARM_CLIENT_ID"),
	}

	if sub := c.Env.Getenv("ARM_SUBSCRIPTION_ID"); sub != "" {
		azureID.SubscriptionID = sub
	}

	// Try to decode JWT access token for richer identity info
	if token := c.Env.Getenv("ARM_ACCESS_TOKEN"); token != "" {
		if claims, err := decodeJWTPayload(token); err == nil {
			c.enrichAzureFromClaims(azureID, claims)
		} else {
			tflog.Debug(ctx, "failed to decode Azure JWT", map[string]interface{}{"error": err.Error()})
		}
	}

	// For workload identity, the federated token may also be available
	if azureID.ObjectID == "" {
		if token := c.Env.Getenv("AZURE_FEDERATED_TOKEN"); token != "" {
			if claims, err := decodeJWTPayload(token); err == nil {
				c.enrichAzureFromClaims(azureID, claims)
			}
		}
	}

	// For CLI auth, enrich from `az account show` if we still lack details
	if authType == "user-cli" && azureID.SubscriptionID == "" {
		c.enrichAzureFromCLI(ctx, azureID)
	}

	principalID := azureID.ObjectID
	if principalID == "" {
		principalID = azureID.ClientID
	}
	if principalID == "" {
		principalID = azureID.UPN
	}

	return &CloudIdentity{
		Provider:    "azure",
		AccountID:   azureID.SubscriptionID,
		PrincipalID: principalID,
		AuthType:    azureID.AuthType,
		DisplayName: azureID.DisplayName,
		Azure:       azureID,
	}
}

// detectAzureAuthType determines the Azure authentication method from env vars or az CLI session.
func (c *Collector) detectAzureAuthType(ctx context.Context) string {
	switch {
	case c.Env.Getenv("ARM_CLIENT_ID") != "" && c.Env.Getenv("ARM_CLIENT_SECRET") != "":
		return "service-principal"
	case c.Env.Getenv("ARM_USE_MSI") == "true":
		return "managed-identity"
	case c.Env.Getenv("AZURE_FEDERATED_TOKEN_FILE") != "":
		return "workload-identity"
	case c.Env.Getenv("ARM_USE_CLI") == "true" || c.Env.Getenv("AZURE_CLI_AUTH") != "":
		return "user-cli"
	case c.Env.Getenv("ARM_TENANT_ID") != "":
		return "user-cli"
	case c.Env.Getenv("ARM_SUBSCRIPTION_ID") != "":
		return "user-cli"
	case c.Env.Getenv("ARM_ACCESS_TOKEN") != "":
		return "user-cli"
	}

	// Last resort: check if az CLI has an active session.
	// The azurerm provider defaults to CLI auth, so this is the common local dev path.
	if c.Cmd != nil {
		if out, err := c.Cmd.Run(ctx, "", "az", "account", "show", "--output", "json"); err == nil && out != "" {
			return "user-cli"
		}
	}

	return ""
}

// enrichAzureFromCLI extracts identity info from `az account show`.
func (c *Collector) enrichAzureFromCLI(ctx context.Context, id *AzureIdentity) {
	if c.Cmd == nil {
		return
	}

	out, err := c.Cmd.Run(ctx, "", "az", "account", "show", "--output", "json")
	if err != nil {
		tflog.Debug(ctx, "az account show failed", map[string]interface{}{"error": err.Error()})
		return
	}

	var acct azureAccountInfo
	if err := json.Unmarshal([]byte(out), &acct); err != nil {
		tflog.Debug(ctx, "failed to parse az account show", map[string]interface{}{"error": err.Error()})
		return
	}

	if acct.ID != "" && id.SubscriptionID == "" {
		id.SubscriptionID = acct.ID
	}
	if acct.TenantID != "" && id.TenantID == "" {
		id.TenantID = acct.TenantID
	}
	if acct.User.Name != "" {
		if id.UPN == "" {
			id.UPN = acct.User.Name
		}
		if id.DisplayName == "" {
			id.DisplayName = acct.User.Name
		}
	}
}

func (c *Collector) enrichAzureFromClaims(id *AzureIdentity, claims *azureJWTClaims) {
	if claims.TenantID != "" {
		id.TenantID = claims.TenantID
	}
	if claims.ObjectID != "" {
		id.ObjectID = claims.ObjectID
	}
	if claims.AppID != "" && id.ClientID == "" {
		id.ClientID = claims.AppID
	}
	if claims.UPN != "" {
		id.UPN = claims.UPN
		// If UPN is present, this is user authentication
		if id.AuthType == "" {
			id.AuthType = "user-cli"
		}
	}
	if claims.DisplayName != "" {
		id.DisplayName = claims.DisplayName
	}
}

// decodeJWTPayload decodes the payload (second segment) of a JWT without verifying the signature.
// This is safe because we only extract identity claims for observability, not for authz decisions.
func decodeJWTPayload(token string) (*azureJWTClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, &jwtError{"invalid JWT format: expected 3 parts"}
	}

	payload := parts[1]
	// JWT uses base64url encoding without padding
	if m := len(payload) % 4; m != 0 {
		payload += strings.Repeat("=", 4-m)
	}

	decoded, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		return nil, &jwtError{"failed to decode JWT payload: " + err.Error()}
	}

	var claims azureJWTClaims
	if err := json.Unmarshal(decoded, &claims); err != nil {
		return nil, &jwtError{"failed to parse JWT claims: " + err.Error()}
	}

	return &claims, nil
}

type jwtError struct {
	msg string
}

func (e *jwtError) Error() string { return e.msg }
