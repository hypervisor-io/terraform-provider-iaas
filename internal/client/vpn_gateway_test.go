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
// CreateVpnGateway
// ---------------------------------------------------------------------------

// TestCreateVpnGateway_Success verifies CreateVpnGateway:
//   - POSTs to /vpc/{vpcId}/vpn-gateway (parent vpc id in the path — create is
//     the ONLY nested operation)
//   - sends the prebuilt body verbatim (vpngw_plan_id + vpc_subnet_id required)
//   - returns the unwrapped "gateway" object WITH its id and deploying status.
//
// Create is async: the response carries status="deploying"; the resource waits on
// the SHOW endpoint until status="active".
func TestCreateVpnGateway_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"VPN gateway is being deployed","gateway":{"id":"vgw-1","name":"vpngw-prod","status":"deploying","tunnel_subnet":"10.99.0.0/24","listen_port":51820,"public_key":"pub=="}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	body := map[string]any{
		"vpngw_plan_id": "plan-1",
		"vpc_subnet_id": "sub-1",
		"name":          "vpngw-prod",
		"tunnel_subnet": "10.99.0.0/24",
		"listen_port":   51820,
	}
	obj, err := c.CreateVpnGateway(context.Background(), "vpc-1", body)
	if err != nil {
		t.Fatalf("CreateVpnGateway returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/vpc/vpc-1/vpn-gateway" {
		t.Errorf("path = %s; want /api/vpc/vpc-1/vpn-gateway", gotPath)
	}
	if obj["id"] != "vgw-1" {
		t.Errorf("obj[id] = %v; want vgw-1", obj["id"])
	}
	if obj["status"] != "deploying" {
		t.Errorf("obj[status] = %v; want deploying", obj["status"])
	}
	if gotBody["vpngw_plan_id"] != "plan-1" {
		t.Errorf("body vpngw_plan_id = %v; want plan-1", gotBody["vpngw_plan_id"])
	}
	if gotBody["vpc_subnet_id"] != "sub-1" {
		t.Errorf("body vpc_subnet_id = %v; want sub-1", gotBody["vpc_subnet_id"])
	}
	if gotBody["name"] != "vpngw-prod" {
		t.Errorf("body name = %v; want vpngw-prod", gotBody["name"])
	}
}

// TestCreateVpnGateway_FeatureGated verifies a feature-gate failure (HTTP 403 —
// vpngw not enabled for the location) surfaces as an error. These routes are NOT
// billing-gated, so the gating is in-controller (403/422/200-success:false).
func TestCreateVpnGateway_FeatureGated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"success":false,"message":"VPN gateways are not available in this location"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.CreateVpnGateway(context.Background(), "vpc-1", map[string]any{})
	if err == nil {
		t.Fatal("CreateVpnGateway: expected error for 403, got nil")
	}
	if !contains(err.Error(), "not available") {
		t.Errorf("error = %q; want it to contain %q", err.Error(), "not available")
	}
}

// TestCreateVpnGateway_OnePerVpc verifies the one-per-VPC guard (HTTP 422)
// surfaces as an error.
func TestCreateVpnGateway_OnePerVpc(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"success":false,"message":"This VPC already has a VPN gateway"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.CreateVpnGateway(context.Background(), "vpc-1", map[string]any{})
	if err == nil {
		t.Fatal("CreateVpnGateway: expected error for 422, got nil")
	}
	if !contains(err.Error(), "already has") {
		t.Errorf("error = %q; want it to contain %q", err.Error(), "already has")
	}
}

// TestCreateVpnGateway_ServiceException verifies a service exception during deploy
// (HTTP 200 success:false — e.g. subnet not public) surfaces as an error (C3).
func TestCreateVpnGateway_ServiceException(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"VPN gateway must be deployed in a public subnet"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.CreateVpnGateway(context.Background(), "vpc-1", map[string]any{})
	if err == nil {
		t.Fatal("CreateVpnGateway: expected error for 200 success:false, got nil")
	}
	if !contains(err.Error(), "public subnet") {
		t.Errorf("error = %q; want it to contain %q", err.Error(), "public subnet")
	}
}

