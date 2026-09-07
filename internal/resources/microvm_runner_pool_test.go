package resources_test

// Resource-layer tests for iaas_microvm_runner_pool.
//
// Follows the microvm_api_key_test.go convention: drive the resource's
// Schema and Create/Read/Update/Delete/ImportState directly through the
// exported framework interfaces against canned httptest responses - the
// mock-backed resource.UnitTest style needs a terraform/opentofu binary on
// PATH, which this environment does not have.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
	"github.com/hypervisor-io/terraform-provider-iaas/internal/resources"
)

// microvmRunnerPoolTestModel mirrors the unexported resource model 1:1 (same
// tfsdk tags) so the external test package can build plans and decode state.
type microvmRunnerPoolTestModel struct {
	ID                types.String `tfsdk:"id"`
	ProviderType      types.String `tfsdk:"provider_type"`
	HypervisorGroupID types.String `tfsdk:"hypervisor_group_id"`
	PlanID            types.String `tfsdk:"plan_id"`
	Labels            types.List   `tfsdk:"labels"`
	MaxConcurrent     types.Int64  `tfsdk:"max_concurrent"`
	Enabled           types.Bool   `tfsdk:"enabled"`
	GitlabURL         types.String `tfsdk:"gitlab_url"`
	GitlabToken       types.String `tfsdk:"gitlab_token"`
	GitlabTagList     types.List   `tfsdk:"gitlab_tag_list"`
	GitlabRunUntagged types.Bool   `tfsdk:"gitlab_run_untagged"`
	WarmCount         types.Int64  `tfsdk:"warm_count"`
}

// runnerPoolGitlabJSON is the canned BARE pool object the MV3-23 store/update
// routes return (the model serialized directly, no envelope) and the object
// the index paginator carries in data[]. gitlab_token is $hidden on the
// Master model and therefore NEVER present.
const runnerPoolGitlabJSON = `{"id":"p1","user_id":"u1","provider":"gitlab","hypervisor_group_id":"hg1","plan_id":"pl1","labels":null,"max_concurrent":4,"enabled":true,"github_installation_id":null,"github_repo_full_name":null,"gitlab_url":"https://gitlab.example.com","gitlab_system_id":"runner-abcd","gitlab_tag_list":["microvm"],"gitlab_run_untagged":false,"warm_count":2,"billing_suspended_at":null,"created_at":"2026-09-06T00:00:00.000000Z","updated_at":"2026-09-06T00:00:00.000000Z"}`

