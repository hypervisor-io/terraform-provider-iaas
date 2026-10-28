package resources

import (
	"context"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hypervisor-io/terraform-provider-iaas/client"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestVolumeProtectionBodyNullableAndZero(t *testing.T) {
	p := volumeProtectionPolicyModel{Name: types.StringValue("nightly"), Frequency: types.StringValue("weekly"), ScheduleTime: types.StringValue("02:30"), ScheduleDay: types.Int64Value(0), Timezone: types.StringNull(), BackupEnabled: types.BoolValue(false), SnapshotEnabled: types.BoolValue(true), RetentionCount: types.Int64Unknown(), KeepLast: types.Int64Value(0)}
	body := volumeProtectionBody(p)
	if body["schedule_day"] != int64(0) || body["timezone"] != nil || body["backup_enabled"] != false || body["keep_last"] != int64(0) {
		t.Fatalf("body=%v", body)
	}
	if _, exists := body["retention_count"]; exists {
		t.Fatal("unknown optional computed count must be omitted")
	}
	// The API canonicalizes a disabled GFS rule from 0 to null. Preserve a
	// configured zero, but observe remote clearing of a formerly positive rule.
	p.KeepDaily = types.Int64Value(7)
	p = volumeProtectionState(map[string]any{"keep_last": nil, "keep_daily": nil, "timezone": nil, "effective_timezone": "Asia/Kolkata"}, p)
	if p.KeepLast.ValueInt64() != 0 || p.KeepLast.IsNull() || !p.KeepDaily.IsNull() || !p.Timezone.IsNull() || p.EffectiveTimezone.ValueString() != "Asia/Kolkata" {
		t.Fatalf("state=%v", p)
	}
}

func TestVolumeProtectionAttachmentReadPreservesUncertainMembership(t *testing.T) {
	for _, tc := range []struct {
		name, body         string
		removed, wantError bool
	}{
		{"present", `{"success":true,"policy":{"id":"policy","volumes":[{"id":"volume"},{"id":"other"}]}}`, false, false},
		{"absent", `{"success":true,"policy":{"id":"policy","volumes":[{"id":"other"}]}}`, true, false},
		{"missing_array", `{"success":true,"policy":{"id":"policy"}}`, false, true},
		{"malformed_member", `{"success":true,"policy":{"id":"policy","volumes":[{}]}}`, false, true},
		{"malformed_after_match", `{"success":true,"policy":{"id":"policy","volumes":[{"id":"volume"},null]}}`, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/storage/volume-policy/policy" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			ctx := context.Background()
			r := &volumeProtectionPolicyAttachmentResource{client: client.New(srv.URL, "token", time.Second, false)}
			var schema resource.SchemaResponse
			r.Schema(ctx, resource.SchemaRequest{}, &schema)
			state := tfsdk.State{Schema: schema.Schema}
			p := volumeProtectionPolicyAttachmentModel{ID: types.StringValue("policy/volume"), PolicyID: types.StringValue("policy"), VolumeID: types.StringValue("volume")}
			if d := state.Set(ctx, p); d.HasError() {
				t.Fatal(d)
			}
			resp := resource.ReadResponse{State: state}
			r.Read(ctx, resource.ReadRequest{State: state}, &resp)
			if resp.Diagnostics.HasError() != tc.wantError || resp.State.Raw.IsNull() != tc.removed {
				t.Fatalf("diagnostics=%v removed=%v", resp.Diagnostics, resp.State.Raw.IsNull())
			}
		})
	}
}
func TestVolumeProtectionAttachmentDeleteAlreadyDetached(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/storage/volume-policy/policy/detach" {
			t.Errorf("request %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(409)
		_, _ = w.Write([]byte(`{"success":false,"reason":"not_attached","message":"not attached"}`))
	}))
	defer srv.Close()
	ctx := context.Background()
	r := &volumeProtectionPolicyAttachmentResource{client: client.New(srv.URL, "token", time.Second, false)}
	var schema resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schema)
	state := tfsdk.State{Schema: schema.Schema}
	if d := state.Set(ctx, volumeProtectionPolicyAttachmentModel{ID: types.StringValue("policy/volume"), PolicyID: types.StringValue("policy"), VolumeID: types.StringValue("volume")}); d.HasError() {
		t.Fatal(d)
	}
	var resp resource.DeleteResponse
	r.Delete(ctx, resource.DeleteRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() || calls != 1 {
		t.Fatalf("calls=%d diagnostics=%v", calls, resp.Diagnostics)
	}
}

