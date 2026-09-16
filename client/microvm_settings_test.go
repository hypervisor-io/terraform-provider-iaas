package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestGetMicrovmSettings_Success verifies GET /microvm/settings unwraps the
// bare {"default_network": {...}} object (no "success"/wrapper key on this
// endpoint, unlike most microvm endpoints).
func TestGetMicrovmSettings_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/microvm/settings" {
			t.Errorf("method/path = %s %s; want GET /api/microvm/settings", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"default_network":{"kind":"vpc","vpc_subnet_id":"vpc-subnet-1"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	envelope, err := c.GetMicrovmSettings(context.Background())
	if err != nil {
		t.Fatalf("GetMicrovmSettings returned error: %v", err)
	}
	defaultNetwork, ok := envelope["default_network"].(map[string]any)
	if !ok || defaultNetwork["kind"] != "vpc" || defaultNetwork["vpc_subnet_id"] != "vpc-subnet-1" {
		t.Fatalf("default_network = %#v", envelope["default_network"])
	}
}

// TestGetMicrovmSettings_NullDefault verifies "no default set" ({"default_network":null})
// decodes to a nil map value rather than an error.
func TestGetMicrovmSettings_NullDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"default_network":null}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	envelope, err := c.GetMicrovmSettings(context.Background())
	if err != nil {
		t.Fatalf("GetMicrovmSettings returned error: %v", err)
	}
	if envelope["default_network"] != nil {
		t.Fatalf("default_network = %#v; want nil", envelope["default_network"])
	}
}

// TestUpdateMicrovmSettings_SendsBodyAndReturnsEcho verifies PUT
// /microvm/settings sends the {"default_network": {...}} body verbatim and
// returns the echoed value.
func TestUpdateMicrovmSettings_SendsBodyAndReturnsEcho(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/microvm/settings" {
			t.Errorf("method/path = %s %s; want PUT /api/microvm/settings", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"default_network":{"kind":"public"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	envelope, err := c.UpdateMicrovmSettings(context.Background(), map[string]any{"kind": "public"})
	if err != nil {
		t.Fatalf("UpdateMicrovmSettings returned error: %v", err)
	}
	sent, ok := gotBody["default_network"].(map[string]any)
	if !ok || sent["kind"] != "public" {
		t.Fatalf("sent body default_network = %#v", gotBody["default_network"])
	}
	if _, exists := sent["subnet_id"]; exists {
		t.Error("sent body must not invent a subnet_id key (C8.1 auto-assign)")
	}
	got, ok := envelope["default_network"].(map[string]any)
	if !ok || got["kind"] != "public" {
		t.Fatalf("returned default_network = %#v", envelope["default_network"])
	}
}

// TestUpdateMicrovmSettings_NilClears verifies passing a nil defaultNetwork
// sends an explicit {"default_network":null} body (clearing the setting,
// C4/C8.6) rather than omitting the key entirely.
func TestUpdateMicrovmSettings_NilClears(t *testing.T) {
	var gotBody map[string]any
	var hadKey bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]any
		_ = json.NewDecoder(r.Body).Decode(&raw)
		_, hadKey = raw["default_network"]
		gotBody = raw
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"default_network":null}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if _, err := c.UpdateMicrovmSettings(context.Background(), nil); err != nil {
		t.Fatalf("UpdateMicrovmSettings returned error: %v", err)
	}
	if !hadKey {
		t.Fatalf("body must send an explicit default_network:null key; got %#v", gotBody)
	}
	if gotBody["default_network"] != nil {
		t.Fatalf("default_network = %#v; want null", gotBody["default_network"])
	}
}
