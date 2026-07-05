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
// an import cycle: acctest → provider → client). The shared `contains` helper
// lives in ssh_key_test.go.

// ---------------------------------------------------------------------------
// ListDBParameterGroups
// ---------------------------------------------------------------------------

// TestListDBParameterGroups_Success verifies that ListDBParameterGroups:
//   - GETs /db/parameter-groups (PLURAL)
//   - extracts the bare "parameter_groups" array (not a paginator)
//   - returns the list items with their id/name/engine/parameters fields.
func TestListDBParameterGroups_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"parameter_groups":[{"id":"pg-1","name":"my-group","engine":"mysql","parameters":{"max_connections":"200"}},{"id":"pg-2","name":"other","engine":"postgresql","parameters":{}}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	items, err := c.ListDBParameterGroups(context.Background())
	if err != nil {
		t.Fatalf("ListDBParameterGroups returned error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %s; want GET", gotMethod)
	}
	if gotPath != "/api/db/parameter-groups" {
		t.Errorf("path = %s; want /api/db/parameter-groups (plural)", gotPath)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d; want 2", len(items))
	}
	if items[0]["id"] != "pg-1" {
		t.Errorf("items[0][id] = %v; want pg-1", items[0]["id"])
	}
	if items[1]["engine"] != "postgresql" {
		t.Errorf("items[1][engine] = %v; want postgresql", items[1]["engine"])
	}
}

// TestListDBParameterGroups_Empty verifies that an empty parameter_groups array
// (not a paginator) is handled without error.
func TestListDBParameterGroups_Empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"parameter_groups":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	items, err := c.ListDBParameterGroups(context.Background())
	if err != nil {
		t.Fatalf("ListDBParameterGroups returned error: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("len(items) = %d; want 0", len(items))
	}
}

// TestListDBParameterGroups_BillingDisabled verifies the billing.enabled gate
// (HTTP 403) surfaces as an error.
func TestListDBParameterGroups_BillingDisabled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"success":false,"message":"This feature is unavailable because billing is disabled."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.ListDBParameterGroups(context.Background())
	if err == nil {
		t.Fatal("ListDBParameterGroups: expected error for 403 billing gate, got nil")
	}
	if !contains(err.Error(), "billing is disabled") {
		t.Errorf("error = %q; want billing-disabled message", err.Error())
	}
}

// ---------------------------------------------------------------------------
// GetDBParameterGroup (list-and-match - no SHOW endpoint)
// ---------------------------------------------------------------------------

// TestGetDBParameterGroup_Success verifies that GetDBParameterGroup finds the
// matching group by scanning the list.
func TestGetDBParameterGroup_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"parameter_groups":[{"id":"pg-1","name":"my-group","engine":"mysql","parameters":{"innodb_buffer_pool_size":"512M"}},{"id":"pg-2","name":"other","engine":"mariadb","parameters":{}}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.GetDBParameterGroup(context.Background(), "pg-1")
	if err != nil {
		t.Fatalf("GetDBParameterGroup returned error: %v", err)
	}
	if obj["id"] != "pg-1" {
		t.Errorf("obj[id] = %v; want pg-1", obj["id"])
	}
	if obj["name"] != "my-group" {
		t.Errorf("obj[name] = %v; want my-group", obj["name"])
	}
	if obj["engine"] != "mysql" {
		t.Errorf("obj[engine] = %v; want mysql", obj["engine"])
	}
	// Verify the parameters map is preserved.
	params, ok := obj["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("expected parameters map, got %T", obj["parameters"])
	}
	if params["innodb_buffer_pool_size"] != "512M" {
		t.Errorf("parameters[innodb_buffer_pool_size] = %v; want 512M", params["innodb_buffer_pool_size"])
	}
}

// TestGetDBParameterGroup_NotFound verifies that a group not in the list returns
// a 404 *APIError recognised by IsNotFound.
func TestGetDBParameterGroup_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"parameter_groups":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.GetDBParameterGroup(context.Background(), "missing-id")
	if err == nil {
		t.Fatal("GetDBParameterGroup: expected error for missing id, got nil")
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound = false; want true (err: %v)", err)
	}
}

// TestGetDBParameterGroup_EmptyID verifies the empty-id guard.
func TestGetDBParameterGroup_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if _, err := c.GetDBParameterGroup(context.Background(), ""); err == nil {
		t.Fatal("GetDBParameterGroup: expected error for empty id, got nil")
	}
}

// ---------------------------------------------------------------------------
// CreateDBParameterGroup
// ---------------------------------------------------------------------------

