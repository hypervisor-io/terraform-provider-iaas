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
// Instance Backup Policy — CreateInstanceBackupPolicy
// ---------------------------------------------------------------------------

func TestCreateInstanceBackupPolicy_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Backup policy created successfully.","policy":{"id":"ibp-uuid-1","name":"daily-full","full_backup_frequency":"daily","full_backup_time":"02:00","max_incremental_chain":3,"retention_count":7,"backup_device":"primary","status":"active"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	body := map[string]any{
		"name":                  "daily-full",
		"full_backup_frequency": "daily",
		"full_backup_time":      "02:00",
		"max_incremental_chain": 3,
		"retention_count":       7,
		"backup_device":         "primary",
	}
	obj, err := c.CreateInstanceBackupPolicy(context.Background(), body)
	if err != nil {
		t.Fatalf("CreateInstanceBackupPolicy returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/backup-policies" {
		t.Errorf("path = %s; want /api/backup-policies (plural)", gotPath)
	}
	if obj["id"] != "ibp-uuid-1" {
		t.Errorf("obj[id] = %v; want ibp-uuid-1", obj["id"])
	}
	if gotBody["name"] != "daily-full" {
		t.Errorf("body[name] = %v; want daily-full", gotBody["name"])
	}
	if gotBody["full_backup_frequency"] != "daily" {
		t.Errorf("body[full_backup_frequency] = %v; want daily", gotBody["full_backup_frequency"])
	}
}

func TestCreateInstanceBackupPolicy_Failure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"The name field is required."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.CreateInstanceBackupPolicy(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("CreateInstanceBackupPolicy: expected error for success:false, got nil")
	}
	if !contains(err.Error(), "name field is required") {
		t.Errorf("error = %q; want it to contain %q", err.Error(), "name field is required")
	}
}

// ---------------------------------------------------------------------------
// Instance Backup Policy — GetInstanceBackupPolicy
// ---------------------------------------------------------------------------

func TestGetInstanceBackupPolicy_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"policy":{"id":"ibp-uuid-1","name":"daily-full","full_backup_frequency":"daily","full_backup_time":"02:00","full_backup_day":null,"max_incremental_chain":3,"retention_count":7,"backup_device":"primary","status":"active","consecutive_failures":0,"last_error":null,"instances":[{"id":"inst-1","hostname":"web01"},{"id":"inst-2","hostname":"web02"}]},"available_instances":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.GetInstanceBackupPolicy(context.Background(), "ibp-uuid-1")
	if err != nil {
		t.Fatalf("GetInstanceBackupPolicy returned error: %v", err)
	}
	if gotPath != "/api/backup-policy/ibp-uuid-1" {
		t.Errorf("path = %s; want /api/backup-policy/ibp-uuid-1 (singular)", gotPath)
	}
	if obj["id"] != "ibp-uuid-1" {
		t.Errorf("obj[id] = %v; want ibp-uuid-1", obj["id"])
	}
	// The embedded instances array must be present.
	insts, ok := obj["instances"].([]any)
	if !ok {
		t.Fatalf("obj[instances] is not an array; got %T", obj["instances"])
	}
	if len(insts) != 2 {
		t.Fatalf("len(instances) = %d; want 2", len(insts))
	}
	first, _ := insts[0].(map[string]any)
	if first["id"] != "inst-1" {
		t.Errorf("instances[0][id] = %v; want inst-1", first["id"])
	}
}

func TestGetInstanceBackupPolicy_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"Not found"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.GetInstanceBackupPolicy(context.Background(), "missing")
	if err == nil {
		t.Fatal("GetInstanceBackupPolicy: expected error for 404, got nil")
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound = false; want true (err: %v)", err)
	}
}

func TestGetInstanceBackupPolicy_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if _, err := c.GetInstanceBackupPolicy(context.Background(), ""); err == nil {
		t.Fatal("GetInstanceBackupPolicy: expected error for empty id, got nil")
	}
}

// ---------------------------------------------------------------------------
// Instance Backup Policy — UpdateInstanceBackupPolicy
// ---------------------------------------------------------------------------

func TestUpdateInstanceBackupPolicy_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Backup policy updated successfully.","policy":{"id":"ibp-uuid-1","name":"weekly-full","full_backup_frequency":"weekly","full_backup_time":"03:00","full_backup_day":0,"max_incremental_chain":0,"retention_count":30,"backup_device":"all","status":"active"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	body := map[string]any{
		"name":                  "weekly-full",
		"full_backup_frequency": "weekly",
		"full_backup_time":      "03:00",
		"full_backup_day":       0,
		"max_incremental_chain": 0,
		"retention_count":       30,
		"backup_device":         "all",
	}
	obj, err := c.UpdateInstanceBackupPolicy(context.Background(), "ibp-uuid-1", body)
	if err != nil {
		t.Fatalf("UpdateInstanceBackupPolicy returned error: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %s; want PATCH", gotMethod)
	}
	if gotPath != "/api/backup-policy/ibp-uuid-1" {
		t.Errorf("path = %s; want /api/backup-policy/ibp-uuid-1 (singular)", gotPath)
	}
	if obj["name"] != "weekly-full" {
		t.Errorf("obj[name] = %v; want weekly-full", obj["name"])
	}
	if gotBody["full_backup_frequency"] != "weekly" {
		t.Errorf("body[full_backup_frequency] = %v; want weekly", gotBody["full_backup_frequency"])
	}
}

