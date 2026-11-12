package resources

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
)

type snapshotAPI struct {
	mu       sync.Mutex
	requests []string
	headers  []string
	bodies   []string
	// gets are the answers of GET /microvm/snapshot/snap-1 in order; the last repeats.
	gets []string
	// getStatus is the HTTP status paired with gets[i] (0 means 200).
	getStatus []int
	// deleteAnswers are consumed by DELETE; empty means 202 deleting.
	deleteAnswers []string
	deleteStatus  []int
	gone          bool
	getCalls      int
}

func (a *snapshotAPI) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		raw, _ := io.ReadAll(r.Body)
		a.requests = append(a.requests, r.Method+" "+r.URL.EscapedPath())
		a.headers = append(a.headers, r.Header.Get("Idempotency-Key"))
		a.bodies = append(a.bodies, string(raw))
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"success":true,"snapshot":{"id":"snap-1","name":"before","status":"creating","source_sandbox_id":"vm-1","kind":"disk"}}`))
		case r.Method == http.MethodDelete:
			if len(a.deleteAnswers) > 0 {
				body, status := a.deleteAnswers[0], a.deleteStatus[0]
				a.deleteAnswers, a.deleteStatus = a.deleteAnswers[1:], a.deleteStatus[1:]
				w.WriteHeader(status)
				_, _ = w.Write([]byte(body))
				return
			}
			a.gone = true
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"success":true,"snapshot":{"id":"snap-1","name":"before","status":"deleting"}}`))
		default:
			if a.gone {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"success":false,"reason":"snapshot_not_found","message":"Snapshot not found."}`))
				return
			}
			i := a.getCalls
			if i >= len(a.gets) {
				i = len(a.gets) - 1
			}
			a.getCalls++
			status := http.StatusOK
			if i < len(a.getStatus) && a.getStatus[i] != 0 {
				status = a.getStatus[i]
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(a.gets[i]))
		}
	})
}

func snapshotFixture(t *testing.T, a *snapshotAPI) (*microvmSnapshotResource, resource.SchemaResponse) {
	t.Helper()
	old := microvmSnapshotPollInterval
	microvmSnapshotPollInterval = 2 * time.Millisecond
	t.Cleanup(func() { microvmSnapshotPollInterval = old })
	srv := httptest.NewServer(a.handler(t))
	t.Cleanup(srv.Close)
	r := &microvmSnapshotResource{client: client.New(srv.URL+"/api", "tok", 5*time.Second, false)}
	var sresp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &sresp)
	return r, sresp
}

// snapshotRaw builds a full object value with every attribute null except the given ones.
func snapshotRaw(sresp resource.SchemaResponse, set map[string]tftypes.Value) tftypes.Value {
	typ := sresp.Schema.Type().TerraformType(context.Background()).(tftypes.Object)
	vals := map[string]tftypes.Value{}
	for k, at := range typ.AttributeTypes {
		vals[k] = tftypes.NewValue(at, nil)
	}
	for k, v := range set {
		vals[k] = v
	}
	return tftypes.NewValue(typ, vals)
}

func str(v string) tftypes.Value { return tftypes.NewValue(tftypes.String, v) }

const snapReady = `{"success":true,"snapshot":{"id":"snap-1","name":"before","status":"ready","kind":"disk","source_sandbox_id":"vm-1","source_state":"running","consistency":"crash","size_bytes":10737418240,"used_bytes":314572800,"disk_gib":10,"vcpu":2,"mem_mib":512,"location_id":"loc-1","image_id":"img-1","capture_method":"paused_copy","paused_ms":2300,"volumes_excluded":2,"billable_gib":1,"price_gib_hour":0.5,"created_at":"2026-11-13T10:00:00Z","ready_at":"2026-11-13T10:00:41Z"}}`
const snapCreating = `{"success":true,"snapshot":{"id":"snap-1","name":"before","status":"creating","kind":"disk","source_sandbox_id":"vm-1"}}`

func TestMicrovmSnapshotCreateWaitsForReadyAndRecordsTheComputedValues(t *testing.T) {
	a := &snapshotAPI{gets: []string{snapCreating, snapCreating, snapReady}}
	r, sresp := snapshotFixture(t, a)
	plan := tfsdk.Plan{Schema: sresp.Schema, Raw: snapshotRaw(sresp, map[string]tftypes.Value{
		"sandbox_id": str("vm-1"), "name": str("before"), "description": str("pre-migration"), "consistency": str("auto"),
	})}
	resp := resource.CreateResponse{State: tfsdk.State{Schema: sresp.Schema, Raw: tftypes.NewValue(sresp.Schema.Type().TerraformType(context.Background()), nil)}}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create: %v", resp.Diagnostics)
	}
	var got microvmSnapshotModel
	resp.Diagnostics.Append(resp.State.Get(context.Background(), &got)...)
	if got.ID.ValueString() != "snap-1" || got.Status.ValueString() != "ready" || got.CaptureMethod.ValueString() != "paused_copy" {
		t.Fatalf("state = %+v", got)
	}
	if got.VolumesExcluded.ValueInt64() != 2 || got.PausedMs.ValueInt64() != 2300 || got.UsedBytes.ValueInt64() != 314572800 || got.PriceGibHour.ValueFloat64() != 0.5 || got.ConsistencyAchieved.ValueString() != "crash" {
		t.Fatalf("computed values = %+v", got)
	}
	if got.SandboxID.ValueString() != "vm-1" || got.Description.ValueString() != "pre-migration" || got.Consistency.ValueString() != "auto" {
		t.Fatalf("configured values lost: %+v", got)
	}
	if a.requests[0] != "POST /api/microvm/vm/vm-1/snapshots" || a.headers[0] == "" {
		t.Fatalf("first request = %s key=%q", a.requests[0], a.headers[0])
	}
	for _, want := range []string{`"name":"before"`, `"kind":"disk"`, `"description":"pre-migration"`, `"consistency":"auto"`} {
		if !strings.Contains(a.bodies[0], want) {
			t.Errorf("body %s lacks %s", a.bodies[0], want)
		}
	}
	if a.getCalls < 3 {
		t.Fatalf("expected polling until ready, GETs = %d", a.getCalls)
	}
}

func TestMicrovmSnapshotCreateFailureNamesThePlatformReasonAndKeepsTheTaintedState(t *testing.T) {
	a := &snapshotAPI{gets: []string{snapCreating, `{"success":true,"snapshot":{"id":"snap-1","status":"failed","failure_reason":"snapshot_requires_pause","failure_detail":{"message":"Pausing or stopping the sandbox first avoids it.","pause_estimate_ms":14000,"pause_limit_ms":10000}}}`}}
	r, sresp := snapshotFixture(t, a)
	plan := tfsdk.Plan{Schema: sresp.Schema, Raw: snapshotRaw(sresp, map[string]tftypes.Value{"sandbox_id": str("vm-1"), "name": str("before")})}
	resp := resource.CreateResponse{State: tfsdk.State{Schema: sresp.Schema, Raw: tftypes.NewValue(sresp.Schema.Type().TerraformType(context.Background()), nil)}}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("a failed snapshot must fail the create")
	}
	if d := resp.Diagnostics.Errors()[0].Detail(); !strings.Contains(d, "snapshot_requires_pause") || !strings.Contains(d, "estimated pause 14000 ms, limit 10000 ms") || !strings.Contains(d, "Pausing or stopping") {
		t.Fatalf("error = %v", resp.Diagnostics)
	}
	var got microvmSnapshotModel
	resp.State.Get(context.Background(), &got)
	if got.ID.ValueString() != "snap-1" {
		t.Fatalf("the id must be recorded before waiting so destroy can clean up: %+v", got)
	}
}

func TestMicrovmSnapshotCreateToleratesAFewTransportBlipsWhileWaiting(t *testing.T) {
	a := &snapshotAPI{gets: []string{`oops`, `oops`, snapReady}, getStatus: []int{http.StatusConflict, http.StatusConflict, 0}}
	r, sresp := snapshotFixture(t, a)
	plan := tfsdk.Plan{Schema: sresp.Schema, Raw: snapshotRaw(sresp, map[string]tftypes.Value{"sandbox_id": str("vm-1"), "name": str("before")})}
	resp := resource.CreateResponse{State: tfsdk.State{Schema: sresp.Schema, Raw: tftypes.NewValue(sresp.Schema.Type().TerraformType(context.Background()), nil)}}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("two non-retried errors while waiting must not fail the create (the client already retries 5xx itself): %v", resp.Diagnostics)
	}
}

func TestMicrovmSnapshotCreateRefusalIsReportedWithItsReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"success":false,"reason":"snapshot_requires_pause","message":"pausing first avoids it","pause_estimate_ms":14000,"pause_limit_ms":10000}`))
	}))
	defer srv.Close()
	r := &microvmSnapshotResource{client: client.New(srv.URL+"/api", "tok", time.Second, false)}
	var sresp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &sresp)
	plan := tfsdk.Plan{Schema: sresp.Schema, Raw: snapshotRaw(sresp, map[string]tftypes.Value{"sandbox_id": str("vm-1"), "name": str("n")})}
	resp := resource.CreateResponse{State: tfsdk.State{Schema: sresp.Schema, Raw: tftypes.NewValue(sresp.Schema.Type().TerraformType(context.Background()), nil)}}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &resp)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "snapshot_requires_pause") {
		t.Fatalf("diagnostics = %v", resp.Diagnostics)
	}
	if !resp.State.Raw.IsNull() {
		t.Fatal("a refused create must leave no state")
	}
}

