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
// rather than internal/acctest.MockServer. acctest imports internal/provider
// which imports internal/client, so importing acctest here would create an
// import cycle.

// ---------------------------------------------------------------------------
// AllocateStaticIP
// ---------------------------------------------------------------------------

// TestAllocateStaticIP_Success verifies that AllocateStaticIP:
//   - POSTs to /static-ips/allocate (with the /allocate suffix, plural base)
//   - sends the prebuilt body the caller supplied
//   - returns the unwrapped static_ip object WITH its id
//
// Allocation is synchronous: the response carries the id directly in the
// "static_ip" envelope at HTTP 200.
func TestAllocateStaticIP_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Static IP 203.0.113.10 allocated successfully.","static_ip":{"id":"sip-uuid-1","status":"allocated","ip":{"id":"ip-uuid-1","ip":"203.0.113.10","subnet_id":"sub-uuid-1"},"hypervisor_group":{"id":"grp-uuid-1","name":"US East"}}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	body := map[string]any{
		"ip_id":               "ip-uuid-1",
		"hypervisor_group_id": "grp-uuid-1",
	}
	obj, err := c.AllocateStaticIP(context.Background(), body)
	if err != nil {
		t.Fatalf("AllocateStaticIP returned error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/static-ips/allocate" {
		t.Errorf("path = %s; want /api/static-ips/allocate", gotPath)
	}
	if obj["id"] != "sip-uuid-1" {
		t.Errorf("obj[id] = %v; want sip-uuid-1", obj["id"])
	}
	if obj["status"] != "allocated" {
		t.Errorf("obj[status] = %v; want allocated", obj["status"])
	}

	// Request body must carry exactly the keys the caller passed.
	if gotBody["ip_id"] != "ip-uuid-1" {
		t.Errorf("body[ip_id] = %v; want ip-uuid-1", gotBody["ip_id"])
	}
	if gotBody["hypervisor_group_id"] != "grp-uuid-1" {
		t.Errorf("body[hypervisor_group_id] = %v; want grp-uuid-1", gotBody["hypervisor_group_id"])
	}
}

// TestAllocateStaticIP_InsufficientCredits verifies a 200 success:false response
// (e.g. insufficient credits, quota exceeded, IP already allocated) surfaces
// the API message as an error (C3).
func TestAllocateStaticIP_InsufficientCredits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"Insufficient credits to allocate a static IP."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.AllocateStaticIP(context.Background(), map[string]any{
		"ip_id":               "ip-uuid-1",
		"hypervisor_group_id": "grp-uuid-1",
	})
	if err == nil {
		t.Fatal("AllocateStaticIP: expected error for success:false, got nil")
	}
	if !contains(err.Error(), "Insufficient credits") {
		t.Errorf("error = %q; want it to contain %q", err.Error(), "Insufficient credits")
	}
}

// TestAllocateStaticIP_BillingDisabled verifies that a 403 from the
// billing.enabled middleware is surfaced as an *APIError (not nil).
func TestAllocateStaticIP_BillingDisabled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"success":false,"message":"This feature is unavailable because billing is disabled."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.AllocateStaticIP(context.Background(), map[string]any{
		"ip_id":               "ip-uuid-1",
		"hypervisor_group_id": "grp-uuid-1",
	})
	if err == nil {
		t.Fatal("AllocateStaticIP: expected error for 403, got nil")
	}
	if !contains(err.Error(), "billing is disabled") {
		t.Errorf("error = %q; want it to contain %q", err.Error(), "billing is disabled")
	}
}

// ---------------------------------------------------------------------------
// GetStaticIP (list-and-scan)
// ---------------------------------------------------------------------------

// TestGetStaticIP_Found verifies GetStaticIP returns the matching item from
// the paginator list.
func TestGetStaticIP_Found(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"current_page":1,"data":[{"id":"sip-uuid-1","status":"allocated","ip":{"ip":"203.0.113.10"}},{"id":"sip-uuid-2","status":"attached","ip":{"ip":"203.0.113.11"}}],"total":2}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.GetStaticIP(context.Background(), "sip-uuid-1")
	if err != nil {
		t.Fatalf("GetStaticIP returned error: %v", err)
	}
	if gotPath != "/api/static-ips" {
		t.Errorf("path = %s; want /api/static-ips (plural)", gotPath)
	}
	if obj["id"] != "sip-uuid-1" {
		t.Errorf("obj[id] = %v; want sip-uuid-1", obj["id"])
	}
	if obj["status"] != "allocated" {
		t.Errorf("obj[status] = %v; want allocated", obj["status"])
	}
}

// TestGetStaticIP_NotFound verifies GetStaticIP returns a 404 *APIError
// (IsNotFound=true) when the id is absent from the list.
func TestGetStaticIP_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"current_page":1,"data":[],"total":0}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.GetStaticIP(context.Background(), "missing-uuid")
	if err == nil {
		t.Fatal("GetStaticIP: expected error for absent id, got nil")
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound = false; want true (err: %v)", err)
	}
}

// TestGetStaticIP_EmptyID verifies the empty-id guard.
func TestGetStaticIP_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	_, err := c.GetStaticIP(context.Background(), "")
	if err == nil {
		t.Fatal("GetStaticIP: expected error for empty id, got nil")
	}
}

// ---------------------------------------------------------------------------
// ListStaticIPs
// ---------------------------------------------------------------------------

// TestListStaticIPs_Success verifies GET /static-ips returns the paginator list.
func TestListStaticIPs_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"current_page":1,"data":[{"id":"sip-uuid-1","status":"allocated"},{"id":"sip-uuid-2","status":"attached"}],"total":2}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	items, err := c.ListStaticIPs(context.Background())
	if err != nil {
		t.Fatalf("ListStaticIPs returned error: %v", err)
	}
	if gotPath != "/api/static-ips" {
		t.Errorf("path = %s; want /api/static-ips", gotPath)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d; want 2", len(items))
	}
	if items[0]["id"] != "sip-uuid-1" {
		t.Errorf("items[0][id] = %v; want sip-uuid-1", items[0]["id"])
	}
}

// ---------------------------------------------------------------------------
// DeleteStaticIP
// ---------------------------------------------------------------------------

// TestDeleteStaticIP_Success verifies DELETE /static-ip/{id} (singular) with success:true.
func TestDeleteStaticIP_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Static IP deallocated successfully."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DeleteStaticIP(context.Background(), "sip-uuid-1"); err != nil {
		t.Fatalf("DeleteStaticIP returned error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s; want DELETE", gotMethod)
	}
	if gotPath != "/api/static-ip/sip-uuid-1" {
		t.Errorf("path = %s; want /api/static-ip/sip-uuid-1 (singular)", gotPath)
	}
}

// TestDeleteStaticIP_AttachedFailure verifies a 200 success:false response
// (e.g. IP still attached to an instance) is surfaced as an error (C3).
func TestDeleteStaticIP_AttachedFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"Cannot deallocate a static IP that is attached to an instance. Detach it first."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	err := c.DeleteStaticIP(context.Background(), "sip-uuid-1")
	if err == nil {
		t.Fatal("DeleteStaticIP: expected error for success:false, got nil")
	}
	if !contains(err.Error(), "Detach it first") {
		t.Errorf("error = %q; want it to contain %q", err.Error(), "Detach it first")
	}
}

// TestDeleteStaticIP_EmptyID verifies the empty-id guard.
func TestDeleteStaticIP_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	err := c.DeleteStaticIP(context.Background(), "")
	if err == nil {
		t.Fatal("DeleteStaticIP: expected error for empty id, got nil")
	}
}