// newMicrovmRunnerPoolTestResource builds the resource via its exported
// constructor, runs Schema + Configure against a client pointed at srv, and
// returns the configured resource plus its schema (needed to build
// tfsdk.Plan/State values).
func newMicrovmRunnerPoolTestResource(t *testing.T, srvURL string) (resource.Resource, schema.Schema) {
	t.Helper()
	ctx := context.Background()

	res := resources.NewMicrovmRunnerPoolResource()

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

// runnerPoolPlan builds a plan from a fully specified model.
func runnerPoolPlan(t *testing.T, sch schema.Schema, m microvmRunnerPoolTestModel) tfsdk.Plan {
	t.Helper()
	plan := tfsdk.Plan{Schema: sch}
	if diags := plan.Set(context.Background(), m); diags.HasError() {
		t.Fatalf("building plan: %v", diags)
	}
	return plan
}

// runnerPoolState builds a state from a fully specified model.
func runnerPoolState(t *testing.T, sch schema.Schema, m microvmRunnerPoolTestModel) tfsdk.State {
	t.Helper()
	state := tfsdk.State{Schema: sch}
	if diags := state.Set(context.Background(), m); diags.HasError() {
		t.Fatalf("building state: %v", diags)
	}
	return state
}

// runnerPoolGitlabCreatePlan is the create-time plan the framework hands
// Create for a gitlab pool: configured fields known, server-assigned fields
// (id/max_concurrent/enabled) unknown.
func runnerPoolGitlabCreatePlan(t *testing.T, sch schema.Schema) tfsdk.Plan {
	t.Helper()
	return runnerPoolPlan(t, sch, microvmRunnerPoolTestModel{
		ID:                types.StringUnknown(),
		ProviderType:      types.StringValue("gitlab"),
		HypervisorGroupID: types.StringValue("hg1"),
		PlanID:            types.StringValue("pl1"),
		Labels:            types.ListNull(types.StringType),
		MaxConcurrent:     types.Int64Unknown(),
		Enabled:           types.BoolUnknown(),
		GitlabURL:         types.StringValue("https://gitlab.example.com"),
		GitlabToken:       types.StringValue("glrt-secret"),
		GitlabTagList:     types.ListValueMust(types.StringType, []attr.Value{types.StringValue("microvm")}),
		GitlabRunUntagged: types.BoolValue(false),
		WarmCount:         types.Int64Value(2),
	})
}

// runnerPoolGitlabState is a known post-create state for the gitlab pool p1.
func runnerPoolGitlabState(t *testing.T, sch schema.Schema) tfsdk.State {
	t.Helper()
	return runnerPoolState(t, sch, microvmRunnerPoolTestModel{
		ID:                types.StringValue("p1"),
		ProviderType:      types.StringValue("gitlab"),
		HypervisorGroupID: types.StringValue("hg1"),
		PlanID:            types.StringValue("pl1"),
		Labels:            types.ListNull(types.StringType),
		MaxConcurrent:     types.Int64Value(4),
		Enabled:           types.BoolValue(true),
		GitlabURL:         types.StringValue("https://gitlab.example.com"),
		GitlabToken:       types.StringValue("glrt-secret"),
		GitlabTagList:     types.ListValueMust(types.StringType, []attr.Value{types.StringValue("microvm")}),
		GitlabRunUntagged: types.BoolValue(false),
		WarmCount:         types.Int64Value(2),
	})
}

// runnerPoolPaginator wraps pool objects in the bare single-page Laravel
// paginator the index route returns.
func runnerPoolPaginator(pools ...string) []byte {
	return []byte(fmt.Sprintf(`{"current_page":1,"data":[%s],"last_page":1,"per_page":25,"total":%d}`,
		strings.Join(pools, ","), len(pools)))
}

// listStrings flattens a types.List(string) for assertions.
func listStrings(t *testing.T, list types.List) []string {
	t.Helper()
	out := make([]string, 0, len(list.Elements()))
	for _, e := range list.Elements() {
		s, ok := e.(types.String)
		if !ok {
			t.Fatalf("list element is %T; want types.String", e)
		}
		out = append(out, s.ValueString())
	}
	return out
}

// ---------------------------------------------------------------------------
// Schema - pins gitlab_token Sensitive and provider_type RequiresReplace.
// ---------------------------------------------------------------------------

// TestMicrovmRunnerPoolResource_GitlabTokenIsSensitive verifies the schema
// marks gitlab_token Sensitive: the token is write-only ($hidden on the
// Master model, never echoed by any response) and must never appear in
// plan/CLI output. Dropping Sensitive must fail this test.
func TestMicrovmRunnerPoolResource_GitlabTokenIsSensitive(t *testing.T) {
	r := resources.NewMicrovmRunnerPoolResource()
	var resp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)

	attr, ok := resp.Schema.Attributes["gitlab_token"]
	if !ok {
		t.Fatal("expected gitlab_token attribute")
	}
	if !attr.IsSensitive() {
		t.Fatal("gitlab_token must be Sensitive")
	}
}

