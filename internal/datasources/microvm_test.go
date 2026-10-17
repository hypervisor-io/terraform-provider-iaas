package datasources

import (
	"encoding/json"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestMicrovmImagesToListFiltersWithoutLeakingSource(t *testing.T) {
	images := []map[string]any{
		{"id": "image-1", "name": "Base Tools", "description": nil, "source_kind": "oci", "status": "ready", "template_name": "u-1", "source": map[string]any{"image": "private.example.test/tools"}},
		{"id": "image-2", "name": "Build Tools", "source_kind": "git", "status": "building", "template_name": "u-2"},
	}
	list, diags := microvmImagesToList(images, microvmImagesModel{Search: types.StringValue("base"), Status: types.StringValue("ready")})
	if diags.HasError() {
		t.Fatalf("microvmImagesToList: %v", diags)
	}
	if len(list.Elements()) != 1 {
		t.Fatalf("images = %v; want one", list)
	}
	object := list.Elements()[0].(types.Object)
	if object.Attributes()["id"].(types.String).ValueString() != "image-1" {
		t.Fatalf("image = %v", object)
	}
	if _, exists := object.Attributes()["source"]; exists {
		t.Fatal("image data source exposed source payload")
	}
}

func TestMicrovmCatalogStateMapsNestedCatalog(t *testing.T) {
	catalog := map[string]any{
		"locations": []any{map[string]any{
			"id": "location-1", "name": "Amsterdam", "country": "NL", "available": true, "locked": false,
			"plans": []any{map[string]any{"id": "plan-1", "name": "Small", "vcpu": 2, "mem_mib": 1024, "disk_gib": 10, "price_vcpu_second": "0.001", "price_mib_second": 0.0001, "price_disk_gib_hour": 0.01, "price_runner_minute": 0}},
		}},
		"images":          []any{map[string]any{"id": "image-1", "name": "base", "is_base": true, "status": "ready", "exposed_ports": []any{8080.0}}},
		"security_groups": []any{map[string]any{"id": "sg-1", "name": "web"}},
		"vpc_subnets":     []any{map[string]any{"id": "subnet-1", "name": "private", "cidr": "10.0.0.0/24", "vpc": "prod"}},
		"limits":          map[string]any{"max_lifetime_seconds": 28800, "default_lifetime_seconds": 3600, "hook_timeout_max": 60, "idle_timeout_min": 30, "max_microvms": 5},
	}
	state, diags := microvmCatalogState(catalog)
	if diags.HasError() {
		t.Fatalf("microvmCatalogState: %v", diags)
	}
	if len(state.Locations.Elements()) != 1 || len(state.Images.Elements()) != 1 || len(state.SecurityGroups.Elements()) != 1 || len(state.VPCSubnets.Elements()) != 1 {
		t.Fatalf("state lists were not populated: %#v", state)
	}
	limits := state.Limits.Attributes()
	if limits["max_lifetime_seconds"].(types.Int64).ValueInt64() != 28800 || limits["max_microvms"].(types.Int64).ValueInt64() != 5 {
		t.Fatalf("limits = %v", limits)
	}
}

const kitJSON = `{"id":"img-1","name":"claude","is_base":false,"status":"ready","source_kind":"dockerfile","template_name":"u-1","exposed_ports":[],"category":"agent","icon":"claude","tagline":"Coding agent","default_plan_vcpu":2,"default_lifetime_seconds":3600,"suggested_secrets":[{"name":"ANTHROPIC_API_KEY","provider":"anthropic","label":"Anthropic API key","placeholder":"your Anthropic API key","icon":"anthropic","role":"provider","help_url":"https://console.anthropic.com/settings/keys"},{"name":"EXTRA"}],"sort_order":10}`

func kitImage(t *testing.T, raw string) map[string]any {
	t.Helper()
	var image map[string]any
	if err := json.Unmarshal([]byte(raw), &image); err != nil {
		t.Fatal(err)
	}
	return image
}

func assertKitAttrs(t *testing.T, attrs map[string]attr.Value) {
	t.Helper()
	if attrs["category"].(types.String).ValueString() != "agent" || attrs["icon"].(types.String).ValueString() != "claude" || attrs["tagline"].(types.String).ValueString() != "Coding agent" {
		t.Fatalf("string kit fields = %v", attrs)
	}
	if attrs["default_plan_vcpu"].(types.Int64).ValueInt64() != 2 || attrs["default_lifetime_seconds"].(types.Int64).ValueInt64() != 3600 || attrs["sort_order"].(types.Int64).ValueInt64() != 10 {
		t.Fatalf("int kit fields = %v", attrs)
	}
	secrets := attrs["suggested_secrets"].(types.List).Elements()
	if len(secrets) != 2 {
		t.Fatalf("suggested_secrets = %v", secrets)
	}
	first := secrets[0].(types.Object).Attributes()
	if first["name"].(types.String).ValueString() != "ANTHROPIC_API_KEY" || first["label"].(types.String).ValueString() != "Anthropic API key" || first["help_url"].(types.String).ValueString() != "https://console.anthropic.com/settings/keys" {
		t.Fatalf("first secret = %v", first)
	}
	// KH-5: the launcher fields reach state; a legacy entry leaves them null.
	for key, want := range map[string]string{"provider": "anthropic", "placeholder": "your Anthropic API key", "icon": "anthropic", "role": "provider"} {
		if got := first[key].(types.String); got.IsNull() || got.ValueString() != want {
			t.Fatalf("first secret %s = %v; want %q", key, first[key], want)
		}
	}
	second := secrets[1].(types.Object).Attributes()
	if second["name"].(types.String).ValueString() != "EXTRA" || !second["label"].(types.String).IsNull() {
		t.Fatalf("second secret = %v", second)
	}
	for _, key := range []string{"provider", "placeholder", "icon", "role"} {
		if !second[key].(types.String).IsNull() {
			t.Fatalf("legacy secret %s = %v; want null", key, second[key])
		}
	}
}

func TestMicrovmImagesExposeKitMetadata(t *testing.T) {
	list, diags := microvmImagesToList([]map[string]any{kitImage(t, kitJSON)}, microvmImagesModel{})
	if diags.HasError() {
		t.Fatal(diags)
	}
	assertKitAttrs(t, list.Elements()[0].(types.Object).Attributes())
}

func TestMicrovmCatalogExposesKitMetadata(t *testing.T) {
	state, diags := microvmCatalogState(map[string]any{"images": []any{kitImage(t, kitJSON)}})
	if diags.HasError() {
		t.Fatal(diags)
	}
	assertKitAttrs(t, state.Images.Elements()[0].(types.Object).Attributes())
}

func TestMicrovmKitMetadataNullWhenMasterOmitsIt(t *testing.T) {
	old := `{"id":"img-1","name":"base","is_base":true,"status":"ready","source_kind":"base","template_name":"u","exposed_ports":[]}`
	list, diags := microvmImagesToList([]map[string]any{kitImage(t, old)}, microvmImagesModel{})
	if diags.HasError() {
		t.Fatal(diags)
	}
	attrs := list.Elements()[0].(types.Object).Attributes()
	for _, k := range []string{"category", "icon", "tagline", "default_plan_vcpu", "default_lifetime_seconds", "suggested_secrets", "sort_order"} {
		if !attrs[k].IsNull() {
			t.Fatalf("%s = %v; want null", k, attrs[k])
		}
	}
	state, diags := microvmCatalogState(map[string]any{"images": []any{kitImage(t, old)}})
	if diags.HasError() {
		t.Fatal(diags)
	}
	if !state.Images.Elements()[0].(types.Object).Attributes()["suggested_secrets"].IsNull() {
		t.Fatal("catalog suggested_secrets not null")
	}
}
