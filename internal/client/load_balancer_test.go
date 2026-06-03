package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// NOTE: like the other client tests, this file uses net/http/httptest directly
// rather than internal/acctest.MockServer (importing acctest here would create
// an import cycle: acctest → provider → client).

// ---------------------------------------------------------------------------
// CreateLoadBalancer
// ---------------------------------------------------------------------------

// TestCreateLoadBalancer_Success verifies CreateLoadBalancer:
//   - POSTs to /load-balancers (PLURAL)
//   - sends the prebuilt body verbatim
//   - unwraps the "load_balancer" envelope, returning the object WITH its id and
//     status="deploying".
//
// Create is async: the real LoadBalancerService::deploy returns
// {success,message,load_balancer:{id,status:"deploying",...}} — the controller's
// Scribe annotation showing only {success,message} is stale (like VPC). The
// resource reads the id from the create response, then polls SHOW until
// status="active".
func TestCreateLoadBalancer_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Load balancer deployment initiated.","load_balancer":{"id":"lb-uuid-1","name":"my-lb","status":"deploying","hypervisor_group_id":"grp-1","lb_plan_id":"plan-1"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	body := map[string]any{
		"name":                "my-lb",
		"lb_plan_id":          "plan-1",
		"hypervisor_group_id": "grp-1",
	}
	obj, err := c.CreateLoadBalancer(context.Background(), body)
	if err != nil {
		t.Fatalf("CreateLoadBalancer returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/load-balancers" {
		t.Errorf("path = %s; want /api/load-balancers (plural)", gotPath)
	}
	if obj["id"] != "lb-uuid-1" {
		t.Errorf("obj[id] = %v; want lb-uuid-1", obj["id"])
	}
	if obj["status"] != "deploying" {
		t.Errorf("obj[status] = %v; want deploying", obj["status"])
	}
	if gotBody["name"] != "my-lb" || gotBody["lb_plan_id"] != "plan-1" || gotBody["hypervisor_group_id"] != "grp-1" {
		t.Errorf("create body = %v; missing required keys", gotBody)
	}
}

// TestCreateLoadBalancer_FeatureDisabled verifies that an in-controller feature
// gate (the LB routes are NOT wrapped in billing.enabled middleware, so gating
// arrives as HTTP 200 success:false — e.g. "Load balancing is not enabled for
// this hypervisor group", quota reached, no public IP) surfaces as an error (C3).
func TestCreateLoadBalancer_FeatureDisabled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// HTTP 200 with success:false — the LB core routes have no 403 billing gate.
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"Load balancing is not enabled for this hypervisor group."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.CreateLoadBalancer(context.Background(), map[string]any{"name": "x", "lb_plan_id": "p", "hypervisor_group_id": "g"})
	if err == nil {
		t.Fatal("CreateLoadBalancer: expected error for 200 success:false, got nil")
	}
	if !contains(err.Error(), "not enabled for this hypervisor group") {
		t.Errorf("error = %q; want it to contain the feature-gate message", err.Error())
	}
}

// TestCreateLoadBalancer_QuotaExceeded verifies a quota breach (returned as
// success:false) surfaces as an error.
func TestCreateLoadBalancer_QuotaExceeded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"You have reached your load balancer limit."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.CreateLoadBalancer(context.Background(), map[string]any{"name": "x", "lb_plan_id": "p", "hypervisor_group_id": "g"})
	if err == nil {
		t.Fatal("CreateLoadBalancer: expected error for quota success:false, got nil")
	}
	if !contains(err.Error(), "load balancer limit") {
		t.Errorf("error = %q; want the quota message", err.Error())
	}
}

// ---------------------------------------------------------------------------
// GetLoadBalancer
// ---------------------------------------------------------------------------