// TestMicrovmRunnerPoolResource_ProviderTypeRequiresReplace verifies
// provider_type is Required and carries stringplanmodifier.RequiresReplace:
// the provider routes are per-provider (POST/PUT
// /microvm/runners/pools/{provider}...) and a pool cannot change provider,
// so a change must destroy+recreate (or re-import), never route to Update.
func TestMicrovmRunnerPoolResource_ProviderTypeRequiresReplace(t *testing.T) {
	r := resources.NewMicrovmRunnerPoolResource()
	var resp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Schema returned errors: %v", resp.Diagnostics)
	}

	attr, ok := resp.Schema.Attributes["provider_type"].(schema.StringAttribute)
	if !ok {
		t.Fatal("provider_type attribute missing or not a StringAttribute")
	}
	if !attr.Required {
		t.Error("provider_type attribute must be Required")
	}
	if !hasStringPlanModifier(attr.PlanModifiers, "RequiresReplace") {
		t.Error("provider_type attribute must have stringplanmodifier.RequiresReplace (a pool cannot change CI provider)")
	}
}

// ---------------------------------------------------------------------------
// Create - gitlab round trip + the github import-only guard.
// ---------------------------------------------------------------------------

// TestUnitMicrovmRunnerPool_CreateGitlabRoundTrip verifies Create posts the
// gitlab body to /microvm/runners/pools/gitlab and builds the state from the
// bare pool response: id and the server-defaulted Optional+Computed
// attributes (max_concurrent/enabled) come from the response, while the
// write-only gitlab_token is preserved from the plan (the API never echoes
// it).
func TestUnitMicrovmRunnerPool_CreateGitlabRoundTrip(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(runnerPoolGitlabJSON))
	}))
	defer srv.Close()

	res, sch := newMicrovmRunnerPoolTestResource(t, srv.URL)

	ctx := context.Background()
	resp := resource.CreateResponse{State: nullState(ctx, sch)}
	res.Create(ctx, resource.CreateRequest{Plan: runnerPoolGitlabCreatePlan(t, sch)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Create returned errors: %v", resp.Diagnostics)
	}

	if gotMethod != http.MethodPost || gotPath != "/api/microvm/runners/pools/gitlab" {
		t.Errorf("request = %s %s; want POST /api/microvm/runners/pools/gitlab", gotMethod, gotPath)
	}
	if gotBody["hypervisor_group_id"] != "hg1" || gotBody["plan_id"] != "pl1" {
		t.Errorf("create body = %v; want hypervisor_group_id hg1 and plan_id pl1", gotBody)
	}
	if gotBody["gitlab_url"] != "https://gitlab.example.com" || gotBody["gitlab_token"] != "glrt-secret" {
		t.Errorf("create body = %v; want gitlab_url and gitlab_token to reach the API", gotBody)
	}
	if gotBody["warm_count"] != float64(2) {
		t.Errorf("create body warm_count = %v; want 2", gotBody["warm_count"])
	}

	var got microvmRunnerPoolTestModel
	if diags := resp.State.Get(ctx, &got); diags.HasError() {
		t.Fatalf("decoding state: %v", diags)
	}
	if got.ID.ValueString() != "p1" {
		t.Errorf("state id = %q; want p1", got.ID.ValueString())
	}
	if got.ProviderType.ValueString() != "gitlab" {
		t.Errorf("state provider_type = %q; want gitlab", got.ProviderType.ValueString())
	}
	if got.MaxConcurrent.ValueInt64() != 4 {
		t.Errorf("state max_concurrent = %v; want 4 resolved from the create response", got.MaxConcurrent.ValueInt64())
	}
	if got.Enabled.ValueBool() != true {
		t.Errorf("state enabled = %v; want true resolved from the create response", got.Enabled.ValueBool())
	}
	if got.WarmCount.ValueInt64() != 2 {
		t.Errorf("state warm_count = %v; want 2", got.WarmCount.ValueInt64())
	}
	if got.GitlabToken.ValueString() != "glrt-secret" {
		t.Errorf("state gitlab_token = %q; want the write-only glrt-secret preserved from the plan", got.GitlabToken.ValueString())
	}
	if tags := listStrings(t, got.GitlabTagList); len(tags) != 1 || tags[0] != "microvm" {
		t.Errorf("state gitlab_tag_list = %v; want [microvm]", tags)
	}
}

