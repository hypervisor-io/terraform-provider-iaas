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
// CreateNatGateway
// ---------------------------------------------------------------------------

// TestCreateNatGateway_Success verifies CreateNatGateway:
//   - POSTs to /vpc/{vpcId}/nat-gateway (parent vpc id in the path)
//   - sends the prebuilt body verbatim
//   - returns the unwrapped "gateway" object WITH its id and pending status.
//
// Create is async: the response carries status="pending"; the resource waits on
// the SHOW endpoint until status="active".
func TestCreateNatGateway_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"NAT gateway created successfully","gateway":{"id":"ngw-1","name":"natgw-prod","status":"pending","nat_enabled":true}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	body := map[string]any{
		"name":        "natgw-prod",
		"nat_enabled": true,
		"subnet_ids":  []string{"sub-1", "sub-2"},
	}
	obj, err := c.CreateNatGateway(context.Background(), "vpc-1", body)
	if err != nil {
		t.Fatalf("CreateNatGateway returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/vpc/vpc-1/nat-gateway" {
		t.Errorf("path = %s; want /api/vpc/vpc-1/nat-gateway", gotPath)
	}
	if obj["id"] != "ngw-1" {
		t.Errorf("obj[id] = %v; want ngw-1", obj["id"])
	}
	if obj["status"] != "pending" {
		t.Errorf("obj[status] = %v; want pending", obj["status"])
	}
	if gotBody["name"] != "natgw-prod" {
		t.Errorf("create body name = %v; want natgw-prod", gotBody["name"])
	}
	if gotBody["nat_enabled"] != true {
		t.Errorf("create body nat_enabled = %v; want true", gotBody["nat_enabled"])
	}
	ids, ok := gotBody["subnet_ids"].([]any)
	if !ok || len(ids) != 2 {
		t.Errorf("create body subnet_ids = %v; want 2 ids", gotBody["subnet_ids"])
	}
}

// TestCreateNatGateway_FeatureGated verifies a feature-gate failure (HTTP 200
// success:false - natgw not enabled / quota reached / already exists) surfaces
// the message as an error (C3). These routes are NOT billing-gated, so the
// gating is in-controller and arrives as 200 success:false, not 403.
func TestCreateNatGateway_FeatureGated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"NAT gateways are not available in this location"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.CreateNatGateway(context.Background(), "vpc-1", map[string]any{})
	if err == nil {
		t.Fatal("CreateNatGateway: expected error for 200 success:false, got nil")
	}
	if !contains(err.Error(), "not available") {
		t.Errorf("error = %q; want it to contain %q", err.Error(), "not available")
	}
}

// TestCreateNatGateway_EmptyVpcID verifies the empty-id guard.
func TestCreateNatGateway_EmptyVpcID(t *testing.T) {
	c := New("http://example/api", "tok", 10*time.Second, false)
	if _, err := c.CreateNatGateway(context.Background(), "", map[string]any{}); err == nil {
		t.Fatal("CreateNatGateway: expected error for empty vpcID, got nil")
	}
}

// ---------------------------------------------------------------------------
// GetNatGateway
// ---------------------------------------------------------------------------

// TestGetNatGateway_Success verifies the SHOW path (parent vpc id + gateway id)
// and that the "gateway" envelope (including embedded subnets[] and public_ip)
// is unwrapped.
func TestGetNatGateway_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"gateway":{"id":"ngw-1","name":"natgw-prod","status":"active","nat_enabled":true,"public_ip":{"ip":"1.2.3.4"},"subnets":[{"id":"sub-1"},{"id":"sub-2"}]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.GetNatGateway(context.Background(), "vpc-1", "ngw-1")
	if err != nil {
		t.Fatalf("GetNatGateway returned error: %v", err)
	}
	if gotPath != "/api/vpc/vpc-1/nat-gateway/ngw-1" {
		t.Errorf("path = %s; want /api/vpc/vpc-1/nat-gateway/ngw-1", gotPath)
	}
	if obj["status"] != "active" {
		t.Errorf("obj[status] = %v; want active", obj["status"])
	}
	pub, _ := obj["public_ip"].(map[string]any)
	if pub == nil || pub["ip"] != "1.2.3.4" {
		t.Errorf("obj[public_ip] = %v; want {ip:1.2.3.4}", obj["public_ip"])
	}
	subs, _ := obj["subnets"].([]any)
	if len(subs) != 2 {
		t.Errorf("obj[subnets] len = %d; want 2", len(subs))
	}
}

// TestGetNatGateway_NotFound verifies a 404 surfaces as an *APIError IsNotFound
// recognises.
func TestGetNatGateway_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"NAT Gateway not found"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.GetNatGateway(context.Background(), "vpc-1", "missing")
	if err == nil {
		t.Fatal("GetNatGateway: expected error for 404, got nil")
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound(err) = false; want true (err=%v)", err)
	}
}

