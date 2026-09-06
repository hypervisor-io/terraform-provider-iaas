package resources_test

// Resource-layer tests for iaas_microvm_api_key.
//
// The sibling resources prove their lifecycle with mock-backed
// resource.UnitTest runs (see certificate_test.go), which need a
// terraform/opentofu binary on PATH. This environment has none, and a skipped
// test cannot pin behaviour, so these tests drive the resource's
// Create/Read/Update/Delete and Schema directly through the exported
// framework interfaces against canned httptest responses - same canned-API
// spirit as certificate_test.go, deterministic everywhere.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
	"github.com/hypervisor-io/terraform-provider-iaas/internal/resources"
)

// microvmApiKeyTestModel mirrors the unexported resource model 1:1 (same tfsdk
// tags) so the external test package can build plans and decode state.
type microvmApiKeyTestModel struct {
	ID     types.String `tfsdk:"id"`
	Name   types.String `tfsdk:"name"`
	Prefix types.String `tfsdk:"prefix"`
	Key    types.String `tfsdk:"key"`
}

// newMicrovmApiKeyTestResource builds the resource via its exported
// constructor, runs Schema + Configure against a client pointed at srv, and
// returns the configured resource plus its schema (needed to build
// tfsdk.Plan/State values).
func newMicrovmApiKeyTestResource(t *testing.T, srvURL string) (resource.Resource, schema.Schema) {
	t.Helper()
	ctx := context.Background()

	res := resources.NewMicrovmApiKeyResource()

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

// microvmPlan builds a create-time plan: name configured, computed attrs
// unknown (exactly what the framework hands Create).
func microvmPlan(t *testing.T, sch schema.Schema, name string) tfsdk.Plan {
	t.Helper()
	plan := tfsdk.Plan{Schema: sch}
	diags := plan.Set(context.Background(), microvmApiKeyTestModel{
		ID:     types.StringUnknown(),
		Name:   types.StringValue(name),
		Prefix: types.StringUnknown(),
		Key:    types.StringUnknown(),
	})
	if diags.HasError() {
		t.Fatalf("building plan: %v", diags)
	}
	return plan
}

// nullState returns the null Raw state the framework pre-seeds on every
// response (resp.State starts as schema-typed null, not a zero tfsdk.State).
func nullState(ctx context.Context, sch schema.Schema) tfsdk.State {
	return tfsdk.State{
		Schema: sch,
		Raw:    tftypes.NewValue(sch.Type().TerraformType(ctx), nil),
	}
}

// microvmState builds a known post-create state.
func microvmState(t *testing.T, sch schema.Schema, id, name, prefix, key string) tfsdk.State {
	t.Helper()
	state := tfsdk.State{Schema: sch}
	diags := state.Set(context.Background(), microvmApiKeyTestModel{
		ID:     types.StringValue(id),
		Name:   types.StringValue(name),
		Prefix: types.StringValue(prefix),
		Key:    types.StringValue(key),
	})
	if diags.HasError() {
		t.Fatalf("building state: %v", diags)
	}
	return state
}

// ---------------------------------------------------------------------------
// Create - pins the shown-once plaintext capture.
// ---------------------------------------------------------------------------

// TestUnitMicrovmApiKey_CreateCapturesPlaintextOnce verifies Create posts
// {name} and persists id/prefix AND the shown-once plaintext key into state.
// Dropping the plaintext capture must fail this test.
func TestUnitMicrovmApiKey_CreateCapturesPlaintextOnce(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"key":{"id":"k1","name":"ci","prefix":"vc_sb_ab12"},"plaintext":"vc_sb_ab12cdef"}`))
	}))
	defer srv.Close()

	res, sch := newMicrovmApiKeyTestResource(t, srv.URL)

	ctx := context.Background()
	resp := resource.CreateResponse{State: nullState(ctx, sch)}
	res.Create(ctx, resource.CreateRequest{Plan: microvmPlan(t, sch, "ci")}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Create returned errors: %v", resp.Diagnostics)
	}

	if gotMethod != http.MethodPost || gotPath != "/api/microvm/api-keys" {
		t.Errorf("request = %s %s; want POST /api/microvm/api-keys", gotMethod, gotPath)
	}
	if gotBody["name"] != "ci" {
		t.Errorf("create body name = %v; want ci", gotBody["name"])
	}

	var got microvmApiKeyTestModel
	if diags := resp.State.Get(context.Background(), &got); diags.HasError() {
		t.Fatalf("decoding state: %v", diags)
	}
	if got.ID.ValueString() != "k1" {
		t.Errorf("state id = %q; want k1", got.ID.ValueString())
	}
	if got.Name.ValueString() != "ci" {
		t.Errorf("state name = %q; want ci", got.Name.ValueString())
	}
	if got.Prefix.ValueString() != "vc_sb_ab12" {
		t.Errorf("state prefix = %q; want vc_sb_ab12", got.Prefix.ValueString())
	}
	if got.Key.ValueString() != "vc_sb_ab12cdef" {
		t.Errorf("state key = %q; want the shown-once plaintext vc_sb_ab12cdef", got.Key.ValueString())
	}
}

// ---------------------------------------------------------------------------
// Read - pins the write-once contract: key is NEVER refreshed from the list.
// ---------------------------------------------------------------------------

// TestUnitMicrovmApiKey_ReadNeverRefreshesKey verifies Read refreshes
// name/prefix from the listing but preserves the captured plaintext key
// verbatim (the listing never carries key material). Overwriting key from the
// list must fail this test.
func TestUnitMicrovmApiKey_ReadNeverRefreshesKey(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"keys":[{"id":"k1","name":"ci-renamed-in-panel","prefix":"vc_sb_ab12"}]}`))
	}))
	defer srv.Close()

	res, sch := newMicrovmApiKeyTestResource(t, srv.URL)

	ctx := context.Background()
	resp := resource.ReadResponse{State: nullState(ctx, sch)}
	res.Read(ctx, resource.ReadRequest{
		State: microvmState(t, sch, "k1", "ci", "vc_sb_ab12", "vc_sb_ab12cdef"),
	}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Read returned errors: %v", resp.Diagnostics)
	}

	if gotMethod != http.MethodGet || gotPath != "/api/microvm/api-keys" {
		t.Errorf("request = %s %s; want GET /api/microvm/api-keys", gotMethod, gotPath)
	}

	var got microvmApiKeyTestModel
	if diags := resp.State.Get(context.Background(), &got); diags.HasError() {
		t.Fatalf("decoding state: %v", diags)
	}
	if got.Name.ValueString() != "ci-renamed-in-panel" {
		t.Errorf("state name = %q; want refreshed ci-renamed-in-panel", got.Name.ValueString())
	}
	if got.Key.ValueString() != "vc_sb_ab12cdef" {
		t.Errorf("state key = %q; want preserved plaintext vc_sb_ab12cdef (never refreshed)", got.Key.ValueString())
	}
}

