package datasources

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestSandboxVolumesToListMapsFieldsFiltersByNameAndKeepsNulls(t *testing.T) {
	rows := []map[string]any{
		{"id": "v1", "name": "data", "size_gib": 20.0, "status": "attached", "location_id": "loc-1", "sandbox_id": "vm-1", "mount_path": "/mnt/data", "delete_on_sandbox_delete": true, "created_at": "2026-11-14T10:00:00Z"},
		{"id": "v2", "name": "cache", "size_gib": 5.0, "status": "available", "location_id": "loc-1", "sandbox_id": nil, "mount_path": nil},
	}
	all, diags := sandboxVolumesToList(rows, sandboxVolumesModel{})
	if diags.HasError() || len(all.Elements()) != 2 {
		t.Fatalf("all = %v, %v", all, diags)
	}
	first := all.Elements()[0].(types.Object).Attributes()
	if first["sandbox_id"].(types.String).ValueString() != "vm-1" || first["size_gib"].(types.Int64).ValueInt64() != 20 || !first["delete_on_sandbox_delete"].(types.Bool).ValueBool() {
		t.Fatalf("first = %v", first)
	}
	second := all.Elements()[1].(types.Object).Attributes()
	if !second["sandbox_id"].(types.String).IsNull() || !second["mount_path"].(types.String).IsNull() || second["delete_on_sandbox_delete"].(types.Bool).ValueBool() {
		t.Fatalf("second = %v", second)
	}
	one, _ := sandboxVolumesToList(rows, sandboxVolumesModel{Name: types.StringValue("CACHE")})
	if len(one.Elements()) != 1 || one.Elements()[0].(types.Object).Attributes()["id"].(types.String).ValueString() != "v2" {
		t.Fatalf("name filter = %v", one)
	}
}

func TestSandboxVolumesSchemaIsValid(t *testing.T) {
	var resp datasource.SchemaResponse
	NewSandboxVolumesDataSource().Schema(context.Background(), datasource.SchemaRequest{}, &resp)
	if diags := resp.Schema.ValidateImplementation(context.Background()); diags.HasError() {
		t.Fatalf("schema: %v", diags)
	}
	for _, k := range []string{"status", "name"} {
		if !resp.Schema.Attributes[k].IsOptional() {
			t.Errorf("%s must be an optional filter", k)
		}
	}
}
