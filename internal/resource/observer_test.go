package resource_test

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	obsresource "terraform-provider-manifestit/internal/resource"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func observerSchema(t *testing.T) schema.Schema {
	t.Helper()
	r := obsresource.NewObserver()
	schResp := &resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, schResp)
	return schResp.Schema
}

func schemaType() tftypes.Type {
	return tftypes.Object{
		AttributeTypes: map[string]tftypes.Type{
			"id": tftypes.String,
		},
	}
}

func emptyPlan(t *testing.T) tfsdk.Plan {
	t.Helper()
	schm := observerSchema(t)
	val := tftypes.NewValue(schemaType(), map[string]tftypes.Value{
		"id": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	})
	return tfsdk.Plan{Schema: schm, Raw: val}
}

func stateWithID(t *testing.T, id string) tfsdk.State {
	t.Helper()
	schm := observerSchema(t)
	val := tftypes.NewValue(schemaType(), map[string]tftypes.Value{
		"id": tftypes.NewValue(tftypes.String, id),
	})
	return tfsdk.State{Schema: schm, Raw: val}
}

// ---------------------------------------------------------------------------
// TestObserver_Metadata
// ---------------------------------------------------------------------------

func TestObserver_Metadata(t *testing.T) {
	obs := obsresource.NewObserver()
	resp := &resource.MetadataResponse{}
	obs.Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "manifestit"}, resp)
	if resp.TypeName != "manifestit_observer" {
		t.Errorf("TypeName = %q, want %q", resp.TypeName, "manifestit_observer")
	}
}

// ---------------------------------------------------------------------------
// TestObserver_Schema_HasOnlyID
// ---------------------------------------------------------------------------

// TestObserver_Schema_HasOnlyID verifies the resource has exactly one attribute
// (id) and no other attributes. The resource is intentionally a no-op; all
// event logic lives in the provider layer (PPID watcher goroutine).
func TestObserver_Schema_HasOnlyID(t *testing.T) {
	schResp := &resource.SchemaResponse{}
	obsresource.NewObserver().Schema(context.Background(), resource.SchemaRequest{}, schResp)

	if _, ok := schResp.Schema.Attributes["id"]; !ok {
		t.Error("expected 'id' attribute in schema")
	}
	if len(schResp.Schema.Attributes) != 1 {
		t.Errorf("expected exactly 1 attribute, got %d: %v",
			len(schResp.Schema.Attributes), schResp.Schema.Attributes)
	}
}

// ---------------------------------------------------------------------------
// TestObserver_Create_setsID
// ---------------------------------------------------------------------------

func TestObserver_Create_setsID(t *testing.T) {
	obs := obsresource.NewObserver()
	schm := observerSchema(t)

	req := resource.CreateRequest{Plan: emptyPlan(t)}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: schm}}

	obs.Create(context.Background(), req, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Create returned errors: %v", resp.Diagnostics)
	}

	var model struct {
		ID types.String `tfsdk:"id"`
	}
	if err := resp.State.Get(context.Background(), &model); err != nil {
		t.Fatalf("Get state: %v", err)
	}
	if model.ID.ValueString() != "manifestit-observer" {
		t.Errorf("id = %q, want %q", model.ID.ValueString(), "manifestit-observer")
	}
}

// ---------------------------------------------------------------------------
// TestObserver_Read_preservesState
// ---------------------------------------------------------------------------

func TestObserver_Read_preservesState(t *testing.T) {
	obs := obsresource.NewObserver()
	schm := observerSchema(t)

	req := resource.ReadRequest{State: stateWithID(t, "manifestit-observer")}
	resp := &resource.ReadResponse{State: tfsdk.State{Schema: schm}}

	obs.Read(context.Background(), req, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Read returned errors: %v", resp.Diagnostics)
	}

	var model struct {
		ID types.String `tfsdk:"id"`
	}
	if err := resp.State.Get(context.Background(), &model); err != nil {
		t.Fatalf("Get state: %v", err)
	}
	if model.ID.ValueString() != "manifestit-observer" {
		t.Errorf("Read changed id to %q, want %q", model.ID.ValueString(), "manifestit-observer")
	}
}

// ---------------------------------------------------------------------------
// TestObserver_Update_preservesState
// ---------------------------------------------------------------------------

func TestObserver_Update_preservesState(t *testing.T) {
	obs := obsresource.NewObserver()
	schm := observerSchema(t)

	req := resource.UpdateRequest{
		State: stateWithID(t, "manifestit-observer"),
		Plan:  emptyPlan(t),
		Config: tfsdk.Config{
			Schema: schm,
			Raw: tftypes.NewValue(schemaType(), map[string]tftypes.Value{
				"id": tftypes.NewValue(tftypes.String, "manifestit-observer"),
			}),
		},
	}
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: schm}}

	obs.Update(context.Background(), req, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Update returned errors: %v", resp.Diagnostics)
	}
}

// ---------------------------------------------------------------------------
// TestObserver_Delete_isNoop
// ---------------------------------------------------------------------------

func TestObserver_Delete_isNoop(t *testing.T) {
	obs := obsresource.NewObserver()
	schm := observerSchema(t)

	req := resource.DeleteRequest{State: stateWithID(t, "manifestit-observer")}
	resp := &resource.DeleteResponse{State: tfsdk.State{Schema: schm}}

	// Must not panic and must produce no diagnostics errors.
	obs.Delete(context.Background(), req, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Delete returned errors: %v", resp.Diagnostics)
	}
}

// ---------------------------------------------------------------------------
// TestObserver_NoDependsOnRequired (documentation test)
// ---------------------------------------------------------------------------

// TestObserver_NoDependsOnRequired verifies there is no last_run_id or
// triggers attribute that would have required the user to configure depends_on.
// The close event is fired by the PPID watcher goroutine, not the resource.
func TestObserver_NoDependsOnRequired(t *testing.T) {
	schResp := &resource.SchemaResponse{}
	obsresource.NewObserver().Schema(context.Background(), resource.SchemaRequest{}, schResp)

	for _, unwanted := range []string{"last_run_id", "triggers"} {
		if _, ok := schResp.Schema.Attributes[unwanted]; ok {
			t.Errorf("attribute %q found — this forces users to add depends_on, which is not allowed", unwanted)
		}
	}
}
