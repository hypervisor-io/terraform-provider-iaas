package client

import (
	"context"
	"encoding/json"
	"io"
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
// CreateKubernetesCluster
// ---------------------------------------------------------------------------

// TestCreateKubernetesCluster_Success verifies CreateKubernetesCluster:
//   - POSTs to /kubernetes/clusters (PLURAL)
//   - sends the prebuilt body verbatim
//   - attaches the supplied Idempotency-Key request header (idempotency.user)
//   - unwraps the "cluster" envelope, returning the object WITH its id and
//     state="created".
//
// Create is async: the real ClusterService::create returns
// {success,cluster:{id,state:"created"},task_id} - the cluster comes up
// asynchronously and the caller polls the cluster's "state" field via SHOW.
func TestCreateKubernetesCluster_Success(t *testing.T) {
	var gotMethod, gotPath, gotIdemKey string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotIdemKey = r.Header.Get("Idempotency-Key")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"cluster":{"id":"k8s-uuid-1","name":"prod","slug":"prod","state":"created"},"task_id":"task-uuid-1"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	body := map[string]any{
		"name":                    "prod",
		"slug":                    "prod",
		"hypervisor_group_id":     "hg-1",
		"vpc_id":                  "vpc-1",
		"cp_vpc_subnet_id":        "sub-cp",
		"worker_vpc_subnet_id":    "sub-wk",
		"kubernetes_version_id":   "ver-1",
		"control_node_count":      1,
		"endpoint_mode":           "public_and_private",
		"cp_instance_plan_id":     "ip-cp",
		"cp_lb_plan_id":           "lbp-1",
		"worker_instance_plan_id": "ip-wk",
	}
	obj, err := c.CreateKubernetesCluster(context.Background(), body, "idem-key-abc")
	if err != nil {
		t.Fatalf("CreateKubernetesCluster returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/kubernetes/clusters" {
		t.Errorf("path = %s; want /api/kubernetes/clusters", gotPath)
	}
	if gotIdemKey != "idem-key-abc" {
		t.Errorf("Idempotency-Key header = %q; want %q", gotIdemKey, "idem-key-abc")
	}
	if gotBody["name"] != "prod" || gotBody["slug"] != "prod" {
		t.Errorf("body did not round-trip: %+v", gotBody)
	}
	if obj["id"] != "k8s-uuid-1" {
		t.Errorf("returned id = %v; want k8s-uuid-1", obj["id"])
	}
	if obj["state"] != "created" {
		t.Errorf("returned state = %v; want created", obj["state"])
	}
}

// TestCreateKubernetesCluster_FeatureGateError verifies that an HTTP-200
// success:false body (the in-controller feature/quota gate, e.g. region missing
// VPC+LB) is surfaced as an error carrying the message (C3).
func TestCreateKubernetesCluster_FeatureGateError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 422 with success:false (region not eligible).
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"success":false,"message":"This region does not have VPC and Load Balancer features enabled. Kubernetes requires both."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.CreateKubernetesCluster(context.Background(), map[string]any{"name": "x"}, "k")
	if err == nil {
		t.Fatal("expected error for 422 feature gate, got nil")
	}
	if !contains(err.Error(), "VPC and Load Balancer") {
		t.Errorf("error = %v; want it to mention the feature gate message", err)
	}
}

// TestCreateKubernetesCluster_GeneratesKeyWhenEmpty verifies that an empty
// idempotency key still sends a NON-empty Idempotency-Key header (the client
// generates a UUID), so a retry within the same client call is deduplicated.
func TestCreateKubernetesCluster_GeneratesKeyWhenEmpty(t *testing.T) {
	var gotIdemKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotIdemKey = r.Header.Get("Idempotency-Key")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"cluster":{"id":"k","state":"created"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if _, err := c.CreateKubernetesCluster(context.Background(), map[string]any{"name": "x"}, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotIdemKey == "" {
		t.Error("expected a generated non-empty Idempotency-Key header when none supplied")
	}
}

// ---------------------------------------------------------------------------
// GetKubernetesCluster
// ---------------------------------------------------------------------------

// TestGetKubernetesCluster_Success verifies the SHOW route is SINGULAR
// (/kubernetes/cluster/{id}) and unwraps the "cluster" envelope. This is the
// async poll source (scan "state" for "running") and the 404 signal for delete.
func TestGetKubernetesCluster_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"cluster":{"id":"k8s-uuid-1","name":"prod","state":"running","endpoint_url":"https://10.0.0.5:6443","worker_count":3}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.GetKubernetesCluster(context.Background(), "k8s-uuid-1")
	if err != nil {
		t.Fatalf("GetKubernetesCluster returned error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %s; want GET", gotMethod)
	}
	if gotPath != "/api/kubernetes/cluster/k8s-uuid-1" {
		t.Errorf("path = %s; want /api/kubernetes/cluster/k8s-uuid-1", gotPath)
	}
	if obj["state"] != "running" {
		t.Errorf("state = %v; want running", obj["state"])
	}
}

// TestGetKubernetesCluster_NotFound verifies a 404 maps to an IsNotFound error.
func TestGetKubernetesCluster_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"Cluster not found"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.GetKubernetesCluster(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected error for 404, got nil")
	}
	if !IsNotFound(err) {
		t.Errorf("expected IsNotFound, got %v", err)
	}
}

// TestGetKubernetesCluster_EmptyID guards the empty-id path argument.
func TestGetKubernetesCluster_EmptyID(t *testing.T) {
	c := New("http://unused/api", "tok", time.Second, false)
	if _, err := c.GetKubernetesCluster(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty id")
	}
}

// ---------------------------------------------------------------------------
// ListKubernetesClusters
// ---------------------------------------------------------------------------

// TestListKubernetesClusters_Success verifies the index unwraps the named
// "clusters" paginator envelope ({success,clusters:{data:[...]}}).
func TestListKubernetesClusters_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"clusters":{"current_page":1,"last_page":1,"data":[{"id":"a","state":"running"},{"id":"b","state":"created"}]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	list, err := c.ListKubernetesClusters(context.Background())
	if err != nil {
		t.Fatalf("ListKubernetesClusters returned error: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("len = %d; want 2", len(list))
	}
	if list[0]["id"] != "a" || list[1]["id"] != "b" {
		t.Errorf("unexpected list contents: %+v", list)
	}
}

// ---------------------------------------------------------------------------
// UpdateKubernetesCluster
// ---------------------------------------------------------------------------

// TestUpdateKubernetesCluster_Success verifies the update route is a PATCH to
// the SINGULAR path and unwraps the "cluster" envelope. Only name/description/
// project_id are mutable; the body is sent verbatim.
func TestUpdateKubernetesCluster_Success(t *testing.T) {
	var gotMethod, gotPath, gotIdemKey string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotIdemKey = r.Header.Get("Idempotency-Key")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"cluster":{"id":"k8s-uuid-1","name":"renamed","state":"running"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.UpdateKubernetesCluster(context.Background(), "k8s-uuid-1", map[string]any{"name": "renamed"}, "idem-upd")
	if err != nil {
		t.Fatalf("UpdateKubernetesCluster returned error: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %s; want PATCH", gotMethod)
	}
	if gotPath != "/api/kubernetes/cluster/k8s-uuid-1" {
		t.Errorf("path = %s; want /api/kubernetes/cluster/k8s-uuid-1", gotPath)
	}
	if gotIdemKey != "idem-upd" {
		t.Errorf("Idempotency-Key = %q; want idem-upd", gotIdemKey)
	}
	if gotBody["name"] != "renamed" {
		t.Errorf("body name = %v; want renamed", gotBody["name"])
	}
	if obj["name"] != "renamed" {
		t.Errorf("returned name = %v; want renamed", obj["name"])
	}
}

// ---------------------------------------------------------------------------
// DeleteKubernetesCluster
// ---------------------------------------------------------------------------

// TestDeleteKubernetesCluster_Success verifies the delete route is a DELETE to
// the SINGULAR path. DELETE is async (returns task_id) and soft-deletes the row
// so a subsequent SHOW 404s. A failure surfaces as success:false (C3).
func TestDeleteKubernetesCluster_Success(t *testing.T) {
	var gotMethod, gotPath, gotIdemKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotIdemKey = r.Header.Get("Idempotency-Key")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"task_id":"del-task-1"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DeleteKubernetesCluster(context.Background(), "k8s-uuid-1", "idem-del"); err != nil {
		t.Fatalf("DeleteKubernetesCluster returned error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s; want DELETE", gotMethod)
	}
	if gotPath != "/api/kubernetes/cluster/k8s-uuid-1" {
		t.Errorf("path = %s; want /api/kubernetes/cluster/k8s-uuid-1", gotPath)
	}
	if gotIdemKey != "idem-del" {
		t.Errorf("Idempotency-Key = %q; want idem-del", gotIdemKey)
	}
}

// TestDeleteKubernetesCluster_AlreadyDestroyed verifies an already-destroyed
// cluster (422 success:false) surfaces as an error.
func TestDeleteKubernetesCluster_AlreadyDestroyed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"success":false,"message":"already destroyed"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	err := c.DeleteKubernetesCluster(context.Background(), "k8s-uuid-1", "k")
	if err == nil {
		t.Fatal("expected error for already-destroyed cluster")
	}
	if !contains(err.Error(), "already destroyed") {
		t.Errorf("error = %v; want it to mention 'already destroyed'", err)
	}
}

// TestDeleteKubernetesCluster_EmptyID guards the empty-id path argument.
func TestDeleteKubernetesCluster_EmptyID(t *testing.T) {
	c := New("http://unused/api", "tok", time.Second, false)
	if err := c.DeleteKubernetesCluster(context.Background(), "", "k"); err == nil {
		t.Fatal("expected error for empty id")
	}
}

// ---------------------------------------------------------------------------
// UpgradeK8sClusterControlPlane / UpgradeK8sClusterWorkers / UpgradeK8sClusterCCM
// / RetryK8sClusterUpgrade (T7/id-G8: in-place version upgrade)
// ---------------------------------------------------------------------------

// TestUpgradeK8sClusterControlPlane_Success verifies the route
// (POST .../upgrade/cp), that the body round-trips verbatim, the
// Idempotency-Key header is attached, and the BARE {task_id,...} envelope (no
// "cluster"/"success" wrapper) is returned unwrapped.
func TestUpgradeK8sClusterControlPlane_Success(t *testing.T) {
	var gotMethod, gotPath, gotIdemKey string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotIdemKey = r.Header.Get("Idempotency-Key")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"task_id":"cp-task-1","target_version_id":"ver-2","current_version_id":"ver-1","planned_waves":[1,1]}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.UpgradeK8sClusterControlPlane(context.Background(), "k8s-uuid-1", map[string]any{
		"target_version_id":  "ver-2",
		"drain_grace_period": 120,
	}, "idem-cp")
	if err != nil {
		t.Fatalf("UpgradeK8sClusterControlPlane returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/kubernetes/cluster/k8s-uuid-1/upgrade/cp" {
		t.Errorf("path = %s; want /api/kubernetes/cluster/k8s-uuid-1/upgrade/cp", gotPath)
	}
	if gotIdemKey != "idem-cp" {
		t.Errorf("Idempotency-Key = %q; want idem-cp", gotIdemKey)
	}
	if gotBody["target_version_id"] != "ver-2" {
		t.Errorf("body target_version_id = %v; want ver-2", gotBody["target_version_id"])
	}
	if obj["task_id"] != "cp-task-1" {
		t.Errorf("returned task_id = %v; want cp-task-1", obj["task_id"])
	}
}

// TestUpgradeK8sClusterControlPlane_TargetRejected verifies a 422
// {"code":"target_not_active"} InvalidUpgradeTargetException surfaces as an error.
func TestUpgradeK8sClusterControlPlane_TargetRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"code":"target_not_active","message":"target version is not active"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.UpgradeK8sClusterControlPlane(context.Background(), "k8s-uuid-1", map[string]any{"target_version_id": "bad"}, "k")
	if err == nil {
		t.Fatal("expected error for 422 target rejection, got nil")
	}
}

// TestUpgradeK8sClusterControlPlane_EmptyID guards the empty-id path argument.
func TestUpgradeK8sClusterControlPlane_EmptyID(t *testing.T) {
	c := New("http://unused/api", "tok", time.Second, false)
	if _, err := c.UpgradeK8sClusterControlPlane(context.Background(), "", map[string]any{}, "k"); err == nil {
		t.Fatal("expected error for empty id")
	}
}

// TestUpgradeK8sClusterWorkers_Success verifies the route
// (POST .../upgrade/workers) and that max_surge/target_version_id/
// drain_grace_period round-trip.
func TestUpgradeK8sClusterWorkers_Success(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"task_id":"wk-task-1","target_version_id":"ver-2","current_version_id":"ver-1","planned_waves":[2,2]}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.UpgradeK8sClusterWorkers(context.Background(), "k8s-uuid-1", map[string]any{
		"target_version_id":  "ver-2",
		"max_surge":          1,
		"drain_grace_period": 120,
	}, "idem-wk")
	if err != nil {
		t.Fatalf("UpgradeK8sClusterWorkers returned error: %v", err)
	}
	if gotPath != "/api/kubernetes/cluster/k8s-uuid-1/upgrade/workers" {
		t.Errorf("path = %s; want /api/kubernetes/cluster/k8s-uuid-1/upgrade/workers", gotPath)
	}
	if gotBody["max_surge"] != float64(1) {
		t.Errorf("body max_surge = %v; want 1", gotBody["max_surge"])
	}
	if obj["task_id"] != "wk-task-1" {
		t.Errorf("returned task_id = %v; want wk-task-1", obj["task_id"])
	}
}

// TestUpgradeK8sClusterWorkers_ExceedsCPVersion verifies a 422
// {"code":"target_exceeds_cp_version"} surfaces as an error.
func TestUpgradeK8sClusterWorkers_ExceedsCPVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"code":"target_exceeds_cp_version","message":"worker target exceeds CP version; upgrade control plane first"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.UpgradeK8sClusterWorkers(context.Background(), "k8s-uuid-1", map[string]any{"target_version_id": "ver-99"}, "k")
	if err == nil {
		t.Fatal("expected error for 422 target_exceeds_cp_version, got nil")
	}
	if !contains(err.Error(), "upgrade control plane first") {
		t.Errorf("error = %v; want it to mention the CP-first message", err)
	}
}

// TestUpgradeK8sClusterWorkers_EmptyID guards the empty-id path argument.
func TestUpgradeK8sClusterWorkers_EmptyID(t *testing.T) {
	c := New("http://unused/api", "tok", time.Second, false)
	if _, err := c.UpgradeK8sClusterWorkers(context.Background(), "", map[string]any{}, "k"); err == nil {
		t.Fatal("expected error for empty id")
	}
}

// TestUpgradeK8sClusterCCM_Success verifies the route (POST .../upgrade/ccm),
// that NO body is sent, and that the synchronous {"success":true,...} response
// (no task_id) is treated as success (nil error).
func TestUpgradeK8sClusterCCM_Success(t *testing.T) {
	var gotPath string
	var gotBodyBytes []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBodyBytes, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"CCM redeployed"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.UpgradeK8sClusterCCM(context.Background(), "k8s-uuid-1", "idem-ccm"); err != nil {
		t.Fatalf("UpgradeK8sClusterCCM returned error: %v", err)
	}
	if gotPath != "/api/kubernetes/cluster/k8s-uuid-1/upgrade/ccm" {
		t.Errorf("path = %s; want /api/kubernetes/cluster/k8s-uuid-1/upgrade/ccm", gotPath)
	}
	if trimmed := string(gotBodyBytes); trimmed != "null" && trimmed != "" {
		t.Errorf("expected an empty/null body, got %q", trimmed)
	}
}

// TestUpgradeK8sClusterCCM_NotRunning verifies a 409 (cluster not running)
// surfaces as an error.
func TestUpgradeK8sClusterCCM_NotRunning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"success":false,"message":"cluster is not running"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	err := c.UpgradeK8sClusterCCM(context.Background(), "k8s-uuid-1", "k")
	if err == nil {
		t.Fatal("expected error for 409 not-running, got nil")
	}
}

