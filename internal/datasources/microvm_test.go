package datasources

import (
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
