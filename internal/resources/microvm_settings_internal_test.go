package resources

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

// TestMicrovmSettingsDefaultNetworkToAPI_Null proves an unset/null
// default_network converts to a nil body (C4/C8.6: clearing the setting).
func TestMicrovmSettingsDefaultNetworkToAPI_Null(t *testing.T) {
	body, diags := microvmSettingsDefaultNetworkToAPI(context.Background(), types.ObjectNull(microvmSettingsDefaultNetworkTypes()))
	if diags.HasError() {
		t.Fatalf("microvmSettingsDefaultNetworkToAPI: %v", diags)
	}
	if body != nil {
		t.Fatalf("body = %#v; want nil", body)
	}
}

// TestMicrovmSettingsDefaultNetworkToAPI_Public proves a public default
// omits subnet_id when unset (C8.1: the platform auto-assigns one) but
// still sends it when the caller names one explicitly.
func TestMicrovmSettingsDefaultNetworkToAPI_Public(t *testing.T) {
	obj, diags := types.ObjectValue(microvmSettingsDefaultNetworkTypes(), map[string]attr.Value{
		"kind":          types.StringValue("public"),
		"subnet_id":     types.StringNull(),
		"vpc_subnet_id": types.StringNull(),
	})
	if diags.HasError() {
		t.Fatalf("building object: %v", diags)
	}
	body, diags := microvmSettingsDefaultNetworkToAPI(context.Background(), obj)
	if diags.HasError() {
		t.Fatalf("microvmSettingsDefaultNetworkToAPI: %v", diags)
	}
	if body["kind"] != "public" {
		t.Fatalf("body[kind] = %v; want public", body["kind"])
	}
	if _, exists := body["subnet_id"]; exists {
		t.Error("body must omit subnet_id when unset (C8.1 auto-assign)")
	}
}

// TestMicrovmSettingsDefaultNetworkToAPI_Vpc proves a vpc default sends its
// vpc_subnet_id.
func TestMicrovmSettingsDefaultNetworkToAPI_Vpc(t *testing.T) {
	obj, diags := types.ObjectValue(microvmSettingsDefaultNetworkTypes(), map[string]attr.Value{
		"kind":          types.StringValue("vpc"),
		"subnet_id":     types.StringNull(),
		"vpc_subnet_id": types.StringValue("vpc-subnet-1"),
	})
	if diags.HasError() {
		t.Fatalf("building object: %v", diags)
	}
	body, diags := microvmSettingsDefaultNetworkToAPI(context.Background(), obj)
	if diags.HasError() {
		t.Fatalf("microvmSettingsDefaultNetworkToAPI: %v", diags)
	}
	if body["kind"] != "vpc" || body["vpc_subnet_id"] != "vpc-subnet-1" {
		t.Fatalf("body = %#v", body)
	}
}

// TestMicrovmSettingsStateFromAPI_Null proves a nil/absent default_network
// key in the envelope reads back as a null object, not an error or a
// zero-valued object.
func TestMicrovmSettingsStateFromAPI_Null(t *testing.T) {
	state, diags := microvmSettingsStateFromAPI(map[string]any{"default_network": nil})
	if diags.HasError() {
		t.Fatalf("microvmSettingsStateFromAPI: %v", diags)
	}
	if state.ID.ValueString() != microvmSettingsSingletonID {
		t.Fatalf("id = %v; want %v", state.ID, microvmSettingsSingletonID)
	}
	if !state.DefaultNetwork.IsNull() {
		t.Fatalf("default_network = %#v; want null", state.DefaultNetwork)
	}
}

// TestMicrovmSettingsStateFromAPI_Public proves a stored public default
// (with no subnet_id, C8.1) round-trips into state with a null subnet_id -
// never an empty string, which would show as a spurious diff.
func TestMicrovmSettingsStateFromAPI_Public(t *testing.T) {
	state, diags := microvmSettingsStateFromAPI(map[string]any{
		"default_network": map[string]any{"kind": "public"},
	})
	if diags.HasError() {
		t.Fatalf("microvmSettingsStateFromAPI: %v", diags)
	}
	var model microvmSettingsDefaultNetworkModel
	if diags := state.DefaultNetwork.As(context.Background(), &model, basetypes.ObjectAsOptions{}); diags.HasError() {
		t.Fatalf("decoding default_network: %v", diags)
	}
	if model.Kind.ValueString() != "public" {
		t.Fatalf("kind = %v; want public", model.Kind)
	}
	if !model.SubnetID.IsNull() {
		t.Fatalf("subnet_id = %v; want null", model.SubnetID)
	}
}

func TestMicrovmSettingsSchemaRejectsUnknownKind(t *testing.T) {
	res := NewMicrovmSettingsResource()
	var resp resource.SchemaResponse
	res.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	network, ok := resp.Schema.Attributes["default_network"]
	if !ok {
		t.Fatal("schema missing default_network")
	}
	if network.IsRequired() {
		t.Fatal("default_network must be Optional (omit/null clears it)")
	}
}
