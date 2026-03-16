package resource

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource = &Observer{}
)

// Observer is a no-op Terraform resource that exists solely to trigger
// provider Configure. The actual collection and posting logic lives in
// the provider's Configure method.
type Observer struct{}

type ObserverModel struct {
	ID types.String `tfsdk:"id"`
}

func NewObserver() resource.Resource {
	return &Observer{}
}

func (r *Observer) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_observer"
}

func (r *Observer) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Activates ManifestIT observer. The provider automatically collects and posts identity, git, and cloud context on every Terraform operation.",
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

func (r *Observer) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
}
