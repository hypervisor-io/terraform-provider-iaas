package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestGetLBWafPolicy_NullPolicyIsNotFound proves the load-bearing fix: a
// 200 {"success":true,"policy":null} response (no policy configured) must
// decode to a *APIError with Status 404 (so client.IsNotFound() is true and
// the Terraform resource's Read can RemoveResource cleanly), NOT a generic
// decode error. Reusing the shared doItem() helper here would return
// "expected object under \"policy\"; got <nil>" instead, because JSON null
// unmarshals to an untyped nil that fails the map[string]any type assertion.
func TestGetLBWafPolicy_NullPolicyIsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"policy":null}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.GetLBWafPolicy(context.Background(), "lb-1")

	if obj != nil {
		t.Errorf("obj = %v; want nil", obj)
	}
	if err == nil {
		t.Fatal("err = nil; want a 404-shaped *APIError")
	}
	if !IsNotFound(err) {
		t.Fatalf("IsNotFound(err) = false; want true (err: %v)", err)
	}
}

// TestGetLBWafPolicy_Success verifies the real-object path decodes normally
// and uses the customer-facing wire field names (sensitivity/exclusions, not
// paranoia_level/crs_exclusions).
func TestGetLBWafPolicy_Success(t *testing.T) {
	var gotMethod, gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"policy":{"id":"pol-1","enabled":true,"mode":"detect","fail_mode":"open","crs_enabled":true,"sensitivity":2,"response_inspection":false,"full_audit":false,"exclusions":[942100]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.GetLBWafPolicy(context.Background(), "lb-1")
	if err != nil {
		t.Fatalf("GetLBWafPolicy returned error: %v", err)
	}
	if gotMethod != http.MethodGet || gotPath != "/api/load-balancer/lb-1/waf/policy" {
		t.Errorf("method/path = %s %s; want GET .../lb-1/waf/policy", gotMethod, gotPath)
	}
	if obj["id"] != "pol-1" || obj["mode"] != "detect" {
		t.Errorf("obj = %v; want id=pol-1, mode=detect", obj)
	}
	if _, present := obj["paranoia_level"]; present {
		t.Errorf("obj must not carry the raw DB column paranoia_level: %v", obj)
	}
}

// TestPutLBWafPolicy_Success verifies the upsert PUT + body + response decode.
func TestPutLBWafPolicy_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"policy":{"id":"pol-1","enabled":true,"mode":"block","sensitivity":3}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.PutLBWafPolicy(context.Background(), "lb-1", map[string]any{
		"enabled": true, "mode": "block", "sensitivity": int64(3),
	})
	if err != nil {
		t.Fatalf("PutLBWafPolicy returned error: %v", err)
	}
	if gotMethod != http.MethodPut || gotPath != "/api/load-balancer/lb-1/waf/policy" {
		t.Errorf("method/path = %s %s; want PUT .../lb-1/waf/policy", gotMethod, gotPath)
	}
	if gotBody["mode"] != "block" {
		t.Errorf("request body mode = %v; want block", gotBody["mode"])
	}
	if obj["mode"] != "block" {
		t.Errorf("response obj mode = %v; want block", obj["mode"])
	}
}