func TestMicrovmSnapshotReadHandlesEveryStatus(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		status     int
		wantRemove bool
		wantWarn   bool
	}{
		{"ready", snapReady, 0, false, false},
		{"creating", snapCreating, 0, false, false},
		{"not found", `{"success":false,"reason":"snapshot_not_found","message":"Snapshot not found."}`, 404, true, false},
		{"failed", `{"success":true,"snapshot":{"id":"snap-1","status":"failed","failure_reason":"snapshot_capture_failed"}}`, 0, true, true},
		{"deleting", `{"success":true,"snapshot":{"id":"snap-1","status":"deleting"}}`, 0, true, false},
		{"unavailable", `{"success":true,"snapshot":{"id":"snap-1","name":"before","status":"unavailable","source_sandbox_id":"vm-1"}}`, 0, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &snapshotAPI{gets: []string{tc.body}, getStatus: []int{tc.status}}
			r, sresp := snapshotFixture(t, a)
			state := tfsdk.State{Schema: sresp.Schema, Raw: snapshotRaw(sresp, map[string]tftypes.Value{"id": str("snap-1"), "sandbox_id": str("vm-1"), "name": str("before")})}
			resp := resource.ReadResponse{State: state}
			r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("read: %v", resp.Diagnostics)
			}
			if removed := resp.State.Raw.IsNull(); removed != tc.wantRemove {
				t.Errorf("removed = %v; want %v", removed, tc.wantRemove)
			}
			if warned := resp.Diagnostics.WarningsCount() > 0; warned != tc.wantWarn {
				t.Errorf("warned = %v; want %v", warned, tc.wantWarn)
			}
		})
	}
}

