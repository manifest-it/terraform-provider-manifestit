package provider

import (
	"context"
	"net/http"
	"os"
	"strconv"
	resource2 "terraform-provider-manifestit/internal/resource"
	"terraform-provider-manifestit/pkg/sdk/providers"
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
	"github.com/rs/zerolog"
)

var _ provider.Provider = &Provider{}

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
	OrgId                     types.String `tfsdk:"org_id"`
	HttpClientRetryMaxRetries types.Int64  `tfsdk:"http_client_retry_max_retries"`
	HttpClientRetryEnabled    types.String `tfsdk:"http_client_retry_enabled"`
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
}

func (p *Provider) ConfigureConfigDefaults(ctx context.Context, config *ProviderSchema) diag.Diagnostics {
	var diags diag.Diagnostics

	if config.ApiKey.IsNull() {
		apiKey, err := utils.GetMultiEnvVar(utils.MITAPIKeyEnvName)
		if err == nil {
			config.ApiKey = types.StringValue(apiKey)
		}
	}

	if config.ApiUrl.IsNull() {
		apiUrl, err := utils.GetMultiEnvVar(utils.MITAPIUrlEnvName)
		if err == nil {
			config.ApiUrl = types.StringValue(apiUrl)
		}
	}

	if config.OrgId.IsNull() {
		orgUUID, err := utils.GetMultiEnvVar(utils.MITOrgIDEnvName)
		if err == nil {
			config.OrgId = types.StringValue(orgUUID)
		}
	}

	if config.HttpClientRetryMaxRetries.IsNull() {
		rTimeout, err := utils.GetMultiEnvVar(utils.MITHTTPRetryMaxRetries)
		if err == nil {
			v, _ := strconv.Atoi(rTimeout)
			config.HttpClientRetryMaxRetries = types.Int64Value(int64(v))
		}
	}

	if config.HttpClientRetryEnabled.IsNull() {
		config.HttpClientRetryEnabled = types.StringValue("true")
	}
	if config.Validate.IsNull() {
		config.Validate = types.StringValue("true")
	}
	// Run validations on the provider config after defaults and values from
	// env var has been set.
	diags.Append(p.ValidateConfigValues(ctx, config)...)

	return diags
}

func (p *Provider) ValidateConfigValues(ctx context.Context, config *ProviderSchema) diag.Diagnostics {
	var diags diag.Diagnostics
	// Init validators we need for purposes of config validation only
	oneOfStringValidator := stringvalidator.OneOf("true", "false")
	int64BetweenValidator := int64validator.Between(1, 5)

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
				Description: "(Required unless validate is false) ManifestIT API key. This can also be set via the MIT_API_KEY environment variable.",
			},
			"api_url": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "The API URL. This can also be set via the MIT_HOST environment variable, and defaults to `https://api.manifestit.tech`",
			},
			"validate": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "Enables validation of the provided API key during provider initialization. Valid values are [`true`, `false`]. Default is true. When false, api_key won't be checked.",
			},
			"org_id": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "The organization ID",
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

func defaultConfigureFunc(p *Provider, request *provider.ConfigureRequest, config *ProviderSchema) diag.Diagnostics {
	var diags diag.Diagnostics

	debug := os.Getenv("DEBUG") == "true"
	logger := zerolog.New(os.Stderr).With().Timestamp().Logger()

	maxRetries := 3
	if !config.HttpClientRetryMaxRetries.IsNull() {
		maxRetries = int(config.HttpClientRetryMaxRetries.ValueInt64())
	}

	client, err := providers.NewProviderClient(providers.Config{
		APIKey:     config.ApiKey.ValueString(),
		BaseURL:    config.ApiUrl.ValueString(),
		OrgID:      config.OrgId.ValueString(),
		HTTPClient: p.HttpClient,
		Debug:      debug,
		Logger:     logger,
		MaxRetries: maxRetries,
	})
	if err != nil {
		diags.AddError("Failed to configure ManifestIT client", err.Error())
		return diags
	}

	p.Client = client
	return diags
}
