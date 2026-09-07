package resources_test

// Resource-layer tests for iaas_microvm_app.
//
// Same shape as microvm_api_key_test.go: the mock-backed resource.UnitTest
// runs (certificate_test.go) need a terraform/opentofu binary on PATH, this
// environment has none and a skipped test cannot pin behaviour, so these
// tests drive the resource's Create/Read/Update/Delete and Schema directly
// through the exported framework interfaces against canned httptest
// responses - same canned-API spirit as certificate_test.go, deterministic
// everywhere.
//
// The helpers nullState and hasStringPlanModifier are shared with
// microvm_api_key_test.go (same external test package) and reused here.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
	"github.com/hypervisor-io/terraform-provider-iaas/internal/resources"
)

// microvmAppTestModel mirrors the unexported resource model 1:1 (same tfsdk
// tags) so the external test package can build plans and decode state.
type microvmAppTestModel struct {
	ID                 types.String `tfsdk:"id"`
	HypervisorGroupID  types.String `tfsdk:"hypervisor_group_id"`
	Slug               types.String `tfsdk:"slug"`
	SourceKind         types.String `tfsdk:"source_kind"`
	SourceImage        types.String `tfsdk:"source_image"`
	SourceRepo         types.String `tfsdk:"source_repo"`
	SourceBranch       types.String `tfsdk:"source_branch"`
	Port               types.Int64  `tfsdk:"port"`
	MinInstances       types.Int64  `tfsdk:"min_instances"`
	IdleTimeoutSeconds types.Int64  `tfsdk:"idle_timeout_seconds"`
	HealthPath         types.String `tfsdk:"health_path"`
	Fqdn               types.String `tfsdk:"fqdn"`
	State              types.String `tfsdk:"state"`
}

// newMicrovmAppTestResource builds the resource via its exported constructor,
// runs Schema + Configure against a client pointed at srv, and returns the
// configured resource plus its schema (needed to build tfsdk.Plan/State
// values).
func newMicrovmAppTestResource(t *testing.T, srvURL string) (resource.Resource, schema.Schema) {
	t.Helper()
	ctx := context.Background()

	res := resources.NewMicrovmAppResource()

	var schemaResp resource.SchemaResponse
	res.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("Schema returned errors: %v", schemaResp.Diagnostics)
	}

	c := client.New(srvURL+"/api", "tok", 10*time.Second, false)
	configurable, ok := res.(resource.ResourceWithConfigure)
	if !ok {
		t.Fatal("resource does not implement resource.ResourceWithConfigure")
	}
	var cfgResp resource.ConfigureResponse
	configurable.Configure(ctx, resource.ConfigureRequest{ProviderData: c}, &cfgResp)
	if cfgResp.Diagnostics.HasError() {
		t.Fatalf("Configure returned errors: %v", cfgResp.Diagnostics)
	}

	return res, schemaResp.Schema
}

// microvmAppPlan builds a plan value from a model (the framework hands Create
// a plan with the computed attributes unknown; Update hands it a plan with a
// known id and unknown computed attributes).
func microvmAppPlan(t *testing.T, sch schema.Schema, m microvmAppTestModel) tfsdk.Plan {
	t.Helper()
	plan := tfsdk.Plan{Schema: sch}
	diags := plan.Set(context.Background(), m)
	if diags.HasError() {
		t.Fatalf("building plan: %v", diags)
	}
	return plan
}

// microvmAppPriorState builds a known prior state from a model.
func microvmAppPriorState(t *testing.T, sch schema.Schema, m microvmAppTestModel) tfsdk.State {
	t.Helper()
	state := tfsdk.State{Schema: sch}
	diags := state.Set(context.Background(), m)
	if diags.HasError() {
		t.Fatalf("building state: %v", diags)
	}
	return state
}

// microvmAppOciModel is the fully-configured oci create model the tests start
// from; callers override individual fields.
func microvmAppOciModel() microvmAppTestModel {
	return microvmAppTestModel{
		ID:                 types.StringUnknown(),
		HypervisorGroupID:  types.StringValue("grp-1"),
		Slug:               types.StringValue("demo"),
		SourceKind:         types.StringValue("oci"),
		SourceImage:        types.StringValue("ghcr.io/acme/demo:latest"),
		SourceRepo:         types.StringNull(),
		SourceBranch:       types.StringNull(),
		Port:               types.Int64Value(8080),
		MinInstances:       types.Int64Value(0),
		IdleTimeoutSeconds: types.Int64Value(300),
		HealthPath:         types.StringValue("/"),
		Fqdn:               types.StringUnknown(),
		State:              types.StringUnknown(),
	}
}

