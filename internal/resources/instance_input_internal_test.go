package resources

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
)

// 3.3.8.8: the panel checks an instance hostname (RFC 1123, 422 invalid_hostname) and the cloud-config (422
// invalid_cloud_config, or a deploy task that fails with that failure_reason). These tests drive the real Create, Update
// and ModifyPlan against a fake panel.

type inputFake struct {
	mu     sync.Mutex
	bodies map[string][]string // "METHOD path" -> bodies
	routes map[string]func(w http.ResponseWriter, body string)
}

func (f *inputFake) body(key string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.bodies[key]...)
}

func inputFixture(t *testing.T, routes map[string]func(w http.ResponseWriter, body string)) (*instanceResource, tftypes.Object, *inputFake) {
	t.Helper()
	t.Setenv("IAAS_INSTANCE_POLL_INTERVAL", "2ms")
	f := &inputFake{bodies: map[string][]string{}, routes: routes}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		key := r.Method + " " + strings.TrimPrefix(r.URL.Path, "/api")
		f.mu.Lock()
		f.bodies[key] = append(f.bodies[key], string(b))
		h := f.routes[key]
		f.mu.Unlock()
		if h == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		h(w, string(b))
	}))
	t.Cleanup(srv.Close)
	r := &instanceResource{client: client.New(srv.URL+"/api", "tok", 5*time.Second, false)}
	var sresp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &sresp)
	return r, sresp.Schema.Type().TerraformType(context.Background()).(tftypes.Object), f
}

func instanceObj(typ tftypes.Object, set map[string]string) tftypes.Value {
	vals := map[string]tftypes.Value{}
	for k, at := range typ.AttributeTypes {
		vals[k] = tftypes.NewValue(at, nil)
	}
	for k, v := range set {
		vals[k] = tftypes.NewValue(tftypes.String, v)
	}
	return tftypes.NewValue(typ, vals)
}

func inRespond(code int, body string) func(w http.ResponseWriter, _ string) {
	return func(w http.ResponseWriter, _ string) {
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}
}

func hasAttrError(d diag.Diagnostics, attr string, contains ...string) bool {
	for _, e := range d.Errors() {
		if wp, ok := e.(diag.DiagnosticWithPath); ok && wp.Path().Equal(path.Root(attr)) {
			ok := true
			for _, c := range contains {
				if !strings.Contains(e.Detail(), c) {
					ok = false
				}
			}
			if ok {
				return true
			}
		}
	}
	return false
}

const hostMsg = "The hostname may contain only letters, digits and hyphens, with dots between parts."