// TestDeleteLBWafPolicy_Success verifies the disable DELETE call.
func TestDeleteLBWafPolicy_Success(t *testing.T) {
	var gotMethod, gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"WAF disabled"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DeleteLBWafPolicy(context.Background(), "lb-1"); err != nil {
		t.Fatalf("DeleteLBWafPolicy returned error: %v", err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/api/load-balancer/lb-1/waf/policy" {
		t.Errorf("method/path = %s %s; want DELETE .../lb-1/waf/policy", gotMethod, gotPath)
	}
}

// TestListLBWafRules_NamedListEnvelope proves the load-bearing fix: the list
// response is a bare Eloquent collection under "rules"
// ({"success":true,"rules":[...]}), NOT a Laravel paginator ({"data":[...]}).
// The shared doList helper (decodeList) only understands a top-level array or
// a "data" envelope - using it here would fail with "no 'data' array and no
// top-level array" on every real response. namedList is the existing helper
// for exactly this envelope shape (see ListLBSecurityGroupRules).
func TestListLBWafRules_NamedListEnvelope(t *testing.T) {
	var gotMethod, gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"rules":[{"id":"rule-1","rule_id":190010,"name":"block-sqli","seclang":"SecRule ARGS \"@rx union select\" \"id:190010,phase:2,deny\"","enabled":true,"sort_order":10}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	rules, err := c.ListLBWafRules(context.Background(), "lb-1")
	if err != nil {
		t.Fatalf("ListLBWafRules returned error: %v", err)
	}
	if gotMethod != http.MethodGet || gotPath != "/api/load-balancer/lb-1/waf/rules" {
		t.Errorf("method/path = %s %s; want GET .../lb-1/waf/rules", gotMethod, gotPath)
	}
	if len(rules) != 1 || rules[0]["rule_id"] != float64(190010) {
		t.Fatalf("rules = %v; want one rule with rule_id=190010", rules)
	}
}

// TestListLBWafRules_Empty proves an empty collection decodes to an empty
// slice, not an error (namedList's contract: absent/empty key -> []).
func TestListLBWafRules_Empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"rules":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	rules, err := c.ListLBWafRules(context.Background(), "lb-1")
	if err != nil {
		t.Fatalf("ListLBWafRules returned error: %v", err)
	}
	if len(rules) != 0 {
		t.Errorf("rules = %v; want empty", rules)
	}
}

// TestCreateLBWafRule_Success verifies the create POST + body + response decode.
func TestCreateLBWafRule_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"rule":{"id":"rule-1","rule_id":190010,"name":"block-sqli","seclang":"SecRule ARGS ...","enabled":true,"sort_order":10}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.CreateLBWafRule(context.Background(), "lb-1", map[string]any{
		"rule_id": int64(190010), "name": "block-sqli",
	})
	if err != nil {
		t.Fatalf("CreateLBWafRule returned error: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/load-balancer/lb-1/waf/rules" {
		t.Errorf("method/path = %s %s; want POST .../lb-1/waf/rules", gotMethod, gotPath)
	}
	if gotBody["name"] != "block-sqli" {
		t.Errorf("request body name = %v; want block-sqli", gotBody["name"])
	}
	if obj["id"] != "rule-1" {
		t.Errorf("response obj id = %v; want rule-1", obj["id"])
	}
}

// TestUpdateLBWafRule_Success verifies the update PATCH call.
func TestUpdateLBWafRule_Success(t *testing.T) {
	var gotMethod, gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"rule":{"id":"rule-1","rule_id":190010,"name":"renamed","seclang":"SecRule ARGS ...","enabled":true,"sort_order":10}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.UpdateLBWafRule(context.Background(), "lb-1", "rule-1", map[string]any{"name": "renamed"})
	if err != nil {
		t.Fatalf("UpdateLBWafRule returned error: %v", err)
	}
	if gotMethod != http.MethodPatch || gotPath != "/api/load-balancer/lb-1/waf/rule/rule-1" {
		t.Errorf("method/path = %s %s; want PATCH .../lb-1/waf/rule/rule-1", gotMethod, gotPath)
	}
	if obj["name"] != "renamed" {
		t.Errorf("response obj name = %v; want renamed", obj["name"])
	}
}

// TestDeleteLBWafRule_Success verifies the delete DELETE call.
func TestDeleteLBWafRule_Success(t *testing.T) {
	var gotMethod, gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Rule deleted"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DeleteLBWafRule(context.Background(), "lb-1", "rule-1"); err != nil {
		t.Fatalf("DeleteLBWafRule returned error: %v", err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/api/load-balancer/lb-1/waf/rule/rule-1" {
		t.Errorf("method/path = %s %s; want DELETE .../lb-1/waf/rule/rule-1", gotMethod, gotPath)
	}
}

// TestGetLBWafRule_ScansListAndMatchesID verifies the no-single-SHOW-route
// scan-and-match behavior, and the 404 shape when absent.
func TestGetLBWafRule_ScansListAndMatchesID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"rules":[{"id":"rule-1","rule_id":190010,"name":"a"},{"id":"rule-2","rule_id":190011,"name":"b"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)

	obj, err := c.GetLBWafRule(context.Background(), "lb-1", "rule-2")
	if err != nil {
		t.Fatalf("GetLBWafRule returned error: %v", err)
	}
	if obj["name"] != "b" {
		t.Errorf("obj = %v; want name=b", obj)
	}

	_, err = c.GetLBWafRule(context.Background(), "lb-1", "rule-missing")
	if err == nil || !IsNotFound(err) {
		t.Fatalf("err = %v; want a 404-shaped *APIError", err)
	}
}