// ---------------------------------------------------------------------------
// GetVpcNatGateway (INDEX - single gateway or null)
// ---------------------------------------------------------------------------

// TestGetVpcNatGateway_Success verifies the INDEX endpoint returns the single
// gateway from the {success,vpc,gateway} envelope.
func TestGetVpcNatGateway_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"vpc":{"id":"vpc-1"},"gateway":{"id":"ngw-1","status":"active"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.GetVpcNatGateway(context.Background(), "vpc-1")
	if err != nil {
		t.Fatalf("GetVpcNatGateway returned error: %v", err)
	}
	if gotPath != "/api/vpc/vpc-1/nat-gateway" {
		t.Errorf("path = %s; want /api/vpc/vpc-1/nat-gateway", gotPath)
	}
	if obj["id"] != "ngw-1" {
		t.Errorf("obj[id] = %v; want ngw-1", obj["id"])
	}
}

// TestGetVpcNatGateway_Null verifies a {gateway:null} envelope (the VPC has no
// gateway) surfaces as a 404-shaped *APIError.
func TestGetVpcNatGateway_Null(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"vpc":{"id":"vpc-1"},"gateway":null}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.GetVpcNatGateway(context.Background(), "vpc-1")
	if err == nil {
		t.Fatal("GetVpcNatGateway: expected 404-shaped error for null gateway, got nil")
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound(err) = false; want true (err=%v)", err)
	}
}

// ---------------------------------------------------------------------------
// UpdateNatGateway
// ---------------------------------------------------------------------------

// TestUpdateNatGateway_Success verifies the PATCH path + body + that the fresh
// gateway is unwrapped from "gateway".
func TestUpdateNatGateway_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"updated","gateway":{"id":"ngw-1","name":"natgw-new","nat_enabled":false,"status":"active"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.UpdateNatGateway(context.Background(), "vpc-1", "ngw-1", map[string]any{"name": "natgw-new", "nat_enabled": false})
	if err != nil {
		t.Fatalf("UpdateNatGateway returned error: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %s; want PATCH", gotMethod)
	}
	if gotPath != "/api/vpc/vpc-1/nat-gateway/ngw-1" {
		t.Errorf("path = %s; want /api/vpc/vpc-1/nat-gateway/ngw-1", gotPath)
	}
	if gotBody["name"] != "natgw-new" {
		t.Errorf("body name = %v; want natgw-new", gotBody["name"])
	}
	if obj["name"] != "natgw-new" {
		t.Errorf("obj[name] = %v; want natgw-new", obj["name"])
	}
}

// ---------------------------------------------------------------------------
// Enable / Disable
// ---------------------------------------------------------------------------