// TestUnitMicrovmRunnerPool_CreateGithubErrorsWithImportGuidance pins the
// import-only contract: Create with provider_type = "github" must return the
// install-then-import error WITHOUT touching the API (GitHub pools are
// linked by the panel's Install GitHub App flow). Removing the guard so
// Create proceeds must fail this test.
func TestUnitMicrovmRunnerPool_CreateGithubErrorsWithImportGuidance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("no request expected for a github create, got %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	res, sch := newMicrovmRunnerPoolTestResource(t, srv.URL)

	plan := runnerPoolPlan(t, sch, microvmRunnerPoolTestModel{
		ID:                types.StringUnknown(),
		ProviderType:      types.StringValue("github"),
		HypervisorGroupID: types.StringValue("hg1"),
		PlanID:            types.StringValue("pl1"),
		Labels:            types.ListValueMust(types.StringType, []attr.Value{types.StringValue("self-hosted")}),
		MaxConcurrent:     types.Int64Unknown(),
		Enabled:           types.BoolUnknown(),
		GitlabURL:         types.StringNull(),
		GitlabToken:       types.StringNull(),
		GitlabTagList:     types.ListNull(types.StringType),
		GitlabRunUntagged: types.BoolNull(),
		WarmCount:         types.Int64Unknown(),
	})

	ctx := context.Background()
	resp := resource.CreateResponse{State: nullState(ctx, sch)}
	res.Create(ctx, resource.CreateRequest{Plan: plan}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("Create with provider_type=github must return an error diagnostic")
	}
	foundSummary, foundDetail := false, false
	for _, d := range resp.Diagnostics.Errors() {
		if strings.Contains(d.Summary(), "github pools cannot be created via Terraform") {
			foundSummary = true
		}
		if strings.Contains(d.Detail(), "terraform import iaas_microvm_runner_pool.") {
			foundDetail = true
		}
	}
	if !foundSummary {
		t.Errorf("expected the %q diagnostic; got %v", "github pools cannot be created via Terraform", resp.Diagnostics)
	}
	if !foundDetail {
		t.Errorf("the diagnostic detail must carry the `terraform import` guidance; got %v", resp.Diagnostics)
	}
}

// ---------------------------------------------------------------------------
// Read - list-and-filter (READ-PATH GAP), token preservation, removal.
// ---------------------------------------------------------------------------

// TestUnitMicrovmRunnerPool_ReadViaListPreservesToken verifies Read goes
// through the LIST endpoint (there is no pool-show route), refreshes the
// tracked fields from the matching pool object, and preserves the write-only
// gitlab_token verbatim (the listing never carries it). Pointing Read at a
// GET /microvm/runners/pools/{id} path or overwriting the token from the
// response must fail this test.
func TestUnitMicrovmRunnerPool_ReadViaListPreservesToken(t *testing.T) {
	var gotMethod, gotPath string
	edited := strings.Replace(runnerPoolGitlabJSON, `"hypervisor_group_id":"hg1"`, `"hypervisor_group_id":"hg2"`, 1)
	edited = strings.Replace(edited, `"warm_count":2`, `"warm_count":3`, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(runnerPoolPaginator(edited))
	}))
	defer srv.Close()

	res, sch := newMicrovmRunnerPoolTestResource(t, srv.URL)

	ctx := context.Background()
	resp := resource.ReadResponse{State: nullState(ctx, sch)}
	res.Read(ctx, resource.ReadRequest{State: runnerPoolGitlabState(t, sch)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Read returned errors: %v", resp.Diagnostics)
	}

	if gotMethod != http.MethodGet || gotPath != "/api/microvm/runners/pools" {
		t.Errorf("request = %s %s; want GET /api/microvm/runners/pools (the list read path)", gotMethod, gotPath)
	}

	var got microvmRunnerPoolTestModel
	if diags := resp.State.Get(ctx, &got); diags.HasError() {
		t.Fatalf("decoding state: %v", diags)
	}
	if got.HypervisorGroupID.ValueString() != "hg2" {
		t.Errorf("state hypervisor_group_id = %q; want refreshed hg2", got.HypervisorGroupID.ValueString())
	}
	if got.WarmCount.ValueInt64() != 3 {
		t.Errorf("state warm_count = %v; want refreshed 3", got.WarmCount.ValueInt64())
	}
	if got.ProviderType.ValueString() != "gitlab" {
		t.Errorf("state provider_type = %q; want gitlab (mapped from the API's provider field)", got.ProviderType.ValueString())
	}
	if got.GitlabToken.ValueString() != "glrt-secret" {
		t.Errorf("state gitlab_token = %q; want preserved glrt-secret (never refreshed)", got.GitlabToken.ValueString())
	}
}

