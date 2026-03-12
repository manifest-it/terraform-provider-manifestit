package resource

import (
	"context"
	"fmt"
	"time"

	"terraform-provider-manifestit/internal/collectors"
	"terraform-provider-manifestit/pkg/sdk/providers"
	"terraform-provider-manifestit/pkg/sdk/providers/observer"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ resource.Resource                = &Observer{}
	_ resource.ResourceWithConfigure   = &Observer{}
	_ resource.ResourceWithModifyPlan  = &Observer{}
)

// Observer is a Terraform resource that collects local identity, git context,
// and cloud identity on every Terraform apply and posts them to the ManifestIT API.
// It uses a computed "last_run" field that always shows as changed during planning,
// ensuring Update is called on every apply even when no other resources change.
type Observer struct {
	client observer.Client
	orgID  string
}

// ObserverModel is the Terraform schema model for the observer resource.
type ObserverModel struct {
	ID      types.String `tfsdk:"id"`
	LastRun types.String `tfsdk:"last_run"`
}

func NewObserver() resource.Resource {
	return &Observer{}
}

func (r *Observer) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_observer"
}

func (r *Observer) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Observes and reports Terraform run context (identity, git, cloud) to ManifestIT on every apply.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "The observer record ID returned by the API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"last_run": schema.StringAttribute{
				Computed:    true,
				Description: "Timestamp of the last observer run. Changes on every apply to ensure the observer always fires.",
			},
		},
	}
}

// ModifyPlan forces Terraform to always see a diff on last_run so Update runs on every apply.
func (r *Observer) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	// If the resource is being destroyed, nothing to do.
	if req.Plan.Raw.IsNull() {
		return
	}
	// If this is a create (no prior state), nothing to do — Create will handle it.
	if req.State.Raw.IsNull() {
		return
	}

	// Set last_run to unknown so Terraform always sees a diff → always calls Update.
	resp.Plan.SetAttribute(ctx, path.Root("last_run"), types.StringUnknown())
}

func (r *Observer) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*providers.ProviderClient)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *providers.ProviderClient, got %T", req.ProviderData),
		)
		return
	}

	r.client = client.Observer
	r.orgID = client.OrgID
}

func (r *Observer) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var state ObserverModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	result := r.collect(ctx)
	now := time.Now().UTC().Format(time.RFC3339)

	apiResp, err := r.client.Post(ctx, observer.ObserverPayload{
		Identity:    result.Identity,
		Git:         result.Git,
		Cloud:       result.Cloud,
		CollectedAt: result.CollectedAt.UTC().Format(time.RFC3339),
		Action:      "apply",
		ResourceID:  "",
		OrgID:       r.orgID,
	})
	if err != nil {
		resp.Diagnostics.AddError("Failed to post observer data", err.Error())
		return
	}

	state.ID = types.StringValue(apiResp.ID)
	state.LastRun = types.StringValue(now)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *Observer) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ObserverModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *Observer) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var prior ObserverModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}

	result := r.collect(ctx)
	now := time.Now().UTC().Format(time.RFC3339)

	apiResp, err := r.client.Post(ctx, observer.ObserverPayload{
		Identity:    result.Identity,
		Git:         result.Git,
		Cloud:       result.Cloud,
		CollectedAt: result.CollectedAt.UTC().Format(time.RFC3339),
		Action:      "apply",
		ResourceID:  prior.ID.ValueString(),
		OrgID:       r.orgID,
	})
	if err != nil {
		resp.Diagnostics.AddError("Failed to post observer data", err.Error())
		return
	}

	var state ObserverModel
	state.ID = types.StringValue(apiResp.ID)
	state.LastRun = types.StringValue(now)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *Observer) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state ObserverModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	result := r.collect(ctx)

	_, err := r.client.Post(ctx, observer.ObserverPayload{
		Identity:    result.Identity,
		Git:         result.Git,
		Cloud:       result.Cloud,
		CollectedAt: result.CollectedAt.UTC().Format(time.RFC3339),
		Action:      "destroy",
		ResourceID:  state.ID.ValueString(),
		OrgID:       r.orgID,
	})
	if err != nil {
		tflog.Warn(ctx, "failed to post observer destroy event", map[string]interface{}{"error": err.Error()})
	}
}

// collect runs all collectors and returns the combined result.
func (r *Observer) collect(ctx context.Context) *collectors.CollectionResult {
	c := collectors.NewCollector(collectors.DefaultCollectConfig())
	return c.Collect(ctx)
}
