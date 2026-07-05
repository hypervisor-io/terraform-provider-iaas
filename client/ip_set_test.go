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
// rather than internal/acctest.MockServer. acctest imports internal/provider
// which imports internal/client, so importing acctest here would create an
// import cycle.

// ---------------------------------------------------------------------------
// CreateIPSet
// ---------------------------------------------------------------------------

// TestCreateIPSet_Success verifies CreateIPSet:
//   - POSTs to /ip-sets (PLURAL)
//   - sends the prebuilt body
//   - unwraps the "ip_set" envelope, returning the object WITH its id
func TestCreateIPSet_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"IP set created successfully","ip_set":{"id":"set-uuid-1","name":"blocklist","description":"bad ips","ip_version":"ipv4"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	body := map[string]any{
		"name":        "blocklist",
		"description": "bad ips",
		"ip_version":  "ipv4",
	}
	obj, err := c.CreateIPSet(context.Background(), body)
	if err != nil {
		t.Fatalf("CreateIPSet returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/ip-sets" {
		t.Errorf("path = %s; want /api/ip-sets (plural)", gotPath)
	}
	if obj["id"] != "set-uuid-1" {
		t.Errorf("obj[id] = %v; want set-uuid-1", obj["id"])
	}
	if obj["ip_version"] != "ipv4" {
		t.Errorf("obj[ip_version] = %v; want ipv4", obj["ip_version"])
	}
	if gotBody["name"] != "blocklist" {
		t.Errorf("body[name] = %v; want blocklist", gotBody["name"])
	}
	if gotBody["ip_version"] != "ipv4" {
		t.Errorf("body[ip_version] = %v; want ipv4", gotBody["ip_version"])
	}
}

// TestCreateIPSet_Failure verifies a 200 success:false response surfaces the
// API message as an error (C3).
func TestCreateIPSet_Failure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"The name field is required."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.CreateIPSet(context.Background(), map[string]any{"ip_version": "ipv4"})
	if err == nil {
		t.Fatal("CreateIPSet: expected error for success:false, got nil")
	}
	if !contains(err.Error(), "name field is required") {
		t.Errorf("error = %q; want it to contain %q", err.Error(), "name field is required")
	}
}

// ---------------------------------------------------------------------------
// GetIPSet
// ---------------------------------------------------------------------------

// TestGetIPSet_Success verifies GetIPSet:
//   - GETs /ip-set/{id} (SINGULAR)
//   - unwraps the "ip_set" envelope
//   - exposes the EMBEDDED entries array (so Read can hydrate them)
func TestGetIPSet_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"ip_set":{"id":"set-uuid-1","name":"blocklist","description":"bad ips","ip_version":"ipv4","entries":[{"id":"e1","cidr":"10.0.0.0/8","description":"office"},{"id":"e2","cidr":"192.168.1.0/24","description":null}],"entries_count":2,"rules_count":0}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.GetIPSet(context.Background(), "set-uuid-1")
	if err != nil {
		t.Fatalf("GetIPSet returned error: %v", err)
	}
	if gotPath != "/api/ip-set/set-uuid-1" {
		t.Errorf("path = %s; want /api/ip-set/set-uuid-1 (singular)", gotPath)
	}
	if obj["name"] != "blocklist" {
		t.Errorf("obj[name] = %v; want blocklist", obj["name"])
	}
	entries, ok := obj["entries"].([]any)
	if !ok {
		t.Fatalf("obj[entries] is not an array; got %T", obj["entries"])
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d; want 2", len(entries))
	}
	first, _ := entries[0].(map[string]any)
	if first["id"] != "e1" || first["cidr"] != "10.0.0.0/8" {
		t.Errorf("entries[0] = %v; want id=e1 cidr=10.0.0.0/8", first)
	}
}

// TestGetIPSet_NotFound verifies a 404 maps to an *APIError (IsNotFound=true).
func TestGetIPSet_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"IP Set not found"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.GetIPSet(context.Background(), "missing")
	if err == nil {
		t.Fatal("GetIPSet: expected error for 404, got nil")
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound = false; want true (err: %v)", err)
	}
}

// TestGetIPSet_EmptyID verifies the empty-id guard.
func TestGetIPSet_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if _, err := c.GetIPSet(context.Background(), ""); err == nil {
		t.Fatal("GetIPSet: expected error for empty id, got nil")
	}
}

// ---------------------------------------------------------------------------
// UpdateIPSet
// ---------------------------------------------------------------------------

