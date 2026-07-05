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
// CreateKubernetesNodePool
// ---------------------------------------------------------------------------

// TestCreateKubernetesNodePool_Success verifies CreateKubernetesNodePool:
//   - POSTs to /kubernetes/cluster/{clusterID}/pools (cluster id in the path,
//     PLURAL "pools" segment)
//   - sends the prebuilt body verbatim
//   - attaches the supplied Idempotency-Key request header (idempotency.user)
//   - unwraps the "pool" envelope, returning the object WITH its id.
//
// Create is effectively synchronous at the row level: the real NodePoolService
// inserts the pool row in a transaction and returns it (201 {pool:{id,...}});
// worker provisioning is dispatched fire-and-forget with NO per-pool status
// field, so there is nothing to poll.
func TestCreateKubernetesNodePool_Success(t *testing.T) {
	var gotMethod, gotPath, gotIdemKey string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotIdemKey = r.Header.Get("Idempotency-Key")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"pool":{"id":"pool-uuid-1","name":"gpu-pool","is_default":false,"target_count":2,"min_size":2,"max_size":5,"instance_plan_id":"ip-1"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	body := map[string]any{
		"name":             "gpu-pool",
		"instance_plan_id": "ip-1",
		"min_size":         2,
		"max_size":         5,
		"target_count":     2,
	}
	obj, err := c.CreateKubernetesNodePool(context.Background(), "cl-1", body, "idem-key-abc")
	if err != nil {
		t.Fatalf("CreateKubernetesNodePool returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/kubernetes/cluster/cl-1/pools" {
		t.Errorf("path = %s; want /api/kubernetes/cluster/cl-1/pools", gotPath)
	}
	if gotIdemKey != "idem-key-abc" {
		t.Errorf("Idempotency-Key header = %q; want %q", gotIdemKey, "idem-key-abc")
	}
	if gotBody["name"] != "gpu-pool" || gotBody["instance_plan_id"] != "ip-1" {
		t.Errorf("body did not round-trip: %+v", gotBody)
	}
	if obj["id"] != "pool-uuid-1" {
		t.Errorf("returned id = %v; want pool-uuid-1", obj["id"])
	}
}

// TestCreateKubernetesNodePool_GeneratesKeyWhenEmpty verifies that an empty
// idempotency key still sends a NON-empty Idempotency-Key header.
func TestCreateKubernetesNodePool_GeneratesKeyWhenEmpty(t *testing.T) {
	var gotIdemKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotIdemKey = r.Header.Get("Idempotency-Key")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"pool":{"id":"p"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if _, err := c.CreateKubernetesNodePool(context.Background(), "cl-1", map[string]any{"name": "x"}, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotIdemKey == "" {
		t.Error("expected a generated non-empty Idempotency-Key header when none supplied")
	}
}

// TestCreateKubernetesNodePool_ValidationError verifies an HTTP-422 error body
// surfaces as an error carrying the field message.
func TestCreateKubernetesNodePool_ValidationError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"errors":{"min_size":["min_size_gt_max_size"]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.CreateKubernetesNodePool(context.Background(), "cl-1", map[string]any{"name": "x"}, "k")
	if err == nil {
		t.Fatal("expected error for 422 validation, got nil")
	}
	if !contains(err.Error(), "min_size") {
		t.Errorf("error = %v; want it to mention the field error", err)
	}
}

// TestCreateKubernetesNodePool_EmptyClusterID guards the empty cluster-id path
// argument.
func TestCreateKubernetesNodePool_EmptyClusterID(t *testing.T) {
	c := New("http://unused/api", "tok", time.Second, false)
	if _, err := c.CreateKubernetesNodePool(context.Background(), "", map[string]any{"name": "x"}, "k"); err == nil {
		t.Fatal("expected error for empty cluster id")
	}
}

// ---------------------------------------------------------------------------
// ListKubernetesNodePools
// ---------------------------------------------------------------------------

// TestListKubernetesNodePools_Success verifies the index unwraps the named
// "pools" key, which is a BARE ARRAY (not a Laravel paginator) - controller
// `index` returns `{"pools":[...]}`.
func TestListKubernetesNodePools_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"pools":[{"id":"a","name":"default","is_default":true},{"id":"b","name":"gpu","is_default":false}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	list, err := c.ListKubernetesNodePools(context.Background(), "cl-1")
	if err != nil {
		t.Fatalf("ListKubernetesNodePools returned error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %s; want GET", gotMethod)
	}
	if gotPath != "/api/kubernetes/cluster/cl-1/pools" {
		t.Errorf("path = %s; want /api/kubernetes/cluster/cl-1/pools", gotPath)
	}
	if len(list) != 2 {
		t.Fatalf("len = %d; want 2", len(list))
	}
	if list[0]["id"] != "a" || list[1]["id"] != "b" {
		t.Errorf("unexpected list contents: %+v", list)
	}
}

// TestListKubernetesNodePools_Empty verifies an empty pool set decodes to an
// empty slice, not an error.
func TestListKubernetesNodePools_Empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"pools":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	list, err := c.ListKubernetesNodePools(context.Background(), "cl-1")
	if err != nil {
		t.Fatalf("ListKubernetesNodePools returned error: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("len = %d; want 0", len(list))
	}
}

// TestListKubernetesNodePools_EmptyClusterID guards the path argument.
func TestListKubernetesNodePools_EmptyClusterID(t *testing.T) {
	c := New("http://unused/api", "tok", time.Second, false)
	if _, err := c.ListKubernetesNodePools(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty cluster id")
	}
}

// ---------------------------------------------------------------------------
// GetKubernetesNodePool (list-and-match - no SHOW endpoint exists)
// ---------------------------------------------------------------------------

// TestGetKubernetesNodePool_Found verifies the read-by-scan over the pool list
// returns the matching pool. The user-API surface has NO per-pool SHOW route,
// so Get lists the cluster's pools and matches by id.
func TestGetKubernetesNodePool_Found(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"pools":[{"id":"a","name":"default"},{"id":"b","name":"gpu","target_count":3}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.GetKubernetesNodePool(context.Background(), "cl-1", "b")
	if err != nil {
		t.Fatalf("GetKubernetesNodePool returned error: %v", err)
	}
	if obj["id"] != "b" || obj["name"] != "gpu" {
		t.Errorf("got %+v; want pool b/gpu", obj)
	}
}

// TestGetKubernetesNodePool_NotFound verifies a pool id absent from the list
// surfaces as an IsNotFound error (so Read removes it from state).
func TestGetKubernetesNodePool_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"pools":[{"id":"a","name":"default"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.GetKubernetesNodePool(context.Background(), "cl-1", "missing")
	if err == nil {
		t.Fatal("expected error for absent pool, got nil")
	}
	if !IsNotFound(err) {
		t.Errorf("expected IsNotFound, got %v", err)
	}
}

// TestGetKubernetesNodePool_ClusterGone verifies that a 404 on the parent
// cluster (the LIST 404s) also surfaces as IsNotFound - the child is gone too.
func TestGetKubernetesNodePool_ClusterGone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"Cluster not found"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.GetKubernetesNodePool(context.Background(), "cl-1", "b")
	if err == nil {
		t.Fatal("expected error when cluster 404s, got nil")
	}
	if !IsNotFound(err) {
		t.Errorf("expected IsNotFound, got %v", err)
	}
}

// TestGetKubernetesNodePool_EmptyIDs guards both path arguments.
func TestGetKubernetesNodePool_EmptyIDs(t *testing.T) {
	c := New("http://unused/api", "tok", time.Second, false)
	if _, err := c.GetKubernetesNodePool(context.Background(), "", "p"); err == nil {
		t.Fatal("expected error for empty cluster id")
	}
	if _, err := c.GetKubernetesNodePool(context.Background(), "cl-1", ""); err == nil {
		t.Fatal("expected error for empty pool id")
	}
}

// ---------------------------------------------------------------------------
// UpdateKubernetesNodePool
// ---------------------------------------------------------------------------

// TestUpdateKubernetesNodePool_Success verifies the update route is a PATCH to
// the SINGULAR pool path, carries the Idempotency-Key header, and unwraps the
// "pool" envelope.
func TestUpdateKubernetesNodePool_Success(t *testing.T) {
	var gotMethod, gotPath, gotIdemKey string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotIdemKey = r.Header.Get("Idempotency-Key")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"pool":{"id":"b","name":"gpu","target_count":4,"min_size":4}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.UpdateKubernetesNodePool(context.Background(), "cl-1", "b", map[string]any{"target_count": 4, "min_size": 4}, "idem-upd")
	if err != nil {
		t.Fatalf("UpdateKubernetesNodePool returned error: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %s; want PATCH", gotMethod)
	}
	if gotPath != "/api/kubernetes/cluster/cl-1/pool/b" {
		t.Errorf("path = %s; want /api/kubernetes/cluster/cl-1/pool/b", gotPath)
	}
	if gotIdemKey != "idem-upd" {
		t.Errorf("Idempotency-Key = %q; want idem-upd", gotIdemKey)
	}
	if gotBody["target_count"] != float64(4) {
		t.Errorf("body target_count = %v; want 4", gotBody["target_count"])
	}
	if obj["target_count"] != float64(4) {
		t.Errorf("returned target_count = %v; want 4", obj["target_count"])
	}
}

// TestUpdateKubernetesNodePool_EmptyIDs guards the path arguments.
func TestUpdateKubernetesNodePool_EmptyIDs(t *testing.T) {
	c := New("http://unused/api", "tok", time.Second, false)
	if _, err := c.UpdateKubernetesNodePool(context.Background(), "", "p", map[string]any{}, "k"); err == nil {
		t.Fatal("expected error for empty cluster id")
	}
	if _, err := c.UpdateKubernetesNodePool(context.Background(), "cl-1", "", map[string]any{}, "k"); err == nil {
		t.Fatal("expected error for empty pool id")
	}
}

// ---------------------------------------------------------------------------
// DeleteKubernetesNodePool
// ---------------------------------------------------------------------------

// TestDeleteKubernetesNodePool_Success verifies the delete route is a DELETE to
// the SINGULAR pool path, carries the Idempotency-Key header, and passes force
// as a query param. The delete is async (returns {task_id,force}); an empty pool
// is soft-deleted with task_id=null.
func TestDeleteKubernetesNodePool_Success(t *testing.T) {
	var gotMethod, gotPath, gotIdemKey, gotForce string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotIdemKey = r.Header.Get("Idempotency-Key")
		gotForce = r.URL.Query().Get("force")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"task_id":"del-task-1","force":false}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DeleteKubernetesNodePool(context.Background(), "cl-1", "b", "idem-del"); err != nil {
		t.Fatalf("DeleteKubernetesNodePool returned error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s; want DELETE", gotMethod)
	}
	if gotPath != "/api/kubernetes/cluster/cl-1/pool/b" {
		t.Errorf("path = %s; want /api/kubernetes/cluster/cl-1/pool/b", gotPath)
	}
	if gotIdemKey != "idem-del" {
		t.Errorf("Idempotency-Key = %q; want idem-del", gotIdemKey)
	}
	if gotForce != "true" {
		t.Errorf("force query = %q; want true (synchronous drain so destroy completes inline)", gotForce)
	}
}

// TestDeleteKubernetesNodePool_Conflict verifies a 409 (op-locked /
// no-eligible-victims) surfaces as an error.
func TestDeleteKubernetesNodePool_Conflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"code":"op_locked","message":"Another operation is in flight on this cluster."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	err := c.DeleteKubernetesNodePool(context.Background(), "cl-1", "b", "k")
	if err == nil {
		t.Fatal("expected error for 409 op-locked")
	}
	if !contains(err.Error(), "in flight") {
		t.Errorf("error = %v; want it to mention the conflict message", err)
	}
}

// TestDeleteKubernetesNodePool_ValidationError verifies a 422 success:false
// (e.g. default_pool_protected) surfaces as an error.
func TestDeleteKubernetesNodePool_ValidationError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"errors":{"is_default":["default_pool_protected"]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	err := c.DeleteKubernetesNodePool(context.Background(), "cl-1", "b", "k")
	if err == nil {
		t.Fatal("expected error for 422 default_pool_protected")
	}
	if !contains(err.Error(), "default_pool_protected") {
		t.Errorf("error = %v; want it to mention default_pool_protected", err)
	}
}

// TestDeleteKubernetesNodePool_EmptyIDs guards the path arguments.
func TestDeleteKubernetesNodePool_EmptyIDs(t *testing.T) {
	c := New("http://unused/api", "tok", time.Second, false)
	if err := c.DeleteKubernetesNodePool(context.Background(), "", "p", "k"); err == nil {
		t.Fatal("expected error for empty cluster id")
	}
	if err := c.DeleteKubernetesNodePool(context.Background(), "cl-1", "", "k"); err == nil {
		t.Fatal("expected error for empty pool id")
	}
}
