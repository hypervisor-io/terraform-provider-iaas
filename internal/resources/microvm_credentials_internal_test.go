package resources

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hypervisor-io/terraform-provider-iaas/client"
)

func credentialSchema(t *testing.T, r resource.Resource) schema.Schema {
	t.Helper()
	var out resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &out)
	if out.Diagnostics.HasError() {
		t.Fatal(out.Diagnostics)
	}
	return out.Schema
}
func credentialPlan(t *testing.T, s schema.Schema, model any) tfsdk.Plan {
	t.Helper()
	p := tfsdk.Plan{Schema: s}
	if d := p.Set(context.Background(), model); d.HasError() {
		t.Fatal(d)
	}
	return p
}
func credentialState(t *testing.T, s schema.Schema, model any) tfsdk.State {
	t.Helper()
	p := credentialPlan(t, s, model)
	return tfsdk.State{Schema: s, Raw: p.Raw}
}
func credentialConfig(t *testing.T, s schema.Schema, model any) tfsdk.Config {
	t.Helper()
	p := credentialPlan(t, s, model)
	return tfsdk.Config{Schema: s, Raw: p.Raw}
}
func credentialNullState(s schema.Schema) tfsdk.State {
	return tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(context.Background()), nil)}
}
func TestMicrovmCredentialSchemasAreTrulyWriteOnly(t *testing.T) {
	for _, tc := range []struct {
		r     resource.Resource
		field string
	}{{NewMicrovmSecretResource(), "value"}, {NewMicrovmMcpServerResource(), "static_token"}} {
		s := credentialSchema(t, tc.r)
		a := s.Attributes[tc.field].(schema.StringAttribute)
		if !a.WriteOnly || !a.Sensitive || !a.Optional || a.Computed {
			t.Fatalf("%s must be sensitive and write-only", tc.field)
		}
	}
}
func TestMicrovmSecretCreateUsesConfigValueAndNeverStoresIt(t *testing.T) {
	const secret = "  sentinel-value  "
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(req.Body).Decode(&body)
		if body["value"] != secret {
			t.Error("did not use untrimmed config value")
		}
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"success":true,"secret":{"id":"s1","name":"KEY","kind":"env"}}`))
	}))
	defer server.Close()
	r := &microvmSecretResource{client: client.New(server.URL, "token", time.Second, false)}
	s := credentialSchema(t, r)
	model := microvmSecretModel{Name: types.StringValue("KEY"), Kind: types.StringValue("env"), ValueVersion: types.Int64Value(1)}
	plan := credentialPlan(t, s, model)
	model.Value = types.StringValue(secret)
	out := resource.CreateResponse{State: credentialNullState(s)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan, Config: credentialConfig(t, s, model)}, &out)
	if out.Diagnostics.HasError() {
		t.Fatal(out.Diagnostics)
	}
	var got microvmSecretModel
	if d := out.State.Get(context.Background(), &got); d.HasError() {
		t.Fatal(d)
	}
	if !got.Value.IsNull() || got.ID.ValueString() != "s1" {
		t.Fatal("create did not retain ID with null secret")
	}
	if strings.Contains(out.State.Raw.String(), secret) {
		t.Fatal("secret entered state")
	}
}
func TestMicrovmSecretUpdatesOnlyRotateOnVersionChange(t *testing.T) {
	for _, rotate := range []bool{false, true} {
		t.Run(map[bool]string{false: "metadata", true: "rotate"}[rotate], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.Method != "PUT" {
					t.Error("wrong verb")
				}
				var body map[string]any
				_ = json.NewDecoder(req.Body).Decode(&body)
				value, has := body["value"]
				if has != rotate || (rotate && value != "sentinel") {
					t.Error("incorrect value presence")
				}
				_, _ = w.Write([]byte(`{"success":true,"secret":{"id":"s1"}}`))
			}))
			defer server.Close()
			r := &microvmSecretResource{client: client.New(server.URL, "token", time.Second, false)}
			s := credentialSchema(t, r)
			before := microvmSecretModel{ID: types.StringValue("s1"), Name: types.StringValue("KEY"), Kind: types.StringValue("env"), ValueVersion: types.Int64Value(1)}
			after := before
			after.Name = types.StringValue("RENAMED")
			if rotate {
				after.ValueVersion = types.Int64Value(2)
			}
			config := after
			config.Value = types.StringValue("sentinel")
			state := credentialState(t, s, before)
			out := resource.UpdateResponse{State: state}
			r.Update(context.Background(), resource.UpdateRequest{State: state, Plan: credentialPlan(t, s, after), Config: credentialConfig(t, s, config)}, &out)
			if out.Diagnostics.HasError() {
				t.Fatal(out.Diagnostics)
			}
			if strings.Contains(out.State.Raw.String(), "sentinel") {
				t.Fatal("state leaked secret")
			}
		})
	}
}
func TestMicrovmSecretReadAllKindsAndMalformedListPreservesState(t *testing.T) {
	for _, payload := range []string{`{"success":true,"secrets":[{"id":"s1","name":"REGISTRY","kind":"registry","registry":"ghcr.io","username":"octocat"}]}`, `{"success":true,"secrets":[{}]}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.URL.Query().Get("kind") != "all" {
				t.Error("registry secret would disappear on refresh")
			}
			_, _ = w.Write([]byte(payload))
		}))
		r := &microvmSecretResource{client: client.New(server.URL, "token", time.Second, false)}
		s := credentialSchema(t, r)
		state := credentialState(t, s, microvmSecretModel{ID: types.StringValue("s1"), Name: types.StringValue("REGISTRY"), Kind: types.StringValue("registry"), ValueVersion: types.Int64Value(1)})
		out := resource.ReadResponse{State: state}
		r.Read(context.Background(), resource.ReadRequest{State: state}, &out)
		if out.State.Raw.IsNull() {
			t.Fatal("read discarded existing resource")
		}
		if strings.Contains(payload, "[{}]") && !out.Diagnostics.HasError() {
			t.Fatal("malformed listing did not fail")
		}
		server.Close()
	}
}
func mcpResourceFixture() map[string]any {
	return map[string]any{"id": "m1", "provider": "custom", "name": "tool", "label": "Tool", "enabled": true, "credential_state": "saved_token", "connection": nil, "revision": map[string]any{"id": "r1", "revision": 1, "url": "https://tools.example/mcp", "resource": "https://tools.example/mcp", "auth_kind": "bearer", "headers": []any{}}, "kit": map[string]any{"supported": nil, "mode": nil}}
}
func TestMicrovmMcpTokenRotationUsesSeparateRoute(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls++
		if req.Method != "POST" || req.URL.Path != "/microvm/mcp-server/m1/rotate-token" {
			t.Errorf("rotation used %s %s", req.Method, req.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(req.Body).Decode(&body)
		if body["static_token"] != "sentinel" {
			t.Error("rotation missing token")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": mcpResourceFixture()})
	}))
	defer server.Close()
	r := &microvmMcpServerResource{client: client.New(server.URL, "token", time.Second, false)}
	s := credentialSchema(t, r)
	var before microvmMcpServerModel
	row := mcpResourceFixture()
	revision := row["revision"].(map[string]any)
	revision["revision"] = float64(1)
	microvmMcpApply(&before, row)
	after := before
	after.StaticTokenVersion = types.Int64Value(2)
	config := after
	config.StaticToken = types.StringValue("sentinel")
	state := credentialState(t, s, before)
	out := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{State: state, Plan: credentialPlan(t, s, after), Config: credentialConfig(t, s, config)}, &out)
	if out.Diagnostics.HasError() {
		t.Fatal(out.Diagnostics)
	}
	if calls != 1 {
		t.Fatalf("want one rotation, got %d requests", calls)
	}
	var got microvmMcpServerModel
	_ = out.State.Get(context.Background(), &got)
	if !got.StaticToken.IsNull() || got.StaticTokenVersion.ValueInt64() != 2 || len(got.Headers.Elements()) != 0 {
		t.Fatal("bad credential or empty PHP array state mapping")
	}
}
func TestMicrovmSecretFailedWritePreservesPriorState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(422)
		_, _ = w.Write([]byte(`{"success":false,"reason":"registry_in_use","message":"sentinel"}`))
	}))
	defer server.Close()
	r := &microvmSecretResource{client: client.New(server.URL, "token", time.Second, false)}
	s := credentialSchema(t, r)
	before := microvmSecretModel{ID: types.StringValue("s1"), Name: types.StringValue("KEY"), Kind: types.StringValue("env"), ValueVersion: types.Int64Value(1)}
	after := before
	after.ValueVersion = types.Int64Value(2)
	config := after
	config.Value = types.StringValue("sentinel")
	state := credentialState(t, s, before)
	out := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{State: state, Plan: credentialPlan(t, s, after), Config: credentialConfig(t, s, config)}, &out)
	if !out.Diagnostics.HasError() || !out.State.Raw.Equal(state.Raw) {
		t.Fatal("failed write changed state")
	}
	for _, d := range out.Diagnostics {
		if strings.Contains(d.Detail(), "sentinel") {
			t.Fatal("diagnostic leaked credential")
		}
	}
}

