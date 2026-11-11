package resources

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
)

func TestMicrovmCreateBodySendsEnvMapAndOmitsDomains(t *testing.T) {
	plan := microvmModel{
		LocationID: types.StringValue("group-1"),
		ImageID:    types.StringValue("image-1"),
		Name:       types.StringValue("build-agent"),
		PlanID:     types.StringValue("plan-1"),
		Env: types.MapValueMust(types.StringType, map[string]attr.Value{
			"MODE": types.StringValue("test"),
		}),
		Domains: types.SetValueMust(types.StringType, []attr.Value{types.StringValue("build.example.test")}),
	}
	body, diags := microvmCreateBody(context.Background(), plan)
	if diags.HasError() {
		t.Fatalf("microvmCreateBody: %v", diags)
	}
	if body["location_id"] != "group-1" || body["image_id"] != "image-1" || body["plan_id"] != "plan-1" {
		t.Fatalf("body = %#v", body)
	}
	for _, key := range []string{"domains", "domain"} {
		if _, exists := body[key]; exists {
			t.Errorf("create body must not include separately reconciled %s", key)
		}
	}
	// 3381: env is applied by POST /microvm/vms as an object map.
	env, ok := body["env"].(map[string]string)
	if !ok || env["MODE"] != "test" {
		t.Errorf("create body env = %#v; want the object map {MODE: test}", body["env"])
	}
}

// TestMicrovmCreateBodyLegacyAliasSendsCanonicalLocationID proves a plan
// configured with only the deprecated hypervisor_group_id alias still sends
// the canonical location_id key on the wire (spec 17 C4, LOC-3 pattern).
func TestMicrovmCreateBodyLegacyAliasSendsCanonicalLocationID(t *testing.T) {
	plan := microvmModel{
		HypervisorGroupID: types.StringValue("group-legacy"),
		ImageID:           types.StringValue("image-1"),
		Name:              types.StringValue("build-agent"),
	}
	body, diags := microvmCreateBody(context.Background(), plan)
	if diags.HasError() {
		t.Fatalf("microvmCreateBody: %v", diags)
	}
	if body["location_id"] != "group-legacy" {
		t.Fatalf("body[location_id] = %v; want group-legacy", body["location_id"])
	}
	if _, exists := body["hypervisor_group_id"]; exists {
		t.Errorf("create body must never send the deprecated hypervisor_group_id key")
	}
}

// TestMicrovmCreateBodySendsSshKeyIDsAndStaticIpID proves ssh_key_ids and a
// public network entry's static_ip_id (C3/C8.3) reach the create body.
func TestMicrovmCreateBodySendsSshKeyIDsAndStaticIpID(t *testing.T) {
	networkType := types.ObjectType{AttrTypes: map[string]attr.Type{
		"kind":               types.StringType,
		"subnet_id":          types.StringType,
		"vpc_subnet_id":      types.StringType,
		"static_ip_id":       types.StringType,
		"security_group_ids": types.ListType{ElemType: types.StringType},
		"rate_mbit":          types.Int64Type,
	}}
	entry, diags := types.ObjectValue(networkType.AttrTypes, map[string]attr.Value{
		"kind":               types.StringValue("public"),
		"subnet_id":          types.StringNull(),
		"vpc_subnet_id":      types.StringNull(),
		"static_ip_id":       types.StringValue("static-ip-1"),
		"security_group_ids": types.ListNull(types.StringType),
		"rate_mbit":          types.Int64Null(),
	})
	if diags.HasError() {
		t.Fatalf("building network entry: %v", diags)
	}
	network, diags := types.ListValue(networkType, []attr.Value{entry})
	if diags.HasError() {
		t.Fatalf("building network list: %v", diags)
	}
	plan := microvmModel{
		LocationID: types.StringValue("group-1"),
		ImageID:    types.StringValue("image-1"),
		Name:       types.StringValue("build-agent"),
		SshKeyIDs:  types.ListValueMust(types.StringType, []attr.Value{types.StringValue("key-1"), types.StringValue("key-2")}),
		Network:    network,
	}
	body, diags := microvmCreateBody(context.Background(), plan)
	if diags.HasError() {
		t.Fatalf("microvmCreateBody: %v", diags)
	}
	ids, ok := body["ssh_key_ids"].([]string)
	if !ok || len(ids) != 2 || ids[0] != "key-1" || ids[1] != "key-2" {
		t.Fatalf("ssh_key_ids = %#v", body["ssh_key_ids"])
	}
	networkBody, ok := body["network"].([]map[string]any)
	if !ok || len(networkBody) != 1 {
		t.Fatalf("network = %#v", body["network"])
	}
	if networkBody[0]["static_ip_id"] != "static-ip-1" {
		t.Fatalf("network[0][static_ip_id] = %v; want static-ip-1", networkBody[0]["static_ip_id"])
	}
}