// ---------------------------------------------------------------------------
// Create - pins the POST /microvm/apps body mapping and state capture.
// ---------------------------------------------------------------------------

// TestUnitMicrovmApp_CreateSendsFullBody verifies Create posts the full body
// (hypervisor_group_id, slug, source_kind, source{image}, port,
// min_instances, idle_timeout_seconds, health_path) to POST
// /api/microvm/apps and captures the computed id/fqdn/state from the
// response into state.
func TestUnitMicrovmApp_CreateSendsFullBody(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"success":true,"app":{"id":"a1","slug":"demo","fqdn":"demo.apps.example.test","state":"building"}}`))
	}))
	defer srv.Close()

	res, sch := newMicrovmAppTestResource(t, srv.URL)

	ctx := context.Background()
	resp := resource.CreateResponse{State: nullState(ctx, sch)}
	res.Create(ctx, resource.CreateRequest{Plan: microvmAppPlan(t, sch, microvmAppOciModel())}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Create returned errors: %v", resp.Diagnostics)
	}

	if gotMethod != http.MethodPost || gotPath != "/api/microvm/apps" {
		t.Errorf("request = %s %s; want POST /api/microvm/apps", gotMethod, gotPath)
	}
	if gotBody["hypervisor_group_id"] != "grp-1" {
		t.Errorf("body[hypervisor_group_id] = %v; want grp-1", gotBody["hypervisor_group_id"])
	}
	if gotBody["slug"] != "demo" {
		t.Errorf("body[slug] = %v; want demo", gotBody["slug"])
	}
	if gotBody["source_kind"] != "oci" {
		t.Errorf("body[source_kind] = %v; want oci", gotBody["source_kind"])
	}
	source, ok := gotBody["source"].(map[string]any)
	if !ok {
		t.Fatalf("body[source] = %v; want an object", gotBody["source"])
	}
	if source["image"] != "ghcr.io/acme/demo:latest" {
		t.Errorf("body[source][image] = %v; want ghcr.io/acme/demo:latest", source["image"])
	}
	if gotBody["port"] != float64(8080) {
		t.Errorf("body[port] = %v; want 8080", gotBody["port"])
	}
	if gotBody["min_instances"] != float64(0) {
		t.Errorf("body[min_instances] = %v; want 0", gotBody["min_instances"])
	}
	if gotBody["idle_timeout_seconds"] != float64(300) {
		t.Errorf("body[idle_timeout_seconds] = %v; want 300", gotBody["idle_timeout_seconds"])
	}
	if gotBody["health_path"] != "/" {
		t.Errorf("body[health_path] = %v; want /", gotBody["health_path"])
	}

	var got microvmAppTestModel
	if diags := resp.State.Get(ctx, &got); diags.HasError() {
		t.Fatalf("decoding state: %v", diags)
	}
	if got.ID.ValueString() != "a1" {
		t.Errorf("state id = %q; want a1", got.ID.ValueString())
	}
	if got.Fqdn.ValueString() != "demo.apps.example.test" {
		t.Errorf("state fqdn = %q; want demo.apps.example.test", got.Fqdn.ValueString())
	}
	if got.State.ValueString() != "building" {
		t.Errorf("state state = %q; want building", got.State.ValueString())
	}
	if got.Slug.ValueString() != "demo" || got.Port.ValueInt64() != 8080 {
		t.Errorf("state lost plan values: slug=%q port=%d", got.Slug.ValueString(), got.Port.ValueInt64())
	}
}

// TestUnitMicrovmApp_CreateGitSourceMapsRepoBranch verifies a git-source
// create sends source{repo,branch} and never an image key.
func TestUnitMicrovmApp_CreateGitSourceMapsRepoBranch(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"success":true,"app":{"id":"a2","fqdn":"from-git.apps.example.test","state":"building"}}`))
	}))
	defer srv.Close()

	res, sch := newMicrovmAppTestResource(t, srv.URL)

	m := microvmAppOciModel()
	m.SourceKind = types.StringValue("git")
	m.SourceImage = types.StringNull()
	m.SourceRepo = types.StringValue("https://github.com/acme/demo.git")
	m.SourceBranch = types.StringValue("develop")

	ctx := context.Background()
	resp := resource.CreateResponse{State: nullState(ctx, sch)}
	res.Create(ctx, resource.CreateRequest{Plan: microvmAppPlan(t, sch, m)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Create returned errors: %v", resp.Diagnostics)
	}

	source, ok := gotBody["source"].(map[string]any)
	if !ok {
		t.Fatalf("body[source] = %v; want an object", gotBody["source"])
	}
	if source["repo"] != "https://github.com/acme/demo.git" {
		t.Errorf("body[source][repo] = %v; want https://github.com/acme/demo.git", source["repo"])
	}
	if source["branch"] != "develop" {
		t.Errorf("body[source][branch] = %v; want develop", source["branch"])
	}
	if _, present := source["image"]; present {
		t.Errorf("git-source body[source] must NOT include image; got %v", source)
	}
}

