package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestCreateLBTarget_Success verifies CreateLBTarget POSTs to the nested targets
// path, sends target_ip/target_port (NOT ip/port), and unwraps "target".
func TestCreateLBTarget_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Target added.","target":{"id":"tg-1","target_ip":"10.0.0.5","target_port":8080,"weight":100},"sync":{}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.CreateLBTarget(context.Background(), "lb-1", "be-1", map[string]any{
		"target_ip":   "10.0.0.5",
		"target_port": 8080,
	})
	if err != nil {
		t.Fatalf("CreateLBTarget returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/load-balancer/lb-1/backend/be-1/targets" {
		t.Errorf("path = %s; want /api/load-balancer/lb-1/backend/be-1/targets", gotPath)
	}
	if obj["id"] != "tg-1" {
		t.Errorf("obj[id] = %v; want tg-1", obj["id"])
	}
	if gotBody["target_ip"] != "10.0.0.5" {
		t.Errorf("create body = %v; want target_ip=10.0.0.5", gotBody)
	}
	if _, present := gotBody["ip"]; present {
		t.Errorf("create body must use 'target_ip', not 'ip': %v", gotBody)
	}
}

// TestCreateLBTarget_DuplicateRejected verifies a 422 success:false (dup ip+port) errors.
func TestCreateLBTarget_DuplicateRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"success":false,"message":"A target with this IP and port already exists in this backend."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if _, err := c.CreateLBTarget(context.Background(), "lb-1", "be-1", map[string]any{"target_ip": "x", "target_port": 1}); err == nil {
		t.Fatal("expected error on duplicate target, got nil")
	}
}

// TestUpdateLBTarget_Success verifies the PATCH path + envelope.
func TestUpdateLBTarget_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"target":{"id":"tg-1","weight":200}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.UpdateLBTarget(context.Background(), "lb-1", "be-1", "tg-1", map[string]any{"weight": 200})
	if err != nil {
		t.Fatalf("UpdateLBTarget returned error: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %s; want PATCH", gotMethod)
	}
	if gotPath != "/api/load-balancer/lb-1/backend/be-1/target/tg-1" {
		t.Errorf("path = %s; want .../backend/be-1/target/tg-1", gotPath)
	}
	if obj["weight"] != float64(200) {
		t.Errorf("obj[weight] = %v; want 200", obj["weight"])
	}
}

// TestDeleteLBTarget_Success verifies the DELETE path.
func TestDeleteLBTarget_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Target removed."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DeleteLBTarget(context.Background(), "lb-1", "be-1", "tg-1"); err != nil {
		t.Fatalf("DeleteLBTarget returned error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s; want DELETE", gotMethod)
	}
	if gotPath != "/api/load-balancer/lb-1/backend/be-1/target/tg-1" {
		t.Errorf("path = %s; want .../target/tg-1", gotPath)
	}
}

// TestGetLBTarget_ScanFound verifies the 3-level read-by-scan
// (LB → backends[bid] → targets[tid]).
func TestGetLBTarget_ScanFound(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"load_balancer":{"id":"lb-1","backends":[{"id":"be-1","targets":[{"id":"tg-1","target_ip":"10.0.0.5","target_port":8080}]},{"id":"be-2","targets":[]}]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.GetLBTarget(context.Background(), "lb-1", "be-1", "tg-1")
	if err != nil {
		t.Fatalf("GetLBTarget returned error: %v", err)
	}
	if gotPath != "/api/load-balancer/lb-1" {
		t.Errorf("scan must call LB SHOW; path = %s", gotPath)
	}
	if obj["target_ip"] != "10.0.0.5" {
		t.Errorf("obj[target_ip] = %v; want 10.0.0.5", obj["target_ip"])
	}
}

// TestGetLBTarget_ScanAbsent verifies absent target (or wrong backend) → IsNotFound.
func TestGetLBTarget_ScanAbsent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"load_balancer":{"id":"lb-1","backends":[{"id":"be-1","targets":[]}]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if _, err := c.GetLBTarget(context.Background(), "lb-1", "be-1", "missing"); err == nil || !IsNotFound(err) {
		t.Errorf("expected IsNotFound for absent target, got %v", err)
	}
	// Wrong backend id → not found.
	if _, err := c.GetLBTarget(context.Background(), "lb-1", "be-missing", "tg-1"); err == nil || !IsNotFound(err) {
		t.Errorf("expected IsNotFound for wrong backend, got %v", err)
	}
}

// TestLBTarget_EmptyIDGuards verifies the path-id guards.
func TestLBTarget_EmptyIDGuards(t *testing.T) {
	c := New("http://unused/api", "tok", time.Second, false)
	ctx := context.Background()
	if _, err := c.CreateLBTarget(ctx, "", "be", map[string]any{}); err == nil {
		t.Error("CreateLBTarget: expected empty-lbID error")
	}
	if _, err := c.CreateLBTarget(ctx, "lb", "", map[string]any{}); err == nil {
		t.Error("CreateLBTarget: expected empty-backendID error")
	}
	if _, err := c.UpdateLBTarget(ctx, "lb", "be", "", map[string]any{}); err == nil {
		t.Error("UpdateLBTarget: expected empty-targetID error")
	}
	if err := c.DeleteLBTarget(ctx, "lb", "be", ""); err == nil {
		t.Error("DeleteLBTarget: expected empty-targetID error")
	}
	if _, err := c.GetLBTarget(ctx, "lb", "be", ""); err == nil {
		t.Error("GetLBTarget: expected empty-targetID error")
	}
}