func TestMicrovmDomainReconciliationUsesDomainIDsForDelete(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.EscapedPath())
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"success":true,"domain":{"id":"domain-2","hostname":"new.example.test"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer server.Close()

	res := &microvmResource{client: client.New(server.URL+"/api", "token", 0, false)}
	desired := types.SetValueMust(types.StringType, []attr.Value{types.StringValue("new.example.test")})
	current := []map[string]any{{"id": "domain-1", "hostname": "old.example.test"}}
	if err := res.reconcileDomains(context.Background(), "vm-1", desired, current); err != nil {
		t.Fatalf("reconcileDomains: %v", err)
	}
	want := map[string]bool{
		"DELETE /api/microvm/vm/vm-1/domain/domain-1": true,
		"POST /api/microvm/vm/vm-1/domain":            true,
	}
	if len(requests) != 2 || !want[requests[0]] || !want[requests[1]] {
		t.Fatalf("requests = %v", requests)
	}
}

func TestMicrovmSchemaProtectsEnvironment(t *testing.T) {
	res := NewMicrovmResource()
	var resp resource.SchemaResponse
	res.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	if !resp.Schema.Attributes["env"].IsSensitive() {
		t.Fatal("env must be sensitive")
	}
	if resp.Schema.Attributes["domains"].IsSensitive() {
		t.Fatal("domains must not be sensitive")
	}
}

// TestMicrovmSchemaLocationIDIsCanonicalAndOptionalComputed pins the LOC-3
// shape: both location_id and the deprecated hypervisor_group_id are
// Optional+Computed (never Required - the API accepts either), and
// hypervisor_group_id carries a DeprecationMessage.
func TestMicrovmSchemaLocationIDIsCanonicalAndOptionalComputed(t *testing.T) {
	res := NewMicrovmResource()
	var resp resource.SchemaResponse
	res.Schema(context.Background(), resource.SchemaRequest{}, &resp)

	locationID := resp.Schema.Attributes["location_id"]
	if locationID == nil || locationID.IsRequired() || !locationID.IsOptional() || !locationID.IsComputed() {
		t.Fatalf("location_id must be Optional+Computed, never Required: %#v", locationID)
	}
	legacy := resp.Schema.Attributes["hypervisor_group_id"]
	if legacy == nil || legacy.IsRequired() || !legacy.IsOptional() || !legacy.IsComputed() {
		t.Fatalf("hypervisor_group_id must be Optional+Computed, never Required: %#v", legacy)
	}
	if legacy.GetDeprecationMessage() == "" {
		t.Fatal("hypervisor_group_id must carry a DeprecationMessage")
	}
}

