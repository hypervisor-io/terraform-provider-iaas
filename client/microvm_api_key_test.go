package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// NOTE: like the other client tests, this file uses net/http/httptest directly
// rather than internal/acctest.MockServer (see static_ip_test.go for why).

// ---------------------------------------------------------------------------
// ListMicrovmApiKeys
// ---------------------------------------------------------------------------

// TestListMicrovmApiKeys_Success verifies GET /microvm/api-keys unwraps the
// "keys" key (a bare collection, not a paginator) and never carries plaintext
// key material.
func TestListMicrovmApiKeys_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"keys":[{"id":"key-uuid-1","name":"CI","prefix":"vc_sb_ab12"},{"id":"key-uuid-2","name":"local","prefix":"vc_sb_xy98"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	items, err := c.ListMicrovmApiKeys(context.Background())
	if err != nil {
		t.Fatalf("ListMicrovmApiKeys returned error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %s; want GET", gotMethod)
	}
	if gotPath != "/api/microvm/api-keys" {
		t.Errorf("path = %s; want /api/microvm/api-keys", gotPath)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d; want 2", len(items))
	}
	if items[0]["id"] != "key-uuid-1" {
		t.Errorf("items[0][id] = %v; want key-uuid-1", items[0]["id"])
	}
	for i, item := range items {
		if _, present := item["plaintext"]; present {
			t.Errorf("items[%d] must NOT include %q; got %v", i, "plaintext", item)
		}
	}
}

// ---------------------------------------------------------------------------
// CreateMicrovmApiKey
// ---------------------------------------------------------------------------

// TestCreateMicrovmApiKey_ReturnsPlaintextOnce verifies POST /microvm/api-keys
// sends {name} and returns the full envelope: the created key object AND the
// plaintext secret, which this endpoint returns exactly once (never on any
// other call).
func TestCreateMicrovmApiKey_ReturnsPlaintextOnce(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"key":{"id":"k1","name":"CI","prefix":"vc_sb_ab12"},"plaintext":"vc_sb_ab12cdef"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	result, err := c.CreateMicrovmApiKey(context.Background(), "CI")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/microvm/api-keys" {
		t.Errorf("path = %s; want /api/microvm/api-keys", gotPath)
	}
	if gotBody["name"] != "CI" {
		t.Errorf("body[name] = %v; want CI", gotBody["name"])
	}
	if result["plaintext"] != "vc_sb_ab12cdef" {
		t.Fatalf("expected plaintext in response, got %v", result)
	}
	keyObj, ok := result["key"].(map[string]any)
	if !ok {
		t.Fatalf("expected a key object in response, got %v", result)
	}
	if keyObj["id"] != "k1" || keyObj["prefix"] != "vc_sb_ab12" {
		t.Errorf("key object = %v; want id k1 and prefix vc_sb_ab12", keyObj)
	}
}

// ---------------------------------------------------------------------------
// DeleteMicrovmApiKey
// ---------------------------------------------------------------------------

// TestDeleteMicrovmApiKey_Success verifies DELETE /microvm/api-key/{id} with
// success:true.
func TestDeleteMicrovmApiKey_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"API key deleted."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DeleteMicrovmApiKey(context.Background(), "key-uuid-1"); err != nil {
		t.Fatalf("DeleteMicrovmApiKey returned error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s; want DELETE", gotMethod)
	}
	if gotPath != "/api/microvm/api-key/key-uuid-1" {
		t.Errorf("path = %s; want /api/microvm/api-key/key-uuid-1", gotPath)
	}
}

// TestDeleteMicrovmApiKey_PathEscapes verifies the id is url.PathEscape'd into
// the request path: an id containing a slash must arrive escaped (%2F) so it
// cannot break out of the /microvm/api-key/ path segment. RequestURI is the
// raw, undecoded request-target, so the escaping is observable there.
func TestDeleteMicrovmApiKey_PathEscapes(t *testing.T) {
	var gotURI string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURI = r.RequestURI
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"API key deleted."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DeleteMicrovmApiKey(context.Background(), "a/b"); err != nil {
		t.Fatalf("DeleteMicrovmApiKey returned error: %v", err)
	}
	if gotURI != "/api/microvm/api-key/a%2Fb" {
		t.Errorf("RequestURI = %s; want /api/microvm/api-key/a%%2Fb", gotURI)
	}
}

// TestDeleteMicrovmApiKey_EmptyID verifies the empty-id guard fires BEFORE any
// HTTP request is attempted: the error must be the guard's, not a transport
// error from dialing the unreachable host.
func TestDeleteMicrovmApiKey_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	err := c.DeleteMicrovmApiKey(context.Background(), "")
	if err == nil {
		t.Fatal("DeleteMicrovmApiKey: expected error for empty id, got nil")
	}
	if !strings.Contains(err.Error(), "empty id") {
		t.Errorf("error = %q; want the empty-id guard error (no request should be attempted)", err.Error())
	}
}