func TestUpdateInstanceBackupPolicy_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if _, err := c.UpdateInstanceBackupPolicy(context.Background(), "", map[string]any{"name": "x"}); err == nil {
		t.Fatal("UpdateInstanceBackupPolicy: expected error for empty id, got nil")
	}
}

// ---------------------------------------------------------------------------
// Instance Backup Policy — DeleteInstanceBackupPolicy
// ---------------------------------------------------------------------------

func TestDeleteInstanceBackupPolicy_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Backup policy deleted successfully."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DeleteInstanceBackupPolicy(context.Background(), "ibp-uuid-1"); err != nil {
		t.Fatalf("DeleteInstanceBackupPolicy returned error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s; want DELETE", gotMethod)
	}
	if gotPath != "/api/backup-policy/ibp-uuid-1" {
		t.Errorf("path = %s; want /api/backup-policy/ibp-uuid-1 (singular)", gotPath)
	}
}

func TestDeleteInstanceBackupPolicy_Failure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"Cannot delete policy with active instances."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	err := c.DeleteInstanceBackupPolicy(context.Background(), "ibp-uuid-1")
	if err == nil {
		t.Fatal("DeleteInstanceBackupPolicy: expected error for success:false, got nil")
	}
	if !contains(err.Error(), "active instances") {
		t.Errorf("error = %q; want it to contain %q", err.Error(), "active instances")
	}
}

func TestDeleteInstanceBackupPolicy_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if err := c.DeleteInstanceBackupPolicy(context.Background(), ""); err == nil {
		t.Fatal("DeleteInstanceBackupPolicy: expected error for empty id, got nil")
	}
}

// ---------------------------------------------------------------------------
// Instance Backup Policy — Attach / Detach
// ---------------------------------------------------------------------------

