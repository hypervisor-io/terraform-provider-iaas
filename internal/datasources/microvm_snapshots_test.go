package datasources

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestMicrovmSnapshotsToListMapsFieldsFiltersByNameAndKeepsNulls(t *testing.T) {
	rows := []map[string]any{
		{"id": "snap-1", "name": "before", "kind": "disk", "status": "ready", "source_sandbox_id": "vm-1", "source_state": "running", "consistency": "crash",
			"size_bytes": 10737418240.0, "used_bytes": 314572800.0, "disk_gib": 10.0, "vcpu": 2.0, "mem_mib": 512.0, "location_id": "loc-1", "image_id": "img-1",
			"capture_method": "paused_copy", "paused_ms": 2300.0, "volumes_excluded": 2.0, "billable_gib": 1.0, "price_gib_hour": 0.5, "created_at": "2026-11-13T10:00:00Z", "ready_at": "2026-11-13T10:00:41Z"},
		{"id": "snap-2", "name": "other", "kind": "disk", "status": "failed", "source_sandbox_id": nil, "failure_reason": "snapshot_capture_failed"},
	}
	all, diags := microvmSnapshotsToList(rows, microvmSnapshotsModel{})
	if diags.HasError() || len(all.Elements()) != 2 {
		t.Fatalf("all = %v, %v", all, diags)
	}
	first := all.Elements()[0].(types.Object).Attributes()
	if first["volumes_excluded"].(types.Int64).ValueInt64() != 2 || first["paused_ms"].(types.Int64).ValueInt64() != 2300 || first["price_gib_hour"].(types.Float64).ValueFloat64() != 0.5 || first["capture_method"].(types.String).ValueString() != "paused_copy" {
		t.Fatalf("first = %v", first)
	}
	second := all.Elements()[1].(types.Object).Attributes()
	if !second["source_sandbox_id"].(types.String).IsNull() || !second["paused_ms"].(types.Int64).IsNull() || second["failure_reason"].(types.String).ValueString() != "snapshot_capture_failed" {
		t.Fatalf("second = %v", second)
	}
	one, _ := microvmSnapshotsToList(rows, microvmSnapshotsModel{Name: types.StringValue("BEFORE")})
	if len(one.Elements()) != 1 || one.Elements()[0].(types.Object).Attributes()["id"].(types.String).ValueString() != "snap-1" {
		t.Fatalf("name filter = %v", one)
	}
}

func TestMicrovmSnapshotsSchemaIsValid(t *testing.T) {
	var resp datasource.SchemaResponse
	NewMicrovmSnapshotsDataSource().Schema(context.Background(), datasource.SchemaRequest{}, &resp)
	if diags := resp.Schema.ValidateImplementation(context.Background()); diags.HasError() {
		t.Fatalf("schema: %v", diags)
	}
	for _, k := range []string{"sandbox_id", "status", "name"} {
		if !resp.Schema.Attributes[k].IsOptional() {
			t.Errorf("%s must be an optional filter", k)
		}
	}
}