func TestInstanceModifyPlan_HostnameRule(t *testing.T) {
	r, typ, _ := inputFixture(t, nil)
	var sresp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &sresp)
	plan := func(cfg string, state *string) resource.ModifyPlanResponse {
		c := instanceObj(typ, map[string]string{"hostname": cfg})
		req := resource.ModifyPlanRequest{
			Config: tfsdk.Config{Schema: sresp.Schema, Raw: c},
			Plan:   tfsdk.Plan{Schema: sresp.Schema, Raw: c},
			State:  tfsdk.State{Schema: sresp.Schema, Raw: tftypes.NewValue(typ, nil)},
		}
		if state != nil {
			req.State.Raw = instanceObj(typ, map[string]string{"hostname": *state})
		}
		var resp resource.ModifyPlanResponse
		r.ModifyPlan(context.Background(), req, &resp)
		return resp
	}
	legacy := "web_1"
	// Create with an underscore: refused at plan, on the attribute, with the panel's sentence.
	resp := plan("web_1", nil)
	if !hasAttrError(resp.Diagnostics, "hostname", hostMsg) {
		t.Fatalf("underscore on create must fail at plan on hostname: %v", resp.Diagnostics)
	}
	// An existing instance whose stored name is the legacy one and whose config is unchanged: clean (the panel accepts it too).
	if plan("web_1", &legacy).Diagnostics.HasError() {
		t.Fatal("an unchanged stored hostname must plan clean")
	}
	// Changing it to another bad name: refused. To a good one: clean.
	if !plan("web_2", &legacy).Diagnostics.HasError() {
		t.Fatal("changing to another underscore name must fail")
	}
	if plan("web-2", &legacy).Diagnostics.HasError() {
		t.Fatal("changing to a valid name must plan clean")
	}
	if plan("Web-01.Example.COM", nil).Diagnostics.HasError() || plan("a", nil).Diagnostics.HasError() {
		t.Fatal("upper case and a single label are valid")
	}
	// Trailing dot: the panel strips it, the provider cannot keep a value that never matches.
	if !hasAttrError(plan("web.", nil).Diagnostics, "hostname", "trailing dot") {
		t.Fatal("trailing dot must be refused with its own sentence")
	}
	// Omitted hostname (null config): nothing to judge.
	var resp2 resource.ModifyPlanResponse
	c := instanceObj(typ, nil)
	r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{
		Config: tfsdk.Config{Schema: sresp.Schema, Raw: c}, Plan: tfsdk.Plan{Schema: sresp.Schema, Raw: c},
		State: tfsdk.State{Schema: sresp.Schema, Raw: tftypes.NewValue(typ, nil)},
	}, &resp2)
	if resp2.Diagnostics.HasError() {
		t.Fatal("no hostname configured must plan clean")
	}
}

const phase1OK = `{"success":true,"instance":{"id":"i1"}}`

func createReq(r *instanceResource, typ tftypes.Object, set map[string]string) (resource.CreateRequest, *resource.CreateResponse) {
	var sresp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &sresp)
	return resource.CreateRequest{Plan: tfsdk.Plan{Schema: sresp.Schema, Raw: instanceObj(typ, set)}},
		&resource.CreateResponse{State: tfsdk.State{Schema: sresp.Schema, Raw: tftypes.NewValue(typ, nil)}}
}

func TestInstanceCreate_Phase1Hostname422IsShownOnTheAttribute(t *testing.T) {
	body := `{"success":false,"reason":"invalid_hostname","message":"` + hostMsg + `","errors":{"hostname":["` + hostMsg + `"]}}`
	r, typ, f := inputFixture(t, map[string]func(http.ResponseWriter, string){"POST /cloud-service/instances": inRespond(422, body)})
	req, resp := createReq(r, typ, map[string]string{"location_id": "l", "plan_id": "p", "image_id": "im", "hostname": "web_1"})
	r.Create(context.Background(), req, resp)
	if !hasAttrError(resp.Diagnostics, "hostname", hostMsg) {
		t.Fatalf("want an error on hostname with the panel sentence: %v", resp.Diagnostics)
	}
	if n := len(f.body("POST /instance/i1/deploy")); n != 0 {
		t.Fatalf("no deploy after a refused create, got %d", n)
	}
}

func TestInstanceCreate_Deploy422CloudcfgIsShownOnTheAttribute(t *testing.T) {
	msg := `The cloud-init key \"runcmd\" must be a list (one \"- \" line per entry), not a single value.`
	body := `{"success":false,"reason":"invalid_cloud_config","message":"` + msg + `","errors":{"cloudcfg":["` + msg + `"]}}`
	r, typ, _ := inputFixture(t, map[string]func(http.ResponseWriter, string){
		"POST /cloud-service/instances": inRespond(200, phase1OK),
		"POST /instance/i1/deploy":      inRespond(422, body),
	})
	req, resp := createReq(r, typ, map[string]string{"location_id": "l", "plan_id": "p", "image_id": "im", "cloudcfg": "#cloud-config\nruncmd: echo hi"})
	r.Create(context.Background(), req, resp)
	if !hasAttrError(resp.Diagnostics, "cloudcfg", `"runcmd" must be a list`, "User Scripts", "tainted") {
		t.Fatalf("want a cloudcfg attribute error naming the key, the scripts hint and the taint note: %v", resp.Diagnostics)
	}
	var id string
	_ = resp.State.GetAttribute(context.Background(), path.Root("id"), &id)
	if id != "i1" {
		t.Fatalf("the created id must stay in state so the next apply replaces it, got %q", id)
	}
}

