package resources

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	resourceschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestMicrovmImageCreateBodyKeepsSecretsOutOfSource(t *testing.T) {
	plan := microvmImageModel{
		Name:             types.StringValue("base-tools"),
		SourceKind:       types.StringValue("oci"),
		SourceImage:      types.StringValue("registry.example.test/base-tools:1"),
		RegistryUsername: types.StringValue("builder"),
		RegistryPassword: types.StringValue("fixture-password"),
		LocationID:       types.StringValue("group-1"),
		Env: types.MapValueMust(types.StringType, map[string]attr.Value{
			"MODE": types.StringValue("test"),
		}),
		LifecycleHooks: types.StringValue(`{"port":8080}`),
	}

	body, err := microvmImageCreateBody(context.Background(), plan)
	if err != nil {
		t.Fatalf("microvmImageCreateBody: %v", err)
	}
	source := body["source"].(map[string]any)
	if source["image"] != "registry.example.test/base-tools:1" {
		t.Fatalf("source = %#v", source)
	}
	if _, exists := source["password"]; exists {
		t.Fatal("registry password leaked into source")
	}
	auth := body["auth"].(map[string]any)
	if auth["password"] != "fixture-password" || body["env"] != `{"MODE":"test"}` {
		t.Fatalf("body auth/env = %#v/%#v", auth, body["env"])
	}
	if body["location_id"] != "group-1" {
		t.Fatalf("body[location_id] = %v; want group-1", body["location_id"])
	}
	if _, exists := body["hypervisor_group_id"]; exists {
		t.Error("create body must never send the deprecated hypervisor_group_id key")
	}
}

// TestMicrovmImageCreateBodyLegacyAliasSendsCanonicalLocationID mirrors the
// microvm resource's LOC-3 coverage: a plan built from only the deprecated
// hypervisor_group_id alias must still send the canonical location_id key.
func TestMicrovmImageCreateBodyLegacyAliasSendsCanonicalLocationID(t *testing.T) {
	plan := microvmImageModel{
		Name:              types.StringValue("base-tools"),
		SourceKind:        types.StringValue("oci"),
		SourceImage:       types.StringValue("registry.example.test/base-tools:1"),
		HypervisorGroupID: types.StringValue("group-legacy"),
	}
	body, err := microvmImageCreateBody(context.Background(), plan)
	if err != nil {
		t.Fatalf("microvmImageCreateBody: %v", err)
	}
	if body["location_id"] != "group-legacy" {
		t.Fatalf("body[location_id] = %v; want group-legacy", body["location_id"])
	}
}

// TestMicrovmImageMissingRequiredBase pins the C6/UX plan-time mirror of
// CreateImageRequest's base_image_id `required_if:source_kind,dockerfile`
// rule: a dockerfile build without a base_image_id fails fast at plan time,
// every other source_kind is unaffected, and an unknown value (not yet
// computed) never falsely triggers.
func TestMicrovmImageMissingRequiredBase(t *testing.T) {
	cases := []struct {
		name    string
		model   microvmImageModel
		missing bool
	}{
		{"dockerfile without base_image_id", microvmImageModel{SourceKind: types.StringValue("dockerfile"), BaseImageID: types.StringNull()}, true},
		{"dockerfile with empty base_image_id", microvmImageModel{SourceKind: types.StringValue("dockerfile"), BaseImageID: types.StringValue("")}, true},
		{"dockerfile with base_image_id", microvmImageModel{SourceKind: types.StringValue("dockerfile"), BaseImageID: types.StringValue("base-1")}, false},
		{"oci without base_image_id", microvmImageModel{SourceKind: types.StringValue("oci"), BaseImageID: types.StringNull()}, false},
		{"git without base_image_id", microvmImageModel{SourceKind: types.StringValue("git"), BaseImageID: types.StringNull()}, false},
		{"dockerfile with unknown base_image_id", microvmImageModel{SourceKind: types.StringValue("dockerfile"), BaseImageID: types.StringUnknown()}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := microvmImageMissingRequiredBase(tc.model); got != tc.missing {
				t.Errorf("microvmImageMissingRequiredBase() = %v; want %v", got, tc.missing)
			}
		})
	}
}

// TestMicrovmImageValidateConfigRequiresBaseForDockerfile exercises the
// resource method end to end (not just the extracted helper) so a future
// edit that stops calling the helper, or stops wiring
// ResourceWithValidateConfig, is caught.
func TestMicrovmImageValidateConfigRequiresBaseForDockerfile(t *testing.T) {
	res := &microvmImageResource{}
	schemaType := microvmImageSchemaType(t)

	missingModel := blankMicrovmImageModel()
	missingModel.SourceKind = types.StringValue("dockerfile")
	missingModel.BaseImageID = types.StringNull()
	missingConfig := tfsdkConfigFromModel(t, schemaType, missingModel)
	respMissing := &resource.ValidateConfigResponse{}
	res.ValidateConfig(context.Background(), resource.ValidateConfigRequest{Config: missingConfig}, respMissing)
	if !respMissing.Diagnostics.HasError() {
		t.Fatal("dockerfile without base_image_id must error")
	}

	presentModel := blankMicrovmImageModel()
	presentModel.SourceKind = types.StringValue("dockerfile")
	presentModel.BaseImageID = types.StringValue("base-1")
	presentConfig := tfsdkConfigFromModel(t, schemaType, presentModel)
	respPresent := &resource.ValidateConfigResponse{}
	res.ValidateConfig(context.Background(), resource.ValidateConfigRequest{Config: presentConfig}, respPresent)
	if respPresent.Diagnostics.HasError() {
		t.Fatalf("dockerfile with base_image_id must not error: %v", respPresent.Diagnostics)
	}
}