// TestGetLoadBalancer_Success verifies the SHOW route is SINGULAR and the
// "load_balancer" envelope (including the nested public_ip object and the
// embedded children arrays) is unwrapped.
func TestGetLoadBalancer_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"load_balancer":{"id":"lb-uuid-1","name":"my-lb","status":"active","hypervisor_group_id":"grp-1","lb_plan_id":"plan-1","instance_id":"inst-1","public_ip":{"id":"ip-1","ip":"203.0.113.5"},"frontends":[],"backends":[],"certificates":[]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.GetLoadBalancer(context.Background(), "lb-uuid-1")
	if err != nil {
		t.Fatalf("GetLoadBalancer returned error: %v", err)
	}
	if gotPath != "/api/load-balancer/lb-uuid-1" {
		t.Errorf("path = %s; want /api/load-balancer/lb-uuid-1 (singular)", gotPath)
	}
	if obj["id"] != "lb-uuid-1" {
		t.Errorf("obj[id] = %v; want lb-uuid-1", obj["id"])
	}
	if obj["status"] != "active" {
		t.Errorf("obj[status] = %v; want active", obj["status"])
	}
	pub, ok := obj["public_ip"].(map[string]any)
	if !ok {
		t.Fatalf("expected nested public_ip object, got %T", obj["public_ip"])
	}
	if pub["ip"] != "203.0.113.5" {
		t.Errorf("public_ip.ip = %v; want 203.0.113.5", pub["ip"])
	}
}

// TestGetLoadBalancer_NotFound verifies a 404 is an *APIError that IsNotFound
// matches (route-model-binding 404 for a missing or non-owned LB).
func TestGetLoadBalancer_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"No query results for model [LoadBalancer]."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.GetLoadBalancer(context.Background(), "missing")
	if err == nil {
		t.Fatal("GetLoadBalancer: expected error for 404, got nil")
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound = false; want true (err: %v)", err)
	}
}

func TestGetLoadBalancer_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if _, err := c.GetLoadBalancer(context.Background(), ""); err == nil {
		t.Fatal("GetLoadBalancer: expected error for empty id, got nil")
	}
}

// ---------------------------------------------------------------------------
// ListLoadBalancers
// ---------------------------------------------------------------------------

// TestListLoadBalancers_Success verifies the LIST route is PLURAL and the
// "load_balancers" paginator envelope is flattened via doList. The index wraps
// the paginator under a named "load_balancers" key, so the client passes that key.
func TestListLoadBalancers_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"load_balancers":{"current_page":1,"data":[{"id":"lb-1","status":"active"},{"id":"lb-2","status":"deploying"}],"total":2}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	items, err := c.ListLoadBalancers(context.Background())
	if err != nil {
		t.Fatalf("ListLoadBalancers returned error: %v", err)
	}
	if gotPath != "/api/load-balancers" {
		t.Errorf("path = %s; want /api/load-balancers", gotPath)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d; want 2", len(items))
	}
}

// ---------------------------------------------------------------------------
// DeleteLoadBalancer
// ---------------------------------------------------------------------------

func TestDeleteLoadBalancer_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Load balancer deleted."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DeleteLoadBalancer(context.Background(), "lb-1"); err != nil {
		t.Fatalf("DeleteLoadBalancer returned error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s; want DELETE", gotMethod)
	}
	if gotPath != "/api/load-balancer/lb-1" {
		t.Errorf("path = %s; want /api/load-balancer/lb-1 (singular)", gotPath)
	}
}

// TestDeleteLoadBalancer_Failure verifies a success:false delete response (e.g.
// the backing instance destroy threw) surfaces as an error (C3).
func TestDeleteLoadBalancer_Failure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"Failed to delete backing instance."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	err := c.DeleteLoadBalancer(context.Background(), "lb-1")
	if err == nil {
		t.Fatal("DeleteLoadBalancer: expected error for success:false, got nil")
	}
	if !contains(err.Error(), "delete backing instance") {
		t.Errorf("error = %q; want the failure message", err.Error())
	}
}

func TestDeleteLoadBalancer_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if err := c.DeleteLoadBalancer(context.Background(), ""); err == nil {
		t.Fatal("DeleteLoadBalancer: expected error for empty id, got nil")
	}
}