func TestMicrovmSnapshotReadKeepsTheConfiguredSandboxWhenTheSourceIsDeleted(t *testing.T) {
	deletedSource := strings.Replace(snapReady, `"source_sandbox_id":"vm-1"`, `"source_sandbox_id":null`, 1)
	a := &snapshotAPI{gets: []string{deletedSource}}
	r, sresp := snapshotFixture(t, a)
	state := tfsdk.State{Schema: sresp.Schema, Raw: snapshotRaw(sresp, map[string]tftypes.Value{"id": str("snap-1"), "sandbox_id": str("vm-1"), "name": str("before"), "consistency": str("crash")})}
	resp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
	var got microvmSnapshotModel
	resp.State.Get(context.Background(), &got)
	if got.SandboxID.ValueString() != "vm-1" || got.Consistency.ValueString() != "crash" || got.Status.ValueString() != "ready" {
		t.Fatalf("deleting the sandbox must not read as drift: %+v", got)
	}
}

func TestMicrovmSnapshotImportReadsWhatThePlatformReturns(t *testing.T) {
	a := &snapshotAPI{gets: []string{snapReady}}
	r, sresp := snapshotFixture(t, a)
	state := tfsdk.State{Schema: sresp.Schema, Raw: snapshotRaw(sresp, map[string]tftypes.Value{"id": str("snap-1")})}
	resp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
	var got microvmSnapshotModel
	resp.State.Get(context.Background(), &got)
	if got.SandboxID.ValueString() != "vm-1" || got.Name.ValueString() != "before" || !got.Description.IsNull() || !got.Consistency.IsNull() {
		t.Fatalf("imported state = %+v", got)
	}
}

