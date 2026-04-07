package provider

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"terraform-provider-manifestit/internal/collectors"
	resource2 "terraform-provider-manifestit/internal/resource"
	"terraform-provider-manifestit/pkg/sdk/providers"
	"terraform-provider-manifestit/pkg/sdk/providers/observer"
	"terraform-provider-manifestit/pkg/utils"

	"github.com/google/uuid"
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

var _ provider.Provider = &Provider{}

type Provider struct {
	HttpClient            *http.Client
	Client                *providers.ProviderClient
	ConfigureCallbackFunc func(p *Provider, req *provider.ConfigureRequest, config *Schema) diag.Diagnostics
	Now                   func() time.Time
	DefaultTags           map[string]string
}

type Schema struct {
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
	return &Provider{ConfigureCallbackFunc: defaultConfigureFunc}
}

func (p *Provider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{resource2.NewObserver}
}

func (p *Provider) DataSources(_ context.Context) []func() datasource.DataSource { return nil }

func (p *Provider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "manifestit"
}

func (p *Provider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"api_key":                       schema.StringAttribute{Optional: true, Sensitive: true, Description: "ManifestIT API key. Can also be set via MIT_API_KEY."},
			"api_url":                       schema.StringAttribute{Required: true, Description: "ManifestIT API endpoint URL."},
			"validate":                      schema.StringAttribute{Required: true, Description: "Validate API key on init. Valid values: true, false."},
			"org_id":                        schema.Int32Attribute{Required: true, Description: "Organization ID."},
			"tracked_branch":                schema.StringAttribute{Required: true, Description: "Branch to track for compliance (e.g. 'main')."},
			"tracked_repo":                  schema.StringAttribute{Required: true, Description: "Git repository URL to track."},
			"provider_configuration_id":     schema.Int32Attribute{Required: true, Description: "Provider configuration ID."},
			"org_key":                       schema.StringAttribute{Required: true, Sensitive: true, Description: "Organization key."},
			"provider_id":                   schema.Int32Attribute{Required: true, Description: "Provider ID."},
			"http_client_retry_enabled":     schema.StringAttribute{Optional: true, Description: "Enable HTTP retries on 429/5xx. Valid values: true, false. Defaults to true."},
			"http_client_retry_max_retries": schema.Int64Attribute{Optional: true, Description: "Max HTTP retry count (1–5). Defaults to 3."},
		},
	}
}

