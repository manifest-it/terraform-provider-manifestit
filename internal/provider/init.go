package provider

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"terraform-provider-manifestit/internal/collectors"
	resource2 "terraform-provider-manifestit/internal/resource"
	"terraform-provider-manifestit/pkg/sdk/providers"
	"terraform-provider-manifestit/pkg/sdk/providers/observer"
	"terraform-provider-manifestit/pkg/utils"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	"github.com/rs/zerolog"
)

var (
	_ provider.Provider = &Provider{}
)

type Provider struct {
	HttpClient *http.Client
	Client     *providers.ProviderClient

	ConfigureCallbackFunc func(p *Provider, request *provider.ConfigureRequest, config *ProviderSchema) diag.Diagnostics
	Now                   func() time.Time
	DefaultTags           map[string]string
}

type ProviderSchema struct {
	ApiKey                    types.String `tfsdk:"api_key"`
	ApiUrl                    types.String `tfsdk:"api_url"`
	Validate                  types.String `tfsdk:"validate"`
	OrgId                     types.Int32  `tfsdk:"org_id"`
	HttpClientRetryMaxRetries types.Int64  `tfsdk:"http_client_retry_max_retries"`
	HttpClientRetryEnabled    types.String `tfsdk:"http_client_retry_enabled"`
	TrackedBranch             types.String `tfsdk:"tracked_branch"`
	TrackedRepo               types.String `tfsdk:"tracked_repo"`
	ProviderConfigurationId   types.Int32  `tfsdk:"provider_configuration_id"`
	OrgKey                    types.String `tfsdk:"org_key"`
	ProviderId                types.Int32  `tfsdk:"provider_id"`
}

func New() provider.Provider {
	return &Provider{
		ConfigureCallbackFunc: defaultConfigureFunc,
	}
}

func (p *Provider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		resource2.NewObserver,
	}
}

func (p *Provider) Configure(ctx context.Context, request provider.ConfigureRequest, response *provider.ConfigureResponse) {
	var config ProviderSchema
	response.Diagnostics.Append(request.Config.Get(ctx, &config)...)
	if response.Diagnostics.HasError() {
		return
	}

	diags := p.ConfigureConfigDefaults(ctx, &config)
	if diags.HasError() {
		response.Diagnostics.Append(diags...)
	}

	response.Diagnostics.Append(p.ConfigureCallbackFunc(p, &request, &config)...)
	if response.Diagnostics.HasError() {
		return
	}

	// Pass the SDK client (not the provider) to avoid import cycles
	response.DataSourceData = p.Client
	response.ResourceData = p.Client

	// Collect and post observer data on every provider configuration.
	// This fires on every terraform operation (plan, apply, destroy).
	// Runs synchronously to ensure it completes before Terraform terminates the provider.
	response.Diagnostics.Append(p.postObserverData(ctx, &config)...)
}

func (p *Provider) ConfigureConfigDefaults(ctx context.Context, config *ProviderSchema) diag.Diagnostics {
	var diags diag.Diagnostics

	// api_key: optional in schema, falls back to env, error if neither set
	if config.ApiKey.IsNull() {
		apiKey, err := utils.GetMultiEnvVar(utils.MITAPIKey)
		if err != nil {
			diags.AddError("Missing API Key", "api_key must be set in the provider block or via the MIT_API_KEY environment variable.")
			return diags
		}
		config.ApiKey = types.StringValue(apiKey)
	}

	// Retry settings: optional with env var fallbacks and defaults.
	if config.HttpClientRetryMaxRetries.IsNull() {
		rTimeout, err := utils.GetMultiEnvVar(utils.MITHTTPRetryMaxRetries)
		if err == nil {
			v, _ := strconv.Atoi(rTimeout)
			config.HttpClientRetryMaxRetries = types.Int64Value(int64(v))
		}
	}

	if config.HttpClientRetryEnabled.IsNull() {
		rEnabled, err := utils.GetMultiEnvVar(utils.MITHTTPRetryEnabled)
		if err == nil {
			config.HttpClientRetryEnabled = types.StringValue(rEnabled)
		} else {
			config.HttpClientRetryEnabled = types.StringValue("true")
		}
	}

	diags.Append(p.ValidateConfigValues(ctx, config)...)

	return diags
}