// TestMicrovmSchemaNetworkKindRejectsIsolated pins C1: isolated is not a
// valid network kind - the schema validator must only accept public and vpc,
// and the schema must expose static_ip_id (C8.3).
func TestMicrovmSchemaNetworkKindRejectsIsolated(t *testing.T) {
	res := NewMicrovmResource()
	var resp resource.SchemaResponse
	res.Schema(context.Background(), resource.SchemaRequest{}, &resp)

	network, ok := resp.Schema.Attributes["network"].(schema.ListNestedAttribute)
	if !ok {
		t.Fatalf("network attribute is not a ListNestedAttribute: %T", resp.Schema.Attributes["network"])
	}
	kind, ok := network.NestedObject.Attributes["kind"].(schema.StringAttribute)
	if !ok {
		t.Fatalf("network.kind is not a StringAttribute: %T", network.NestedObject.Attributes["kind"])
	}
	req := validator.StringRequest{ConfigValue: types.StringValue("isolated")}
	var validateResp validator.StringResponse
	for _, v := range kind.Validators {
		v.ValidateString(context.Background(), req, &validateResp)
	}
	if !validateResp.Diagnostics.HasError() {
		t.Fatal("network.kind must reject isolated (C1: isolated is not a valid network kind)")
	}
	if _, ok := network.NestedObject.Attributes["static_ip_id"]; !ok {
		t.Fatal("network must expose static_ip_id (C8.3)")
	}
	if subnetID, ok := network.NestedObject.Attributes["subnet_id"].(schema.StringAttribute); !ok || subnetID.IsRequired() {
		t.Fatal("network.subnet_id must stay optional (C8.1: the platform auto-assigns a public subnet)")
	}
}

// envMaster selects how the fake Master treats env on create.
type envMaster int

const (
	// envCurrent applies an object map on create (3381) and reports its key names.
	envCurrent envMaster = iota
	// envOlder is the real pre-3381 Master: CreateMicrovmRequest validated env as
	// 'nullable|string', so an object map is a 422 with errors.env and creates no row; a
	// string would be accepted and ignored.
	envOlder
	// envSilent accepts the map but never reports its keys (defence in depth for the key check).
	envSilent
)

type microvmEnvFake struct {
	mode        envMaster
	createCalls int
	rows        int
	createEnv   []any // env of every create request, in order (nil when absent)
	putEnv      map[string]any
	putCalls    int
	storedKeys  []string
}

func (f *microvmEnvFake) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /microvm/vms", func(w http.ResponseWriter, req *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(req.Body).Decode(&body)
		f.createCalls++
		f.createEnv = append(f.createEnv, body["env"])
		env, hasEnv := body["env"]
		if _, isMap := env.(map[string]any); hasEnv && isMap && f.mode == envOlder {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"The env field must be a string.","errors":{"env":["The env field must be a string."]}}`))
			return
		}
		if m, isMap := env.(map[string]any); isMap && f.mode == envCurrent {
			for k := range m {
				f.storedKeys = append(f.storedKeys, k)
			}
		}
		f.rows++
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"success":true,"microvm":{"id":"vm-1","name":"n","state":"creating","location_id":"loc-1"}}`))
	})
	mux.HandleFunc("GET /microvm/vm/vm-1", func(w http.ResponseWriter, req *http.Request) {
		keys, _ := json.Marshal(append([]string{}, f.storedKeys...))
		_, _ = w.Write([]byte(`{"success":true,"microvm":{"id":"vm-1","name":"n","state":"running","location_id":"loc-1"},"domains":[],"env_keys":` + string(keys) + `}`))
	})
	mux.HandleFunc("PUT /microvm/vm/vm-1/env", func(w http.ResponseWriter, req *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(req.Body).Decode(&body)
		f.putCalls++
		f.putEnv, _ = body["env"].(map[string]any)
		f.storedKeys = nil
		for k := range f.putEnv {
			f.storedKeys = append(f.storedKeys, k)
		}
		_, _ = w.Write([]byte(`{"success":true,"keys":[]}`))
	})
	return mux
}

