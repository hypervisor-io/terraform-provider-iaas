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
// CreateVolume
// ---------------------------------------------------------------------------

// TestCreateVolume_Success verifies CreateVolume:
//   - POSTs to /storage/volumes (PLURAL)
//   - sends the prebuilt body verbatim
//   - returns the unwrapped "volume" object WITH its id and pending status.
//
// Create is async: the response carries status="pending"; the resource waits on
// the SHOW endpoint until status="available".
func TestCreateVolume_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Volume creation initiated.","volume":{"id":"vol-uuid-1","name":"data","status":"pending","deployed":0,"size":50,"volume_plan_id":"plan-1","hypervisor_group_id":"grp-1"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	body := map[string]any{
		"name":                "data",
		"volume_plan_id":      "plan-1",
		"hypervisor_group_id": "grp-1",
	}
	obj, err := c.CreateVolume(context.Background(), body)
	if err != nil {
		t.Fatalf("CreateVolume returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/storage/volumes" {
		t.Errorf("path = %s; want /api/storage/volumes (plural)", gotPath)
	}
	if obj["id"] != "vol-uuid-1" {
		t.Errorf("obj[id] = %v; want vol-uuid-1", obj["id"])
	}
	if obj["status"] != "pending" {
		t.Errorf("obj[status] = %v; want pending", obj["status"])
	}
	if gotBody["name"] != "data" || gotBody["volume_plan_id"] != "plan-1" || gotBody["hypervisor_group_id"] != "grp-1" {
		t.Errorf("create body = %v; missing required keys", gotBody)
	}
}

// TestCreateVolume_QuotaExceeded verifies a 422 success:false response (e.g. the
// per-account max_volumes quota was hit) surfaces the message as an error (C3).
func TestCreateVolume_QuotaExceeded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"success":false,"message":"You have reached your volume limit."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.CreateVolume(context.Background(), map[string]any{"name": "x", "volume_plan_id": "p", "hypervisor_group_id": "g"})
	if err == nil {
		t.Fatal("CreateVolume: expected error for 422 success:false, got nil")
	}
	if !contains(err.Error(), "volume limit") {
		t.Errorf("error = %q; want it to contain %q", err.Error(), "volume limit")
	}
}

// TestCreateVolume_BillingDisabled verifies a 403 from the billing.enabled
// middleware surfaces as an *APIError.
func TestCreateVolume_BillingDisabled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"success":false,"message":"This feature is unavailable because billing is disabled."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.CreateVolume(context.Background(), map[string]any{"name": "x", "volume_plan_id": "p", "hypervisor_group_id": "g"})
	if err == nil {
		t.Fatal("CreateVolume: expected error for 403, got nil")
	}
	if !contains(err.Error(), "billing is disabled") {
		t.Errorf("error = %q; want it to contain %q", err.Error(), "billing is disabled")
	}
}

// ---------------------------------------------------------------------------
// GetVolume
// ---------------------------------------------------------------------------

// TestGetVolume_Success verifies the SHOW route is SINGULAR and the "volume"
// envelope (including embedded snapshots[]) is unwrapped.
func TestGetVolume_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"volume":{"id":"vol-uuid-1","name":"data","status":"available","deployed":1,"size":50,"snapshots":[{"id":"snap-1","name":"s1","status":"available"}]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.GetVolume(context.Background(), "vol-uuid-1")
	if err != nil {
		t.Fatalf("GetVolume returned error: %v", err)
	}
	if gotPath != "/api/storage/volume/vol-uuid-1" {
		t.Errorf("path = %s; want /api/storage/volume/vol-uuid-1 (singular)", gotPath)
	}
	if obj["id"] != "vol-uuid-1" {
		t.Errorf("obj[id] = %v; want vol-uuid-1", obj["id"])
	}
	if obj["status"] != "available" {
		t.Errorf("obj[status] = %v; want available", obj["status"])
	}
	if _, ok := obj["snapshots"].([]any); !ok {
		t.Errorf("expected embedded snapshots[] in SHOW payload, got %T", obj["snapshots"])
	}
}

// TestGetVolume_NotFound verifies a 404 is an *APIError that IsNotFound matches.
func TestGetVolume_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"Volume not found"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.GetVolume(context.Background(), "missing")
	if err == nil {
		t.Fatal("GetVolume: expected error for 404, got nil")
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound = false; want true (err: %v)", err)
	}
}

// TestGetVolume_EmptyID verifies the empty-id guard.
func TestGetVolume_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if _, err := c.GetVolume(context.Background(), ""); err == nil {
		t.Fatal("GetVolume: expected error for empty id, got nil")
	}
}

// ---------------------------------------------------------------------------
// ListVolumes
// ---------------------------------------------------------------------------

func TestListVolumes_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"current_page":1,"data":[{"id":"vol-1","status":"available"},{"id":"vol-2","status":"attached"}],"total":2}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	items, err := c.ListVolumes(context.Background())
	if err != nil {
		t.Fatalf("ListVolumes returned error: %v", err)
	}
	if gotPath != "/api/storage/volumes" {
		t.Errorf("path = %s; want /api/storage/volumes", gotPath)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d; want 2", len(items))
	}
}

// ---------------------------------------------------------------------------
// AttachVolume / DetachVolume
// ---------------------------------------------------------------------------