// TestUnitMicrovmApp_CreateOmitsUnsetOptionals verifies unconfigured optional
// attributes are OMITTED from the create body so the server-side defaults
// apply: the merged MV2-25 CreateAppRequest validates port as min:1 and
// idle_timeout_seconds as min:30, so sending the zero value for an unset
// attribute would be rejected with a 422 instead of falling back to the
// documented defaults (port 8080, idle 300s).
func TestUnitMicrovmApp_CreateOmitsUnsetOptionals(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"success":true,"app":{"id":"a3","fqdn":"minimal.apps.example.test","state":"building"}}`))
	}))
	defer srv.Close()

	res, sch := newMicrovmAppTestResource(t, srv.URL)

	m := microvmAppOciModel()
	m.Port = types.Int64Null()
	m.MinInstances = types.Int64Null()
	m.IdleTimeoutSeconds = types.Int64Null()
	m.HealthPath = types.StringNull()

	ctx := context.Background()
	resp := resource.CreateResponse{State: nullState(ctx, sch)}
	res.Create(ctx, resource.CreateRequest{Plan: microvmAppPlan(t, sch, m)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Create returned errors: %v", resp.Diagnostics)
	}

	for _, absent := range []string{"port", "min_instances", "idle_timeout_seconds", "health_path"} {
		if _, present := gotBody[absent]; present {
			t.Errorf("create body must NOT include unconfigured %q; got %v", absent, gotBody)
		}
	}
	source, ok := gotBody["source"].(map[string]any)
	if !ok {
		t.Fatalf("body[source] = %v; want an object", gotBody["source"])
	}
	if source["image"] != "ghcr.io/acme/demo:latest" {
		t.Errorf("body[source][image] = %v; want ghcr.io/acme/demo:latest", source["image"])
	}
	if _, present := source["branch"]; present {
		t.Errorf("body[source] must NOT include unconfigured branch; got %v", source)
	}
}

// ---------------------------------------------------------------------------
// Read - pins the SHOW refresh and the 404 removal.
// ---------------------------------------------------------------------------

// TestUnitMicrovmApp_ReadRefreshesComputed verifies Read fetches
// GET /api/microvm/app/{id} and refreshes the computed fqdn/state from the
// response.
func TestUnitMicrovmApp_ReadRefreshesComputed(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"app":{"id":"a1","slug":"demo","fqdn":"demo.apps.example.test","state":"running"},"revisions":[]}`))
	}))
	defer srv.Close()

	res, sch := newMicrovmAppTestResource(t, srv.URL)

	prior := microvmAppOciModel()
	prior.ID = types.StringValue("a1")
	prior.Fqdn = types.StringValue("stale.apps.example.test")
	prior.State = types.StringValue("building")

	ctx := context.Background()
	resp := resource.ReadResponse{State: nullState(ctx, sch)}
	res.Read(ctx, resource.ReadRequest{State: microvmAppPriorState(t, sch, prior)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Read returned errors: %v", resp.Diagnostics)
	}

	if gotMethod != http.MethodGet || gotPath != "/api/microvm/app/a1" {
		t.Errorf("request = %s %s; want GET /api/microvm/app/a1", gotMethod, gotPath)
	}

	var got microvmAppTestModel
	if diags := resp.State.Get(ctx, &got); diags.HasError() {
		t.Fatalf("decoding state: %v", diags)
	}
	if got.Fqdn.ValueString() != "demo.apps.example.test" {
		t.Errorf("state fqdn = %q; want refreshed demo.apps.example.test", got.Fqdn.ValueString())
	}
	if got.State.ValueString() != "running" {
		t.Errorf("state state = %q; want refreshed running", got.State.ValueString())
	}
}

