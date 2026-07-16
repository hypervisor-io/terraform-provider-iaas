package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// NOTE: like the other client tests, this file uses net/http/httptest
// directly rather than internal/acctest.MockServer (import-cycle reasons -
// see backup_policy_test.go).

// ---------------------------------------------------------------------------
// User surface - VM snapshots
// ---------------------------------------------------------------------------

func TestListInstanceSnapshots_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"snapshots":[{"name":"pre-upgrade","description":"","snaptime":1234567890,"vmstate":false,"parent":null},{"name":"post-upgrade","description":"after","snaptime":1234567999,"vmstate":true,"parent":"pre-upgrade"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	snaps, err := c.ListInstanceSnapshots(context.Background(), "inst-1")
	if err != nil {
		t.Fatalf("ListInstanceSnapshots returned error: %v", err)
	}
	if gotPath != "/api/instance/inst-1/snapshots" {
		t.Errorf("path = %s; want /api/instance/inst-1/snapshots", gotPath)
	}
	if len(snaps) != 2 {
		t.Fatalf("len(snaps) = %d; want 2", len(snaps))
	}
	if snaps[0]["name"] != "pre-upgrade" {
		t.Errorf("snaps[0][name] = %v; want pre-upgrade", snaps[0]["name"])
	}
	if snaps[1]["parent"] != "pre-upgrade" {
		t.Errorf("snaps[1][parent] = %v; want pre-upgrade", snaps[1]["parent"])
	}
}

func TestListInstanceSnapshots_NotProxmox(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"success":false,"message":"VM snapshots are a Proxmox-only feature."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.ListInstanceSnapshots(context.Background(), "inst-1")
	if err == nil {
		t.Fatal("ListInstanceSnapshots: expected error, got nil")
	}
	if !contains(err.Error(), "Proxmox-only feature") {
		t.Errorf("error = %q; want it to contain %q", err.Error(), "Proxmox-only feature")
	}
}

func TestListInstanceSnapshots_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if _, err := c.ListInstanceSnapshots(context.Background(), ""); err == nil {
		t.Fatal("ListInstanceSnapshots: expected error for empty id, got nil")
	}
}