func (p *Provider) ValidateConfigValues(ctx context.Context, config *ProviderSchema) diag.Diagnostics {
	var diags diag.Diagnostics
	oneOfStringValidator := stringvalidator.OneOf("true", "false")
	int64BetweenValidator := int64validator.Between(1, 5)

	// Validate boolean-style string fields
	if !config.Validate.IsNull() {
		res := validator.StringResponse{}
		oneOfStringValidator.ValidateString(ctx, validator.StringRequest{ConfigValue: config.Validate}, &res)
		diags.Append(res.Diagnostics...)
	}

	if !config.HttpClientRetryEnabled.IsNull() {
		res := validator.StringResponse{}
		oneOfStringValidator.ValidateString(ctx, validator.StringRequest{ConfigValue: config.HttpClientRetryEnabled}, &res)
		diags.Append(res.Diagnostics...)
	}

	if !config.HttpClientRetryMaxRetries.IsNull() {
		res := validator.Int64Response{}
		int64BetweenValidator.ValidateInt64(ctx, validator.Int64Request{ConfigValue: config.HttpClientRetryMaxRetries}, &res)
		diags.Append(res.Diagnostics...)
	}

	return diags
}

func (p *Provider) DataSources(_ context.Context) []func() datasource.DataSource {
	return nil
}

func (p *Provider) Metadata(_ context.Context, _ provider.MetadataRequest, response *provider.MetadataResponse) {
	response.TypeName = "manifestit"
}

func (p *Provider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"api_key": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "ManifestIT API key. Can also be set via the MIT_API_KEY environment variable.",
			},
			"api_url": schema.StringAttribute{
				Required:    true,
				Description: "The ManifestIT API endpoint URL.",
			},
			"validate": schema.StringAttribute{
				Required:    true,
				Description: "Enables validation of the provided API key during provider initialization. Valid values are [`true`, `false`].",
			},
			"org_id": schema.Int32Attribute{
				Required:    true,
				Description: "The organization ID.",
			},
			"tracked_branch": schema.StringAttribute{
				Required:    true,
				Description: "The primary branch to track for compliance (e.g., 'main'). The provider checks whether the current code has been merged to this branch.",
			},
			"tracked_repo": schema.StringAttribute{
				Required:    true,
				Description: "The Git repository URL to track (e.g., 'https://github.com/org/infra'). Used to identify which repository this Terraform workspace belongs to.",
			},
			"provider_configuration_id": schema.Int32Attribute{
				Required:    true,
				Description: "The provider configuration ID.",
			},
			"org_key": schema.StringAttribute{
				Required:    true,
				Sensitive:   true,
				Description: "The organization key.",
			},
			"provider_id": schema.Int32Attribute{
				Required:    true,
				Description: "The provider ID.",
			},
			"http_client_retry_enabled": schema.StringAttribute{
				Optional:    true,
				Description: "Enables request retries on HTTP status codes 429 and 5xx. Valid values are [`true`, `false`]. Defaults to `true`.",
			},
			"http_client_retry_max_retries": schema.Int64Attribute{
				Optional:    true,
				Description: "The HTTP request maximum retry number. Defaults to 3.",
			},
		},
	}
}

// detectTerraformOperation inspects the parent process command line to determine
// the current Terraform operation (plan, apply, destroy, etc.).
// The provider runs as a child process of terraform, so the parent's command
// line reveals which subcommand was invoked.
func detectTerraformOperation() string {
	// Debug mode: provider runs standalone via go run / binary + TF_REATTACH_PROVIDERS.
	// Parent is shell/IDE, so default to "apply" to ensure posting.
	if os.Getenv("TF_REATTACH_PROVIDERS") != "" {
		return "apply"
	}

	ppid := os.Getppid()
	out, err := exec.Command("ps", "-o", "args=", "-p", strconv.Itoa(ppid)).Output()
	if err != nil {
		// ps failed, can't determine operation
		return "unknown"
	}

	cmdLine := strings.TrimSpace(string(out))

	// Check for known terraform subcommands in order of specificity.
	// "apply" must be checked before "plan" since "terraform apply" also
	// runs a plan phase internally.
	for _, op := range []string{"apply", "destroy", "import", "refresh", "plan"} {
		if strings.Contains(cmdLine, op) {
			return op
		}
	}
	return "unknown"
}

