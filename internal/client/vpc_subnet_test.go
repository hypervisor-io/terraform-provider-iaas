package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// NOTE: like vpc_test.go / ssh_key_test.go, these tests use net/http/httptest
// directly rather than internal/acctest.MockServer. acctest imports
// internal/provider which imports internal/client, so importing acctest here
// would create an import cycle.
//
// vpc_subnet is a CHILD resource: the parent VPC id is part of the URL path.
// Note the path asymmetry from the controller (routes/user_api.php):
//   - collection/create is PLURAL:  POST /vpc/{vpcId}/subnets
//   - item ops are SINGULAR:        GET/PATCH/DELETE /vpc/{vpcId}/subnet/{id}

// TestCreateVPCSubnet_Success verifies that CreateVPCSubnet:
//   - POSTs to the PLURAL collection path /vpc/{vpcID}/subnets
//   - sends the prebuilt body the caller supplied (cidr present; unset
//     name/type omitted — gateway/netmask are DERIVED server-side, never sent)
//   - returns the unwrapped subnet object WITH its id and derived fields.
//
// The subnet ROW is created synchronously (id returned immediately). IP
// generation is async on a queue, so used/free populate later — there is NO
// status field to wait on.
func TestCreateVPCSubnet_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK) // create returns 200, not 201
		_, _ = w.Write([]byte(`{"success":true,"message":"Subnet created","subnet":{"id":"s1","cidr":"192.168.10.0/24","netmask":"255.255.255.0","gateway":"192.168.10.1","type":"public","name":"web","used":0,"free":253}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	// Caller passes only cidr (name/type omitted) to prove the client sends
	// exactly the body it was given.
	body := map[string]any{
		"cidr": "192.168.10.0/24",
	}
	obj, err := c.CreateVPCSubnet(context.Background(), "v1", body)
	if err != nil {
		t.Fatalf("CreateVPCSubnet returned error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/vpc/v1/subnets" {
		t.Errorf("path = %s; want /api/vpc/v1/subnets (PLURAL collection)", gotPath)
	}
	if obj["id"] != "s1" {
		t.Errorf("obj[id] = %v; want s1", obj["id"])
	}
	if obj["gateway"] != "192.168.10.1" {
		t.Errorf("obj[gateway] = %v; want 192.168.10.1", obj["gateway"])
	}

	// Body must carry cidr and OMIT unset name/type. It must NOT carry the
	// server-derived gateway/netmask.
	if gotBody["cidr"] != "192.168.10.0/24" {
		t.Errorf("body[cidr] = %v; want 192.168.10.0/24", gotBody["cidr"])
	}
	if _, present := gotBody["name"]; present {
		t.Errorf("body must NOT include name when caller omits it; body = %v", gotBody)
	}
	if _, present := gotBody["type"]; present {
		t.Errorf("body must NOT include type when caller omits it; body = %v", gotBody)
	}
	for _, derived := range []string{"gateway", "netmask"} {
		if _, present := gotBody[derived]; present {
			t.Errorf("body must NOT include server-derived %q; body = %v", derived, gotBody)
		}
	}
}

// TestCreateVPCSubnet_Failure verifies a 200 success:false response surfaces the
// API message as an error (C3 — create signals failure at HTTP 200, e.g. CIDR
// overlap or out-of-range).
func TestCreateVPCSubnet_Failure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"CIDR overlaps an existing subnet"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.CreateVPCSubnet(context.Background(), "v1", map[string]any{"cidr": "192.168.10.0/24"})
	if err == nil {
		t.Fatal("CreateVPCSubnet: expected error for success:false, got nil")
	}
	if !contains(err.Error(), "CIDR overlaps an existing subnet") {
		t.Errorf("error = %q; want it to contain %q", err.Error(), "CIDR overlaps an existing subnet")
	}
}

// TestGetVPCSubnet_Success verifies GET /vpc/{vpcID}/subnet/{id} (SINGULAR)
// unwraps the subnet object.
func TestGetVPCSubnet_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"subnet":{"id":"s1","cidr":"192.168.10.0/24","netmask":"255.255.255.0","gateway":"192.168.10.1","type":"public","name":"web","used":2,"free":251,"ips":[]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.GetVPCSubnet(context.Background(), "v1", "s1")
	if err != nil {
		t.Fatalf("GetVPCSubnet returned error: %v", err)
	}
	if gotPath != "/api/vpc/v1/subnet/s1" {
		t.Errorf("path = %s; want /api/vpc/v1/subnet/s1 (SINGULAR)", gotPath)
	}
	if obj["id"] != "s1" {
		t.Errorf("obj[id] = %v; want s1", obj["id"])
	}
}

// TestGetVPCSubnet_NotFound verifies a 404 (absent / wrong vpc) is recognised by
// client.IsNotFound.
func TestGetVPCSubnet_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not found."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.GetVPCSubnet(context.Background(), "v1", "missing")
	if err == nil {
		t.Fatal("GetVPCSubnet: expected error for 404, got nil")
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound = false; want true (err: %v)", err)
	}
}

// TestUpdateVPCSubnet_Success verifies PATCH /vpc/{vpcID}/subnet/{id} (SINGULAR)
// sends the mutable name and returns the FRESH subnet object.
func TestUpdateVPCSubnet_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Subnet updated","subnet":{"id":"s1","cidr":"192.168.10.0/24","netmask":"255.255.255.0","gateway":"192.168.10.1","type":"public","name":"renamed","used":2,"free":251}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.UpdateVPCSubnet(context.Background(), "v1", "s1", map[string]any{"name": "renamed"})
	if err != nil {
		t.Fatalf("UpdateVPCSubnet returned error: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %s; want PATCH", gotMethod)
	}
	if gotPath != "/api/vpc/v1/subnet/s1" {
		t.Errorf("path = %s; want /api/vpc/v1/subnet/s1 (SINGULAR)", gotPath)
	}
	if gotBody["name"] != "renamed" {
		t.Errorf("body[name] = %v; want renamed", gotBody["name"])
	}
	if obj["name"] != "renamed" {
		t.Errorf("obj[name] = %v; want renamed (fresh object)", obj["name"])
	}
}

// TestDeleteVPCSubnet_Success verifies DELETE /vpc/{vpcID}/subnet/{id}
// (SINGULAR) with success:true.
func TestDeleteVPCSubnet_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"deleted"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DeleteVPCSubnet(context.Background(), "v1", "s1"); err != nil {
		t.Fatalf("DeleteVPCSubnet returned error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s; want DELETE", gotMethod)
	}
	if gotPath != "/api/vpc/v1/subnet/s1" {
		t.Errorf("path = %s; want /api/vpc/v1/subnet/s1 (SINGULAR)", gotPath)
	}
}

// TestDeleteVPCSubnet_Failure verifies a 200 success:false delete surfaces the
// message as an error (C3 — e.g. an IP is in use).
func TestDeleteVPCSubnet_Failure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"an IP in this subnet is in use"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	err := c.DeleteVPCSubnet(context.Background(), "v1", "s1")
	if err == nil {
		t.Fatal("DeleteVPCSubnet: expected error for success:false, got nil")
	}
	if !contains(err.Error(), "an IP in this subnet is in use") {
		t.Errorf("error = %q; want it to contain %q", err.Error(), "an IP in this subnet is in use")
	}
}
