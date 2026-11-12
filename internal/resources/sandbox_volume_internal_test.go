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

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
)

type volumeAPI struct {
	mu       sync.Mutex
	requests []string
	bodies   []string
	// deleteAnswers are consumed by DELETE; empty means 202 deleting and the volume is gone afterwards.
	deleteAnswers []string
	deleteStatus  []int
	gone          bool
	show          string
	// staySeen is how many reads after the delete still answer the volume (a delete the node has not finished yet).
	staySeen int
	afterDel int
}

func (a *volumeAPI) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		raw, _ := io.ReadAll(r.Body)
		a.requests = append(a.requests, r.Method+" "+r.URL.EscapedPath())
		a.bodies = append(a.bodies, string(raw))
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"success":true,"volume":{"id":"vol-1","name":"data","for":"sandbox","size_gib":20,"status":"available","location_id":"loc-1","sandbox_id":null,"mount_path":null,"created_at":"2026-11-14T10:00:00Z"}}`))
		case r.Method == http.MethodDelete:
			if len(a.deleteAnswers) > 0 {
				body, status := a.deleteAnswers[0], a.deleteStatus[0]
				a.deleteAnswers, a.deleteStatus = a.deleteAnswers[1:], a.deleteStatus[1:]
				w.WriteHeader(status)
				_, _ = w.Write([]byte(body))
				return
			}
			a.gone = a.staySeen == 0
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"success":true,"volume":{"id":"vol-1","status":"deleting"}}`))
		default:
			if a.staySeen > 0 && a.afterDel >= 0 && len(a.requests) > 1 && a.requests[len(a.requests)-2] != "" {
				// after a delete was recorded, count reads until the volume is finally gone
				for _, prior := range a.requests[:len(a.requests)-1] {
					if prior == "DELETE /api/storage/volume/vol-1" {
						a.afterDel++
						break
					}
				}
				if a.afterDel > a.staySeen {
					a.gone = true
				}
			}
			if a.gone {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"success":false,"reason":"volume_not_found","message":"Volume not found."}`))
				return
			}
			_, _ = w.Write([]byte(a.show))
		}
	})
}

func volumeFixture(t *testing.T, a *volumeAPI) (*sandboxVolumeResource, resource.SchemaResponse) {
	t.Helper()
	old := sandboxVolumePollInterval
	sandboxVolumePollInterval = 2 * time.Millisecond
	t.Cleanup(func() { sandboxVolumePollInterval = old })
	srv := httptest.NewServer(a.handler())
	t.Cleanup(srv.Close)
	r := &sandboxVolumeResource{client: client.New(srv.URL+"/api", "tok", 5*time.Second, false)}
	var sresp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &sresp)
	return r, sresp
}

const volShow = `{"volume":{"id":"vol-1","name":"data","for":"sandbox","size_gib":20,"status":"attached","location_id":"loc-1","sandbox_id":"vm-1","mount_path":"/mnt/data","created_at":"2026-11-14T10:00:00Z"}}`

func TestSandboxVolumeCreateSendsTheSandboxBodyAndRecordsTheComputedValues(t *testing.T) {
	a := &volumeAPI{}
	r, sresp := volumeFixture(t, a)
	plan := tfsdk.Plan{Schema: sresp.Schema, Raw: snapshotRaw(sresp, map[string]tftypes.Value{
		"name": str("data"), "size_gib": tftypes.NewValue(tftypes.Number, 20), "location_id": str("loc-1"),
	})}
	resp := resource.CreateResponse{State: tfsdk.State{Schema: sresp.Schema, Raw: tftypes.NewValue(sresp.Schema.Type().TerraformType(context.Background()), nil)}}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create: %v", resp.Diagnostics)
	}
	var got sandboxVolumeModel
	resp.State.Get(context.Background(), &got)
	if got.ID.ValueString() != "vol-1" || got.Status.ValueString() != "available" || got.SizeGib.ValueInt64() != 20 || !got.SandboxID.IsNull() {
		t.Fatalf("state = %+v", got)
	}
	if a.requests[0] != "POST /api/storage/volumes" {
		t.Fatalf("request = %s", a.requests[0])
	}
	for _, want := range []string{`"for":"sandbox"`, `"name":"data"`, `"size_gib":20`, `"location_id":"loc-1"`} {
		if !strings.Contains(a.bodies[0], want) {
			t.Errorf("body %s lacks %s", a.bodies[0], want)
		}
	}
}

func TestSandboxVolumeCreateRefusalIsReportedWithItsReasonAndLeavesNoState(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"success":false,"reason":"no_sandbox_capacity","message":"No node in this location has room for a volume of this size right now."}`))
	}))
	defer srv.Close()
	r := &sandboxVolumeResource{client: client.New(srv.URL+"/api", "tok", time.Second, false)}
	var sresp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &sresp)
	plan := tfsdk.Plan{Schema: sresp.Schema, Raw: snapshotRaw(sresp, map[string]tftypes.Value{"name": str("d"), "size_gib": tftypes.NewValue(tftypes.Number, 5), "location_id": str("l")})}
	resp := resource.CreateResponse{State: tfsdk.State{Schema: sresp.Schema, Raw: tftypes.NewValue(sresp.Schema.Type().TerraformType(context.Background()), nil)}}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &resp)
	if !resp.Diagnostics.HasError() || !resp.State.Raw.IsNull() {
		t.Fatalf("a refused create must fail without state: %v", resp.Diagnostics)
	}
}