// TestCreateDBParameterGroup_Success verifies CreateDBParameterGroup:
//   - POSTs to /db/parameter-groups (PLURAL)
//   - sends name, engine, parameters in the body
//   - unwraps the "parameter_group" envelope, returning the object with id.
func TestCreateDBParameterGroup_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"parameter_group":{"id":"pg-new","name":"Custom MySQL","engine":"mysql","parameters":{"max_connections":"200","innodb_buffer_pool_size":"512M"}}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	body := map[string]any{
		"name":   "Custom MySQL",
		"engine": "mysql",
		"parameters": map[string]any{
			"max_connections":         "200",
			"innodb_buffer_pool_size": "512M",
		},
	}
	obj, err := c.CreateDBParameterGroup(context.Background(), body)
	if err != nil {
		t.Fatalf("CreateDBParameterGroup returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/db/parameter-groups" {
		t.Errorf("path = %s; want /api/db/parameter-groups (plural)", gotPath)
	}
	if obj["id"] != "pg-new" {
		t.Errorf("obj[id] = %v; want pg-new", obj["id"])
	}
	// Verify required body keys were sent.
	for _, k := range []string{"name", "engine", "parameters"} {
		if _, ok := gotBody[k]; !ok {
			t.Errorf("create body missing key %q; got %v", k, gotBody)
		}
	}
}

// TestCreateDBParameterGroup_BillingDisabled verifies the billing.enabled gate
// (HTTP 403) surfaces as an error.
func TestCreateDBParameterGroup_BillingDisabled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"success":false,"message":"This feature is unavailable because billing is disabled."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.CreateDBParameterGroup(context.Background(), map[string]any{"name": "x"})
	if err == nil {
		t.Fatal("CreateDBParameterGroup: expected error for 403 billing gate, got nil")
	}
	if !contains(err.Error(), "billing is disabled") {
		t.Errorf("error = %q; want billing-disabled message", err.Error())
	}
}

// TestCreateDBParameterGroup_ValidationError verifies a 422 validation error
// (invalid engine, unknown parameter key) surfaces as an error.
func TestCreateDBParameterGroup_ValidationError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"The given data was invalid.","errors":{"engine":["The selected engine is invalid."]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.CreateDBParameterGroup(context.Background(), map[string]any{"name": "x", "engine": "badengine"})
	if err == nil {
		t.Fatal("CreateDBParameterGroup: expected error for 422, got nil")
	}
}

// ---------------------------------------------------------------------------
// UpdateDBParameterGroup
// ---------------------------------------------------------------------------

// TestUpdateDBParameterGroup_Success verifies UpdateDBParameterGroup:
//   - PATCHes /db/parameter-group/{id} (SINGULAR)
//   - unwraps the "parameter_group" envelope.
func TestUpdateDBParameterGroup_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"parameter_group":{"id":"pg-1","name":"Renamed Group","engine":"mysql","parameters":{"max_connections":"500"}}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	body := map[string]any{
		"name":       "Renamed Group",
		"parameters": map[string]any{"max_connections": "500"},
	}
	obj, err := c.UpdateDBParameterGroup(context.Background(), "pg-1", body)
	if err != nil {
		t.Fatalf("UpdateDBParameterGroup returned error: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %s; want PATCH", gotMethod)
	}
	if gotPath != "/api/db/parameter-group/pg-1" {
		t.Errorf("path = %s; want /api/db/parameter-group/pg-1 (singular)", gotPath)
	}
	if obj["name"] != "Renamed Group" {
		t.Errorf("obj[name] = %v; want Renamed Group", obj["name"])
	}
	if gotBody["name"] != "Renamed Group" {
		t.Errorf("update body name = %v; want Renamed Group", gotBody["name"])
	}
}

// TestUpdateDBParameterGroup_EmptyID verifies the empty-id guard.
func TestUpdateDBParameterGroup_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if _, err := c.UpdateDBParameterGroup(context.Background(), "", map[string]any{"name": "x"}); err == nil {
		t.Fatal("UpdateDBParameterGroup: expected error for empty id, got nil")
	}
}

// ---------------------------------------------------------------------------
// DeleteDBParameterGroup
// ---------------------------------------------------------------------------

// TestDeleteDBParameterGroup_Success verifies DeleteDBParameterGroup:
//   - DELETEs /db/parameter-group/{id} (SINGULAR)
//   - accepts {success,message} as success.
func TestDeleteDBParameterGroup_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Parameter group deleted."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DeleteDBParameterGroup(context.Background(), "pg-1"); err != nil {
		t.Fatalf("DeleteDBParameterGroup returned error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s; want DELETE", gotMethod)
	}
	if gotPath != "/api/db/parameter-group/pg-1" {
		t.Errorf("path = %s; want /api/db/parameter-group/pg-1 (singular)", gotPath)
	}
}

// TestDeleteDBParameterGroup_Failure verifies a success:false delete surfaces as
// an error (C3).
func TestDeleteDBParameterGroup_Failure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"Cannot delete parameter group in use."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	err := c.DeleteDBParameterGroup(context.Background(), "pg-1")
	if err == nil {
		t.Fatal("DeleteDBParameterGroup: expected error for success:false, got nil")
	}
	if !contains(err.Error(), "in use") {
		t.Errorf("error = %q; want the failure message", err.Error())
	}
}

// TestDeleteDBParameterGroup_EmptyID verifies the empty-id guard.
func TestDeleteDBParameterGroup_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if err := c.DeleteDBParameterGroup(context.Background(), ""); err == nil {
		t.Fatal("DeleteDBParameterGroup: expected error for empty id, got nil")
	}
}
