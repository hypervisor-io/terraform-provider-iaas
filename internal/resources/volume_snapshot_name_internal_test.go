package resources

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
)

func TestVolumeSnapshotNameSchemaValidation(t *testing.T) {
	ctx := context.Background()
	r := &volumeSnapshotResource{}
	var sch resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sch)
	nameAttribute := sch.Schema.Attributes["name"].(schema.StringAttribute)
	for _, tc := range []struct {
		name    string
		invalid bool
	}{
		{"", true}, {"   ", true}, {" nightly ", true}, {"nightly\t", true}, {"\nnightly", true}, {"0", true},
		{"nightly", false}, {"00", false}, {"nightly copy", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := validator.StringRequest{Path: path.Root("name"), ConfigValue: types.StringValue(tc.name)}
			var resp validator.StringResponse
			for _, v := range nameAttribute.Validators {
				v.ValidateString(ctx, req, &resp)
			}
			if resp.Diagnostics.HasError() != tc.invalid {
				t.Fatalf("name=%q diagnostics=%v; invalid=%v", tc.name, resp.Diagnostics, tc.invalid)
			}
		})
	}
	for _, value := range []types.String{types.StringNull(), types.StringUnknown()} {
		var resp validator.StringResponse
		for _, v := range nameAttribute.Validators {
			v.ValidateString(ctx, validator.StringRequest{Path: path.Root("name"), ConfigValue: value}, &resp)
		}
		if resp.Diagnostics.HasError() {
			t.Fatalf("deferred value %v: %v", value, resp.Diagnostics)
		}
	}
}

// Calls Create directly against a loopback fake API; no Terraform/OpenTofu subprocess.
func TestVolumeSnapshotCreateNameRoundTrip(t *testing.T) {
	t.Setenv("IAAS_INSTANCE_POLL_INTERVAL", "1ms")
	for _, tc := range []struct {
		name    string
		invalid bool
	}{
		{"", true}, {"   ", true}, {" nightly ", true}, {"0", true},
		{"nightly", false}, {"00", false}, {"nightly copy", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			var mu sync.Mutex
			gets, posts := 0, 0
			var snapshots []any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				switch req.Method {
				case http.MethodGet:
					gets++
					_ = json.NewEncoder(w).Encode(map[string]any{"volume": map[string]any{"id": "vol-1", "snapshots": snapshots}})
				case http.MethodPost:
					posts++
					var body map[string]any
					_ = json.NewDecoder(req.Body).Decode(&body)
					name, _ := body["name"].(string)
					if name != tc.name {
						t.Errorf("request changed name %q to %q", tc.name, name)
					}
					storedName := strings.TrimSpace(name)
					if storedName == "" || storedName == "0" {
						storedName = "snap-generated"
					}
					snapshots = append(snapshots, map[string]any{"id": "snap-1", "name": storedName, "status": "available", "size": int64(1)})
					_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "queue": map[string]any{"id": "q-1"}})
				default:
					t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
				}
			}))
			defer srv.Close()
			r := &volumeSnapshotResource{client: client.New(srv.URL, "test-token", time.Second, false)}
			var sch resource.SchemaResponse
			r.Schema(ctx, resource.SchemaRequest{}, &sch)
			plan := tfsdk.Plan{Schema: sch.Schema}
			model := volumeSnapshotModel{ID: types.StringUnknown(), VolumeID: types.StringValue("vol-1"), Name: types.StringValue(tc.name), Status: types.StringUnknown(), Size: types.Int64Unknown(),
				Timeouts: timeouts.Value{Object: types.ObjectValueMust(map[string]attr.Type{"create": types.StringType, "delete": types.StringType}, map[string]attr.Value{"create": types.StringValue("100ms"), "delete": types.StringNull()})}}
			if diags := plan.Set(ctx, model); diags.HasError() {
				t.Fatal(diags)
			}
			resp := resource.CreateResponse{State: tfsdk.State{Schema: sch.Schema, Raw: tftypes.NewValue(sch.Schema.Type().TerraformType(ctx), nil)}}
			r.Create(ctx, resource.CreateRequest{Plan: plan}, &resp)
			mu.Lock()
			defer mu.Unlock()
			if tc.invalid {
				if !resp.Diagnostics.HasError() || !resp.State.Raw.IsNull() || gets != 0 || posts != 0 || len(snapshots) != 0 {
					t.Fatalf("name=%q diagnostics=%v state=%v reads=%d sends=%d leftovers=%d; want validation error, no id and no API calls", tc.name, resp.Diagnostics, resp.State.Raw, gets, posts, len(snapshots))
				}
			} else {
				var state volumeSnapshotModel
				if diags := resp.State.Get(ctx, &state); diags.HasError() {
					t.Fatal(diags)
				}
				if resp.Diagnostics.HasError() || state.ID.ValueString() != "snap-1" || state.Name.ValueString() != tc.name || posts != 1 || len(snapshots) != 1 {
					t.Fatalf("name=%q diagnostics=%v state=%v sends=%d leftovers=%d; want original name and tracked snapshot", tc.name, resp.Diagnostics, state, posts, len(snapshots))
				}
			}
		})
	}
}