func TestSandboxVolumeReadHandlesEveryStatusAndTheKvmKind(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantRemove bool
		wantWarn   bool
		wantError  bool
	}{
		{"attached", volShow, false, false, false},
		{"deleting", `{"volume":{"id":"vol-1","for":"sandbox","status":"deleting"}}`, true, false, false},
		{"error", `{"volume":{"id":"vol-1","name":"data","for":"sandbox","size_gib":20,"status":"error","failure_reason":"volume_size_invalid","location_id":"loc-1"}}`, false, true, false},
		{"a KVM volume", `{"volume":{"id":"vol-1","name":"data","status":"available"}}`, false, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &volumeAPI{show: tc.body}
			r, sresp := volumeFixture(t, a)
			state := tfsdk.State{Schema: sresp.Schema, Raw: snapshotRaw(sresp, map[string]tftypes.Value{"id": str("vol-1"), "name": str("data"), "location_id": str("loc-1"), "size_gib": tftypes.NewValue(tftypes.Number, 20)})}
			resp := resource.ReadResponse{State: state}
			r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
			if resp.Diagnostics.HasError() != tc.wantError {
				t.Fatalf("error = %v: %v", resp.Diagnostics.HasError(), resp.Diagnostics)
			}
			if tc.wantError {
				return
			}
			if resp.State.Raw.IsNull() != tc.wantRemove {
				t.Errorf("removed = %v; want %v", resp.State.Raw.IsNull(), tc.wantRemove)
			}
			if (resp.Diagnostics.WarningsCount() > 0) != tc.wantWarn {
				t.Errorf("warned = %v; want %v", resp.Diagnostics.WarningsCount() > 0, tc.wantWarn)
			}
		})
	}
}

func TestSandboxVolumeReadOfAMissingVolumeRemovesItAndImportReadsEverything(t *testing.T) {
	a := &volumeAPI{show: volShow, gone: true}
	r, sresp := volumeFixture(t, a)
	state := tfsdk.State{Schema: sresp.Schema, Raw: snapshotRaw(sresp, map[string]tftypes.Value{"id": str("vol-1")})}
	resp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
	if !resp.State.Raw.IsNull() {
		t.Fatal("a volume deleted outside Terraform is dropped from state")
	}

	a = &volumeAPI{show: volShow}
	r, sresp = volumeFixture(t, a)
	state = tfsdk.State{Schema: sresp.Schema, Raw: snapshotRaw(sresp, map[string]tftypes.Value{"id": str("vol-1")})}
	resp = resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
	var got sandboxVolumeModel
	resp.State.Get(context.Background(), &got)
	if got.Name.ValueString() != "data" || got.SizeGib.ValueInt64() != 20 || got.LocationID.ValueString() != "loc-1" || got.SandboxID.ValueString() != "vm-1" || got.MountPath.ValueString() != "/mnt/data" {
		t.Fatalf("imported state = %+v", got)
	}
}

