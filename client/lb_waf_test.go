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
