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
// CreateManagedDatabase
// ---------------------------------------------------------------------------

// TestCreateManagedDatabase_Success verifies CreateManagedDatabase:
//   - POSTs to /databases (PLURAL)
//   - sends the prebuilt body verbatim (name, engine, engine_version, db_plan_id,
//     vpc_id, vpc_subnet_id)
//   - unwraps the "managed_database" envelope, returning the object WITH its id and
//     status="deploying".
//
// Create is async: the real ManagedDatabaseService::deploy returns
// {success,message,managed_database:{id,status:"deploying",...}} — the controller's
// Scribe annotation showing only {success,message} is stale (like VPC/LB).
func TestCreateManagedDatabase_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Managed database deployment initiated.","managed_database":{"id":"db-uuid-1","name":"my-db","engine":"mysql","engine_version":"8.0","status":"deploying","db_plan_id":"plan-1","port":3306,"admin_user":"dbadmin"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	body := map[string]any{
		"name":           "my-db",
		"engine":         "mysql",
		"engine_version": "8.0",
		"db_plan_id":     "plan-1",
		"vpc_id":         "vpc-1",
		"vpc_subnet_id":  "sub-1",
	}
	obj, err := c.CreateManagedDatabase(context.Background(), body)
	if err != nil {
		t.Fatalf("CreateManagedDatabase returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/databases" {
		t.Errorf("path = %s; want /api/databases (plural)", gotPath)
	}
	if obj["id"] != "db-uuid-1" {
		t.Errorf("obj[id] = %v; want db-uuid-1", obj["id"])
	}
	if obj["status"] != "deploying" {
		t.Errorf("obj[status] = %v; want deploying", obj["status"])
	}
	for _, k := range []string{"name", "engine", "engine_version", "db_plan_id", "vpc_id", "vpc_subnet_id"} {
		if _, ok := gotBody[k]; !ok {
			t.Errorf("create body missing key %q; got %v", k, gotBody)
		}
	}
}

// TestCreateManagedDatabase_BillingDisabled verifies the billing.enabled gate
// (HTTP 403) surfaces as an error via responseError.
func TestCreateManagedDatabase_BillingDisabled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"success":false,"message":"This feature is unavailable because billing is disabled."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.CreateManagedDatabase(context.Background(), map[string]any{"name": "x"})
	if err == nil {
		t.Fatal("CreateManagedDatabase: expected error for 403 billing gate, got nil")
	}
	if !contains(err.Error(), "billing is disabled") {
		t.Errorf("error = %q; want the billing-disabled message", err.Error())
	}
}

// TestCreateManagedDatabase_FeatureDisabled verifies an in-controller feature/quota
// gate (HTTP 200 success:false — e.g. plan disabled, quota, db_enabled false, no
// public IP) surfaces as an error (C3).
func TestCreateManagedDatabase_FeatureDisabled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"Managed databases are not enabled for this hypervisor group."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.CreateManagedDatabase(context.Background(), map[string]any{"name": "x"})
	if err == nil {
		t.Fatal("CreateManagedDatabase: expected error for 200 success:false, got nil")
	}
	if !contains(err.Error(), "not enabled for this hypervisor group") {
		t.Errorf("error = %q; want the feature-gate message", err.Error())
	}
}

// ---------------------------------------------------------------------------
// GetManagedDatabase
// ---------------------------------------------------------------------------

// TestGetManagedDatabase_Success verifies the SHOW route is SINGULAR and the
// "managed_database" envelope (including the nested public_ip object and the
// connection fields admin_user/port) is unwrapped.
func TestGetManagedDatabase_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"managed_database":{"id":"db-uuid-1","name":"my-db","engine":"mysql","engine_version":"8.0","status":"active","db_plan_id":"plan-1","port":3306,"admin_user":"dbadmin","vpc_id":"vpc-1","public_ip":{"id":"ip-1","ip":"203.0.113.5"}}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.GetManagedDatabase(context.Background(), "db-uuid-1")
	if err != nil {
		t.Fatalf("GetManagedDatabase returned error: %v", err)
	}
	if gotPath != "/api/database/db-uuid-1" {
		t.Errorf("path = %s; want /api/database/db-uuid-1 (singular)", gotPath)
	}
	if obj["status"] != "active" {
		t.Errorf("obj[status] = %v; want active", obj["status"])
	}
	if obj["admin_user"] != "dbadmin" {
		t.Errorf("obj[admin_user] = %v; want dbadmin", obj["admin_user"])
	}
	pub, ok := obj["public_ip"].(map[string]any)
	if !ok {
		t.Fatalf("expected nested public_ip object, got %T", obj["public_ip"])
	}
	if pub["ip"] != "203.0.113.5" {
		t.Errorf("public_ip.ip = %v; want 203.0.113.5", pub["ip"])
	}
}