func TestSandboxVolumeDeleteWaitsForGoneAndExplainsAHeldVolume(t *testing.T) {
	a := &volumeAPI{show: volShow}
	r, sresp := volumeFixture(t, a)
	state := tfsdk.State{Schema: sresp.Schema, Raw: snapshotRaw(sresp, map[string]tftypes.Value{"id": str("vol-1")})}
	resp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("delete: %v", resp.Diagnostics)
	}
	if a.requests[0] != "DELETE /api/storage/volume/vol-1" {
		t.Fatalf("first request = %s", a.requests[0])
	}

	held := &volumeAPI{show: volShow, deleteAnswers: []string{`{"success":false,"reason":"volume_in_use","message":"held"}`}, deleteStatus: []int{409}}
	r, sresp = volumeFixture(t, held)
	state = tfsdk.State{Schema: sresp.Schema, Raw: snapshotRaw(sresp, map[string]tftypes.Value{"id": str("vol-1")})}
	resp = resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "volume_in_use") {
		t.Fatalf("a held volume must fail the destroy with a hint: %v", resp.Diagnostics)
	}
}

func TestSandboxVolumeEveryArgumentForcesANewVolume(t *testing.T) {
	var sresp resource.SchemaResponse
	(&sandboxVolumeResource{}).Schema(context.Background(), resource.SchemaRequest{}, &sresp)
	for _, name := range []string{"name", "size_gib", "location_id"} {
		attrib := sresp.Schema.Attributes[name]
		if attrib == nil || !attrib.IsRequired() {
			t.Errorf("%s must be a required argument", name)
		}
	}
	// the three computed values the holder owns are never configurable
	for _, name := range []string{"status", "sandbox_id", "mount_path"} {
		if !sresp.Schema.Attributes[name].IsComputed() {
			t.Errorf("%s must be computed", name)
		}
	}
}

func TestMicrovmCreateBodyCarriesVolumesInSlotOrder(t *testing.T) {
	obj := func(id string, path types.String, ro types.Bool) attr.Value {
		return types.ObjectValueMust(microvmVolumeAttrTypes, map[string]attr.Value{
			"volume_id": types.StringValue(id), "mount_path": path, "read_only": ro,
			"noexec": types.BoolNull(), "delete_on_sandbox_delete": types.BoolValue(true),
		})
	}
	plan := microvmModel{
		LocationID: types.StringValue("loc-1"), ImageID: types.StringValue("img"), Name: types.StringValue("n"),
		Volumes: types.ListValueMust(types.ObjectType{AttrTypes: microvmVolumeAttrTypes}, []attr.Value{
			obj("v1", types.StringValue("/data"), types.BoolValue(true)),
			obj("v2", types.StringNull(), types.BoolNull()),
		}),
	}
	body, diags := microvmCreateBody(context.Background(), plan)
	if diags.HasError() {
		t.Fatal(diags)
	}
	vols, _ := body["volumes"].([]map[string]any)
	if len(vols) != 2 || vols[0]["volume_id"] != "v1" || vols[0]["mount_path"] != "/data" || vols[0]["read_only"] != true || vols[0]["delete_on_sandbox_delete"] != true {
		t.Fatalf("volumes = %#v", body["volumes"])
	}
	if _, has := vols[1]["mount_path"]; has {
		t.Errorf("an unset mount_path must not be sent: %#v", vols[1])
	}
	if _, has := vols[0]["noexec"]; has {
		t.Errorf("an unset flag must not be sent: %#v", vols[0])
	}
	plan.Volumes = types.ListNull(types.ObjectType{AttrTypes: microvmVolumeAttrTypes})
	body, _ = microvmCreateBody(context.Background(), plan)
	if _, has := body["volumes"]; has {
		t.Error("no volumes configured: no volumes key")
	}
}