// TestEnableNatGateway_Success verifies the enable path + envelope unwrap.
func TestEnableNatGateway_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"enabled","gateway":{"id":"ngw-1","nat_enabled":true,"status":"active"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.EnableNatGateway(context.Background(), "vpc-1", "ngw-1")
	if err != nil {
		t.Fatalf("EnableNatGateway returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/vpc/vpc-1/nat-gateway/ngw-1/enable" {
		t.Errorf("path = %s; want .../enable", gotPath)
	}
	if obj["nat_enabled"] != true {
		t.Errorf("obj[nat_enabled] = %v; want true", obj["nat_enabled"])
	}
}

// TestEnableNatGateway_BandwidthSuspended verifies a 200 success:false (the
// gateway is bandwidth-suspended) surfaces as an error.
func TestEnableNatGateway_BandwidthSuspended(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"Gateway is suspended due to bandwidth overage"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.EnableNatGateway(context.Background(), "vpc-1", "ngw-1")
	if err == nil {
		t.Fatal("EnableNatGateway: expected error for 200 success:false, got nil")
	}
	if !contains(err.Error(), "bandwidth") {
		t.Errorf("error = %q; want it to contain %q", err.Error(), "bandwidth")
	}
}

// TestDisableNatGateway_Success verifies the disable path + envelope unwrap.
func TestDisableNatGateway_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"disabled","gateway":{"id":"ngw-1","nat_enabled":false,"status":"active"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.DisableNatGateway(context.Background(), "vpc-1", "ngw-1")
	if err != nil {
		t.Fatalf("DisableNatGateway returned error: %v", err)
	}
	if gotPath != "/api/vpc/vpc-1/nat-gateway/ngw-1/disable" {
		t.Errorf("path = %s; want .../disable", gotPath)
	}
	if obj["nat_enabled"] != false {
		t.Errorf("obj[nat_enabled] = %v; want false", obj["nat_enabled"])
	}
}

// ---------------------------------------------------------------------------
// Attach / Detach subnet
// ---------------------------------------------------------------------------

// TestAttachNatGatewaySubnet_Success verifies the attach path + body {subnet_id}
// + envelope unwrap.
func TestAttachNatGatewaySubnet_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"attached","gateway":{"id":"ngw-1","status":"active","subnets":[{"id":"sub-9"}]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.AttachNatGatewaySubnet(context.Background(), "vpc-1", "ngw-1", "sub-9")
	if err != nil {
		t.Fatalf("AttachNatGatewaySubnet returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/vpc/vpc-1/nat-gateway/ngw-1/subnet" {
		t.Errorf("path = %s; want .../subnet", gotPath)
	}
	if gotBody["subnet_id"] != "sub-9" {
		t.Errorf("body subnet_id = %v; want sub-9", gotBody["subnet_id"])
	}
	if obj["id"] != "ngw-1" {
		t.Errorf("obj[id] = %v; want ngw-1", obj["id"])
	}
}

// TestAttachNatGatewaySubnet_NotPrivate verifies a 200 success:false (subnet not
// private / already attached) surfaces as an error.
func TestAttachNatGatewaySubnet_NotPrivate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"Only private subnets can be attached"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.AttachNatGatewaySubnet(context.Background(), "vpc-1", "ngw-1", "sub-pub")
	if err == nil {
		t.Fatal("AttachNatGatewaySubnet: expected error for 200 success:false, got nil")
	}
	if !contains(err.Error(), "private") {
		t.Errorf("error = %q; want it to contain %q", err.Error(), "private")
	}
}

// TestDetachNatGatewaySubnet_Success verifies the detach path (DELETE, subnet id
// in the path) + envelope unwrap.
func TestDetachNatGatewaySubnet_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"detached","gateway":{"id":"ngw-1","status":"active","subnets":[]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.DetachNatGatewaySubnet(context.Background(), "vpc-1", "ngw-1", "sub-9")
	if err != nil {
		t.Fatalf("DetachNatGatewaySubnet returned error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s; want DELETE", gotMethod)
	}
	if gotPath != "/api/vpc/vpc-1/nat-gateway/ngw-1/subnet/sub-9" {
		t.Errorf("path = %s; want .../subnet/sub-9", gotPath)
	}
	if obj["id"] != "ngw-1" {
		t.Errorf("obj[id] = %v; want ngw-1", obj["id"])
	}
}

// ---------------------------------------------------------------------------
// DeleteNatGateway
// ---------------------------------------------------------------------------

// TestDeleteNatGateway_Success verifies the DELETE path and that a {success}
// body is accepted.
func TestDeleteNatGateway_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"NAT gateway deleted successfully"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DeleteNatGateway(context.Background(), "vpc-1", "ngw-1"); err != nil {
		t.Fatalf("DeleteNatGateway returned error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s; want DELETE", gotMethod)
	}
	if gotPath != "/api/vpc/vpc-1/nat-gateway/ngw-1" {
		t.Errorf("path = %s; want /api/vpc/vpc-1/nat-gateway/ngw-1", gotPath)
	}
}

// TestDeleteNatGateway_Failure verifies a 200 success:false delete failure
// surfaces as an error (C3).
func TestDeleteNatGateway_Failure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"teardown failed"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	err := c.DeleteNatGateway(context.Background(), "vpc-1", "ngw-1")
	if err == nil {
		t.Fatal("DeleteNatGateway: expected error for 200 success:false, got nil")
	}
	if !contains(err.Error(), "teardown failed") {
		t.Errorf("error = %q; want it to contain %q", err.Error(), "teardown failed")
	}
}

// TestNatGateway_EmptyIDGuards exercises the empty-id guards on the path-id args.
func TestNatGateway_EmptyIDGuards(t *testing.T) {
	c := New("http://example/api", "tok", 10*time.Second, false)
	ctx := context.Background()

	if _, err := c.GetNatGateway(ctx, "", "x"); err == nil {
		t.Error("GetNatGateway empty vpcID: expected error")
	}
	if _, err := c.GetNatGateway(ctx, "v", ""); err == nil {
		t.Error("GetNatGateway empty id: expected error")
	}
	if _, err := c.UpdateNatGateway(ctx, "", "x", nil); err == nil {
		t.Error("UpdateNatGateway empty vpcID: expected error")
	}
	if _, err := c.EnableNatGateway(ctx, "v", ""); err == nil {
		t.Error("EnableNatGateway empty id: expected error")
	}
	if _, err := c.AttachNatGatewaySubnet(ctx, "v", "g", ""); err == nil {
		t.Error("AttachNatGatewaySubnet empty subnetID: expected error")
	}
	if _, err := c.DetachNatGatewaySubnet(ctx, "v", "", "s"); err == nil {
		t.Error("DetachNatGatewaySubnet empty id: expected error")
	}
	if err := c.DeleteNatGateway(ctx, "", "x"); err == nil {
		t.Error("DeleteNatGateway empty vpcID: expected error")
	}
}
