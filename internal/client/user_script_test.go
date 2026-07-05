package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// NOTE: this test deliberately uses net/http/httptest directly rather than
// internal/acctest.MockServer. acctest imports internal/provider which imports
// internal/client, so importing acctest from a client test would create an
// import cycle.

// TestGetUserScript_NotFound verifies that GetUserScript synthesises a 404
// *APIError recognised by IsNotFound when the /user-scripts list (there is no
// SHOW route) does not contain the requested id — the drift-detection path
// that lets Read() call resp.State.RemoveResource.
func TestGetUserScript_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/user-scripts" {
			t.Errorf("path = %s; want /api/user-scripts", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"id":"s1","name":"n1","type":"bash"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.GetUserScript(context.Background(), "missing")
	if err == nil {
		t.Fatal("GetUserScript: expected error when id absent from list, got nil")
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound = false; want true (err: %v)", err)
	}
}

// TestGetUserScript_Found verifies that GetUserScript returns the matching
// object when the /user-scripts list contains the requested id.
func TestGetUserScript_Found(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"id":"s1","name":"n1","type":"bash"},{"id":"s2","name":"n2","type":"cloud-init"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.GetUserScript(context.Background(), "s2")
	if err != nil {
		t.Fatalf("GetUserScript returned error: %v", err)
	}
	if obj["id"] != "s2" {
		t.Errorf("obj[id] = %v; want s2", obj["id"])
	}
	if obj["name"] != "n2" {
		t.Errorf("obj[name] = %v; want n2", obj["name"])
	}
	if obj["type"] != "cloud-init" {
		t.Errorf("obj[type] = %v; want cloud-init", obj["type"])
	}
}