func TestMicrovmVolumesFromAPIKeepsDefaultsQuietAndShowsDrift(t *testing.T) {
	ctx := context.Background()
	ot := types.ObjectType{AttrTypes: microvmVolumeAttrTypes}
	prior := types.ListValueMust(ot, []attr.Value{types.ObjectValueMust(microvmVolumeAttrTypes, map[string]attr.Value{
		"volume_id": types.StringValue("v1"), "mount_path": types.StringNull(), "read_only": types.BoolNull(), "noexec": types.BoolNull(), "delete_on_sandbox_delete": types.BoolNull(),
	})})
	api := []any{map[string]any{"id": "v1", "mount_path": "/mnt/data", "slot": float64(0), "read_only": false, "noexec": false, "delete_on_sandbox_delete": false}}

	got, diags := microvmVolumesFromAPI(ctx, api, prior)
	if diags.HasError() || !got.Equal(prior) {
		t.Fatalf("a platform default must not read as a change: %v %v", got, diags)
	}
	// a flag turned on outside Terraform is drift
	api[0].(map[string]any)["read_only"] = true
	got, _ = microvmVolumesFromAPI(ctx, api, prior)
	if got.Equal(prior) {
		t.Fatal("read_only changed outside Terraform must show")
	}
	// a volume detached outside Terraform is drift (an empty list, not null)
	got, _ = microvmVolumesFromAPI(ctx, []any{}, prior)
	if got.IsNull() || len(got.Elements()) != 0 {
		t.Fatalf("detached outside Terraform = %v", got)
	}
	// import (no prior): every field is read
	got, _ = microvmVolumesFromAPI(ctx, api, types.ListNull(ot))
	first := got.Elements()[0].(types.Object).Attributes()
	if first["mount_path"].(types.String).ValueString() != "/mnt/data" || first["read_only"].(types.Bool).ValueBool() != true {
		t.Fatalf("imported = %v", first)
	}
	// no volumes anywhere stays null; a platform without the field keeps the prior value
	got, _ = microvmVolumesFromAPI(ctx, []any{}, types.ListNull(ot))
	if !got.IsNull() {
		t.Fatal("no volumes, none configured: null")
	}
	got, _ = microvmVolumesFromAPI(ctx, nil, prior)
	if !got.Equal(prior) {
		t.Fatal("an older platform does not return volumes: keep the configuration")
	}
}