// TestCreateVpnGateway_EmptyVpcID verifies the empty-id guard.
func TestCreateVpnGateway_EmptyVpcID(t *testing.T) {
	c := New("http://example/api", "tok", 10*time.Second, false)
	if _, err := c.CreateVpnGateway(context.Background(), "", map[string]any{}); err == nil {
		t.Fatal("CreateVpnGateway: expected error for empty vpcID, got nil")
	}
}

// ---------------------------------------------------------------------------
// GetVpnGateway
// ---------------------------------------------------------------------------

// TestGetVpnGateway_Success verifies the FLAT SHOW path (no vpc in the path) and
// that the "gateway" envelope (including the embedded peers[]) is unwrapped.
func TestGetVpnGateway_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"gateway":{"id":"vgw-1","name":"vpngw-prod","status":"active","public_key":"pub==","tunnel_subnet":"10.99.0.0/24","listen_port":51820,"vpc_ip":"192.168.0.2","peers":[{"id":"peer-1","type":"road_warrior","name":"laptop"}]},"other_gateways":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.GetVpnGateway(context.Background(), "vgw-1")
	if err != nil {
		t.Fatalf("GetVpnGateway returned error: %v", err)
	}
	if gotPath != "/api/vpn-gateway/vgw-1" {
		t.Errorf("path = %s; want /api/vpn-gateway/vgw-1", gotPath)
	}
	if obj["status"] != "active" {
		t.Errorf("obj[status] = %v; want active", obj["status"])
	}
	if obj["public_key"] != "pub==" {
		t.Errorf("obj[public_key] = %v; want pub==", obj["public_key"])
	}
	peers, _ := obj["peers"].([]any)
	if len(peers) != 1 {
		t.Errorf("obj[peers] len = %d; want 1", len(peers))
	}
}

// TestGetVpnGateway_NotFound verifies a 404 surfaces as an *APIError IsNotFound
// recognises.
func TestGetVpnGateway_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"VPN Gateway not found"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.GetVpnGateway(context.Background(), "missing")
	if err == nil {
		t.Fatal("GetVpnGateway: expected error for 404, got nil")
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound(err) = false; want true (err=%v)", err)
	}
}

// ---------------------------------------------------------------------------
// DeleteVpnGateway
// ---------------------------------------------------------------------------

// TestDeleteVpnGateway_Success verifies the FLAT DELETE path and that a {success}
// body is accepted.
func TestDeleteVpnGateway_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"VPN gateway deleted successfully"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DeleteVpnGateway(context.Background(), "vgw-1"); err != nil {
		t.Fatalf("DeleteVpnGateway returned error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s; want DELETE", gotMethod)
	}
	if gotPath != "/api/vpn-gateway/vgw-1" {
		t.Errorf("path = %s; want /api/vpn-gateway/vgw-1", gotPath)
	}
}

// TestDeleteVpnGateway_Failure verifies a 200 success:false delete failure
// surfaces as an error (C3).
func TestDeleteVpnGateway_Failure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"teardown failed"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	err := c.DeleteVpnGateway(context.Background(), "vgw-1")
	if err == nil {
		t.Fatal("DeleteVpnGateway: expected error for 200 success:false, got nil")
	}
	if !contains(err.Error(), "teardown failed") {
		t.Errorf("error = %q; want it to contain %q", err.Error(), "teardown failed")
	}
}

// ---------------------------------------------------------------------------
// AddVpnPeer
// ---------------------------------------------------------------------------

