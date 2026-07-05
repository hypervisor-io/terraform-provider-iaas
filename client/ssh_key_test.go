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

// NOTE: this test deliberately uses net/http/httptest directly rather than
// internal/acctest.MockServer. acctest imports internal/provider which imports
// internal/client, so importing acctest from a client test would create an
// import cycle.

// TestCreateSSHKey_Success verifies that CreateSSHKey:
//   - POSTs to /ssh-keys
//   - sends only {"name","public_key"} (NOT comments - the server derives it)
//   - returns the unwrapped ssh_key object.
func TestCreateSSHKey_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK) // create returns 200, not 201
		_, _ = w.Write([]byte(`{"success":true,"ssh_key":{"id":"k1","name":"n","public_key":"pk","fingerprint":"SHA256:x","comments":"user@host"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.CreateSSHKey(context.Background(), "n", "pk")
	if err != nil {
		t.Fatalf("CreateSSHKey returned error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/ssh-keys" {
		t.Errorf("path = %s; want /api/ssh-keys", gotPath)
	}
	if obj["id"] != "k1" {
		t.Errorf("obj[id] = %v; want k1", obj["id"])
	}
	if obj["fingerprint"] != "SHA256:x" {
		t.Errorf("obj[fingerprint] = %v; want SHA256:x", obj["fingerprint"])
	}

	// Request body must contain name + public_key and MUST NOT include comments.
	if gotBody["name"] != "n" {
		t.Errorf("body[name] = %v; want n", gotBody["name"])
	}
	if gotBody["public_key"] != "pk" {
		t.Errorf("body[public_key] = %v; want pk", gotBody["public_key"])
	}
	if _, present := gotBody["comments"]; present {
		t.Errorf("body must NOT include comments (server derives it); body = %v", gotBody)
	}
}

// TestCreateSSHKey_Failure verifies that a 200 success:false response surfaces
// the API message as an error (C3 - the create endpoint signals failure at 200).
func TestCreateSSHKey_Failure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"bad key"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.CreateSSHKey(context.Background(), "n", "not-a-key")
	if err == nil {
		t.Fatal("CreateSSHKey: expected error for success:false, got nil")
	}
	if !contains(err.Error(), "bad key") {
		t.Errorf("error = %q; want it to contain %q", err.Error(), "bad key")
	}
}

// TestGetSSHKey_Success verifies GET /ssh-key/{id} (singular) unwraps ssh_key.
func TestGetSSHKey_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ssh_key":{"id":"k1","name":"n","public_key":"pk"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.GetSSHKey(context.Background(), "k1")
	if err != nil {
		t.Fatalf("GetSSHKey returned error: %v", err)
	}
	if gotPath != "/api/ssh-key/k1" {
		t.Errorf("path = %s; want /api/ssh-key/k1 (singular)", gotPath)
	}
	if obj["id"] != "k1" {
		t.Errorf("obj[id] = %v; want k1", obj["id"])
	}
}

// TestGetSSHKey_NotFound verifies a 404 is recognised by client.IsNotFound.
func TestGetSSHKey_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not found."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.GetSSHKey(context.Background(), "missing")
	if err == nil {
		t.Fatal("GetSSHKey: expected error for 404, got nil")
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound = false; want true (err: %v)", err)
	}
}

// TestUpdateSSHKey_Success verifies PATCH /ssh-key/{id} (singular) sends the
// supplied fields and unwraps the fresh ssh_key from the response.
func TestUpdateSSHKey_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"ssh_key":{"id":"k1","name":"n2","public_key":"pk"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.UpdateSSHKey(context.Background(), "k1", map[string]any{"name": "n2"})
	if err != nil {
		t.Fatalf("UpdateSSHKey returned error: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %s; want PATCH", gotMethod)
	}
	if gotPath != "/api/ssh-key/k1" {
		t.Errorf("path = %s; want /api/ssh-key/k1 (singular)", gotPath)
	}
	if gotBody["name"] != "n2" {
		t.Errorf("body[name] = %v; want n2", gotBody["name"])
	}
	if obj["name"] != "n2" {
		t.Errorf("obj[name] = %v; want n2", obj["name"])
	}
}

// TestUpdateSSHKey_Failure verifies a 422 surfaces as an error.
func TestUpdateSSHKey_Failure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"success":false,"message":"name invalid"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.UpdateSSHKey(context.Background(), "k1", map[string]any{"name": "INVALID!"})
	if err == nil {
		t.Fatal("UpdateSSHKey: expected error for 422, got nil")
	}
}

// TestDeleteSSHKey_Success verifies DELETE /ssh-keys/{id} (plural) with success:true.
func TestDeleteSSHKey_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"deleted"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DeleteSSHKey(context.Background(), "k1"); err != nil {
		t.Fatalf("DeleteSSHKey returned error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s; want DELETE", gotMethod)
	}
	if gotPath != "/api/ssh-keys/k1" {
		t.Errorf("path = %s; want /api/ssh-keys/k1 (plural)", gotPath)
	}
}

// TestDeleteSSHKey_Failure verifies a 200 success:false delete surfaces the
// message as an error (C3).
func TestDeleteSSHKey_Failure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"nope"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	err := c.DeleteSSHKey(context.Background(), "k1")
	if err == nil {
		t.Fatal("DeleteSSHKey: expected error for success:false, got nil")
	}
	if !contains(err.Error(), "nope") {
		t.Errorf("error = %q; want it to contain %q", err.Error(), "nope")
	}
}

// contains is a small substring helper shared by the client test files.
func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}