func TestMicrovmMalformedPresenterRejectedBeforeApply(t *testing.T) {
	valid := map[string]any{"id": "m1", "provider": "custom", "name": "tool", "label": "Tool", "enabled": true, "credential_state": "saved_token", "revision": map[string]any{"id": "r1", "url": "https://example.test", "auth_kind": "bearer", "revision": float64(1), "resource": nil, "headers": []any{}}}
	if err := microvmMcpValidate(valid); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "provider", "name", "label", "enabled", "credential_state", "revision"} {
		malformed := map[string]any{}
		for k, v := range valid {
			malformed[k] = v
		}
		delete(malformed, key)
		if microvmMcpValidate(malformed) == nil {
			t.Fatalf("missing %s accepted", key)
		}
		malformed[key] = 42
		if microvmMcpValidate(malformed) == nil {
			t.Fatalf("malformed %s accepted", key)
		}
	}
	valid["revision"] = nil
	if err := microvmMcpValidate(valid); err != nil {
		t.Fatalf("contract-null revision refused: %v", err)
	}
	if microvmSecretValidate(map[string]any{"id": "s1"}) == nil {
		t.Fatal("id-only secret accepted")
	}
	if err := microvmSecretValidate(map[string]any{"id": "s1", "name": "KEY", "kind": "env", "registry": nil, "username": nil}); err != nil {
		t.Fatal(err)
	}
}