// TestAddVpnPeer_Success verifies the peer-add path + body + that the "peer"
// envelope is unwrapped WITH the new peer id.
func TestAddVpnPeer_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Peer added successfully","peer":{"id":"peer-1","type":"road_warrior","name":"laptop","tunnel_ip":"10.99.0.2","allowed_ips":["10.99.0.2/32"],"keepalive":25,"enabled":1}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	body := map[string]any{
		"type":        "road_warrior",
		"name":        "laptop",
		"public_key":  "clientpub==",
		"allowed_ips": []string{"10.99.0.2/32"},
		"keepalive":   25,
	}
	obj, err := c.AddVpnPeer(context.Background(), "vgw-1", body)
	if err != nil {
		t.Fatalf("AddVpnPeer returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/vpn-gateway/vgw-1/peer" {
		t.Errorf("path = %s; want /api/vpn-gateway/vgw-1/peer", gotPath)
	}
	if gotBody["type"] != "road_warrior" {
		t.Errorf("body type = %v; want road_warrior", gotBody["type"])
	}
	if gotBody["public_key"] != "clientpub==" {
		t.Errorf("body public_key = %v; want clientpub==", gotBody["public_key"])
	}
	if obj["id"] != "peer-1" {
		t.Errorf("obj[id] = %v; want peer-1", obj["id"])
	}
}

// TestAddVpnPeer_ValidationError verifies a 200 success:false (invalid peer
// data) surfaces as an error (C3).
func TestAddVpnPeer_ValidationError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"Requested IP is already in use"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.AddVpnPeer(context.Background(), "vgw-1", map[string]any{"type": "road_warrior"})
	if err == nil {
		t.Fatal("AddVpnPeer: expected error for 200 success:false, got nil")
	}
	if !contains(err.Error(), "already in use") {
		t.Errorf("error = %q; want it to contain %q", err.Error(), "already in use")
	}
}

// ---------------------------------------------------------------------------
// UpdateVpnPeer
// ---------------------------------------------------------------------------

// TestUpdateVpnPeer_Success verifies the PATCH path + body + that the fresh peer
// is unwrapped from "peer".
func TestUpdateVpnPeer_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Peer updated successfully","peer":{"id":"peer-1","name":"renamed","enabled":0}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.UpdateVpnPeer(context.Background(), "vgw-1", "peer-1", map[string]any{"name": "renamed", "enabled": false})
	if err != nil {
		t.Fatalf("UpdateVpnPeer returned error: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %s; want PATCH", gotMethod)
	}
	if gotPath != "/api/vpn-gateway/vgw-1/peer/peer-1" {
		t.Errorf("path = %s; want /api/vpn-gateway/vgw-1/peer/peer-1", gotPath)
	}
	if gotBody["name"] != "renamed" {
		t.Errorf("body name = %v; want renamed", gotBody["name"])
	}
	if obj["name"] != "renamed" {
		t.Errorf("obj[name] = %v; want renamed", obj["name"])
	}
}

// ---------------------------------------------------------------------------
// RemoveVpnPeer
// ---------------------------------------------------------------------------

// TestRemoveVpnPeer_Success verifies the DELETE path (peer id in the path) and
// that a {success} body is accepted.
func TestRemoveVpnPeer_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Peer removed successfully"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.RemoveVpnPeer(context.Background(), "vgw-1", "peer-1"); err != nil {
		t.Fatalf("RemoveVpnPeer returned error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s; want DELETE", gotMethod)
	}
	if gotPath != "/api/vpn-gateway/vgw-1/peer/peer-1" {
		t.Errorf("path = %s; want /api/vpn-gateway/vgw-1/peer/peer-1", gotPath)
	}
}

// ---------------------------------------------------------------------------
// GetVpnPeer (read-by-scan from the gateway SHOW)
// ---------------------------------------------------------------------------

// TestGetVpnPeer_Found verifies the peer is resolved by scanning the gateway
// SHOW's embedded peers[].
func TestGetVpnPeer_Found(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"gateway":{"id":"vgw-1","status":"active","peers":[{"id":"peer-1","name":"a"},{"id":"peer-2","name":"b"}]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.GetVpnPeer(context.Background(), "vgw-1", "peer-2")
	if err != nil {
		t.Fatalf("GetVpnPeer returned error: %v", err)
	}
	// GetVpnPeer scans the gateway SHOW, so it hits the gateway path.
	if gotPath != "/api/vpn-gateway/vgw-1" {
		t.Errorf("path = %s; want /api/vpn-gateway/vgw-1", gotPath)
	}
	if obj["name"] != "b" {
		t.Errorf("obj[name] = %v; want b", obj["name"])
	}
}

// TestGetVpnPeer_Absent verifies a peer id not present in the embedded peers[]
// yields a 404-shaped *APIError (IsNotFound).
func TestGetVpnPeer_Absent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"gateway":{"id":"vgw-1","status":"active","peers":[{"id":"peer-1"}]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.GetVpnPeer(context.Background(), "vgw-1", "missing")
	if err == nil {
		t.Fatal("GetVpnPeer: expected 404-shaped error for absent peer, got nil")
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound(err) = false; want true (err=%v)", err)
	}
}

// TestGetVpnPeer_ParentNotFound verifies a 404 on the parent gateway propagates.
func TestGetVpnPeer_ParentNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"VPN Gateway not found"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.GetVpnPeer(context.Background(), "gone", "peer-1")
	if err == nil {
		t.Fatal("GetVpnPeer: expected error for parent 404, got nil")
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound(err) = false; want true (err=%v)", err)
	}
}