func runMicrovmEnvCreate(t *testing.T, f *microvmEnvFake) {
	t.Helper()
	server := httptest.NewServer(f.handler(t))
	defer server.Close()
	r := &microvmResource{client: client.New(server.URL, "token", time.Second, false)}
	s := credentialSchema(t, r)
	model := microvmModel{
		LocationID: types.StringValue("loc-1"),
		ImageID:    types.StringValue("img-1"),
		Name:       types.StringValue("n"),
		Env: types.MapValueMust(types.StringType, map[string]attr.Value{
			"API_KEY": types.StringValue("  keep  "),
			"MODE":    types.StringValue("test"),
		}),
		SecurityGroupIDs: types.SetNull(types.StringType),
		Metadata:         types.MapNull(types.StringType),
		SshKeyIDs:        types.ListNull(s.Attributes["ssh_key_ids"].GetType().(types.ListType).ElemType),
		Ingress:          types.ObjectNull(s.Attributes["ingress"].GetType().(types.ObjectType).AttrTypes),
		EgressPolicy:     types.ObjectNull(s.Attributes["egress_policy"].GetType().(types.ObjectType).AttrTypes),
		Network:          types.ListNull(s.Attributes["network"].GetType().(types.ListType).ElemType),
		Domains:          types.SetNull(types.StringType),
		Interfaces:       types.ListNull(s.Attributes["interfaces"].GetType().(types.ListType).ElemType),
	}
	out := resource.CreateResponse{State: credentialNullState(s)}
	r.Create(context.Background(), resource.CreateRequest{Plan: credentialPlan(t, s, model)}, &out)
	if out.Diagnostics.HasError() {
		t.Fatal(out.Diagnostics)
	}
}

// (b) The current Master takes the map on create: one create, no PUT.
func TestMicrovmCreateWithCurrentMasterSendsOneCreateAndNoPut(t *testing.T) {
	f := &microvmEnvFake{mode: envCurrent}
	runMicrovmEnvCreate(t, f)
	env, ok := f.createEnv[0].(map[string]any)
	if !ok || env["API_KEY"] != "  keep  " || env["MODE"] != "test" {
		t.Fatalf("create body env = %#v; want the object map", f.createEnv[0])
	}
	if f.createCalls != 1 || f.rows != 1 || f.putCalls != 0 {
		t.Errorf("creates=%d rows=%d puts=%d; want 1, 1, 0", f.createCalls, f.rows, f.putCalls)
	}
}

// (a) The real older Master refuses a map env with 422 errors.env (no row). The create is
// retried once without env and the variables are set through PUT, the pre-3381 sequence.
func TestMicrovmCreateAgainstOlderMasterRetriesWithoutEnvThenPuts(t *testing.T) {
	f := &microvmEnvFake{mode: envOlder}
	runMicrovmEnvCreate(t, f)
	if f.createCalls != 2 {
		t.Fatalf("create calls = %d; want 2 (refused, then retried)", f.createCalls)
	}
	if f.createEnv[0] == nil {
		t.Errorf("first create must carry env; got %#v", f.createEnv[0])
	}
	if f.createEnv[1] != nil {
		t.Errorf("retried create must not carry env; got %#v", f.createEnv[1])
	}
	if f.rows != 1 {
		t.Errorf("rows = %d; want exactly one sandbox (the 422 creates none)", f.rows)
	}
	if f.putCalls != 1 || f.putEnv["API_KEY"] != "  keep  " || f.putEnv["MODE"] != "test" {
		t.Errorf("puts=%d env=%#v; want one PUT with the full configured map", f.putCalls, f.putEnv)
	}
}

// A Master that accepts the map but does not report its keys still ends with the variables set.
func TestMicrovmCreateWhenKeysAreNotReportedFallsBackToPut(t *testing.T) {
	f := &microvmEnvFake{mode: envSilent}
	runMicrovmEnvCreate(t, f)
	if f.createCalls != 1 || f.putCalls != 1 {
		t.Errorf("creates=%d puts=%d; want 1 and 1", f.createCalls, f.putCalls)
	}
}

