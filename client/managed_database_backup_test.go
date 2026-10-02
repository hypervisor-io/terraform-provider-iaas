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

func TestBackupManagedDatabaseOfType_SendsBackupType(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"success":true,"message":"Backup initiated"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if _, err := c.BackupManagedDatabaseOfType(context.Background(), "db-1", "incremental"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/database/db-1/backup" {
		t.Errorf("request = %s %s", gotMethod, gotPath)
	}
	if gotBody["backup_type"] != "incremental" {
		t.Errorf("backup_type = %v; want incremental", gotBody["backup_type"])
	}
}

func TestBackupManagedDatabaseOfType_EmptyTypeSendsNoBody(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"success":true,"message":"Backup initiated"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if _, err := c.BackupManagedDatabaseOfType(context.Background(), "db-1", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := gotBody["backup_type"]; ok {
		t.Errorf("empty type must not send backup_type, got %v", gotBody)
	}
}

func TestBackupManagedDatabaseOfType_RefusalCarriesMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"success":false,"reason":"incremental_requires_pitr","message":"A PostgreSQL incremental backup needs point-in-time recovery to be active."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.BackupManagedDatabaseOfType(context.Background(), "db-1", "incremental")
	if err == nil || !strings.Contains(err.Error(), "point-in-time") {
		t.Fatalf("want a refusal carrying the server message, got %v", err)
	}
}

func TestBackupManagedDatabaseOfType_RejectsUnknownTypeBeforeRequest(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if _, err := c.BackupManagedDatabaseOfType(context.Background(), "db-1", "differential"); err == nil {
		t.Fatal("want an error for an unknown backup type")
	}
	if called {
		t.Error("an unknown backup type must never reach the API")
	}
}