// TestUpgradeK8sClusterCCM_EmptyID guards the empty-id path argument.
func TestUpgradeK8sClusterCCM_EmptyID(t *testing.T) {
	c := New("http://unused/api", "tok", time.Second, false)
	if err := c.UpgradeK8sClusterCCM(context.Background(), "", "k"); err == nil {
		t.Fatal("expected error for empty id")
	}
}

// TestRetryK8sClusterUpgrade_Success verifies the route
// (POST .../upgrade/retry) and that the {success,task_id,cleanup_errors}
// envelope is returned unwrapped.
func TestRetryK8sClusterUpgrade_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"task_id":"retry-task-1","cleanup_errors":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.RetryK8sClusterUpgrade(context.Background(), "k8s-uuid-1", "idem-retry")
	if err != nil {
		t.Fatalf("RetryK8sClusterUpgrade returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/kubernetes/cluster/k8s-uuid-1/upgrade/retry" {
		t.Errorf("path = %s; want /api/kubernetes/cluster/k8s-uuid-1/upgrade/retry", gotPath)
	}
	if obj["task_id"] != "retry-task-1" {
		t.Errorf("returned task_id = %v; want retry-task-1", obj["task_id"])
	}
}

// TestRetryK8sClusterUpgrade_NotInErrorState verifies the 422 gate (cluster not
// in "error" state) surfaces as an error - the ground-truth behavior that makes
// this endpoint unsuitable as an automatic fail path for a failed CP/worker
// rolling upgrade (which leaves cluster.state=="running", never "error").
func TestRetryK8sClusterUpgrade_NotInErrorState(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"success":false,"message":"Retry is only available for clusters in 'error' state (current: running)."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.RetryK8sClusterUpgrade(context.Background(), "k8s-uuid-1", "k")
	if err == nil {
		t.Fatal("expected error for 422 not-in-error-state, got nil")
	}
	if !contains(err.Error(), "only available for clusters in 'error' state") {
		t.Errorf("error = %v; want it to mention the error-state gate", err)
	}
}

// TestRetryK8sClusterUpgrade_EmptyID guards the empty-id path argument.
func TestRetryK8sClusterUpgrade_EmptyID(t *testing.T) {
	c := New("http://unused/api", "tok", time.Second, false)
	if _, err := c.RetryK8sClusterUpgrade(context.Background(), "", "k"); err == nil {
		t.Fatal("expected error for empty id")
	}
}