// TestUpdateIPSet_Success verifies UpdateIPSet PATCHes /ip-set/{id} (SINGULAR)
// and tolerates the body-less {success,message} response (no ip_set wrapper).
func TestUpdateIPSet_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"IP set updated successfully"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.UpdateIPSet(context.Background(), "set-uuid-1", map[string]any{"name": "renamed"})
	if err != nil {
		t.Fatalf("UpdateIPSet returned error: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %s; want PATCH", gotMethod)
	}
	if gotPath != "/api/ip-set/set-uuid-1" {
		t.Errorf("path = %s; want /api/ip-set/set-uuid-1 (singular)", gotPath)
	}
	if gotBody["name"] != "renamed" {
		t.Errorf("body[name] = %v; want renamed", gotBody["name"])
	}
}

// TestUpdateIPSet_Failure verifies a 200 success:false surfaces an error (C3).
func TestUpdateIPSet_Failure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"Cannot modify a global IP set."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.UpdateIPSet(context.Background(), "set-uuid-1", map[string]any{"name": "x"})
	if err == nil {
		t.Fatal("UpdateIPSet: expected error for success:false, got nil")
	}
	if !contains(err.Error(), "global IP set") {
		t.Errorf("error = %q; want it to contain %q", err.Error(), "global IP set")
	}
}

// ---------------------------------------------------------------------------
// DeleteIPSet
// ---------------------------------------------------------------------------

// TestDeleteIPSet_Success verifies DELETE /ip-set/{id} (SINGULAR).
func TestDeleteIPSet_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"IP set deleted successfully"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DeleteIPSet(context.Background(), "set-uuid-1"); err != nil {
		t.Fatalf("DeleteIPSet returned error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s; want DELETE", gotMethod)
	}
	if gotPath != "/api/ip-set/set-uuid-1" {
		t.Errorf("path = %s; want /api/ip-set/set-uuid-1 (singular)", gotPath)
	}
}

// TestDeleteIPSet_InUse verifies a 200 success:false (set referenced by a rule)
// surfaces an error (C3).
func TestDeleteIPSet_InUse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"Cannot delete: this IP set is in use by a security group rule."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	err := c.DeleteIPSet(context.Background(), "set-uuid-1")
	if err == nil {
		t.Fatal("DeleteIPSet: expected error for success:false, got nil")
	}
	if !contains(err.Error(), "in use") {
		t.Errorf("error = %q; want it to contain %q", err.Error(), "in use")
	}
}

// TestDeleteIPSet_EmptyID verifies the empty-id guard.
func TestDeleteIPSet_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if err := c.DeleteIPSet(context.Background(), ""); err == nil {
		t.Fatal("DeleteIPSet: expected error for empty id, got nil")
	}
}

// ---------------------------------------------------------------------------
// AddIPSetEntry
// ---------------------------------------------------------------------------

// TestAddIPSetEntry_Success verifies AddIPSetEntry:
//   - POSTs to /ip-set/{id}/entries
//   - sends cidr + description (description preserved, unlike bulk-add)
//   - unwraps the "entry" envelope, returning the entry WITH its server id
func TestAddIPSetEntry_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Entry added successfully","entry":{"id":"entry-uuid-1","cidr":"10.0.0.0/8","description":"office"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	entry, err := c.AddIPSetEntry(context.Background(), "set-uuid-1", map[string]any{
		"cidr":        "10.0.0.0/8",
		"description": "office",
	})
	if err != nil {
		t.Fatalf("AddIPSetEntry returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/ip-set/set-uuid-1/entries" {
		t.Errorf("path = %s; want /api/ip-set/set-uuid-1/entries", gotPath)
	}
	if entry["id"] != "entry-uuid-1" {
		t.Errorf("entry[id] = %v; want entry-uuid-1", entry["id"])
	}
	if entry["cidr"] != "10.0.0.0/8" {
		t.Errorf("entry[cidr] = %v; want 10.0.0.0/8", entry["cidr"])
	}
	if gotBody["cidr"] != "10.0.0.0/8" {
		t.Errorf("body[cidr] = %v; want 10.0.0.0/8", gotBody["cidr"])
	}
	if gotBody["description"] != "office" {
		t.Errorf("body[description] = %v; want office", gotBody["description"])
	}
}

// TestAddIPSetEntry_DuplicateFailure verifies a 200 success:false (duplicate
// cidr) surfaces an error (C3).
func TestAddIPSetEntry_DuplicateFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"This entry already exists in the IP set."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.AddIPSetEntry(context.Background(), "set-uuid-1", map[string]any{"cidr": "10.0.0.0/8"})
	if err == nil {
		t.Fatal("AddIPSetEntry: expected error for success:false, got nil")
	}
	if !contains(err.Error(), "already exists") {
		t.Errorf("error = %q; want it to contain %q", err.Error(), "already exists")
	}
}