// alreadyPostedForParent checks if we've already posted for the current parent
// terraform process. Uses a temp file keyed on the parent PID to deduplicate
// across separate provider process invocations within the same terraform command.
func alreadyPostedForParent() bool {
	ppid := os.Getppid()
	lockFile := fmt.Sprintf("/tmp/manifestit-observer-%d.lock", ppid)

	// Check if lock file exists and is recent (within last 30 seconds)
	if info, err := os.Stat(lockFile); err == nil {
		if time.Since(info.ModTime()) < 30*time.Second {
			return true
		}
	}

	// Create/update the lock file
	_ = os.WriteFile(lockFile, []byte(strconv.Itoa(ppid)), 0644)
	return false
}

// postObserverData collects local identity, git context, and cloud identity,
// then posts them to the ManifestIT API. Skips posting during plan-only operations.
func (p *Provider) postObserverData(ctx context.Context, config *ProviderSchema) diag.Diagnostics {
	var diags diag.Diagnostics
	operation := detectTerraformOperation()

	// Skip posting for plan and refresh operations.
	// Plan is read-only; refresh only syncs drift and increments serial,
	// which would confuse backend serial tracking.
	if operation == "plan" || operation == "refresh" {
		tflog.Debug(ctx, "skipping observer post", map[string]interface{}{"operation": operation})
		return diags
	}

	// Terraform spawns separate provider processes for plan and apply phases.
	// Deduplicate by checking a lock file keyed on the parent terraform PID.
	if alreadyPostedForParent() {
		return diags
	}

	c := collectors.NewCollector(collectors.DefaultCollectConfig())
	if !config.TrackedBranch.IsNull() {
		c.TrackedBranch = config.TrackedBranch.ValueString()
	}
	if !config.TrackedRepo.IsNull() {
		c.TrackedRepo = config.TrackedRepo.ValueString()
	}
	result := c.Collect(ctx)

	orgID := ""
	if !config.OrgId.IsNull() {
		orgID = strconv.FormatInt(int64(config.OrgId.ValueInt32()), 10)
	}

	_, err := p.Client.Observer.Post(ctx, observer.ObserverPayload{
		Identity:    result.Identity,
		Git:         result.Git,
		Cloud:       result.Cloud,
		State:       result.State,
		CollectedAt: result.CollectedAt.UTC().Format(time.RFC3339),
		Action:      "operation",
		ResourceID:  "",
		OrgID:       orgID,
	})
	if err != nil {
		diags.AddWarning("ManifestIT observer post failed", err.Error())
	}
	return diags
}

func defaultConfigureFunc(p *Provider, request *provider.ConfigureRequest, config *ProviderSchema) diag.Diagnostics {
	var diags diag.Diagnostics

	debug := os.Getenv("DEBUG") == "true"
	logger := zerolog.New(os.Stderr).With().Timestamp().Logger()

	maxRetries := 3
	if !config.HttpClientRetryMaxRetries.IsNull() {
		maxRetries = int(config.HttpClientRetryMaxRetries.ValueInt64())
	}

	providerConfigID := ""
	if !config.ProviderConfigurationId.IsNull() {
		providerConfigID = strconv.FormatInt(int64(config.ProviderConfigurationId.ValueInt32()), 10)
	}

	providerID := ""
	if !config.ProviderId.IsNull() {
		providerID = strconv.FormatInt(int64(config.ProviderId.ValueInt32()), 10)
	}

	client, err := providers.NewProviderClient(providers.Config{
		APIKey:                  config.ApiKey.ValueString(),
		BaseURL:                 config.ApiUrl.ValueString(),
		OrgID:                   strconv.FormatInt(int64(config.OrgId.ValueInt32()), 10),
		OrgKey:                  config.OrgKey.ValueString(),
		ProviderID:              providerID,
		ProviderConfigurationID: providerConfigID,
		HTTPClient:              p.HttpClient,
		Debug:                   debug,
		Logger:                  logger,
		MaxRetries:              maxRetries,
	})
	if err != nil {
		diags.AddError("Failed to configure ManifestIT client", err.Error())
		return diags
	}

	p.Client = client
	return diags
}