func (p *Provider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config Schema
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if diags := p.applyConfigDefaults(&config); diags.HasError() {
		resp.Diagnostics.Append(diags...)
	}

	resp.Diagnostics.Append(p.ConfigureCallbackFunc(p, &req, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.DataSourceData = p.Client
	resp.ResourceData = p.Client
	resp.Diagnostics.Append(p.startLifecycle(ctx, &config)...)
}

func (p *Provider) applyConfigDefaults(config *Schema) diag.Diagnostics {
	var diags diag.Diagnostics

	if config.ApiKey.IsNull() {
		apiKey, err := utils.GetMultiEnvVar(utils.MITAPIKey)
		if err != nil {
			diags.AddError("Missing API Key", "api_key must be set in the provider block or via MIT_API_KEY.")
			return diags
		}
		config.ApiKey = types.StringValue(apiKey)
	}

	if config.HttpClientRetryMaxRetries.IsNull() {
		if v, err := utils.GetMultiEnvVar(utils.MITHTTPRetryMaxRetries); err == nil {
			if n, err := strconv.Atoi(v); err == nil {
				config.HttpClientRetryMaxRetries = types.Int64Value(int64(n))
			}
		}
	}

	if config.HttpClientRetryEnabled.IsNull() {
		if v, err := utils.GetMultiEnvVar(utils.MITHTTPRetryEnabled); err == nil {
			config.HttpClientRetryEnabled = types.StringValue(v)
		} else {
			config.HttpClientRetryEnabled = types.StringValue("true")
		}
	}

	diags.Append(p.validateConfig(config)...)
	return diags
}

func (p *Provider) validateConfig(config *Schema) diag.Diagnostics {
	var diags diag.Diagnostics
	strValidator := stringvalidator.OneOf("true", "false")
	intValidator := int64validator.Between(1, 5)

	if !config.Validate.IsNull() {
		res := validator.StringResponse{}
		strValidator.ValidateString(context.Background(), validator.StringRequest{ConfigValue: config.Validate}, &res)
		diags.Append(res.Diagnostics...)
	}
	if !config.HttpClientRetryEnabled.IsNull() {
		res := validator.StringResponse{}
		strValidator.ValidateString(context.Background(), validator.StringRequest{ConfigValue: config.HttpClientRetryEnabled}, &res)
		diags.Append(res.Diagnostics...)
	}
	if !config.HttpClientRetryMaxRetries.IsNull() {
		res := validator.Int64Response{}
		intValidator.ValidateInt64(context.Background(), validator.Int64Request{ConfigValue: config.HttpClientRetryMaxRetries}, &res)
		diags.Append(res.Diagnostics...)
	}
	return diags
}

func defaultConfigureFunc(p *Provider, _ *provider.ConfigureRequest, config *Schema) diag.Diagnostics {
	var diags diag.Diagnostics

	maxRetries := 3
	if !config.HttpClientRetryMaxRetries.IsNull() {
		maxRetries = int(config.HttpClientRetryMaxRetries.ValueInt64())
	}

	client, err := providers.NewProviderClient(providers.Config{
		APIKey:                  config.ApiKey.ValueString(),
		BaseURL:                 config.ApiUrl.ValueString(),
		OrgID:                   strconv.FormatInt(int64(config.OrgId.ValueInt32()), 10),
		OrgKey:                  config.OrgKey.ValueString(),
		ProviderID:              int32ToString(config.ProviderId),
		ProviderConfigurationID: int32ToString(config.ProviderConfigurationId),
		HTTPClient:              p.HttpClient,
		Debug:                   os.Getenv("DEBUG") == "true",
		Logger:                  zerolog.New(os.Stderr).With().Timestamp().Logger(),
		MaxRetries:              maxRetries,
	})
	if err != nil {
		diags.AddError("Failed to configure ManifestIT client", err.Error())
		return diags
	}

	p.Client = client
	return diags
}

// buildProviderClient creates a minimal observer client for the watcher subprocess.
func buildProviderClient(apiKey, baseURL, orgID, orgKey string) (observer.Client, error) {
	client, err := providers.NewProviderClient(providers.Config{
		APIKey:     apiKey,
		BaseURL:    baseURL,
		OrgID:      orgID,
		OrgKey:     orgKey,
		Debug:      os.Getenv("DEBUG") == "true",
		Logger:     zerolog.New(os.Stderr).With().Timestamp().Logger(),
		MaxRetries: 3,
	})
	if err != nil {
		return nil, err
	}
	return client.Observer, nil
}

// startLifecycle is called from Configure(). Fires POST /open, spawns the
// watcher subprocess, and registers the SIGTERM handler. Guards via
// providerRunOnce so it runs at most once per process lifetime.
func (p *Provider) startLifecycle(ctx context.Context, config *Schema) diag.Diagnostics {
	var diags diag.Diagnostics

	operation := detectTerraformOperation()
	providerLog("operation=%s pid=%d ppid=%d", operation, os.Getpid(), os.Getppid())

	if operation != "apply" && operation != "destroy" {
		tflog.Debug(ctx, "manifestit: skipping lifecycle", map[string]interface{}{"operation": operation})
		return diags
	}

	cleanStaleFiles()

	providerRunOnce.Do(func() {
		diags = p.runLifecycle(ctx, config, operation)
	})
	return diags
}

func (p *Provider) runLifecycle(ctx context.Context, config *Schema, operation string) diag.Diagnostics {
	var diags diag.Diagnostics

	runID, lockPath, alreadyPosted := acquireRunLock()
	if alreadyPosted {
		providerLog("lock held — skipping (another instance owns this run)")
		return diags
	}
	providerLog("lifecycle start run_id=%s pid=%d ppid=%d", runID, os.Getpid(), os.Getppid())

	orgID := strconv.FormatInt(int64(config.OrgId.ValueInt32()), 10)

	c := collectors.NewCollector(collectors.DefaultCollectConfig())
	c.TrackedBranch = config.TrackedBranch.ValueString()
	c.TrackedRepo = config.TrackedRepo.ValueString()
	collectCtx, collectCancel := context.WithTimeout(ctx, 8*time.Second)
	result := c.Collect(collectCtx)
	collectCancel()

	_, postErr := p.Client.Observer.Post(ctx, observer.ObserverPayload{
		RunID:       runID,
		Status:      "open",
		CollectedAt: time.Now().UTC().Format(time.RFC3339),
		Action:      operation,
		OrgID:       orgID,
	})
	if postErr != nil {
		providerLog("POST /open FAILED: %v", postErr)
		os.Remove(lockPath)
		diags.AddWarning("ManifestIT open event failed", postErr.Error())
		return diags
	}
	providerLog("POST /open OK run_id=%s", runID)

	state := runState{
		RunID:    runID,
		Action:   operation,
		APIKey:   config.ApiKey.ValueString(),
		BaseURL:  config.ApiUrl.ValueString(),
		OrgID:    orgID,
		OrgKey:   config.OrgKey.ValueString(),
		LockPath: lockPath,
		PPID:     os.Getppid(),
		Identity: result.Identity,
		Git:      result.Git,
	}

	_, cancel := context.WithCancel(context.Background())
	registerSIGTERMHandler(cancel, p.Client.Observer, runID, state)
	providerLog("SIGTERM handler registered")

	statePath, err := writeRunState(state)
	if err != nil {
		providerLog("watcher state write FAILED: %v", err)
		diags.AddWarning("ManifestIT watcher state write failed", err.Error())
		return diags
	}
	if err := spawnWatcher(statePath); err != nil {
		providerLog("watcher spawn FAILED: %v", err)
		os.Remove(statePath)
		diags.AddWarning("ManifestIT watcher spawn failed", err.Error())
		return diags
	}
	providerLog("watcher spawned ppid=%d", os.Getppid())
	return diags
}

func int32ToString(v types.Int32) string {
	if v.IsNull() {
		return ""
	}
	return strconv.FormatInt(int64(v.ValueInt32()), 10)
}

func detectTerraformOperation() string {
	if os.Getenv("TF_REATTACH_PROVIDERS") != "" {
		return "apply"
	}
	ppid := os.Getppid()
	cmdLine, err := getParentCommandLine(ppid)
	if err != nil {
		return "unknown"
	}
	for _, op := range []string{"apply", "destroy", "import", "refresh", "plan"} {
		if strings.Contains(cmdLine, op) {
			return op
		}
	}
	return "unknown"
}

func getParentCommandLine(pid int) (string, error) {
	return getParentCommandLinePlatform(pid)
}

func observerLockPath() string {
	dir := stateDir()
	_ = os.MkdirAll(dir, 0700)
	return filepath.Join(dir, fmt.Sprintf("observer-%d.lock", os.Getppid()))
}

// acquireRunLock atomically creates a per-PPID lock file using os.Link.
// Returns alreadyPosted=true if another plugin instance for the same terraform
// run already owns the lock.
func acquireRunLock() (runID, lockPath string, alreadyPosted bool) {
	lockPath = observerLockPath()
	runID = generateRunID()

	ppid := os.Getppid()
	content := fmt.Sprintf("%d:%s", ppid, runID)
	dir := filepath.Dir(lockPath)

	tmp, err := os.CreateTemp(dir, ".lock-tmp-")
	if err != nil {
		return "", lockPath, true
	}
	tmpPath := tmp.Name()
	fmt.Fprint(tmp, content)
	tmp.Close()
	defer os.Remove(tmpPath)

	if err := os.Link(tmpPath, lockPath); err == nil {
		return runID, lockPath, false
	}

	data, err := os.ReadFile(lockPath)
	if err != nil {
		return "", lockPath, true
	}
	parts := strings.SplitN(strings.TrimSpace(string(data)), ":", 2)
	if len(parts) < 1 {
		return "", lockPath, true
	}
	ownerPPID, err := strconv.Atoi(parts[0])
	if err != nil {
		return "", lockPath, true
	}
	if processExists(ownerPPID) {
		return "", lockPath, true
	}

	// Stale lock from a dead terraform run — reclaim.
	os.Remove(lockPath)
	if err := os.Link(tmpPath, lockPath); err != nil {
		return "", lockPath, true
	}
	check, err := os.ReadFile(lockPath)
	if err != nil || strings.TrimSpace(string(check)) != content {
		return "", lockPath, true
	}
	return runID, lockPath, false
}

func generateRunID() string {
	return uuid.New().String()
}

func processExists(pid int) bool {
	return processExistsPlatform(pid)
}