func TestMicrovmSnapshotDeleteRetriesWhileInUseThenWaitsUntilGone(t *testing.T) {
	a := &snapshotAPI{
		gets:          []string{snapReady},
		deleteAnswers: []string{`{"success":false,"reason":"snapshot_in_use","message":"in use"}`},
		deleteStatus:  []int{http.StatusConflict},
	}
	r, sresp := snapshotFixture(t, a)
	state := tfsdk.State{Schema: sresp.Schema, Raw: snapshotRaw(sresp, map[string]tftypes.Value{"id": str("snap-1")})}
	resp := resource.DeleteResponse{}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("delete: %v", resp.Diagnostics)
	}
	deletes := 0
	for _, req := range a.requests {
		if strings.HasPrefix(req, "DELETE ") {
			deletes++
		}
	}
	if deletes != 2 {
		t.Fatalf("expected one retried delete, requests = %v", a.requests)
	}
}

func TestMicrovmSnapshotDeleteOfAnAlreadyGoneSnapshotSucceeds(t *testing.T) {
	a := &snapshotAPI{gets: []string{snapReady}, deleteAnswers: []string{`{"success":false,"reason":"snapshot_not_found","message":"gone"}`}, deleteStatus: []int{http.StatusNotFound}}
	r, sresp := snapshotFixture(t, a)
	state := tfsdk.State{Schema: sresp.Schema, Raw: snapshotRaw(sresp, map[string]tftypes.Value{"id": str("snap-1")})}
	resp := resource.DeleteResponse{}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("delete: %v", resp.Diagnostics)
	}
}

func TestMicrovmSnapshotPlanModifierReplacesUnlessAdopting(t *testing.T) {
	m := snapshotReplaceUnlessAdopting{}
	run := func(state, plan tftypes.Value, stateVal, planVal types.String) bool {
		req := planmodifier.StringRequest{State: tfsdk.State{Raw: state}, Plan: tfsdk.Plan{Raw: plan}, StateValue: stateVal, PlanValue: planVal}
		resp := &planmodifier.StringResponse{}
		m.PlanModifyString(context.Background(), req, resp)
		return resp.RequiresReplace
	}
	obj := tftypes.NewValue(tftypes.Object{AttributeTypes: map[string]tftypes.Type{}}, map[string]tftypes.Value{})
	null := tftypes.NewValue(tftypes.Object{AttributeTypes: map[string]tftypes.Type{}}, nil)
	if run(null, obj, types.StringNull(), types.StringValue("a")) {
		t.Error("create must not require replace")
	}
	if !run(obj, obj, types.StringValue("a"), types.StringValue("b")) {
		t.Error("a changed value must replace")
	}
	if run(obj, obj, types.StringValue("a"), types.StringValue("a")) {
		t.Error("an unchanged value must not replace")
	}
	if run(obj, obj, types.StringNull(), types.StringValue("a")) {
		t.Error("a value the state does not hold (import) is adopted, not replaced")
	}
	if !run(obj, obj, types.StringValue("a"), types.StringNull()) {
		t.Error("removing a configured value must replace")
	}
}

func TestMicrovmSnapshotSchemaEveryArgumentIsImmutableAndComputedAreComputed(t *testing.T) {
	var sresp resource.SchemaResponse
	NewMicrovmSnapshotResource().Schema(context.Background(), resource.SchemaRequest{}, &sresp)
	for _, name := range []string{"sandbox_id", "name"} {
		if !sresp.Schema.Attributes[name].IsRequired() {
			t.Errorf("%s must be required", name)
		}
	}
	for _, name := range []string{"description", "consistency"} {
		a := sresp.Schema.Attributes[name]
		if !a.IsOptional() || a.IsComputed() {
			t.Errorf("%s must be optional and not computed (the platform cannot return it)", name)
		}
	}
	for _, name := range []string{"status", "kind", "size_bytes", "used_bytes", "disk_gib", "paused_ms", "volumes_excluded", "capture_method", "price_gib_hour", "billable_gib", "location_id", "ready_at"} {
		if !sresp.Schema.Attributes[name].IsComputed() {
			t.Errorf("%s must be computed", name)
		}
	}
	if diags := sresp.Schema.ValidateImplementation(context.Background()); diags.HasError() {
		t.Fatalf("schema: %v", diags)
	}
	_ = path.Root("id")
}
