package provider

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"terraform-provider-manifestit/internal/collectors"
	resource2 "terraform-provider-manifestit/internal/resource"
	"terraform-provider-manifestit/pkg/sdk/providers"
	"terraform-provider-manifestit/pkg/sdk/providers/observer"
	"terraform-provider-manifestit/pkg/utils"
	"time"

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

// heartbeatCancel holds the cancel func for the running heartbeat goroutine.
// Set once inside providerRunOnce; read by RunCleanup.
var heartbeatCancel context.CancelFunc

var (
	_ provider.Provider = &Provider{}
)

type Provider struct {
	HttpClient *http.Client
	Client     *providers.ProviderClient

	ConfigureCallbackFunc func(p *Provider, request *provider.ConfigureRequest, config *Schema) diag.Diagnostics
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
	var config Schema
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

	response.DataSourceData = p.Client
	response.ResourceData = p.Client
	response.Diagnostics.Append(p.postObserverData(ctx, &config)...)
}

func (p *Provider) ConfigureConfigDefaults(ctx context.Context, config *Schema) diag.Diagnostics {
	var diags diag.Diagnostics

	if config.ApiKey.IsNull() {
		apiKey, err := utils.GetMultiEnvVar(utils.MITAPIKey)
		if err != nil {
			diags.AddError("Missing API Key", "api_key must be set in the provider block or via the MIT_API_KEY environment variable.")
			return diags
		}
		config.ApiKey = types.StringValue(apiKey)
	}

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

func (p *Provider) ValidateConfigValues(ctx context.Context, config *Schema) diag.Diagnostics {
	var diags diag.Diagnostics
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

func defaultConfigureFunc(p *Provider, request *provider.ConfigureRequest, config *Schema) diag.Diagnostics {
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

// buildProviderClient creates a minimal ProviderClient with an observer.Client.
// Used by the watcher subprocess (MIT_WATCHER_MODE=1) via buildObserverFromState.
func buildProviderClient(apiKey, baseURL, orgID, orgKey string) (observer.Client, error) {
	debug := os.Getenv("DEBUG") == "true"
	logger := zerolog.New(os.Stderr).With().Timestamp().Logger()
	client, err := providers.NewProviderClient(providers.Config{
		APIKey:     apiKey,
		BaseURL:    baseURL,
		OrgID:      orgID,
		OrgKey:     orgKey,
		Debug:      debug,
		Logger:     logger,
		MaxRetries: 3,
	})
	if err != nil {
		return nil, err
	}
	return client.Observer, nil
}

func (p *Provider) postObserverData(ctx context.Context, config *Schema) diag.Diagnostics {
	var diags diag.Diagnostics
	operation := detectTerraformOperation()
	providerLog("detected operation=%s pid=%d ppid=%d", operation, os.Getpid(), os.Getppid())

	if operation != "apply" && operation != "destroy" {
		providerLog("skipping lifecycle — operation is not apply/destroy")
		tflog.Debug(ctx, "skipping observer post", map[string]interface{}{"operation": operation})
		return diags
	}

	// providerRunOnce ensures the full open/heartbeat/close lifecycle starts
	// at most once per process, even when Configure() is called multiple times
	// (e.g. aliased provider blocks). The second call is a complete no-op.
	providerRunOnce.Do(func() {
		diags = p.startObserverLifecycle(ctx, config, operation)
	})

	return diags
}

// startObserverLifecycle acquires the lock, POSTs /open, starts the heartbeat,
// registers the SIGTERM handler, and registers the RunCleanup close path.
// It is called at most once per process via providerRunOnce.Do.
func (p *Provider) startObserverLifecycle(ctx context.Context, config *Schema, operation string) diag.Diagnostics {
	var diags diag.Diagnostics

	runID, lockPath, alreadyPosted := acquireRunLock()
	if alreadyPosted {
		providerLog("lock already held — skipping lifecycle (another instance owns this run)")
		return diags
	}
	providerLog("lifecycle start  operation=%s run_id=%s pid=%d ppid=%d",
		operation, runID, os.Getpid(), os.Getppid())

	orgID := ""
	if !config.OrgId.IsNull() {
		orgID = strconv.FormatInt(int64(config.OrgId.ValueInt32()), 10)
	}
	trackedBranch := ""
	if !config.TrackedBranch.IsNull() {
		trackedBranch = config.TrackedBranch.ValueString()
	}
	trackedRepo := ""
	if !config.TrackedRepo.IsNull() {
		trackedRepo = config.TrackedRepo.ValueString()
	}
	providerConfigID := ""
	if !config.ProviderConfigurationId.IsNull() {
		providerConfigID = strconv.FormatInt(int64(config.ProviderConfigurationId.ValueInt32()), 10)
	}
	providerID := ""
	if !config.ProviderId.IsNull() {
		providerID = strconv.FormatInt(int64(config.ProviderId.ValueInt32()), 10)
	}

	c := collectors.NewCollector(collectors.DefaultCollectConfig())
	c.TrackedBranch = trackedBranch
	c.TrackedRepo = trackedRepo
	collectCtx, collectCancel := context.WithTimeout(ctx, 8*time.Second)
	result := c.Collect(collectCtx)
	collectCancel()
	providerLog("identity/git collected  identity_type=%s git_available=%v",
		result.Identity.Type, result.Git.Available)

	// POST /open
	providerLog("POST /open  run_id=%s base_url=%s", runID, config.ApiUrl.ValueString())
	_, postErr := p.Client.Observer.Post(ctx, observer.ObserverPayload{
		RunID:       runID,
		Status:      "open",
		CollectedAt: time.Now().UTC().Format(time.RFC3339),
		Action:      operation,
		OrgID:       orgID,
	})
	if postErr != nil {
		providerLog("POST /open FAILED: %v — removing lock", postErr)
		_ = os.Remove(lockPath)
		diags.AddWarning("ManifestIT observer open event failed", postErr.Error())
		return diags
	}
	providerLog("POST /open OK")

	state := ClosureState{
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
	_ = providerConfigID
	_ = providerID

	obs := p.Client.Observer
	hbCtx, cancel := context.WithCancel(context.Background())
	heartbeatCancel = cancel

	startHeartbeat(hbCtx, obs, runID)
	providerLog("heartbeat goroutine started  interval=%s", HeartbeatInterval)

	registerSIGTERMHandler(hbCtx, cancel, obs, runID, state)
	providerLog("SIGTERM handler registered")

	// Spawn watcher subprocess — PRIMARY close path
	statePath, writeErr := writeClosureState(state)
	if writeErr != nil {
		providerLog("watcher state write FAILED: %v", writeErr)
		tflog.Warn(ctx, "ManifestIT: failed to write watcher state — closed event may not fire",
			map[string]interface{}{"error": writeErr.Error()})
		diags.AddWarning("ManifestIT watcher state write failed", writeErr.Error())
	} else if spawnErr := spawnWatcher(statePath); spawnErr != nil {
		providerLog("watcher spawn FAILED: %v", spawnErr)
		os.Remove(statePath)
		tflog.Warn(ctx, "ManifestIT: failed to spawn watcher — closed event may not fire",
			map[string]interface{}{"error": spawnErr.Error()})
		diags.AddWarning("ManifestIT watcher spawn failed", spawnErr.Error())
	} else {
		providerLog("watcher subprocess spawned  state=%s ppid=%d", statePath, os.Getppid())
	}

	RegisterCleanup(func() { cancel() })
	providerLog("lifecycle setup complete — watcher will fire PATCH /closed when PPID exits")

	tflog.Debug(ctx, "ManifestIT observer lifecycle started",
		map[string]interface{}{"run_id": runID, "operation": operation})

	return diags
}

// generateRunID generates a random UUID v4 using github.com/google/uuid.
func generateRunID() string {
	return uuid.New().String()
}

// detectTerraformOperation reads the parent process command line to determine
// the terraform subcommand (apply, destroy, plan, etc.).
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
	ppid := os.Getppid()
	return filepath.Join(os.TempDir(), fmt.Sprintf("manifestit-observer-%d.lock", ppid))
}

// acquireRunLock atomically creates the per-PPID lock file and returns:
//   - runID       – a newly generated UUID v4 for this terraform run
//   - lockPath    – the path of the lock file written
//   - alreadyPosted – true if another Configure() call for the same terraform
//     invocation already acquired the lock (idempotency guard)
//
// Atomicity: uses os.Link (hard link) for the creation step. link(2) is atomic
// on POSIX and fails with EEXIST when the target already exists, so exactly one
// concurrent caller wins per lock-creation attempt.
//
// Stale locks (owner PID no longer alive) are reclaimed via remove + re-link,
// with a read-back verify to detect the rare case where two callers both removed
// the stale lock and raced to re-link.
func acquireRunLock() (runID string, lockPath string, alreadyPosted bool) {
	lockPath = observerLockPath()
	runID = generateRunID()

	// Use PPID (terraform binary) as the owner identifier — NOT the plugin's
	// own PID. Terraform spawns the provider binary multiple times per apply
	// (GetSchema, ValidateConfig, PlanResourceChange, ApplyResourceChange),
	// each as a short-lived process that exits in <100ms. If we stored the
	// plugin's own PID, each process death makes the lock look stale and the
	// next instance reclaims it → duplicate open events.
	//
	// Storing PPID: as long as terraform is alive, processExists(PPID)=true →
	// all subsequent plugin instances see a live owner → alreadyPosted=true →
	// exactly one open event per terraform run.
	//
	// Note: the lock path itself encodes PPID (manifestit-observer-{ppid}.lock),
	// so locks from different terraform runs naturally use different paths —
	// there is NO cross-run conflict and NO stale reclaim is ever needed.
	// Any existing lock at our path was created by THIS terraform run (same PPID).
	ppid := os.Getppid()
	ppidS := strconv.Itoa(ppid)
	content := ppidS + ":" + runID
	dir := filepath.Dir(lockPath)

	// Write candidate content to a uniquely-named temp file, then hard-link
	// it to the lock path. link(2) is atomic on POSIX: exactly one caller
	// wins when multiple race on the same target path.
	tmp, tmpErr := os.CreateTemp(dir, ".lock-tmp-")
	if tmpErr != nil {
		return "", lockPath, true
	}
	tmpPath := tmp.Name()
	_, _ = tmp.WriteString(content)
	_ = tmp.Close()
	defer os.Remove(tmpPath)

	if linkErr := os.Link(tmpPath, lockPath); linkErr == nil {
		// Atomically created the lock — we are the owner.
		return runID, lockPath, false
	}

	// Lock already exists at this PPID-keyed path.
	// Since the path encodes PPID, this lock was created by another plugin
	// instance from the same terraform run. Read it to check liveness.
	data, readErr := os.ReadFile(lockPath)
	if readErr != nil {
		// Lock disappeared (cleaned up between our failed Link and now).
		// Be conservative: treat as already posted to avoid a duplicate open.
		return "", lockPath, true
	}
	parts := strings.SplitN(strings.TrimSpace(string(data)), ":", 2)
	if len(parts) < 1 {
		return "", lockPath, true
	}
	ownerPPID, parseErr := strconv.Atoi(parts[0])
	if parseErr != nil {
		return "", lockPath, true
	}
	if processExists(ownerPPID) {
		// Terraform is alive → same run → already posted.
		return "", lockPath, true
	}

	// Owner PPID is dead. This means a PREVIOUS terraform run left a stale
	// lock (shouldn't happen since the path encodes PPID, but guard anyway —
	// e.g. if PPID was reused by the OS). Clean it up and claim ownership.
	//
	// Since we are the only instance that can reach here (same PPID path,
	// same terraform run — terraform is dead so no concurrent instances),
	// just remove and re-link. The final verify catches any remaining race.
	_ = os.Remove(lockPath)
	if linkErr := os.Link(tmpPath, lockPath); linkErr != nil {
		return "", lockPath, true
	}
	check, checkErr := os.ReadFile(lockPath)
	if checkErr != nil || strings.TrimSpace(string(check)) != content {
		return "", lockPath, true
	}

	return runID, lockPath, false
}

func processExists(pid int) bool {
	return processExistsPlatform(pid)
}
