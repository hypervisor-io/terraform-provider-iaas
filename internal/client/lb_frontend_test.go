package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestCreateLBFrontend_Success verifies the POST path + envelope + body (port/protocol, not bind_port).
func TestCreateLBFrontend_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Frontend created.","frontend":{"id":"fe-1","name":"http","port":80,"protocol":"http","mode":"http"},"sync":{}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.CreateLBFrontend(context.Background(), "lb-1", map[string]any{
		"name":     "http",
		"port":     80,
		"protocol": "http",
	})
	if err != nil {
		t.Fatalf("CreateLBFrontend returned error: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/load-balancer/lb-1/frontends" {
		t.Errorf("method/path = %s %s; want POST /api/load-balancer/lb-1/frontends", gotMethod, gotPath)
	}
	if obj["id"] != "fe-1" {
		t.Errorf("obj[id] = %v; want fe-1", obj["id"])
	}
	if gotBody["port"] != float64(80) || gotBody["protocol"] != "http" {
		t.Errorf("create body = %v; want port=80 protocol=http", gotBody)
	}
	if _, present := gotBody["bind_port"]; present {
		t.Errorf("create body must use 'port', not 'bind_port': %v", gotBody)
	}
}

// TestCreateLBFrontend_Conflict verifies a 200 success:false (port conflict) → error.
func TestCreateLBFrontend_Conflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"Port 80/http is already in use by an existing frontend \"http\"."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if _, err := c.CreateLBFrontend(context.Background(), "lb-1", map[string]any{"name": "x", "port": 80}); err == nil {
		t.Fatal("expected error on port conflict, got nil")
	}
}

func TestUpdateLBFrontend_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"frontend":{"id":"fe-1","name":"https","port":443,"protocol":"https"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.UpdateLBFrontend(context.Background(), "lb-1", "fe-1", map[string]any{"name": "https"})
	if err != nil {
		t.Fatalf("UpdateLBFrontend returned error: %v", err)
	}
	if gotMethod != http.MethodPatch || gotPath != "/api/load-balancer/lb-1/frontend/fe-1" {
		t.Errorf("method/path = %s %s; want PATCH .../frontend/fe-1", gotMethod, gotPath)
	}
	if obj["name"] != "https" {
		t.Errorf("obj[name] = %v; want https", obj["name"])
	}
}

func TestDeleteLBFrontend_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Frontend deleted."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DeleteLBFrontend(context.Background(), "lb-1", "fe-1"); err != nil {
		t.Fatalf("DeleteLBFrontend returned error: %v", err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/api/load-balancer/lb-1/frontend/fe-1" {
		t.Errorf("method/path = %s %s; want DELETE .../frontend/fe-1", gotMethod, gotPath)
	}
}

func TestGetLBFrontend_ScanFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"load_balancer":{"id":"lb-1","frontends":[{"id":"fe-1","name":"http","port":80},{"id":"fe-2","name":"https","port":443}]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.GetLBFrontend(context.Background(), "lb-1", "fe-2")
	if err != nil {
		t.Fatalf("GetLBFrontend returned error: %v", err)
	}
	if obj["name"] != "https" {
		t.Errorf("obj[name] = %v; want https", obj["name"])
	}
}

func TestGetLBFrontend_ScanAbsent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"load_balancer":{"id":"lb-1","frontends":[]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if _, err := c.GetLBFrontend(context.Background(), "lb-1", "missing"); err == nil || !IsNotFound(err) {
		t.Errorf("expected IsNotFound for absent frontend, got %v", err)
	}
}

func TestLBFrontend_EmptyIDGuards(t *testing.T) {
	c := New("http://unused/api", "tok", time.Second, false)
	ctx := context.Background()
	if _, err := c.CreateLBFrontend(ctx, "", map[string]any{}); err == nil {
		t.Error("CreateLBFrontend: expected empty-lbID error")
	}
	if _, err := c.UpdateLBFrontend(ctx, "lb", "", map[string]any{}); err == nil {
		t.Error("UpdateLBFrontend: expected empty-frontendID error")
	}
	if err := c.DeleteLBFrontend(ctx, "lb", ""); err == nil {
		t.Error("DeleteLBFrontend: expected empty-frontendID error")
	}
	if _, err := c.GetLBFrontend(ctx, "lb", ""); err == nil {
		t.Error("GetLBFrontend: expected empty-frontendID error")
	}
}