// TestAddIPSetEntry_EmptySetID verifies the empty-setID guard.
func TestAddIPSetEntry_EmptySetID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if _, err := c.AddIPSetEntry(context.Background(), "", map[string]any{"cidr": "x"}); err == nil {
		t.Fatal("AddIPSetEntry: expected error for empty setID, got nil")
	}
}

// ---------------------------------------------------------------------------
// BulkAddIPSetEntries
// ---------------------------------------------------------------------------

// TestBulkAddIPSetEntries_Success verifies BulkAddIPSetEntries:
//   - POSTs to /ip-set/{id}/bulk-add
//   - sends {cidrs:[...]} (array of strings)
//   - returns the bare envelope with the "created" array
func TestBulkAddIPSetEntries_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"2 entries added successfully","created":[{"id":"e1","cidr":"10.0.0.0/8"},{"id":"e2","cidr":"192.168.1.0/24"}],"errors":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	env, err := c.BulkAddIPSetEntries(context.Background(), "set-uuid-1", []string{"10.0.0.0/8", "192.168.1.0/24"})
	if err != nil {
		t.Fatalf("BulkAddIPSetEntries returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/ip-set/set-uuid-1/bulk-add" {
		t.Errorf("path = %s; want /api/ip-set/set-uuid-1/bulk-add", gotPath)
	}
	cidrs, ok := gotBody["cidrs"].([]any)
	if !ok || len(cidrs) != 2 {
		t.Fatalf("body[cidrs] = %v; want 2-element array", gotBody["cidrs"])
	}
	if cidrs[0] != "10.0.0.0/8" {
		t.Errorf("body[cidrs][0] = %v; want 10.0.0.0/8", cidrs[0])
	}
	created, ok := env["created"].([]any)
	if !ok || len(created) != 2 {
		t.Fatalf("env[created] = %v; want 2-element array", env["created"])
	}
}

// TestBulkAddIPSetEntries_EmptySetID verifies the empty-setID guard.
func TestBulkAddIPSetEntries_EmptySetID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if _, err := c.BulkAddIPSetEntries(context.Background(), "", []string{"x"}); err == nil {
		t.Fatal("BulkAddIPSetEntries: expected error for empty setID, got nil")
	}
}

// ---------------------------------------------------------------------------
// DeleteIPSetEntry
// ---------------------------------------------------------------------------

// TestDeleteIPSetEntry_Success verifies DELETE /ip-set/{setID}/entry/{entryID}
// (singular "entry" segment).
func TestDeleteIPSetEntry_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Entry removed successfully"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DeleteIPSetEntry(context.Background(), "set-uuid-1", "entry-uuid-1"); err != nil {
		t.Fatalf("DeleteIPSetEntry returned error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s; want DELETE", gotMethod)
	}
	if gotPath != "/api/ip-set/set-uuid-1/entry/entry-uuid-1" {
		t.Errorf("path = %s; want /api/ip-set/set-uuid-1/entry/entry-uuid-1", gotPath)
	}
}

// TestDeleteIPSetEntry_EmptyIDs verifies the empty-id guards on both args.
func TestDeleteIPSetEntry_EmptyIDs(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if err := c.DeleteIPSetEntry(context.Background(), "", "e1"); err == nil {
		t.Fatal("DeleteIPSetEntry: expected error for empty setID, got nil")
	}
	if err := c.DeleteIPSetEntry(context.Background(), "set-uuid-1", ""); err == nil {
		t.Fatal("DeleteIPSetEntry: expected error for empty entryID, got nil")
	}
}

// ---------------------------------------------------------------------------
// ListIPSets
// ---------------------------------------------------------------------------

// TestListIPSets_Success verifies GET /ip-sets returns the paginator list.
func TestListIPSets_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"current_page":1,"data":[{"id":"set-uuid-1","name":"blocklist"},{"id":"set-uuid-2","name":"allowlist"}],"total":2}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	items, err := c.ListIPSets(context.Background())
	if err != nil {
		t.Fatalf("ListIPSets returned error: %v", err)
	}
	if gotPath != "/api/ip-sets" {
		t.Errorf("path = %s; want /api/ip-sets", gotPath)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d; want 2", len(items))
	}
	if items[0]["id"] != "set-uuid-1" {
		t.Errorf("items[0][id] = %v; want set-uuid-1", items[0]["id"])
	}
}