func TestAttachInstanceToBackupPolicy_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Instance \"web01\" attached to policy."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.AttachInstanceToBackupPolicy(context.Background(), "ibp-uuid-1", "inst-1"); err != nil {
		t.Fatalf("AttachInstanceToBackupPolicy returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/backup-policy/ibp-uuid-1/attach" {
		t.Errorf("path = %s; want /api/backup-policy/ibp-uuid-1/attach", gotPath)
	}
	if gotBody["instance_id"] != "inst-1" {
		t.Errorf("body[instance_id] = %v; want inst-1", gotBody["instance_id"])
	}
}

func TestAttachInstanceToBackupPolicy_EmptyArgs(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if err := c.AttachInstanceToBackupPolicy(context.Background(), "", "inst-1"); err == nil {
		t.Fatal("expected error for empty policyID")
	}
	if err := c.AttachInstanceToBackupPolicy(context.Background(), "ibp-1", ""); err == nil {
		t.Fatal("expected error for empty instanceID")
	}
}

func TestDetachInstanceFromBackupPolicy_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Instance \"web01\" detached from policy."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DetachInstanceFromBackupPolicy(context.Background(), "ibp-uuid-1", "inst-1"); err != nil {
		t.Fatalf("DetachInstanceFromBackupPolicy returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/backup-policy/ibp-uuid-1/detach" {
		t.Errorf("path = %s; want /api/backup-policy/ibp-uuid-1/detach", gotPath)
	}
	if gotBody["instance_id"] != "inst-1" {
		t.Errorf("body[instance_id] = %v; want inst-1", gotBody["instance_id"])
	}
}

func TestDetachInstanceFromBackupPolicy_EmptyArgs(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if err := c.DetachInstanceFromBackupPolicy(context.Background(), "", "inst-1"); err == nil {
		t.Fatal("expected error for empty policyID")
	}
	if err := c.DetachInstanceFromBackupPolicy(context.Background(), "ibp-1", ""); err == nil {
		t.Fatal("expected error for empty instanceID")
	}
}

// ---------------------------------------------------------------------------
// Database Backup Policy — CreateDBBackupPolicy
// ---------------------------------------------------------------------------

func TestCreateDBBackupPolicy_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Backup policy created successfully.","policy":{"id":"dbp-uuid-1","name":"prod-db-backup","s3_endpoint":"s3.example.com","s3_bucket":"my-backups","s3_region":"us-east-1","full_backup_frequency":"daily","full_backup_time":"01:00","incremental_frequency":"6h","pitr_enabled":false,"retention_full_count":7,"retention_incremental_days":14,"retention_pitr_hours":72,"encryption_enabled":false,"status":"active"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	body := map[string]any{
		"name":                       "prod-db-backup",
		"s3_endpoint":                "s3.example.com",
		"s3_bucket":                  "my-backups",
		"s3_region":                  "us-east-1",
		"s3_access_key":              "AKIAIOSFODNN7EXAMPLE",
		"s3_secret_key":              "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		"full_backup_frequency":      "daily",
		"full_backup_time":           "01:00",
		"incremental_frequency":      "6h",
		"pitr_enabled":               false,
		"retention_full_count":       7,
		"retention_incremental_days": 14,
		"retention_pitr_hours":       72,
		"encryption_enabled":         false,
	}
	obj, err := c.CreateDBBackupPolicy(context.Background(), body)
	if err != nil {
		t.Fatalf("CreateDBBackupPolicy returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/networking/db-backup-policies" {
		t.Errorf("path = %s; want /api/networking/db-backup-policies (plural)", gotPath)
	}
	if obj["id"] != "dbp-uuid-1" {
		t.Errorf("obj[id] = %v; want dbp-uuid-1", obj["id"])
	}
	if gotBody["s3_bucket"] != "my-backups" {
		t.Errorf("body[s3_bucket] = %v; want my-backups", gotBody["s3_bucket"])
	}
	if gotBody["s3_secret_key"] != "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY" {
		t.Errorf("body[s3_secret_key] = %v; want the example key", gotBody["s3_secret_key"])
	}
}

func TestCreateDBBackupPolicy_Failure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"S3 credential validation failed: Access Denied"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.CreateDBBackupPolicy(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("CreateDBBackupPolicy: expected error for success:false, got nil")
	}
	if !contains(err.Error(), "S3 credential validation failed") {
		t.Errorf("error = %q; want it to contain %q", err.Error(), "S3 credential validation failed")
	}
}

// ---------------------------------------------------------------------------
// Database Backup Policy — GetDBBackupPolicy
// ---------------------------------------------------------------------------

func TestGetDBBackupPolicy_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		// Note: s3_access_key, s3_secret_key, encryption_key are $hidden — absent.
		_, _ = w.Write([]byte(`{"policy":{"id":"dbp-uuid-1","name":"prod-db-backup","s3_endpoint":"s3.example.com","s3_bucket":"my-backups","s3_region":"us-east-1","s3_path_prefix":"backups","full_backup_frequency":"daily","full_backup_time":"01:00","full_backup_day":null,"incremental_frequency":"6h","pitr_enabled":false,"retention_full_count":7,"retention_incremental_days":14,"retention_pitr_hours":72,"encryption_enabled":false,"status":"active","consecutive_failures":0,"last_error":null,"managed_databases":[{"id":"db-1","name":"app-db"},{"id":"db-2","name":"analytics-db"}]},"available_databases":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.GetDBBackupPolicy(context.Background(), "dbp-uuid-1")
	if err != nil {
		t.Fatalf("GetDBBackupPolicy returned error: %v", err)
	}
	if gotPath != "/api/networking/db-backup-policy/dbp-uuid-1" {
		t.Errorf("path = %s; want /api/networking/db-backup-policy/dbp-uuid-1 (singular)", gotPath)
	}
	if obj["id"] != "dbp-uuid-1" {
		t.Errorf("obj[id] = %v; want dbp-uuid-1", obj["id"])
	}
	// Credentials must not be present (model $hidden).
	if _, ok := obj["s3_access_key"]; ok {
		t.Error("obj contains s3_access_key; it should be hidden by the model")
	}
	if _, ok := obj["s3_secret_key"]; ok {
		t.Error("obj contains s3_secret_key; it should be hidden by the model")
	}
	// The embedded managed_databases array must be present.
	dbs, ok := obj["managed_databases"].([]any)
	if !ok {
		t.Fatalf("obj[managed_databases] is not an array; got %T", obj["managed_databases"])
	}
	if len(dbs) != 2 {
		t.Fatalf("len(managed_databases) = %d; want 2", len(dbs))
	}
}

func TestGetDBBackupPolicy_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"Not found"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.GetDBBackupPolicy(context.Background(), "missing")
	if err == nil {
		t.Fatal("GetDBBackupPolicy: expected error for 404, got nil")
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound = false; want true (err: %v)", err)
	}
}

func TestGetDBBackupPolicy_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if _, err := c.GetDBBackupPolicy(context.Background(), ""); err == nil {
		t.Fatal("GetDBBackupPolicy: expected error for empty id, got nil")
	}
}

// ---------------------------------------------------------------------------
// Database Backup Policy — UpdateDBBackupPolicy
// ---------------------------------------------------------------------------

func TestUpdateDBBackupPolicy_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Backup policy updated successfully.","policy":{"id":"dbp-uuid-1","name":"prod-db-backup-v2","s3_endpoint":"s3.example.com","s3_bucket":"my-backups","s3_region":"us-east-1","full_backup_frequency":"weekly","retention_full_count":14,"status":"active"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	body := map[string]any{
		"name":                  "prod-db-backup-v2",
		"full_backup_frequency": "weekly",
		"full_backup_day":       0,
		"retention_full_count":  14,
	}
	obj, err := c.UpdateDBBackupPolicy(context.Background(), "dbp-uuid-1", body)
	if err != nil {
		t.Fatalf("UpdateDBBackupPolicy returned error: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %s; want PATCH", gotMethod)
	}
	if gotPath != "/api/networking/db-backup-policy/dbp-uuid-1" {
		t.Errorf("path = %s; want /api/networking/db-backup-policy/dbp-uuid-1 (singular)", gotPath)
	}
	if obj["name"] != "prod-db-backup-v2" {
		t.Errorf("obj[name] = %v; want prod-db-backup-v2", obj["name"])
	}
}

func TestUpdateDBBackupPolicy_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if _, err := c.UpdateDBBackupPolicy(context.Background(), "", map[string]any{"name": "x"}); err == nil {
		t.Fatal("UpdateDBBackupPolicy: expected error for empty id, got nil")
	}
}

// ---------------------------------------------------------------------------
// Database Backup Policy — DeleteDBBackupPolicy
// ---------------------------------------------------------------------------

func TestDeleteDBBackupPolicy_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Backup policy deleted successfully."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DeleteDBBackupPolicy(context.Background(), "dbp-uuid-1"); err != nil {
		t.Fatalf("DeleteDBBackupPolicy returned error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s; want DELETE", gotMethod)
	}
	if gotPath != "/api/networking/db-backup-policy/dbp-uuid-1" {
		t.Errorf("path = %s; want /api/networking/db-backup-policy/dbp-uuid-1 (singular)", gotPath)
	}
}

func TestDeleteDBBackupPolicy_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if err := c.DeleteDBBackupPolicy(context.Background(), ""); err == nil {
		t.Fatal("DeleteDBBackupPolicy: expected error for empty id, got nil")
	}
}

// ---------------------------------------------------------------------------
// Database Backup Policy — AttachDatabaseToBackupPolicy / Detach
// ---------------------------------------------------------------------------

func TestAttachDatabaseToBackupPolicy_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Database attached to backup policy. Configuration is being applied."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.AttachDatabaseToBackupPolicy(context.Background(), "dbp-uuid-1", "db-1"); err != nil {
		t.Fatalf("AttachDatabaseToBackupPolicy returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/networking/db-backup-policy/dbp-uuid-1/attach" {
		t.Errorf("path = %s; want /api/networking/db-backup-policy/dbp-uuid-1/attach", gotPath)
	}
	if gotBody["managed_database_id"] != "db-1" {
		t.Errorf("body[managed_database_id] = %v; want db-1", gotBody["managed_database_id"])
	}
}

func TestAttachDatabaseToBackupPolicy_EmptyArgs(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if err := c.AttachDatabaseToBackupPolicy(context.Background(), "", "db-1"); err == nil {
		t.Fatal("expected error for empty policyID")
	}
	if err := c.AttachDatabaseToBackupPolicy(context.Background(), "dbp-1", ""); err == nil {
		t.Fatal("expected error for empty databaseID")
	}
}

func TestDetachDatabaseFromBackupPolicy_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Database detached from backup policy."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DetachDatabaseFromBackupPolicy(context.Background(), "dbp-uuid-1", "db-1"); err != nil {
		t.Fatalf("DetachDatabaseFromBackupPolicy returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/networking/db-backup-policy/dbp-uuid-1/detach" {
		t.Errorf("path = %s; want /api/networking/db-backup-policy/dbp-uuid-1/detach", gotPath)
	}
	if gotBody["managed_database_id"] != "db-1" {
		t.Errorf("body[managed_database_id] = %v; want db-1", gotBody["managed_database_id"])
	}
}

func TestDetachDatabaseFromBackupPolicy_EmptyArgs(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if err := c.DetachDatabaseFromBackupPolicy(context.Background(), "", "db-1"); err == nil {
		t.Fatal("expected error for empty policyID")
	}
	if err := c.DetachDatabaseFromBackupPolicy(context.Background(), "dbp-1", ""); err == nil {
		t.Fatal("expected error for empty databaseID")
	}
}