func microvmImageSchemaType(t *testing.T) resourceschema.Schema {
	t.Helper()
	res := NewMicrovmImageResource()
	var schemaResp resource.SchemaResponse
	res.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)
	return schemaResp.Schema
}

// blankMicrovmImageModel returns a microvmImageModel with every collection-
// typed attribute (env, features - types.Map has no usable zero value; a
// bare struct literal leaving them unset panics ValueFrom's reflection with
// a "MISSING TYPE" conversion error) set to a properly-typed null, so a test
// can override only the fields it cares about.
func blankMicrovmImageModel() microvmImageModel {
	return microvmImageModel{
		Env:      types.MapNull(types.StringType),
		Features: types.MapNull(types.BoolType),
	}
}

// tfsdkConfigFromModel builds a tfsdk.Config from a partial model via the
// same reflection ValueFrom uses to convert plan/state structs, so
// ValidateConfig's req.Config.Get sees a fully-typed object matching the
// real schema.
func tfsdkConfigFromModel(t *testing.T, s resourceschema.Schema, model microvmImageModel) tfsdk.Config {
	t.Helper()
	ctx := context.Background()
	var obj types.Object
	if diags := tfsdk.ValueFrom(ctx, model, s.Type(), &obj); diags.HasError() {
		t.Fatalf("building config value: %v", diags)
	}
	raw, err := obj.ToTerraformValue(ctx)
	if err != nil {
		t.Fatalf("converting config value: %v", err)
	}
	return tfsdk.Config{Raw: raw, Schema: s}
}

// TestMicrovmImageStateFromAPIReadsOsMetadataAndFeatures pins the C5 fields:
// os_id/os_family/os_name/os_version/os_codename/arch and the features map.
func TestMicrovmImageStateFromAPIReadsOsMetadataAndFeatures(t *testing.T) {
	obj := map[string]any{
		"id":          "img-1",
		"name":        "debian-13",
		"source_kind": "base",
		"os_id":       "debian-13",
		"os_family":   "debian",
		"os_name":     "Debian",
		"os_version":  "13",
		"os_codename": "trixie",
		"arch":        "x86_64",
		"features":    map[string]any{"sshd": true, "envd": false, "vcagent": true},
	}
	state := microvmImageStateFromAPI(obj, microvmImageModel{})
	if state.OsID.ValueString() != "debian-13" || state.OsFamily.ValueString() != "debian" ||
		state.OsName.ValueString() != "Debian" || state.OsVersion.ValueString() != "13" ||
		state.OsCodename.ValueString() != "trixie" || state.Arch.ValueString() != "x86_64" {
		t.Fatalf("os metadata = %#v/%#v/%#v/%#v/%#v/%#v", state.OsID, state.OsFamily, state.OsName, state.OsVersion, state.OsCodename, state.Arch)
	}
	var features map[string]bool
	if diags := state.Features.ElementsAs(context.Background(), &features, false); diags.HasError() {
		t.Fatalf("decoding features: %v", diags)
	}
	if !features["sshd"] || features["envd"] || !features["vcagent"] {
		t.Fatalf("features = %#v", features)
	}
}

// TestMicrovmImageEnvelopePartsUnwrapsShowEnvelope pins the Read/Update
// bugfix: GetMicrovmImage returns the bare SHOW envelope
// ({image,versions,microvms_count}), so the resource must unwrap "image"
// before handing it to microvmImageStateFromAPI - passing the raw envelope
// silently no-ops every field (falls back to prior) because "id"/"name"/...
// live one level down.
func TestMicrovmImageEnvelopePartsUnwrapsShowEnvelope(t *testing.T) {
	envelope := map[string]any{
		"success":        true,
		"image":          map[string]any{"id": "img-1", "name": "debian-13"},
		"versions":       []any{map[string]any{"id": "v-1", "version": 1}},
		"microvms_count": float64(2),
	}
	obj := microvmImageEnvelopeParts(envelope)
	if obj["id"] != "img-1" || obj["name"] != "debian-13" {
		t.Fatalf("microvmImageEnvelopeParts unwrap = %#v", obj)
	}

	state := microvmImageStateFromAPI(obj, microvmImageModel{})
	if state.ID.ValueString() != "img-1" || state.Name.ValueString() != "debian-13" {
		t.Fatalf("state after unwrap = %#v/%#v", state.ID, state.Name)
	}
}

func TestMicrovmImageSchemaMarksCredentialsSensitive(t *testing.T) {
	res := NewMicrovmImageResource()
	var resp resource.SchemaResponse
	res.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	for _, name := range []string{"registry_password", "env"} {
		attribute := resp.Schema.Attributes[name]
		if !attribute.IsSensitive() {
			t.Errorf("%s must be sensitive", name)
		}
	}
}