func TestAttachVolume_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"volume":{"id":"vol-1","status":"attached","instance_id":"inst-1","dev":"xvda"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.AttachVolume(context.Background(), "vol-1", map[string]any{"instance_id": "inst-1"})
	if err != nil {
		t.Fatalf("AttachVolume returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/storage/volume/vol-1/attach" {
		t.Errorf("path = %s; want /api/storage/volume/vol-1/attach", gotPath)
	}
	if gotBody["instance_id"] != "inst-1" {
		t.Errorf("body[instance_id] = %v; want inst-1", gotBody["instance_id"])
	}
	if obj["status"] != "attached" {
		t.Errorf("obj[status] = %v; want attached", obj["status"])
	}
}

// TestAttachVolume_Precondition verifies a 422 success:false (e.g. volume not
// available, wrong hypervisor group) is surfaced as an error.
func TestAttachVolume_Precondition(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"success":false,"message":"Volume must be in available status to attach."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.AttachVolume(context.Background(), "vol-1", map[string]any{"instance_id": "inst-1"})
	if err == nil {
		t.Fatal("AttachVolume: expected error for 422 success:false, got nil")
	}
	if !contains(err.Error(), "available status to attach") {
		t.Errorf("error = %q; want precondition message", err.Error())
	}
}

func TestDetachVolume_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"volume":{"id":"vol-1","status":"available","instance_id":null,"dev":null}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.DetachVolume(context.Background(), "vol-1")
	if err != nil {
		t.Fatalf("DetachVolume returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/storage/volume/vol-1/detach" {
		t.Errorf("path = %s; want /api/storage/volume/vol-1/detach", gotPath)
	}
	if obj["status"] != "available" {
		t.Errorf("obj[status] = %v; want available", obj["status"])
	}
}

func TestAttachVolume_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if _, err := c.AttachVolume(context.Background(), "", map[string]any{"instance_id": "i"}); err == nil {
		t.Fatal("AttachVolume: expected error for empty id, got nil")
	}
}

// ---------------------------------------------------------------------------
// ResizeVolume
// ---------------------------------------------------------------------------

func TestResizeVolume_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"is_downgrade":false,"volume":{"id":"vol-1","status":"available","size":100,"volume_plan_id":"plan-2"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.ResizeVolume(context.Background(), "vol-1", map[string]any{"volume_plan_id": "plan-2"})
	if err != nil {
		t.Fatalf("ResizeVolume returned error: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %s; want PATCH", gotMethod)
	}
	if gotPath != "/api/storage/volume/vol-1/resize" {
		t.Errorf("path = %s; want /api/storage/volume/vol-1/resize", gotPath)
	}
	if gotBody["volume_plan_id"] != "plan-2" {
		t.Errorf("body[volume_plan_id] = %v; want plan-2", gotBody["volume_plan_id"])
	}
	if obj["volume_plan_id"] != "plan-2" {
		t.Errorf("obj[volume_plan_id] = %v; want plan-2", obj["volume_plan_id"])
	}
}

// ---------------------------------------------------------------------------
// DeleteVolume
// ---------------------------------------------------------------------------

func TestDeleteVolume_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Volume deletion initiated."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DeleteVolume(context.Background(), "vol-1"); err != nil {
		t.Fatalf("DeleteVolume returned error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s; want DELETE", gotMethod)
	}
	if gotPath != "/api/storage/volume/vol-1" {
		t.Errorf("path = %s; want /api/storage/volume/vol-1 (singular)", gotPath)
	}
}

func TestDeleteVolume_Failure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"success":false,"message":"Failed to detach volume before deletion."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	err := c.DeleteVolume(context.Background(), "vol-1")
	if err == nil {
		t.Fatal("DeleteVolume: expected error for success:false, got nil")
	}
	if !contains(err.Error(), "detach volume before deletion") {
		t.Errorf("error = %q; want detach-before-delete message", err.Error())
	}
}

// ---------------------------------------------------------------------------
// CreateVolumeSnapshot / DeleteVolumeSnapshot
// ---------------------------------------------------------------------------

// TestCreateVolumeSnapshot_Success verifies the snapshot CREATE posts to the
// nested /snapshot path, sends the optional name, and unwraps the "queue"
// envelope (the controller returns the queue, NOT the snapshot).
func TestCreateVolumeSnapshot_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Snapshot queued.","queue":{"id":"queue-1","operation":"snapshot","source_id":"snap-1","status":"pending"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.CreateVolumeSnapshot(context.Background(), "vol-1", map[string]any{"name": "nightly"})
	if err != nil {
		t.Fatalf("CreateVolumeSnapshot returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/storage/volume/vol-1/snapshot" {
		t.Errorf("path = %s; want /api/storage/volume/vol-1/snapshot", gotPath)
	}
	if gotBody["name"] != "nightly" {
		t.Errorf("body[name] = %v; want nightly", gotBody["name"])
	}
	// The queue carries source_id = the snapshot id.
	if obj["source_id"] != "snap-1" {
		t.Errorf("queue source_id = %v; want snap-1", obj["source_id"])
	}
}

func TestDeleteVolumeSnapshot_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Delete queued.","queue":{"id":"queue-2","status":"pending"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DeleteVolumeSnapshot(context.Background(), "vol-1", "snap-1"); err != nil {
		t.Fatalf("DeleteVolumeSnapshot returned error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s; want DELETE", gotMethod)
	}
	if gotPath != "/api/storage/volume/vol-1/snapshot/snap-1" {
		t.Errorf("path = %s; want /api/storage/volume/vol-1/snapshot/snap-1", gotPath)
	}
}

func TestDeleteVolumeSnapshot_EmptyIDs(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if err := c.DeleteVolumeSnapshot(context.Background(), "", "snap-1"); err == nil {
		t.Fatal("DeleteVolumeSnapshot: expected error for empty volumeID")
	}
	if err := c.DeleteVolumeSnapshot(context.Background(), "vol-1", ""); err == nil {
		t.Fatal("DeleteVolumeSnapshot: expected error for empty snapshotID")
	}
}