func TestInstanceCreate_DeployTaskInvalidCloudConfigIsShownOnTheAttribute(t *testing.T) {
	r, typ, _ := inputFixture(t, map[string]func(http.ResponseWriter, string){
		"POST /cloud-service/instances": inRespond(200, phase1OK),
		"POST /instance/i1/deploy":      inRespond(200, `{"success":true,"task_id":"t1"}`),
		"GET /instance/i1/task/t1":      inRespond(200, `{"task":{"id":"t1","status":"failed","message":"The cloud-init key \"packages\" must be a list.","failure_reason":"invalid_cloud_config"}}`),
	})
	req, resp := createReq(r, typ, map[string]string{"location_id": "l", "plan_id": "p", "image_id": "im", "cloudcfg": "#cloud-config\npackages: htop"})
	r.Create(context.Background(), req, resp)
	if !hasAttrError(resp.Diagnostics, "cloudcfg", `"packages" must be a list`, "invalid_cloud_config") {
		t.Fatalf("want the failed task shown on cloudcfg: %v", resp.Diagnostics)
	}
}

func TestInstanceUpdate_UnknownHostnameIsNotSent_And422IsOnTheAttribute(t *testing.T) {
	body := `{"success":false,"reason":"invalid_hostname","message":"` + hostMsg + `","errors":{"hostname":["` + hostMsg + `"]}}`
	patched := func(w http.ResponseWriter, b string) {
		var m map[string]any
		_ = json.Unmarshal([]byte(b), &m)
		if h, ok := m["hostname"]; ok && h != "good-1" {
			inRespond(422, body)(w, b)
			return
		}
		inRespond(200, `{"success":true,"instance":{"id":"i1"}}`)(w, b)
	}
	r, typ, f := inputFixture(t, map[string]func(http.ResponseWriter, string){
		"PATCH /instance/i1": patched,
		"GET /instance/i1":   inRespond(200, `{"id":"i1","hostname":"old-1","display_name":"d","status":1,"deployed":1}`),
	})
	var sresp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &sresp)
	state := tfsdk.State{Schema: sresp.Schema, Raw: instanceObj(typ, map[string]string{"id": "i1", "hostname": "old-1", "display_name": "d", "tags": ""})}

	// Only an unrelated attribute changes: hostname and display_name are unknown in the plan and must not be sent as "".
	planVals := map[string]tftypes.Value{}
	base := instanceObj(typ, map[string]string{"id": "i1", "tags": ""})
	_ = base.As(&planVals)
	planVals["hostname"] = tftypes.NewValue(tftypes.String, tftypes.UnknownValue)
	planVals["display_name"] = tftypes.NewValue(tftypes.String, tftypes.UnknownValue)
	plan := tfsdk.Plan{Schema: sresp.Schema, Raw: tftypes.NewValue(typ, planVals)}
	resp := &resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, resp)
	for _, b := range f.body("PATCH /instance/i1") {
		if strings.Contains(b, "hostname") || strings.Contains(b, "display_name") {
			t.Fatalf("an unknown hostname or display name must not be sent, body %s", b)
		}
	}

	// A changed, bad hostname: the panel's 422 lands on the attribute.
	planVals["hostname"] = tftypes.NewValue(tftypes.String, "bad_name")
	plan = tfsdk.Plan{Schema: sresp.Schema, Raw: tftypes.NewValue(typ, planVals)}
	resp = &resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, resp)
	if !hasAttrError(resp.Diagnostics, "hostname", hostMsg) {
		t.Fatalf("want the 422 on hostname: %v", resp.Diagnostics)
	}
}