// TestGetManagedDatabase_NotFound verifies a 404 is an *APIError that IsNotFound
// matches (route-model-binding 404 for a missing or non-owned DB).
func TestGetManagedDatabase_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"Managed Database not found"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.GetManagedDatabase(context.Background(), "missing")
	if err == nil {
		t.Fatal("GetManagedDatabase: expected error for 404, got nil")
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound = false; want true (err: %v)", err)
	}
}

func TestGetManagedDatabase_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if _, err := c.GetManagedDatabase(context.Background(), ""); err == nil {
		t.Fatal("GetManagedDatabase: expected error for empty id, got nil")
	}
}

// ---------------------------------------------------------------------------
// ListManagedDatabases
// ---------------------------------------------------------------------------

// TestListManagedDatabases_Success verifies the LIST route is PLURAL and the
// "managed_databases" paginator envelope is flattened.
func TestListManagedDatabases_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"managed_databases":{"current_page":1,"data":[{"id":"db-1","status":"active"},{"id":"db-2","status":"deploying"}],"total":2}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	items, err := c.ListManagedDatabases(context.Background())
	if err != nil {
		t.Fatalf("ListManagedDatabases returned error: %v", err)
	}
	if gotPath != "/api/databases" {
		t.Errorf("path = %s; want /api/databases", gotPath)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d; want 2", len(items))
	}
}

// ---------------------------------------------------------------------------
// DeleteManagedDatabase
// ---------------------------------------------------------------------------

func TestDeleteManagedDatabase_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Managed database deleted."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DeleteManagedDatabase(context.Background(), "db-1"); err != nil {
		t.Fatalf("DeleteManagedDatabase returned error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s; want DELETE", gotMethod)
	}
	if gotPath != "/api/database/db-1" {
		t.Errorf("path = %s; want /api/database/db-1 (singular)", gotPath)
	}
}

// TestDeleteManagedDatabase_Failure verifies a success:false delete (e.g. a primary
// that still has replicas) surfaces as an error (C3).
func TestDeleteManagedDatabase_Failure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"Cannot destroy a primary database that has replicas. Destroy all replicas first."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	err := c.DeleteManagedDatabase(context.Background(), "db-1")
	if err == nil {
		t.Fatal("DeleteManagedDatabase: expected error for success:false, got nil")
	}
	if !contains(err.Error(), "has replicas") {
		t.Errorf("error = %q; want the failure message", err.Error())
	}
}

func TestDeleteManagedDatabase_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if err := c.DeleteManagedDatabase(context.Background(), ""); err == nil {
		t.Fatal("DeleteManagedDatabase: expected error for empty id, got nil")
	}
}

// ---------------------------------------------------------------------------
// ResizeManagedDatabase
// ---------------------------------------------------------------------------

// TestResizeManagedDatabase_Success verifies the resize route is PATCH
// /database/{id}/resize and the {db_plan_id} body is sent verbatim, with the
// "managed_database" envelope unwrapped.
func TestResizeManagedDatabase_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Managed database resize initiated.","managed_database":{"id":"db-1","db_plan_id":"plan-2","status":"active"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.ResizeManagedDatabase(context.Background(), "db-1", map[string]any{"db_plan_id": "plan-2"})
	if err != nil {
		t.Fatalf("ResizeManagedDatabase returned error: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %s; want PATCH", gotMethod)
	}
	if gotPath != "/api/database/db-1/resize" {
		t.Errorf("path = %s; want /api/database/db-1/resize", gotPath)
	}
	if gotBody["db_plan_id"] != "plan-2" {
		t.Errorf("resize body db_plan_id = %v; want plan-2", gotBody["db_plan_id"])
	}
	if obj["db_plan_id"] != "plan-2" {
		t.Errorf("obj[db_plan_id] = %v; want plan-2", obj["db_plan_id"])
	}
}

// TestResizeManagedDatabase_Failure verifies a storage-downgrade rejection
// (success:false) surfaces as an error.
func TestResizeManagedDatabase_Failure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"Cannot downgrade storage."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.ResizeManagedDatabase(context.Background(), "db-1", map[string]any{"db_plan_id": "plan-2"})
	if err == nil {
		t.Fatal("ResizeManagedDatabase: expected error for success:false, got nil")
	}
	if !contains(err.Error(), "downgrade storage") {
		t.Errorf("error = %q; want the downgrade message", err.Error())
	}
}