// ---------------------------------------------------------------------------
// DownloadVpnPeerConfig (raw text/plain)
// ---------------------------------------------------------------------------

// TestDownloadVpnPeerConfig_Success verifies the config download path and that
// the RAW text/plain body is returned verbatim (NOT JSON-decoded).
func TestDownloadVpnPeerConfig_Success(t *testing.T) {
	var gotPath string
	const wgConf = "[Interface]\nPrivateKey = [YOUR_PRIVATE_KEY]\nAddress = 10.99.0.2/32\n\n[Peer]\nPublicKey = pub==\nAllowedIPs = 192.168.0.0/16, 10.99.0.0/24\nEndpoint = 1.2.3.4:51820\nPersistentKeepalive = 25\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(wgConf))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	cfg, err := c.DownloadVpnPeerConfig(context.Background(), "vgw-1", "peer-1")
	if err != nil {
		t.Fatalf("DownloadVpnPeerConfig returned error: %v", err)
	}
	if gotPath != "/api/vpn-gateway/vgw-1/peer/peer-1/config" {
		t.Errorf("path = %s; want /api/vpn-gateway/vgw-1/peer/peer-1/config", gotPath)
	}
	if cfg != wgConf {
		t.Errorf("config = %q; want the raw wg conf verbatim", cfg)
	}
}

// TestDownloadVpnPeerConfig_NotRoadWarrior verifies a 422 (config download is
// road-warrior-only) surfaces as an error.
func TestDownloadVpnPeerConfig_NotRoadWarrior(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"success":false,"message":"Config download is only available for road_warrior peers"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.DownloadVpnPeerConfig(context.Background(), "vgw-1", "peer-1")
	if err == nil {
		t.Fatal("DownloadVpnPeerConfig: expected error for 422, got nil")
	}
	if !contains(err.Error(), "road_warrior") {
		t.Errorf("error = %q; want it to contain %q", err.Error(), "road_warrior")
	}
}

// ---------------------------------------------------------------------------
// Empty-id guards
// ---------------------------------------------------------------------------

// TestVpnGateway_EmptyIDGuards exercises the empty-id guards on the path-id args.
func TestVpnGateway_EmptyIDGuards(t *testing.T) {
	c := New("http://example/api", "tok", 10*time.Second, false)
	ctx := context.Background()

	if _, err := c.GetVpnGateway(ctx, ""); err == nil {
		t.Error("GetVpnGateway empty id: expected error")
	}
	if err := c.DeleteVpnGateway(ctx, ""); err == nil {
		t.Error("DeleteVpnGateway empty id: expected error")
	}
	if _, err := c.AddVpnPeer(ctx, "", map[string]any{}); err == nil {
		t.Error("AddVpnPeer empty gatewayID: expected error")
	}
	if _, err := c.UpdateVpnPeer(ctx, "g", "", nil); err == nil {
		t.Error("UpdateVpnPeer empty peerID: expected error")
	}
	if err := c.RemoveVpnPeer(ctx, "", "p"); err == nil {
		t.Error("RemoveVpnPeer empty gatewayID: expected error")
	}
	if _, err := c.GetVpnPeer(ctx, "g", ""); err == nil {
		t.Error("GetVpnPeer empty peerID: expected error")
	}
	if _, err := c.DownloadVpnPeerConfig(ctx, "", "p"); err == nil {
		t.Error("DownloadVpnPeerConfig empty gatewayID: expected error")
	}
}