// TestUnitMicrovmApiKey_ReadRemovesMissing verifies a key absent from the
// listing (deleted out of band) is removed from state.
func TestUnitMicrovmApiKey_ReadRemovesMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"keys":[]}`))
	}))
	defer srv.Close()

	res, sch := newMicrovmApiKeyTestResource(t, srv.URL)

	ctx := context.Background()
	resp := resource.ReadResponse{State: nullState(ctx, sch)}
	res.Read(ctx, resource.ReadRequest{
		State: microvmState(t, sch, "k1", "ci", "vc_sb_ab12", "vc_sb_ab12cdef"),
	}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Read returned errors: %v", resp.Diagnostics)
	}
	if !resp.State.Raw.IsNull() {
		t.Error("expected the resource to be removed from state, but state is non-null")
	}
}

// ---------------------------------------------------------------------------
// Schema - pins RequiresReplace on name and the write-once key shape.
// ---------------------------------------------------------------------------

// TestUnitMicrovmApiKey_NameRequiresReplace verifies the name attribute
// carries stringplanmodifier.RequiresReplace (there is no rename endpoint, so
// a rename must destroy+recreate, never route to Update) and that the key
// attribute is Sensitive with UseStateForUnknown (write-once secret).
func TestUnitMicrovmApiKey_NameRequiresReplace(t *testing.T) {
	ctx := context.Background()
	res := resources.NewMicrovmApiKeyResource()

	var resp resource.SchemaResponse
	res.Schema(ctx, resource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Schema returned errors: %v", resp.Diagnostics)
	}

	nameAttr, ok := resp.Schema.Attributes["name"].(schema.StringAttribute)
	if !ok {
		t.Fatal("name attribute missing or not a StringAttribute")
	}
	if !nameAttr.Required {
		t.Error("name attribute must be Required")
	}
	if !hasStringPlanModifier(nameAttr.PlanModifiers, "RequiresReplace") {
		t.Error("name attribute must have stringplanmodifier.RequiresReplace (no rename endpoint)")
	}

	keyAttr, ok := resp.Schema.Attributes["key"].(schema.StringAttribute)
	if !ok {
		t.Fatal("key attribute missing or not a StringAttribute")
	}
	if !keyAttr.Computed {
		t.Error("key attribute must be Computed (server-issued at create time only)")
	}
	if !keyAttr.Sensitive {
		t.Error("key attribute must be Sensitive (plaintext secret)")
	}
	if !hasStringPlanModifier(keyAttr.PlanModifiers, "UseStateForUnknown") {
		t.Error("key attribute must have stringplanmodifier.UseStateForUnknown (write-once)")
	}
}

// hasStringPlanModifier reports whether mods contains a string plan modifier
// whose concrete type name contains want (e.g. "RequiresReplace"). The
// framework's modifier types are unexported ("requiresReplaceModifier"), so
// the match is case-insensitive.
func hasStringPlanModifier(mods []planmodifier.String, want string) bool {
	for _, m := range mods {
		if strings.Contains(strings.ToLower(fmt.Sprintf("%T", m)), strings.ToLower(want)) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Update - unreachable by design; must fail loudly if ever invoked.
// ---------------------------------------------------------------------------

// TestUnitMicrovmApiKey_UpdateIsUnreachable verifies Update always errors -
// every attribute forces replacement, so the framework must never call it.
func TestUnitMicrovmApiKey_UpdateIsUnreachable(t *testing.T) {
	res := resources.NewMicrovmApiKeyResource()

	var resp resource.UpdateResponse
	res.Update(context.Background(), resource.UpdateRequest{}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("Update must always return an error diagnostic")
	}
	found := false
	for _, d := range resp.Diagnostics.Errors() {
		if strings.Contains(d.Summary(), "Update not supported") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected an %q diagnostic; got %v", "Update not supported", resp.Diagnostics)
	}
}

// ---------------------------------------------------------------------------
// Delete - pins the server-side revoke.
// ---------------------------------------------------------------------------

// TestUnitMicrovmApiKey_DeleteCallsAPI verifies Delete issues DELETE
// /microvm/api-key/{id} for the state id.
func TestUnitMicrovmApiKey_DeleteCallsAPI(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"API key deleted."}`))
	}))
	defer srv.Close()

	res, sch := newMicrovmApiKeyTestResource(t, srv.URL)

	var resp resource.DeleteResponse
	res.Delete(context.Background(), resource.DeleteRequest{
		State: microvmState(t, sch, "k1", "ci", "vc_sb_ab12", "vc_sb_ab12cdef"),
	}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Delete returned errors: %v", resp.Diagnostics)
	}
	if gotMethod != http.MethodDelete || gotPath != "/api/microvm/api-key/k1" {
		t.Errorf("request = %s %s; want DELETE /api/microvm/api-key/k1", gotMethod, gotPath)
	}
}

// TestUnitMicrovmApiKey_DeleteEmptyIDFails verifies a state with an empty id
// surfaces the client's empty-id guard as an error diagnostic instead of
// issuing a malformed request.
func TestUnitMicrovmApiKey_DeleteEmptyIDFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("no request expected for an empty id, got %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	res, sch := newMicrovmApiKeyTestResource(t, srv.URL)

	var resp resource.DeleteResponse
	res.Delete(context.Background(), resource.DeleteRequest{
		State: microvmState(t, sch, "", "ci", "vc_sb_ab12", "vc_sb_ab12cdef"),
	}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("Delete with an empty id must return an error diagnostic")
	}
}