func TestSandboxVolumeCreateWithoutAnIdIsAnErrorThatNamesTheRecovery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"success":true,"volume":{"name":"data"}}`))
	}))
	defer srv.Close()
	r := &sandboxVolumeResource{client: client.New(srv.URL+"/api", "tok", time.Second, false)}
	var sresp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &sresp)
	plan := tfsdk.Plan{Schema: sresp.Schema, Raw: snapshotRaw(sresp, map[string]tftypes.Value{"name": str("d"), "size_gib": tftypes.NewValue(tftypes.Number, 5), "location_id": str("l")})}
	resp := resource.CreateResponse{State: tfsdk.State{Schema: sresp.Schema, Raw: tftypes.NewValue(sresp.Schema.Type().TerraformType(context.Background()), nil)}}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &resp)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "for=sandbox") || !resp.State.Raw.IsNull() {
		t.Fatalf("a created volume without an id must not enter state: %v", resp.Diagnostics)
	}
}

func TestSandboxVolumeReadWarnsOnErrorWithTheReason(t *testing.T) {
	a := &volumeAPI{show: `{"volume":{"id":"vol-1","name":"data","for":"sandbox","size_gib":20,"status":"error","failure_reason":"volume_size_invalid","location_id":"loc-1"}}`}
	r, sresp := volumeFixture(t, a)
	state := tfsdk.State{Schema: sresp.Schema, Raw: snapshotRaw(sresp, map[string]tftypes.Value{"id": str("vol-1")})}
	resp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
	warnings := resp.Diagnostics.Warnings()
	if len(warnings) != 1 || warnings[0].Summary() != "Sandbox volume in error" || !strings.Contains(warnings[0].Detail(), "volume_size_invalid") {
		t.Fatalf("warnings = %v", warnings)
	}
}

func TestSandboxVolumeDeleteHandlesEachAnswerOfTheDeleteCall(t *testing.T) {
	// a held volume: the hint names the way out
	held := &volumeAPI{show: volShow, deleteAnswers: []string{`{"success":false,"reason":"volume_in_use","message":"held"}`}, deleteStatus: []int{409}}
	r, sresp := volumeFixture(t, held)
	state := tfsdk.State{Schema: sresp.Schema, Raw: snapshotRaw(sresp, map[string]tftypes.Value{"id": str("vol-1")})}
	resp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "remove it from the sandbox's volumes") {
		t.Fatalf("a held volume must explain what to do: %v", resp.Diagnostics)
	}

	// already gone (404): nothing to wait for
	gone := &volumeAPI{show: volShow, deleteAnswers: []string{`{"success":false,"reason":"volume_not_found","message":"Volume not found."}`}, deleteStatus: []int{404}}
	r, sresp = volumeFixture(t, gone)
	state = tfsdk.State{Schema: sresp.Schema, Raw: snapshotRaw(sresp, map[string]tftypes.Value{"id": str("vol-1")})}
	resp = resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("a volume that is already gone is a finished delete: %v", resp.Diagnostics)
	}

	// the node was unreachable: the platform keeps the delete admitted, the next look succeeds
	flaky := &volumeAPI{show: volShow, deleteAnswers: []string{`{"success":false,"reason":"volume_node_unavailable","message":"later"}`}, deleteStatus: []int{409}}
	r, sresp = volumeFixture(t, flaky)
	state = tfsdk.State{Schema: sresp.Schema, Raw: snapshotRaw(sresp, map[string]tftypes.Value{"id": str("vol-1")})}
	resp = resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("an unreachable node is retried, not an error: %v", resp.Diagnostics)
	}
	if got := len(flaky.requests); got < 3 {
		t.Fatalf("the delete must be asked again after the unreachable node (requests: %v)", flaky.requests)
	}

	// a refusal that is none of those fails the destroy with the platform's reason
	broken := &volumeAPI{show: volShow, deleteAnswers: []string{`{"success":false,"reason":"volume_node_error","message":"boom"}`}, deleteStatus: []int{502}}
	r, sresp = volumeFixture(t, broken)
	state = tfsdk.State{Schema: sresp.Schema, Raw: snapshotRaw(sresp, map[string]tftypes.Value{"id": str("vol-1")})}
	resp = resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("an unknown refusal fails the destroy")
	}
}

func TestSandboxVolumeDeleteKeepsWaitingWhileTheVolumeIsStillThere(t *testing.T) {
	a := &volumeAPI{show: volShow, staySeen: 3}
	r, sresp := volumeFixture(t, a)
	state := tfsdk.State{Schema: sresp.Schema, Raw: snapshotRaw(sresp, map[string]tftypes.Value{"id": str("vol-1")})}
	resp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("delete: %v", resp.Diagnostics)
	}
	reads := 0
	for _, req := range a.requests {
		if req == "GET /api/storage/volume/vol-1" {
			reads++
		}
	}
	if reads < 4 {
		t.Fatalf("destroy returned after %d reads while the volume was still listed; it must wait for the 404 (requests: %v)", reads, a.requests)
	}
}

func TestMicrovmVolumesForceANewSandboxWhenTheListChanges(t *testing.T) {
	var sresp resource.SchemaResponse
	NewMicrovmResource().Schema(context.Background(), resource.SchemaRequest{}, &sresp)
	attrib, ok := sresp.Schema.Attributes["volumes"].(schema.ListNestedAttribute)
	if !ok {
		t.Fatal("volumes must be a nested list attribute")
	}
	if len(attrib.PlanModifiers) != 1 || !strings.Contains(attrib.PlanModifiers[0].Description(context.Background()), "destroy and recreate") {
		t.Fatalf("volumes must force a new MicroVM when it changes (attaching needs a stopped sandbox): %v", attrib.PlanModifiers)
	}
	if !attrib.IsOptional() || attrib.IsComputed() {
		t.Fatal("volumes is optional and not computed")
	}
}
