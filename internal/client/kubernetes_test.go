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
// {success,cluster:{id,state:"created"},task_id} — the cluster comes up
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