func TestVolumeProtectionAttachmentCreateRetainsUncertainState(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(503)
		_, _ = w.Write([]byte(`{"success":false,"message":"acknowledgement lost"}`))
	}))
	defer srv.Close()
	ctx := context.Background()
	r := &volumeProtectionPolicyAttachmentResource{client: client.New(srv.URL, "token", time.Second, false)}
	var sch resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sch)
	plan := tfsdk.Plan{Schema: sch.Schema}
	if d := plan.Set(ctx, volumeProtectionPolicyAttachmentModel{ID: types.StringUnknown(), PolicyID: types.StringValue("policy"), VolumeID: types.StringValue("volume")}); d.HasError() {
		t.Fatal(d)
	}
	resp := resource.CreateResponse{State: tfsdk.State{Schema: sch.Schema}}
	r.Create(ctx, resource.CreateRequest{Plan: plan}, &resp)
	var state volumeProtectionPolicyAttachmentModel
	if d := resp.State.Get(ctx, &state); d.HasError() {
		t.Fatal(d)
	}
	if !resp.Diagnostics.HasError() || calls != 1 || state.ID.ValueString() != "policy/volume" {
		t.Fatalf("calls=%d state=%v diagnostics=%v", calls, state, resp.Diagnostics)
	}
}

func TestVolumeProtectionCreateUsesMutationResponse(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" {
			t.Errorf("unnecessary follow-up %s", r.Method)
		}
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"success":true,"policy":{"id":"policy","name":"nightly","frequency":"daily","schedule_time":"02:30","schedule_day":null,"timezone":null,"backup_enabled":true,"snapshot_enabled":false,"retention_mode":"count","retention_count":7,"keep_last":null,"keep_within_days":null,"keep_daily":null,"keep_weekly":null,"keep_monthly":null,"keep_yearly":null,"status":"active","effective_timezone":"Asia/Kolkata"}}`))
	}))
	defer srv.Close()
	ctx := context.Background()
	r := &volumeProtectionPolicyResource{client: client.New(srv.URL, "token", time.Second, false)}
	var sch resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sch)
	plan := tfsdk.Plan{Schema: sch.Schema}
	model := volumeProtectionPolicyModel{ID: types.StringUnknown(), Name: types.StringValue("nightly"), Frequency: types.StringValue("daily"), ScheduleTime: types.StringValue("02:30"), BackupEnabled: types.BoolValue(true), SnapshotEnabled: types.BoolValue(false), RetentionMode: types.StringValue("count"), RetentionCount: types.Int64Value(7), Status: types.StringUnknown(), EffectiveTimezone: types.StringUnknown()}
	if d := plan.Set(ctx, model); d.HasError() {
		t.Fatal(d)
	}
	resp := resource.CreateResponse{State: tfsdk.State{Schema: sch.Schema}}
	r.Create(ctx, resource.CreateRequest{Plan: plan}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var state volumeProtectionPolicyModel
	if d := resp.State.Get(ctx, &state); d.HasError() {
		t.Fatal(d)
	}
	if state.ID.ValueString() != "policy" || state.EffectiveTimezone.ValueString() != "Asia/Kolkata" || !state.Timezone.IsNull() || calls != 1 {
		t.Fatalf("calls=%d state=%v", calls, state)
	}
}