func TestCreateInstanceSnapshot_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.CreateInstanceSnapshot(context.Background(), "inst-1", "pre-upgrade", true)
	if err != nil {
		t.Fatalf("CreateInstanceSnapshot returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/instance/inst-1/snapshot" {
		t.Errorf("path = %s; want /api/instance/inst-1/snapshot", gotPath)
	}
	// The Master reads the name from "snapname", not "name" - InstanceService::
	// createSnapshot / SnapshotHandler::create both read $params['snapshot_name']
	// sourced from the request's "snapname" field.
	if gotBody["snapname"] != "pre-upgrade" {
		t.Errorf(`body["snapname"] = %v; want "pre-upgrade"`, gotBody["snapname"])
	}
	if gotBody["vmstate"] != true {
		t.Errorf(`body["vmstate"] = %v; want true`, gotBody["vmstate"])
	}
	if obj["success"] != true {
		t.Errorf(`obj["success"] = %v; want true`, obj["success"])
	}
}

func TestCreateInstanceSnapshot_EmptyArgs(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if _, err := c.CreateInstanceSnapshot(context.Background(), "", "x", false); err == nil {
		t.Fatal("expected error for empty instanceID")
	}
	if _, err := c.CreateInstanceSnapshot(context.Background(), "inst-1", "", false); err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestRollbackInstanceSnapshot_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if _, err := c.RollbackInstanceSnapshot(context.Background(), "inst-1", "pre-upgrade"); err != nil {
		t.Fatalf("RollbackInstanceSnapshot returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/instance/inst-1/snapshot/rollback" {
		t.Errorf("path = %s; want /api/instance/inst-1/snapshot/rollback", gotPath)
	}
	if gotBody["snapname"] != "pre-upgrade" {
		t.Errorf(`body["snapname"] = %v; want "pre-upgrade"`, gotBody["snapname"])
	}
}

func TestRollbackInstanceSnapshot_EmptyArgs(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if _, err := c.RollbackInstanceSnapshot(context.Background(), "", "x"); err == nil {
		t.Fatal("expected error for empty instanceID")
	}
	if _, err := c.RollbackInstanceSnapshot(context.Background(), "inst-1", ""); err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestDeleteInstanceSnapshot_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if _, err := c.DeleteInstanceSnapshot(context.Background(), "inst-1", "pre-upgrade"); err != nil {
		t.Fatalf("DeleteInstanceSnapshot returned error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s; want DELETE", gotMethod)
	}
	if gotPath != "/api/instance/inst-1/snapshot" {
		t.Errorf("path = %s; want /api/instance/inst-1/snapshot", gotPath)
	}
	if gotBody["snapname"] != "pre-upgrade" {
		t.Errorf(`body["snapname"] = %v; want "pre-upgrade"`, gotBody["snapname"])
	}
}

func TestDeleteInstanceSnapshot_EmptyArgs(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if _, err := c.DeleteInstanceSnapshot(context.Background(), "", "x"); err == nil {
		t.Fatal("expected error for empty instanceID")
	}
	if _, err := c.DeleteInstanceSnapshot(context.Background(), "inst-1", ""); err == nil {
		t.Fatal("expected error for empty name")
	}
}

// ---------------------------------------------------------------------------
// User surface - tags, guest IPs, backup file browsing
// ---------------------------------------------------------------------------

func TestSetInstanceTags_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Tags updated"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.SetInstanceTags(context.Background(), "inst-1", "web,prod")
	if err != nil {
		t.Fatalf("SetInstanceTags returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/instance/inst-1/tags" {
		t.Errorf("path = %s; want /api/instance/inst-1/tags", gotPath)
	}
	if gotBody["tags"] != "web,prod" {
		t.Errorf(`body["tags"] = %v; want "web,prod"`, gotBody["tags"])
	}
	if obj["message"] != "Tags updated" {
		t.Errorf(`obj["message"] = %v; want "Tags updated"`, obj["message"])
	}
}

func TestSetInstanceTags_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if _, err := c.SetInstanceTags(context.Background(), "", "web"); err == nil {
		t.Fatal("expected error for empty instanceID")
	}
}

func TestGetInstanceGuestIPs_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"data":[{"nic":"eth0","mac":"aa:bb:cc:dd:ee:ff","ip":"203.0.113.9","type":"ipv4"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	ips, err := c.GetInstanceGuestIPs(context.Background(), "inst-1")
	if err != nil {
		t.Fatalf("GetInstanceGuestIPs returned error: %v", err)
	}
	if gotPath != "/api/instance/inst-1/guest-ips" {
		t.Errorf("path = %s; want /api/instance/inst-1/guest-ips", gotPath)
	}
	if len(ips) != 1 {
		t.Fatalf("len(ips) = %d; want 1", len(ips))
	}
	if ips[0]["ip"] != "203.0.113.9" {
		t.Errorf(`ips[0]["ip"] = %v; want "203.0.113.9"`, ips[0]["ip"])
	}
}

func TestGetInstanceGuestIPs_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if _, err := c.GetInstanceGuestIPs(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty instanceID")
	}
}

func TestListInstanceBackupFiles_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"files":[{"filepath":"/etc","type":"d"},{"filepath":"/etc/hostname","type":"f"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.ListInstanceBackupFiles(context.Background(), "inst-1", "backup-1", "/etc")
	if err != nil {
		t.Fatalf("ListInstanceBackupFiles returned error: %v", err)
	}
	if gotPath != "/api/instance/inst-1/backup/backup-1/files?filepath=%2Fetc" {
		t.Errorf("path = %s; want /api/instance/inst-1/backup/backup-1/files?filepath=%%2Fetc", gotPath)
	}
	files, ok := obj["files"].([]any)
	if !ok {
		t.Fatalf("obj[files] is not an array; got %T", obj["files"])
	}
	if len(files) != 2 {
		t.Fatalf("len(files) = %d; want 2", len(files))
	}
}

func TestListInstanceBackupFiles_NoFilepath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"files":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if _, err := c.ListInstanceBackupFiles(context.Background(), "inst-1", "backup-1", ""); err != nil {
		t.Fatalf("ListInstanceBackupFiles returned error: %v", err)
	}
	if gotPath != "/api/instance/inst-1/backup/backup-1/files" {
		t.Errorf("path = %s; want /api/instance/inst-1/backup/backup-1/files (no query string)", gotPath)
	}
}

func TestListInstanceBackupFiles_EmptyArgs(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if _, err := c.ListInstanceBackupFiles(context.Background(), "", "backup-1", ""); err == nil {
		t.Fatal("expected error for empty instanceID")
	}
	if _, err := c.ListInstanceBackupFiles(context.Background(), "inst-1", "", ""); err == nil {
		t.Fatal("expected error for empty backupID")
	}
}

// ---------------------------------------------------------------------------
// Admin surface - node issues
// ---------------------------------------------------------------------------

func TestAdminListNodeIssues_QueryParam(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"current_page":1,"data":[{"id":"issue-1","type":"webssh_proxy_install","status":"open"}],"last_page":1}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	issues, err := c.AdminListNodeIssues(context.Background(), "open")
	if err != nil {
		t.Fatalf("AdminListNodeIssues returned error: %v", err)
	}
	if gotPath != "/api/v1/proxmox/node-issues?status=open" {
		t.Errorf("path = %s; want /api/v1/proxmox/node-issues?status=open", gotPath)
	}
	if len(issues) != 1 {
		t.Fatalf("len(issues) = %d; want 1", len(issues))
	}
	if issues[0]["id"] != "issue-1" {
		t.Errorf(`issues[0]["id"] = %v; want "issue-1"`, issues[0]["id"])
	}
}

func TestAdminListNodeIssues_NoStatus(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"current_page":1,"data":[],"last_page":1}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if _, err := c.AdminListNodeIssues(context.Background(), ""); err != nil {
		t.Fatalf("AdminListNodeIssues returned error: %v", err)
	}
	if gotPath != "/api/v1/proxmox/node-issues" {
		t.Errorf("path = %s; want /api/v1/proxmox/node-issues (no query string)", gotPath)
	}
}

func TestAdminRetryNodeIssue_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Retry dispatched for tap_bandwidth."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.AdminRetryNodeIssue(context.Background(), "issue-1")
	if err != nil {
		t.Fatalf("AdminRetryNodeIssue returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/v1/proxmox/node-issues/issue-1/retry" {
		t.Errorf("path = %s; want /api/v1/proxmox/node-issues/issue-1/retry", gotPath)
	}
	if obj["message"] != "Retry dispatched for tap_bandwidth." {
		t.Errorf(`obj["message"] = %v`, obj["message"])
	}
}

func TestAdminRetryNodeIssue_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"No query results for model [App\\Models\\ProxmoxNodeIssue] issue-missing"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.AdminRetryNodeIssue(context.Background(), "issue-missing")
	if err == nil {
		t.Fatal("AdminRetryNodeIssue: expected error for 404, got nil")
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound = false; want true (err: %v)", err)
	}
}

func TestAdminRetryNodeIssue_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if _, err := c.AdminRetryNodeIssue(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty issueID")
	}
}

