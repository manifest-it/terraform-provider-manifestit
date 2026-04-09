package resource

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ resource.Resource = &Observer{}

// Observer is a no-op Terraform resource whose sole purpose is to trigger
// provider Configure(). All event logic (open/close/heartbeat) lives in the
// provider layer, not here.
//
// The close event fires when the terraform binary (PPID) exits — which happens
// only after ALL providers have finished applying all resources. This requires
// no user action: no depends_on, no triggers, no configuration.
type Observer struct{}

type ObserverModel struct {
	ID types.String `tfsdk:"id"`
}

func NewObserver() resource.Resource {
	return &Observer{}
}

// NewObserverForTest is a test helper that returns an *Observer directly.
// Only used in observer_test.go for unit tests.
func NewObserverForTest(_ func()) *Observer {
	return &Observer{}
}

func (r *Observer) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_observer"
}

func (r *Observer) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Activates ManifestIT observer. Place this resource anywhere in your configuration — no depends_on required. The provider automatically fires the open event at the start of the apply and the closed event when terraform finishes all resources.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "Static identifier for the observer resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *Observer) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var state ObserverModel
	state.ID = types.StringValue("manifestit-observer")
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
	var state ObserverModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *Observer) Delete(_ context.Context, _ resource.DeleteRequest, _ *resource.DeleteResponse) {}
