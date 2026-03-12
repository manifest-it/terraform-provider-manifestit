package provider

import (
	"context"
	"errors"

	"terraform-provider-manifestit/pkg/sdk/providers/changelog"

	sdkErrors "terraform-provider-manifestit/pkg/sdk/errors"
	"terraform-provider-manifestit/pkg/sdk/providers"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func NewChangelog() resource.Resource {
	return &Changelog{}
}

type Changelog struct {
	client changelog.Client
}

type changelogModel struct {
	ID   types.String `tfsdk:"id"`
	Name types.String `tfsdk:"name"`
}

func (c *Changelog) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*providers.ProviderClient)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			"Expected *providers.ProviderClient, got unexpected type",
		)
		return
	}

	c.client = client.Changelog
}

func (c *Changelog) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan changelogModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	result, err := c.client.Create(ctx, changelog.CreateInput{
		Name: plan.Name.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.AddError("Failed to create changelog", err.Error())
		return
	}

	plan.ID = types.StringValue(result.ID)
	if result.Name != "" {
		plan.Name = types.StringValue(result.Name)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (c *Changelog) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state changelogModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	result, err := c.client.Read(ctx, state.ID.ValueString())
	if err != nil {
		if errors.Is(err, sdkErrors.ErrNotFound) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read changelog", err.Error())
		return
	}

	state.ID = types.StringValue(result.ID)
	if result.Name != "" {
		state.Name = types.StringValue(result.Name)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (c *Changelog) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan changelogModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var state changelogModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	result, err := c.client.Update(ctx, state.ID.ValueString(), changelog.UpdateInput{
		Name: plan.Name.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.AddError("Failed to update changelog", err.Error())
		return
	}

	state.ID = types.StringValue(result.ID)
	if result.Name != "" {
		state.Name = types.StringValue(result.Name)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (c *Changelog) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state changelogModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := c.client.Delete(ctx, state.ID.ValueString())
	if err != nil {
		if errors.Is(err, sdkErrors.ErrNotFound) {
			return
		}
		resp.Diagnostics.AddError("Failed to delete changelog", err.Error())
		return
	}

	resp.State.RemoveResource(ctx)
}

func (c *Changelog) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_changelog"
}

func (c *Changelog) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "Resource identifier returned by the API.",
			},
			"name": schema.StringAttribute{
				Optional:    true,
				Description: "Changelog name.",
			},
		},
	}
}