// TestUnitMicrovmRunnerPool_ReadRemovesMissing verifies a pool absent from
// the listing (deleted out of band) is removed from state.
func TestUnitMicrovmRunnerPool_ReadRemovesMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(runnerPoolPaginator())
	}))
	defer srv.Close()

	res, sch := newMicrovmRunnerPoolTestResource(t, srv.URL)

	ctx := context.Background()
	resp := resource.ReadResponse{State: nullState(ctx, sch)}
	res.Read(ctx, resource.ReadRequest{State: runnerPoolGitlabState(t, sch)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Read returned errors: %v", resp.Diagnostics)
	}
	if !resp.State.Raw.IsNull() {
		t.Error("expected the resource to be removed from state, but state is non-null")
	}
}

// ---------------------------------------------------------------------------
// Update - provider-scoped path pins.
// ---------------------------------------------------------------------------

// TestUnitMicrovmRunnerPool_UpdateGitlabRoutesProviderPath pins the gitlab
// update route: PUT /api/microvm/runners/pools/gitlab/{id} with the
// provider-correct body (shared fields + warm_count, NOT enabled). Dropping
// the provider segment from the path must fail this test.
func TestUnitMicrovmRunnerPool_UpdateGitlabRoutesProviderPath(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(runnerPoolGitlabJSON))
	}))
	defer srv.Close()

	res, sch := newMicrovmRunnerPoolTestResource(t, srv.URL)

	plan := runnerPoolPlan(t, sch, microvmRunnerPoolTestModel{
		ID:                types.StringValue("p1"),
		ProviderType:      types.StringValue("gitlab"),
		HypervisorGroupID: types.StringValue("hg1"),
		PlanID:            types.StringValue("pl1"),
		Labels:            types.ListNull(types.StringType),
		MaxConcurrent:     types.Int64Value(5),
		Enabled:           types.BoolValue(true),
		GitlabURL:         types.StringValue("https://gitlab.example.com"),
		GitlabToken:       types.StringValue("glrt-secret"),
		GitlabTagList:     types.ListValueMust(types.StringType, []attr.Value{types.StringValue("microvm")}),
		GitlabRunUntagged: types.BoolValue(false),
		WarmCount:         types.Int64Value(3),
	})

	ctx := context.Background()
	resp := resource.UpdateResponse{State: nullState(ctx, sch)}
	res.Update(ctx, resource.UpdateRequest{Plan: plan, State: runnerPoolGitlabState(t, sch)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Update returned errors: %v", resp.Diagnostics)
	}

	if gotMethod != http.MethodPut || gotPath != "/api/microvm/runners/pools/gitlab/p1" {
		t.Errorf("request = %s %s; want PUT /api/microvm/runners/pools/gitlab/p1", gotMethod, gotPath)
	}
	if gotBody["max_concurrent"] != float64(5) {
		t.Errorf("update body max_concurrent = %v; want 5", gotBody["max_concurrent"])
	}
	if gotBody["warm_count"] != float64(3) {
		t.Errorf("update body warm_count = %v; want 3 (gitlab-specific field)", gotBody["warm_count"])
	}
	if _, present := gotBody["enabled"]; present {
		t.Errorf("gitlab update body must NOT carry enabled (github-only); got %v", gotBody)
	}

	var got microvmRunnerPoolTestModel
	if diags := resp.State.Get(ctx, &got); diags.HasError() {
		t.Fatalf("decoding state: %v", diags)
	}
	if got.MaxConcurrent.ValueInt64() != 5 || got.WarmCount.ValueInt64() != 3 {
		t.Errorf("state max_concurrent/warm_count = %v/%v; want 5/3 from the plan", got.MaxConcurrent.ValueInt64(), got.WarmCount.ValueInt64())
	}
	if got.GitlabToken.ValueString() != "glrt-secret" {
		t.Errorf("state gitlab_token = %q; want glrt-secret preserved from the plan", got.GitlabToken.ValueString())
	}
}