// TestUnitMicrovmApp_ReadRemovesOn404 verifies a 404 from the SHOW endpoint
// (app deleted out of band) removes the resource from state via the shared
// client.IsNotFound classification.
func TestUnitMicrovmApp_ReadRemovesOn404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer srv.Close()

	res, sch := newMicrovmAppTestResource(t, srv.URL)

	prior := microvmAppOciModel()
	prior.ID = types.StringValue("a1")
	prior.Fqdn = types.StringValue("demo.apps.example.test")
	prior.State = types.StringValue("running")

	ctx := context.Background()
	resp := resource.ReadResponse{State: nullState(ctx, sch)}
	res.Read(ctx, resource.ReadRequest{State: microvmAppPriorState(t, sch, prior)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Read returned errors: %v", resp.Diagnostics)
	}
	if !resp.State.Raw.IsNull() {
		t.Error("expected the resource to be removed from state, but state is non-null")
	}
}

// ---------------------------------------------------------------------------
// Update - pins the single-path create-and-deploy flow.
// ---------------------------------------------------------------------------

// TestUnitMicrovmApp_UpdateRerunsCreateAndDeployPath verifies Update re-runs
// the create-and-deploy path: a tracked field change (here port) issues a
// fresh POST /api/microvm/apps (the server creates the revision and
// dispatches its build in the same call) and state is rebuilt from that
// response. Only that single endpoint may be hit.
func TestUnitMicrovmApp_UpdateRerunsCreateAndDeployPath(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		if r.Method == http.MethodPost && r.URL.Path == "/api/microvm/apps" {
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"success":true,"app":{"id":"a1","slug":"demo","fqdn":"demo.apps.example.test","state":"building"}}`))
			return
		}
		t.Errorf("unexpected request during Update: %s %s (only the create-and-deploy POST is allowed)", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	res, sch := newMicrovmAppTestResource(t, srv.URL)

	prior := microvmAppOciModel()
	prior.ID = types.StringValue("a1")
	prior.Fqdn = types.StringValue("demo.apps.example.test")
	prior.State = types.StringValue("running")

	plan := microvmAppOciModel()
	plan.ID = types.StringValue("a1")
	plan.Port = types.Int64Value(9090)

	ctx := context.Background()
	resp := resource.UpdateResponse{State: nullState(ctx, sch)}
	res.Update(ctx, resource.UpdateRequest{
		Plan:  microvmAppPlan(t, sch, plan),
		State: microvmAppPriorState(t, sch, prior),
	}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Update returned errors: %v", resp.Diagnostics)
	}

	if gotMethod != http.MethodPost || gotPath != "/api/microvm/apps" {
		t.Errorf("request = %s %s; want POST /api/microvm/apps (single-path create-and-deploy)", gotMethod, gotPath)
	}
	if gotBody["port"] != float64(9090) {
		t.Errorf("body[port] = %v; want the changed 9090", gotBody["port"])
	}

	var got microvmAppTestModel
	if diags := resp.State.Get(ctx, &got); diags.HasError() {
		t.Fatalf("decoding state: %v", diags)
	}
	if got.ID.ValueString() != "a1" {
		t.Errorf("state id = %q; want the preserved a1", got.ID.ValueString())
	}
	if got.Port.ValueInt64() != 9090 {
		t.Errorf("state port = %d; want the planned 9090", got.Port.ValueInt64())
	}
	if got.State.ValueString() != "building" {
		t.Errorf("state state = %q; want refreshed building", got.State.ValueString())
	}
}

// ---------------------------------------------------------------------------
// Schema - pins RequiresReplace on the immutable identity fields.
// ---------------------------------------------------------------------------

// TestUnitMicrovmApp_ImmutableFieldsRequireReplace verifies
// hypervisor_group_id, slug and source_kind carry
// stringplanmodifier.RequiresReplace (no rename/re-home endpoint exists, so
// changing any of them must destroy+recreate, never route to Update), while
// the updatable fields (source_image/source_repo/source_branch/port/
// min_instances/idle_timeout_seconds/health_path) do NOT force replacement.
func TestUnitMicrovmApp_ImmutableFieldsRequireReplace(t *testing.T) {
	ctx := context.Background()
	res := resources.NewMicrovmAppResource()

	var resp resource.SchemaResponse
	res.Schema(ctx, resource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Schema returned errors: %v", resp.Diagnostics)
	}

	for _, name := range []string{"hypervisor_group_id", "slug", "source_kind"} {
		attr, ok := resp.Schema.Attributes[name].(schema.StringAttribute)
		if !ok {
			t.Fatalf("%s attribute missing or not a StringAttribute", name)
		}
		if !attr.Required {
			t.Errorf("%s attribute must be Required", name)
		}
		if !hasStringPlanModifier(attr.PlanModifiers, "RequiresReplace") {
			t.Errorf("%s attribute must have stringplanmodifier.RequiresReplace (no rename/re-home endpoint)", name)
		}
	}

	for _, name := range []string{"source_image", "source_repo", "source_branch", "health_path"} {
		attr, ok := resp.Schema.Attributes[name].(schema.StringAttribute)
		if !ok {
			t.Fatalf("%s attribute missing or not a StringAttribute", name)
		}
		if !attr.Optional {
			t.Errorf("%s attribute must be Optional", name)
		}
		if hasStringPlanModifier(attr.PlanModifiers, "RequiresReplace") {
			t.Errorf("%s attribute must NOT have RequiresReplace (updatable through the single-path Update)", name)
		}
	}

	for _, name := range []string{"port", "min_instances", "idle_timeout_seconds"} {
		attr, ok := resp.Schema.Attributes[name].(schema.Int64Attribute)
		if !ok {
			t.Fatalf("%s attribute missing or not an Int64Attribute", name)
		}
		if !attr.Optional {
			t.Errorf("%s attribute must be Optional", name)
		}
		if len(attr.PlanModifiers) != 0 {
			t.Errorf("%s attribute must NOT carry plan modifiers (updatable through the single-path Update); got %d", name, len(attr.PlanModifiers))
		}
	}

	idAttr, ok := resp.Schema.Attributes["id"].(schema.StringAttribute)
	if !ok {
		t.Fatal("id attribute missing or not a StringAttribute")
	}
	if !idAttr.Computed {
		t.Error("id attribute must be Computed")
	}
	if !hasStringPlanModifier(idAttr.PlanModifiers, "UseStateForUnknown") {
		t.Error("id attribute must have stringplanmodifier.UseStateForUnknown")
	}

	for _, name := range []string{"fqdn", "state"} {
		attr, ok := resp.Schema.Attributes[name].(schema.StringAttribute)
		if !ok {
			t.Fatalf("%s attribute missing or not a StringAttribute", name)
		}
		if !attr.Computed {
			t.Errorf("%s attribute must be Computed", name)
		}
	}
}

// ---------------------------------------------------------------------------
// Delete - pins the DELETE call and the server-error surfacing.
// ---------------------------------------------------------------------------

// TestUnitMicrovmApp_DeleteCallsAPI verifies Delete issues DELETE
// /api/microvm/app/{id} for the state id.
func TestUnitMicrovmApp_DeleteCallsAPI(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"App deleted."}`))
	}))
	defer srv.Close()

	res, sch := newMicrovmAppTestResource(t, srv.URL)

	prior := microvmAppOciModel()
	prior.ID = types.StringValue("a1")
	prior.Fqdn = types.StringValue("demo.apps.example.test")
	prior.State = types.StringValue("running")

	var resp resource.DeleteResponse
	res.Delete(context.Background(), resource.DeleteRequest{
		State: microvmAppPriorState(t, sch, prior),
	}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Delete returned errors: %v", resp.Diagnostics)
	}
	if gotMethod != http.MethodDelete || gotPath != "/api/microvm/app/a1" {
		t.Errorf("request = %s %s; want DELETE /api/microvm/app/a1", gotMethod, gotPath)
	}
}

// TestUnitMicrovmApp_DeleteSurfacesServerError verifies a 500 from the delete
// endpoint surfaces as an error diagnostic - Delete must never report success
// for an app the server failed to destroy. (The client retries 5xx with its
// default backoff, so this test takes a few seconds; that is intentional: it
// pins the real production retry+error path.)
func TestUnitMicrovmApp_DeleteSurfacesServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"Server is on fire."}`))
	}))
	defer srv.Close()

	res, sch := newMicrovmAppTestResource(t, srv.URL)

	prior := microvmAppOciModel()
	prior.ID = types.StringValue("a1")
	prior.Fqdn = types.StringValue("demo.apps.example.test")
	prior.State = types.StringValue("running")

	var resp resource.DeleteResponse
	res.Delete(context.Background(), resource.DeleteRequest{
		State: microvmAppPriorState(t, sch, prior),
	}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("Delete against a 500 must return an error diagnostic")
	}
	found := false
	for _, d := range resp.Diagnostics.Errors() {
		if d.Summary() == "Error deleting microvm app" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected an %q diagnostic; got %v", "Error deleting microvm app", resp.Diagnostics)
	}
}
