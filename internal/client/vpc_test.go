package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// NOTE: like ssh_key_test.go, this test uses net/http/httptest directly rather
// than internal/acctest.MockServer. acctest imports internal/provider which
// imports internal/client, so importing acctest here would create an import
// cycle.

// TestCreateVPC_Success verifies that CreateVPC:
//   - POSTs to /vpcs (plural)
//   - sends the prebuilt body the caller supplied (name, cidr,
//     hypervisor_group_id; description only when set)
//   - returns the unwrapped vpc object WITH its id and appended vni_number.
//
// VPC create is SYNCHRONOUS: the controller's VpcService::store returns the
// refreshed object (with id) under key "vpc" at HTTP 200. There is no task and
// no list-and-match read-back.
func TestCreateVPC_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK) // create returns 200, not 201
		_, _ = w.Write([]byte(`{"success":true,"message":"VPC created","vpc":{"id":"v1","name":"prod","cidr":"10.0.0.0/24","hypervisor_group_id":"g1","description":"web tier","vni_number":4097}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	body := map[string]any{
		"name":                "prod",
		"cidr":                "10.0.0.0/24",
		"hypervisor_group_id": "g1",
		"description":         "web tier",
	}
	obj, err := c.CreateVPC(context.Background(), body)
	if err != nil {
		t.Fatalf("CreateVPC returned error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/vpcs" {
		t.Errorf("path = %s; want /api/vpcs (plural)", gotPath)
	}
	if obj["id"] != "v1" {
		t.Errorf("obj[id] = %v; want v1", obj["id"])
	}
	// vni_number is a JSON number → float64 after decode.
	if vni, ok := obj["vni_number"].(float64); !ok || vni != 4097 {
		t.Errorf("obj[vni_number] = %v (%T); want 4097", obj["vni_number"], obj["vni_number"])
	}

	// Request body must carry exactly the keys the caller passed.
	if gotBody["name"] != "prod" {
		t.Errorf("body[name] = %v; want prod", gotBody["name"])
	}
	if gotBody["cidr"] != "10.0.0.0/24" {
		t.Errorf("body[cidr] = %v; want 10.0.0.0/24", gotBody["cidr"])
	}
	if gotBody["hypervisor_group_id"] != "g1" {
		t.Errorf("body[hypervisor_group_id] = %v; want g1", gotBody["hypervisor_group_id"])
	}
	if gotBody["description"] != "web tier" {
		t.Errorf("body[description] = %v; want 'web tier'", gotBody["description"])
	}
}

// TestCreateVPC_OmitsDescription verifies the client sends exactly the body it
// was given: when the caller omits description, no description key is sent.
func TestCreateVPC_OmitsDescription(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"vpc":{"id":"v1","name":"prod","cidr":"10.0.0.0/24","hypervisor_group_id":"g1","vni_number":4098}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	body := map[string]any{
		"name":                "prod",
		"cidr":                "10.0.0.0/24",
		"hypervisor_group_id": "g1",
	}
	if _, err := c.CreateVPC(context.Background(), body); err != nil {
		t.Fatalf("CreateVPC returned error: %v", err)
	}
	if _, present := gotBody["description"]; present {
		t.Errorf("body must NOT include description when caller omits it; body = %v", gotBody)
	}
}

// TestCreateVPC_Failure verifies a 200 success:false response surfaces the API
// message as an error (C3 - create signals failure at HTTP 200, e.g. invalid
// CIDR, location not VPC-enabled, or quota exceeded).
func TestCreateVPC_Failure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"location is not VPC-enabled"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.CreateVPC(context.Background(), map[string]any{"name": "prod", "cidr": "10.0.0.0/24", "hypervisor_group_id": "g1"})
	if err == nil {
		t.Fatal("CreateVPC: expected error for success:false, got nil")
	}
	if !contains(err.Error(), "location is not VPC-enabled") {
		t.Errorf("error = %q; want it to contain %q", err.Error(), "location is not VPC-enabled")
	}
}

// TestGetVPC_Success verifies GET /vpc/{id} (singular) unwraps vpc.
func TestGetVPC_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"vpc":{"id":"v1","name":"prod","cidr":"10.0.0.0/24","hypervisor_group_id":"g1","vni_number":4097,"subnets":[]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.GetVPC(context.Background(), "v1")
	if err != nil {
		t.Fatalf("GetVPC returned error: %v", err)
	}
	if gotPath != "/api/vpc/v1" {
		t.Errorf("path = %s; want /api/vpc/v1 (singular)", gotPath)
	}
	if obj["id"] != "v1" {
		t.Errorf("obj[id] = %v; want v1", obj["id"])
	}
}

// TestGetVPC_NotFound verifies a 404 is recognised by client.IsNotFound.
func TestGetVPC_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not found."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.GetVPC(context.Background(), "missing")
	if err == nil {
		t.Fatal("GetVPC: expected error for 404, got nil")
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound = false; want true (err: %v)", err)
	}
}

// TestListVPCs_Success verifies GET /vpcs unwraps the Laravel paginator.
func TestListVPCs_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"current_page":1,"data":[{"id":"v1","name":"prod"},{"id":"v2","name":"staging"}],"total":2}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	items, err := c.ListVPCs(context.Background())
	if err != nil {
		t.Fatalf("ListVPCs returned error: %v", err)
	}
	if gotPath != "/api/vpcs" {
		t.Errorf("path = %s; want /api/vpcs", gotPath)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d; want 2", len(items))
	}
	if items[0]["id"] != "v1" {
		t.Errorf("items[0][id] = %v; want v1", items[0]["id"])
	}
}

// TestDeleteVPC_Success verifies DELETE /vpc/{id} (singular) with success:true.
func TestDeleteVPC_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"deleted"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DeleteVPC(context.Background(), "v1"); err != nil {
		t.Fatalf("DeleteVPC returned error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s; want DELETE", gotMethod)
	}
	if gotPath != "/api/vpc/v1" {
		t.Errorf("path = %s; want /api/vpc/v1 (singular)", gotPath)
	}
}

// TestDeleteVPC_Failure verifies a 200 success:false delete surfaces the
// message as an error (C3 - e.g. a subnet still has used IPs).
func TestDeleteVPC_Failure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"subnet still has used IPs"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	err := c.DeleteVPC(context.Background(), "v1")
	if err == nil {
		t.Fatal("DeleteVPC: expected error for success:false, got nil")
	}
	if !contains(err.Error(), "subnet still has used IPs") {
		t.Errorf("error = %q; want it to contain %q", err.Error(), "subnet still has used IPs")
	}
}