func TestAdminResolveNodeIssue_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Issue marked solved."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if _, err := c.AdminResolveNodeIssue(context.Background(), "issue-1"); err != nil {
		t.Fatalf("AdminResolveNodeIssue returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/v1/proxmox/node-issues/issue-1/resolve" {
		t.Errorf("path = %s; want /api/v1/proxmox/node-issues/issue-1/resolve", gotPath)
	}
}

func TestAdminResolveNodeIssue_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if _, err := c.AdminResolveNodeIssue(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty issueID")
	}
}

// ---------------------------------------------------------------------------
// Admin surface - PVE-native backup jobs
// ---------------------------------------------------------------------------

func TestAdminListBackupJobs_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"jobs":[{"id":"job-1","schedule":"02:00","storage":"local"},{"id":"job-2","schedule":"03:00","storage":"local"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	jobs, err := c.AdminListBackupJobs(context.Background(), "group-1")
	if err != nil {
		t.Fatalf("AdminListBackupJobs returned error: %v", err)
	}
	if gotPath != "/api/v1/hypervisor-group/group-1/backup-jobs" {
		t.Errorf("path = %s; want /api/v1/hypervisor-group/group-1/backup-jobs", gotPath)
	}
	if len(jobs) != 2 {
		t.Fatalf("len(jobs) = %d; want 2", len(jobs))
	}
	if jobs[0]["id"] != "job-1" {
		t.Errorf(`jobs[0]["id"] = %v; want "job-1"`, jobs[0]["id"])
	}
}

func TestAdminListBackupJobs_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if _, err := c.AdminListBackupJobs(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty groupID")
	}
}