func TestMicrovmIdOnlyReadsRetainPriorState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.Contains(req.URL.Path, "mcp-server") {
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":"m1"}}`))
		} else {
			_, _ = w.Write([]byte(`{"success":true,"secrets":[{"id":"s1"}]}`))
		}
	}))
	defer server.Close()
	c := client.New(server.URL, "token", time.Second, false)
	secret := &microvmSecretResource{client: c}
	ss := credentialSchema(t, secret)
	priorSecret := credentialState(t, ss, microvmSecretModel{ID: types.StringValue("s1"), Name: types.StringValue("KEY"), Kind: types.StringValue("env"), ValueVersion: types.Int64Value(1)})
	secretOut := resource.ReadResponse{State: priorSecret}
	secret.Read(context.Background(), resource.ReadRequest{State: priorSecret}, &secretOut)
	if !secretOut.Diagnostics.HasError() || !secretOut.State.Raw.Equal(priorSecret.Raw) {
		t.Fatal("malformed secret read changed state or omitted diagnostic")
	}
	mcp := &microvmMcpServerResource{client: c}
	ms := credentialSchema(t, mcp)
	priorMcp := credentialState(t, ms, microvmMcpServerModel{ID: types.StringValue("m1"), Name: types.StringValue("tool"), Provider: types.StringValue("custom"), Label: types.StringValue("Tool"), Enabled: types.BoolValue(true), Headers: types.MapNull(types.StringType), StaticTokenVersion: types.Int64Value(1)})
	mcpOut := resource.ReadResponse{State: priorMcp}
	mcp.Read(context.Background(), resource.ReadRequest{State: priorMcp}, &mcpOut)
	if !mcpOut.Diagnostics.HasError() || !mcpOut.State.Raw.Equal(priorMcp.Raw) {
		t.Fatal("malformed MCP read changed state or omitted diagnostic")
	}
}

func TestMicrovmMcpMalformedCreateRetainsRecoveryID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"success":true,"data":{"id":"created-server"}}`))
	}))
	defer server.Close()
	r := &microvmMcpServerResource{client: client.New(server.URL, "token", time.Second, false)}
	s := credentialSchema(t, r)
	model := microvmMcpServerModel{Provider: types.StringValue("custom"), Name: types.StringValue("tool"), Label: types.StringValue("Tool"), Enabled: types.BoolValue(true), URL: types.StringValue("https://example.test"), AuthKind: types.StringValue("bearer"), StaticTokenVersion: types.Int64Value(1)}
	model.RevisionID = types.StringUnknown()
	model.RevisionNumber = types.Int64Unknown()
	model.CredentialState = types.StringUnknown()
	model.Headers = types.MapUnknown(types.StringType)
	plan := credentialPlan(t, s, model)
	model.StaticToken = types.StringValue("sentinel")
	out := resource.CreateResponse{State: credentialNullState(s)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan, Config: credentialConfig(t, s, model)}, &out)
	var got microvmMcpServerModel
	if d := out.State.Get(context.Background(), &got); d.HasError() {
		t.Fatal(d)
	}
	if !out.Diagnostics.HasError() || got.ID.ValueString() != "created-server" || !got.StaticToken.IsNull() {
		t.Fatal("malformed successful create lost recovery identity or retained token")
	}
}