// TestUnitMicrovmRunnerPool_UpdateGithubRoutesProviderPath pins the github
// update route: PUT /api/microvm/runners/pools/github/{id} with the
// provider-correct body (shared fields + enabled, NOT warm_count). This is
// the update half of the import-only github lifecycle.
func TestUnitMicrovmRunnerPool_UpdateGithubRoutesProviderPath(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"p2","provider":"github","hypervisor_group_id":"hg1","plan_id":"pl1","labels":["self-hosted","linux","x64"],"max_concurrent":4,"enabled":true}`))
	}))
	defer srv.Close()

	res, sch := newMicrovmRunnerPoolTestResource(t, srv.URL)

	plan := runnerPoolPlan(t, sch, microvmRunnerPoolTestModel{
		ID:                types.StringValue("p2"),
		ProviderType:      types.StringValue("github"),
		HypervisorGroupID: types.StringValue("hg1"),
		PlanID:            types.StringValue("pl1"),
		Labels:            types.ListValueMust(types.StringType, []attr.Value{types.StringValue("self-hosted"), types.StringValue("linux"), types.StringValue("x64")}),
		MaxConcurrent:     types.Int64Value(4),
		Enabled:           types.BoolValue(true),
		GitlabURL:         types.StringNull(),
		GitlabToken:       types.StringNull(),
		GitlabTagList:     types.ListNull(types.StringType),
		GitlabRunUntagged: types.BoolNull(),
		WarmCount:         types.Int64Value(0),
	})
	state := runnerPoolState(t, sch, microvmRunnerPoolTestModel{
		ID:                types.StringValue("p2"),
		ProviderType:      types.StringValue("github"),
		HypervisorGroupID: types.StringValue("hg1"),
		PlanID:            types.StringValue("pl1"),
		Labels:            types.ListValueMust(types.StringType, []attr.Value{types.StringValue("self-hosted"), types.StringValue("linux"), types.StringValue("x64")}),
		MaxConcurrent:     types.Int64Value(2),
		Enabled:           types.BoolValue(true),
		GitlabURL:         types.StringNull(),
		GitlabToken:       types.StringNull(),
		GitlabTagList:     types.ListNull(types.StringType),
		GitlabRunUntagged: types.BoolNull(),
		WarmCount:         types.Int64Value(0),
	})

	ctx := context.Background()
	resp := resource.UpdateResponse{State: nullState(ctx, sch)}
	res.Update(ctx, resource.UpdateRequest{Plan: plan, State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Update returned errors: %v", resp.Diagnostics)
	}

	if gotMethod != http.MethodPut || gotPath != "/api/microvm/runners/pools/github/p2" {
		t.Errorf("request = %s %s; want PUT /api/microvm/runners/pools/github/p2", gotMethod, gotPath)
	}
	if gotBody["enabled"] != true {
		t.Errorf("update body enabled = %v; want true (github-specific field)", gotBody["enabled"])
	}
	if _, present := gotBody["warm_count"]; present {
		t.Errorf("github update body must NOT carry warm_count (gitlab-only); got %v", gotBody)
	}
}

// ---------------------------------------------------------------------------
// Delete
// ---------------------------------------------------------------------------

// TestUnitMicrovmRunnerPool_DeleteCallsAPI verifies Delete issues DELETE
// /microvm/runners/pools/{id} (the provider-agnostic destroy route).
func TestUnitMicrovmRunnerPool_DeleteCallsAPI(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	res, sch := newMicrovmRunnerPoolTestResource(t, srv.URL)

	var resp resource.DeleteResponse
	res.Delete(context.Background(), resource.DeleteRequest{State: runnerPoolGitlabState(t, sch)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Delete returned errors: %v", resp.Diagnostics)
	}
	if gotMethod != http.MethodDelete || gotPath != "/api/microvm/runners/pools/p1" {
		t.Errorf("request = %s %s; want DELETE /api/microvm/runners/pools/p1", gotMethod, gotPath)
	}
}

// TestUnitMicrovmRunnerPool_DeleteEmptyIDFails verifies a state with an empty
// id surfaces the client's empty-id guard as an error diagnostic instead of
// issuing a malformed request.
func TestUnitMicrovmRunnerPool_DeleteEmptyIDFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("no request expected for an empty id, got %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	res, sch := newMicrovmRunnerPoolTestResource(t, srv.URL)

	empty := runnerPoolState(t, sch, microvmRunnerPoolTestModel{
		ID:                types.StringValue(""),
		ProviderType:      types.StringValue("gitlab"),
		HypervisorGroupID: types.StringValue("hg1"),
		PlanID:            types.StringValue("pl1"),
		Labels:            types.ListNull(types.StringType),
		MaxConcurrent:     types.Int64Value(4),
		Enabled:           types.BoolValue(true),
		GitlabURL:         types.StringValue("https://gitlab.example.com"),
		GitlabToken:       types.StringValue("glrt-secret"),
		GitlabTagList:     types.ListValueMust(types.StringType, []attr.Value{types.StringValue("microvm")}),
		GitlabRunUntagged: types.BoolValue(false),
		WarmCount:         types.Int64Value(2),
	})

	var resp resource.DeleteResponse
	res.Delete(context.Background(), resource.DeleteRequest{State: empty}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("Delete with an empty id must return an error diagnostic")
	}
}

// ---------------------------------------------------------------------------
// ImportState
// ---------------------------------------------------------------------------

// TestUnitMicrovmRunnerPool_ImportStatePassthrough verifies ImportState
// writes the import id straight into the id attribute (passthrough) - the
// entry point of the github import-only lifecycle.
func TestUnitMicrovmRunnerPool_ImportStatePassthrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("no request expected during ImportState, got %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	res, sch := newMicrovmRunnerPoolTestResource(t, srv.URL)

	importer, ok := res.(resource.ResourceWithImportState)
	if !ok {
		t.Fatal("resource does not implement resource.ResourceWithImportState")
	}

	ctx := context.Background()
	resp := resource.ImportStateResponse{State: nullState(ctx, sch)}
	importer.ImportState(ctx, resource.ImportStateRequest{ID: "p1"}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("ImportState returned errors: %v", resp.Diagnostics)
	}

	var got microvmRunnerPoolTestModel
	if diags := resp.State.Get(ctx, &got); diags.HasError() {
		t.Fatalf("decoding state: %v", diags)
	}
	if got.ID.ValueString() != "p1" {
		t.Errorf("state id = %q; want the import id p1 passed through", got.ID.ValueString())
	}
}