func TestResizeManagedDatabase_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if _, err := c.ResizeManagedDatabase(context.Background(), "", map[string]any{"db_plan_id": "p"}); err == nil {
		t.Fatal("ResizeManagedDatabase: expected error for empty id, got nil")
	}
}

// ---------------------------------------------------------------------------
// RestartManagedDatabase
// ---------------------------------------------------------------------------

func TestRestartManagedDatabase_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Database service restart initiated."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.RestartManagedDatabase(context.Background(), "db-1"); err != nil {
		t.Fatalf("RestartManagedDatabase returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/database/db-1/restart" {
		t.Errorf("path = %s; want /api/database/db-1/restart", gotPath)
	}
}

func TestRestartManagedDatabase_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if err := c.RestartManagedDatabase(context.Background(), ""); err == nil {
		t.Fatal("RestartManagedDatabase: expected error for empty id, got nil")
	}
}

// ---------------------------------------------------------------------------
// ResetManagedDatabasePassword
// ---------------------------------------------------------------------------

// TestResetManagedDatabasePassword_Success verifies the reset-password route is
// POST /database/{id}/reset-password and the top-level cleartext "password" field
// (the ONLY place a password is returned) is read with key="".
func TestResetManagedDatabasePassword_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Database password has been reset.","password":"s3cr3t-new-pw"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.ResetManagedDatabasePassword(context.Background(), "db-1")
	if err != nil {
		t.Fatalf("ResetManagedDatabasePassword returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/database/db-1/reset-password" {
		t.Errorf("path = %s; want /api/database/db-1/reset-password", gotPath)
	}
	if obj["password"] != "s3cr3t-new-pw" {
		t.Errorf("obj[password] = %v; want s3cr3t-new-pw", obj["password"])
	}
}

func TestResetManagedDatabasePassword_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if _, err := c.ResetManagedDatabasePassword(context.Background(), ""); err == nil {
		t.Fatal("ResetManagedDatabasePassword: expected error for empty id, got nil")
	}
}

// ---------------------------------------------------------------------------
// CreateDatabaseReplica
// ---------------------------------------------------------------------------

// TestCreateDatabaseReplica_Success verifies the replica route is POST
// /database/{primaryID}/replica, the {name,db_plan_id,vpc_subnet_id} body is sent,
// and the "replica" envelope is unwrapped, returning the replica WITH its id and
// status="deploying".
func TestCreateDatabaseReplica_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Replica deployment initiated.","replica":{"id":"rep-1","name":"my-db-replica","status":"deploying","db_plan_id":"plan-1","primary_database_id":"db-1","role":"replica"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	body := map[string]any{"name": "my-db-replica", "db_plan_id": "plan-1", "vpc_subnet_id": "sub-1"}
	obj, err := c.CreateDatabaseReplica(context.Background(), "db-1", body)
	if err != nil {
		t.Fatalf("CreateDatabaseReplica returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/database/db-1/replica" {
		t.Errorf("path = %s; want /api/database/db-1/replica", gotPath)
	}
	if obj["id"] != "rep-1" {
		t.Errorf("obj[id] = %v; want rep-1", obj["id"])
	}
	if obj["status"] != "deploying" {
		t.Errorf("obj[status] = %v; want deploying", obj["status"])
	}
	for _, k := range []string{"name", "db_plan_id", "vpc_subnet_id"} {
		if _, ok := gotBody[k]; !ok {
			t.Errorf("replica body missing key %q; got %v", k, gotBody)
		}
	}
}

// TestCreateDatabaseReplica_Failure verifies a guard rejection (primary not active,
// storage too small, replica limit) surfaces as an error (C3).
func TestCreateDatabaseReplica_Failure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"Primary database must be active before creating a replica."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.CreateDatabaseReplica(context.Background(), "db-1", map[string]any{"db_plan_id": "p", "vpc_subnet_id": "s"})
	if err == nil {
		t.Fatal("CreateDatabaseReplica: expected error for success:false, got nil")
	}
	if !contains(err.Error(), "must be active") {
		t.Errorf("error = %q; want the guard message", err.Error())
	}
}

func TestCreateDatabaseReplica_EmptyPrimaryID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if _, err := c.CreateDatabaseReplica(context.Background(), "", map[string]any{"db_plan_id": "p"}); err == nil {
		t.Fatal("CreateDatabaseReplica: expected error for empty primary id, got nil")
	}
}