func TestAdminCreateBackupJob_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	data := map[string]any{"target_type": "all", "storage": "local", "schedule": "02:00"}
	if _, err := c.AdminCreateBackupJob(context.Background(), "group-1", data); err != nil {
		t.Fatalf("AdminCreateBackupJob returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/v1/hypervisor-group/group-1/backup-jobs" {
		t.Errorf("path = %s; want /api/v1/hypervisor-group/group-1/backup-jobs", gotPath)
	}
	if gotBody["storage"] != "local" {
		t.Errorf(`body["storage"] = %v; want "local"`, gotBody["storage"])
	}
}

func TestAdminCreateBackupJob_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if _, err := c.AdminCreateBackupJob(context.Background(), "", map[string]any{}); err == nil {
		t.Fatal("expected error for empty groupID")
	}
}

func TestAdminUpdateBackupJob_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if _, err := c.AdminUpdateBackupJob(context.Background(), "group-1", "job-1", map[string]any{"schedule": "04:00"}); err != nil {
		t.Fatalf("AdminUpdateBackupJob returned error: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %s; want PUT", gotMethod)
	}
	if gotPath != "/api/v1/hypervisor-group/group-1/backup-jobs/job-1" {
		t.Errorf("path = %s; want /api/v1/hypervisor-group/group-1/backup-jobs/job-1", gotPath)
	}
}

func TestAdminUpdateBackupJob_EmptyArgs(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if _, err := c.AdminUpdateBackupJob(context.Background(), "", "job-1", map[string]any{}); err == nil {
		t.Fatal("expected error for empty groupID")
	}
	if _, err := c.AdminUpdateBackupJob(context.Background(), "group-1", "", map[string]any{}); err == nil {
		t.Fatal("expected error for empty jobID")
	}
}

func TestAdminDeleteBackupJob_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if _, err := c.AdminDeleteBackupJob(context.Background(), "group-1", "job-1"); err != nil {
		t.Fatalf("AdminDeleteBackupJob returned error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s; want DELETE", gotMethod)
	}
	if gotPath != "/api/v1/hypervisor-group/group-1/backup-jobs/job-1" {
		t.Errorf("path = %s; want /api/v1/hypervisor-group/group-1/backup-jobs/job-1", gotPath)
	}
}

func TestAdminDeleteBackupJob_EmptyArgs(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if _, err := c.AdminDeleteBackupJob(context.Background(), "", "job-1"); err == nil {
		t.Fatal("expected error for empty groupID")
	}
	if _, err := c.AdminDeleteBackupJob(context.Background(), "group-1", ""); err == nil {
		t.Fatal("expected error for empty jobID")
	}
}

// ---------------------------------------------------------------------------
// Admin surface - cluster migration
// ---------------------------------------------------------------------------

func TestAdminMigratePrecheck_QueryParam(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"local_disks":{}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if _, err := c.AdminMigratePrecheck(context.Background(), "inst-1", "node2"); err != nil {
		t.Fatalf("AdminMigratePrecheck returned error: %v", err)
	}
	if gotPath != "/api/v1/instance/inst-1/proxmox-migrate/precheck?target_node=node2" {
		t.Errorf("path = %s; want /api/v1/instance/inst-1/proxmox-migrate/precheck?target_node=node2", gotPath)
	}
}

func TestAdminMigratePrecheck_NoTargetNode(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if _, err := c.AdminMigratePrecheck(context.Background(), "inst-1", ""); err != nil {
		t.Fatalf("AdminMigratePrecheck returned error: %v", err)
	}
	if gotPath != "/api/v1/instance/inst-1/proxmox-migrate/precheck" {
		t.Errorf("path = %s; want /api/v1/instance/inst-1/proxmox-migrate/precheck (no query string)", gotPath)
	}
}

func TestAdminMigratePrecheck_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if _, err := c.AdminMigratePrecheck(context.Background(), "", "node2"); err == nil {
		t.Fatal("expected error for empty instanceID")
	}
}

func TestAdminMigrateInstance_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	opts := map[string]any{"target_node": "node2", "online": true}
	if _, err := c.AdminMigrateInstance(context.Background(), "inst-1", opts); err != nil {
		t.Fatalf("AdminMigrateInstance returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/v1/instance/inst-1/proxmox-migrate" {
		t.Errorf("path = %s; want /api/v1/instance/inst-1/proxmox-migrate", gotPath)
	}
	if gotBody["target_node"] != "node2" {
		t.Errorf(`body["target_node"] = %v; want "node2"`, gotBody["target_node"])
	}
}

func TestAdminMigrateInstance_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if _, err := c.AdminMigrateInstance(context.Background(), "", map[string]any{"target_node": "node2"}); err == nil {
		t.Fatal("expected error for empty instanceID")
	}
}