// A 422 about another field, or one that names env plus another field, is not retried.
func TestIsEnvOnlyValidationError(t *testing.T) {
	mk := func(status int, fields map[string][]string) error {
		return &client.APIError{Status: status, FieldErrors: fields}
	}
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"env only", mk(422, map[string][]string{"env": {"x"}}), true},
		{"env nested", mk(422, map[string][]string{"env.KEY": {"x"}}), true},
		{"env and name", mk(422, map[string][]string{"env": {"x"}, "name": {"y"}}), false},
		{"other field", mk(422, map[string][]string{"name": {"y"}}), false},
		{"not 422", mk(500, map[string][]string{"env": {"x"}}), false},
		{"no fields", mk(422, nil), false},
		{"plain error", errors.New("boom"), false},
	}
	for _, c := range cases {
		if got := isEnvOnlyValidationError(c.err); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

// SBX-SG-ATTACH: security groups apply to the whole sandbox. The top-level
// attribute is sent on create, diffed into attach/detach calls on update, and
// read back from the API's security_groups list.
func TestMicrovmCreateBodySendsTopLevelSecurityGroupIDs(t *testing.T) {
	plan := microvmModel{
		LocationID:       types.StringValue("group-1"),
		ImageID:          types.StringValue("image-1"),
		Name:             types.StringValue("fw"),
		SecurityGroupIDs: types.SetValueMust(types.StringType, []attr.Value{types.StringValue("sg-1")}),
	}
	body, diags := microvmCreateBody(context.Background(), plan)
	if diags.HasError() {
		t.Fatalf("microvmCreateBody: %v", diags)
	}
	ids, ok := body["security_group_ids"].([]string)
	if !ok || len(ids) != 1 || ids[0] != "sg-1" {
		t.Fatalf("security_group_ids = %#v", body["security_group_ids"])
	}

	plan.SecurityGroupIDs = types.SetUnknown(types.StringType)
	body, _ = microvmCreateBody(context.Background(), plan)
	if _, exists := body["security_group_ids"]; exists {
		t.Error("an unknown (not configured) set must not be sent")
	}
}

func TestMicrovmSecurityGroupReconciliationAttachesAndDetaches(t *testing.T) {
	var requests []string
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.EscapedPath())
		buf := make([]byte, 256)
		n, _ := r.Body.Read(buf)
		bodies = append(bodies, string(buf[:n]))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"sandbox_ids":["vm-1"]}`))
	}))
	defer server.Close()

	res := &microvmResource{client: client.New(server.URL+"/api", "token", 0, false)}
	desired := types.SetValueMust(types.StringType, []attr.Value{types.StringValue("sg-keep"), types.StringValue("sg-new")})
	current := types.SetValueMust(types.StringType, []attr.Value{types.StringValue("sg-keep"), types.StringValue("sg-old")})
	if err := res.reconcileSecurityGroups(context.Background(), "vm-1", desired, current); err != nil {
		t.Fatalf("reconcileSecurityGroups: %v", err)
	}
	if len(requests) != 2 || requests[0] != "POST /api/security-group/sg-old/detach-sandboxes" || requests[1] != "POST /api/security-group/sg-new/attach-sandboxes" {
		t.Fatalf("requests = %v", requests)
	}
	for _, b := range bodies {
		if b != `{"sandbox_ids":["vm-1"]}` {
			t.Errorf("body = %s", b)
		}
	}

	requests = nil
	if err := res.reconcileSecurityGroups(context.Background(), "vm-1", types.SetNull(types.StringType), current); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 0 {
		t.Fatalf("a null plan must leave the groups alone, got %v", requests)
	}
}

func TestMicrovmStateReadsSecurityGroupsFromTheAPI(t *testing.T) {
	prior := microvmModel{SecurityGroupIDs: types.SetUnknown(types.StringType)}
	obj := map[string]any{
		"id":              "vm-1",
		"security_groups": []any{map[string]any{"id": "sg-1", "name": "web"}, map[string]any{"id": "sg-2", "name": "db"}},
	}
	var diags diag.Diagnostics
	state := microvmStateFromAPI(context.Background(), obj, nil, prior, &diags)
	if diags.HasError() {
		t.Fatalf("diags: %v", diags)
	}
	var got []string
	state.SecurityGroupIDs.ElementsAs(context.Background(), &got, false)
	if len(got) != 2 {
		t.Fatalf("security_group_ids = %v", got)
	}

	// An API without the field must not leave a computed value unknown.
	delete(obj, "security_groups")
	state = microvmStateFromAPI(context.Background(), obj, nil, prior, &diags)
	if state.SecurityGroupIDs.IsUnknown() {
		t.Error("security_group_ids stayed unknown after apply")
	}
}

// SBX-LATENCY: last_resume_mode, last_resume_reason and last_resumed_at are
// read-only attributes fed from the user API sandbox read.
func TestMicrovmSchemaResumeFieldsAreComputedReadOnly(t *testing.T) {
	var resp resource.SchemaResponse
	NewMicrovmResource().Schema(context.Background(), resource.SchemaRequest{}, &resp)
	for _, name := range []string{"last_resume_mode", "last_resume_reason", "last_resumed_at"} {
		attr, ok := resp.Schema.Attributes[name].(schema.StringAttribute)
		if !ok {
			t.Fatalf("%s missing or not a string attribute", name)
		}
		if !attr.Computed || attr.Optional || attr.Required {
			t.Errorf("%s must be computed only: %+v", name, attr)
		}
	}
}

func TestMicrovmStateFromAPIMapsResumeFields(t *testing.T) {
	var diags diag.Diagnostics
	obj := map[string]any{
		"id": "vm-1", "name": "n", "state": "running",
		"last_resume_mode": "fresh_boot", "last_resume_reason": "snapshot_missing", "last_resumed_at": "2026-11-10T09:30:00.000000Z",
	}
	st := microvmStateFromAPI(context.Background(), obj, nil, microvmModel{}, &diags)
	if st.LastResumeMode.ValueString() != "fresh_boot" || st.LastResumeReason.ValueString() != "snapshot_missing" || st.LastResumedAt.ValueString() != "2026-11-10T09:30:00.000000Z" {
		t.Fatalf("resume fields = %v %v %v", st.LastResumeMode, st.LastResumeReason, st.LastResumedAt)
	}

	// Never resumed: the API answers null for all three.
	obj["last_resume_mode"], obj["last_resume_reason"], obj["last_resumed_at"] = nil, nil, nil
	st = microvmStateFromAPI(context.Background(), obj, nil, microvmModel{}, &diags)
	if !st.LastResumeMode.IsNull() || !st.LastResumedAt.IsNull() {
		t.Fatalf("null from the API must stay null: %v %v", st.LastResumeMode, st.LastResumedAt)
	}

	// A Master that predates the fields omits the keys; an unknown prior must not stay unknown.
	delete(obj, "last_resume_mode")
	delete(obj, "last_resume_reason")
	delete(obj, "last_resumed_at")
	prior := microvmModel{LastResumeMode: types.StringUnknown(), LastResumeReason: types.StringUnknown(), LastResumedAt: types.StringUnknown()}
	st = microvmStateFromAPI(context.Background(), obj, nil, prior, &diags)
	if st.LastResumeMode.IsUnknown() || st.LastResumeReason.IsUnknown() || st.LastResumedAt.IsUnknown() {
		t.Fatalf("unknown must resolve to null after apply: %v %v %v", st.LastResumeMode, st.LastResumeReason, st.LastResumedAt)
	}
	if diags.HasError() {
		t.Fatalf("diags: %v", diags)
	}
}
