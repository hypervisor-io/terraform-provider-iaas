package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Like the other client tests, this file uses net/http/httptest directly
// (importing internal/acctest here would create an import cycle).

// TestCreateLBBackend_Success verifies CreateLBBackend POSTs to
// /load-balancer/{lbId}/backends, sends the prebuilt body verbatim (algorithm,
// not "balance"), and unwraps the "backend" envelope returning the id.
func TestCreateLBBackend_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Backend created.","backend":{"id":"be-1","name":"web","algorithm":"roundrobin","mode":"http"},"sync":{"status":"ok"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.CreateLBBackend(context.Background(), "lb-1", map[string]any{
		"name":      "web",
		"algorithm": "roundrobin",
		"mode":      "http",
	})
	if err != nil {
		t.Fatalf("CreateLBBackend returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/load-balancer/lb-1/backends" {
		t.Errorf("path = %s; want /api/load-balancer/lb-1/backends", gotPath)
	}
	if obj["id"] != "be-1" {
		t.Errorf("obj[id] = %v; want be-1", obj["id"])
	}
	if gotBody["algorithm"] != "roundrobin" || gotBody["name"] != "web" {
		t.Errorf("create body = %v; missing algorithm/name", gotBody)
	}
	if _, present := gotBody["balance"]; present {
		t.Errorf("create body must use 'algorithm', not 'balance': %v", gotBody)
	}
}

// TestCreateLBBackend_SuccessFalse verifies a 200 success:false maps to an error (C3).
func TestCreateLBBackend_SuccessFalse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"Invalid algorithm."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if _, err := c.CreateLBBackend(context.Background(), "lb-1", map[string]any{"name": "x"}); err == nil {
		t.Fatal("expected error on 200 success:false, got nil")
	}
}

// TestUpdateLBBackend_Success verifies UpdateLBBackend PATCHes the singular
// backend path and unwraps the fresh backend.
func TestUpdateLBBackend_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Backend updated.","backend":{"id":"be-1","name":"web2","algorithm":"leastconn"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.UpdateLBBackend(context.Background(), "lb-1", "be-1", map[string]any{"name": "web2"})
	if err != nil {
		t.Fatalf("UpdateLBBackend returned error: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %s; want PATCH", gotMethod)
	}
	if gotPath != "/api/load-balancer/lb-1/backend/be-1" {
		t.Errorf("path = %s; want /api/load-balancer/lb-1/backend/be-1", gotPath)
	}
	if obj["name"] != "web2" {
		t.Errorf("obj[name] = %v; want web2", obj["name"])
	}
}

// TestDeleteLBBackend_Success verifies DeleteLBBackend DELETEs the singular path.
func TestDeleteLBBackend_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Backend deleted."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DeleteLBBackend(context.Background(), "lb-1", "be-1"); err != nil {
		t.Fatalf("DeleteLBBackend returned error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s; want DELETE", gotMethod)
	}
	if gotPath != "/api/load-balancer/lb-1/backend/be-1" {
		t.Errorf("path = %s; want /api/load-balancer/lb-1/backend/be-1", gotPath)
	}
}

// TestGetLBBackend_ScanFound verifies the read-by-scan resolves a backend from
// the parent LB SHOW embedded backends[] array.
func TestGetLBBackend_ScanFound(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"load_balancer":{"id":"lb-1","backends":[{"id":"be-1","name":"web","algorithm":"roundrobin"},{"id":"be-2","name":"api"}]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.GetLBBackend(context.Background(), "lb-1", "be-2")
	if err != nil {
		t.Fatalf("GetLBBackend returned error: %v", err)
	}
	if gotPath != "/api/load-balancer/lb-1" {
		t.Errorf("scan must call LB SHOW; path = %s; want /api/load-balancer/lb-1", gotPath)
	}
	if obj["name"] != "api" {
		t.Errorf("obj[name] = %v; want api", obj["name"])
	}
}

// TestGetLBBackend_ScanAbsent verifies an absent backend id yields IsNotFound.
func TestGetLBBackend_ScanAbsent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"load_balancer":{"id":"lb-1","backends":[]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.GetLBBackend(context.Background(), "lb-1", "missing")
	if err == nil {
		t.Fatal("expected IsNotFound error for absent backend, got nil")
	}
	if !IsNotFound(err) {
		t.Errorf("expected IsNotFound, got %v", err)
	}
}

// TestGetLBBackend_ParentNotFound verifies a 404 on the parent LB propagates.
func TestGetLBBackend_ParentNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"success":false,"message":"Not found."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.GetLBBackend(context.Background(), "lb-x", "be-1")
	if err == nil || !IsNotFound(err) {
		t.Errorf("expected IsNotFound from parent 404, got %v", err)
	}
}

// TestLBBackend_EmptyIDGuards verifies the path-id guards.
func TestLBBackend_EmptyIDGuards(t *testing.T) {
	c := New("http://unused/api", "tok", time.Second, false)
	ctx := context.Background()
	if _, err := c.CreateLBBackend(ctx, "", map[string]any{}); err == nil {
		t.Error("CreateLBBackend: expected empty-lbID error")
	}
	if _, err := c.UpdateLBBackend(ctx, "", "be", map[string]any{}); err == nil {
		t.Error("UpdateLBBackend: expected empty-lbID error")
	}
	if _, err := c.UpdateLBBackend(ctx, "lb", "", map[string]any{}); err == nil {
		t.Error("UpdateLBBackend: expected empty-backendID error")
	}
	if err := c.DeleteLBBackend(ctx, "", "be"); err == nil {
		t.Error("DeleteLBBackend: expected empty-lbID error")
	}
	if err := c.DeleteLBBackend(ctx, "lb", ""); err == nil {
		t.Error("DeleteLBBackend: expected empty-backendID error")
	}
	if _, err := c.GetLBBackend(ctx, "", "be"); err == nil {
		t.Error("GetLBBackend: expected empty-lbID error")
	}
	if _, err := c.GetLBBackend(ctx, "lb", ""); err == nil {
		t.Error("GetLBBackend: expected empty-backendID error")
	}
}
