package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Like the other client tests, this file uses net/http/httptest directly
// (importing internal/acctest here would create an import cycle). The shared
// `contains` helper lives in ssh_key_test.go.

// ---------------------------------------------------------------------------
// CreateKubernetesClusterSgRule
// ---------------------------------------------------------------------------

// TestCreateKubernetesClusterSgRule_Success verifies
// CreateKubernetesClusterSgRule:
//   - POSTs to /kubernetes/cluster/{clusterID}/security-group/{scope}
//   - sends the prebuilt body verbatim
//   - attaches the supplied Idempotency-Key request header (idempotency.user)
//   - unwraps the "rule" envelope, returning the object WITH its id.
func TestCreateKubernetesClusterSgRule_Success(t *testing.T) {
	var gotMethod, gotPath, gotIdemKey string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotIdemKey = r.Header.Get("Idempotency-Key")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Rule added","rule":{"id":"rule-1","direction":"ingress","protocol":"tcp"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	body := map[string]any{
		"direction":      "ingress",
		"protocol":       "tcp",
		"port_range_min": int64(30000),
		"port_range_max": int64(32767),
		"ip_version":     "ipv4",
		"cidr":           "10.0.0.0/8",
	}
	obj, err := c.CreateKubernetesClusterSgRule(context.Background(), "cl-1", "worker", body, "idem-key-abc")
	if err != nil {
		t.Fatalf("CreateKubernetesClusterSgRule returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/kubernetes/cluster/cl-1/security-group/worker" {
		t.Errorf("path = %s; want /api/kubernetes/cluster/cl-1/security-group/worker", gotPath)
	}
	if gotIdemKey != "idem-key-abc" {
		t.Errorf("Idempotency-Key header = %q; want %q", gotIdemKey, "idem-key-abc")
	}
	if gotBody["direction"] != "ingress" || gotBody["protocol"] != "tcp" || gotBody["cidr"] != "10.0.0.0/8" {
		t.Errorf("body did not round-trip: %+v", gotBody)
	}
	if obj["id"] != "rule-1" {
		t.Errorf("returned id = %v; want rule-1", obj["id"])
	}
}

// TestCreateKubernetesClusterSgRule_GeneratesKeyWhenEmpty verifies that an
// empty idempotency key still sends a NON-empty Idempotency-Key header.
func TestCreateKubernetesClusterSgRule_GeneratesKeyWhenEmpty(t *testing.T) {
	var gotIdemKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotIdemKey = r.Header.Get("Idempotency-Key")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"rule":{"id":"r"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if _, err := c.CreateKubernetesClusterSgRule(context.Background(), "cl-1", "cp", map[string]any{"direction": "ingress"}, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotIdemKey == "" {
		t.Error("expected a generated non-empty Idempotency-Key header when none supplied")
	}
}

// TestCreateKubernetesClusterSgRule_ValidationError verifies a 422 (e.g.
// "security group not provisioned for this scope") surfaces as an error
// carrying the message.
func TestCreateKubernetesClusterSgRule_ValidationError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"success":false,"message":"invalid scope"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.CreateKubernetesClusterSgRule(context.Background(), "cl-1", "bogus", map[string]any{"direction": "ingress"}, "k")
	if err == nil {
		t.Fatal("expected error for 422 validation, got nil")
	}
	if !contains(err.Error(), "invalid scope") {
		t.Errorf("error = %v; want it to mention the message", err)
	}
}

// TestCreateKubernetesClusterSgRule_EmptyIDs guards both path arguments.
func TestCreateKubernetesClusterSgRule_EmptyIDs(t *testing.T) {
	c := New("http://unused/api", "tok", time.Second, false)
	if _, err := c.CreateKubernetesClusterSgRule(context.Background(), "", "cp", map[string]any{}, "k"); err == nil {
		t.Fatal("expected error for empty cluster id")
	}
	if _, err := c.CreateKubernetesClusterSgRule(context.Background(), "cl-1", "", map[string]any{}, "k"); err == nil {
		t.Fatal("expected error for empty scope")
	}
}

// ---------------------------------------------------------------------------
// ListKubernetesClusterSgRules / ListKubernetesClusterSgRulesEnvelope
// ---------------------------------------------------------------------------

// TestListKubernetesClusterSgRules_Success verifies the LIST unwraps the
// "rules" array and hits the (cluster,scope) path.
func TestListKubernetesClusterSgRules_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"rules":[{"id":"a"},{"id":"b"}],"security_group":{"id":"sg-1","name":"cluster-cp-x"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	list, err := c.ListKubernetesClusterSgRules(context.Background(), "cl-1", "cp")
	if err != nil {
		t.Fatalf("ListKubernetesClusterSgRules returned error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %s; want GET", gotMethod)
	}
	if gotPath != "/api/kubernetes/cluster/cl-1/security-group/cp" {
		t.Errorf("path = %s; want /api/kubernetes/cluster/cl-1/security-group/cp", gotPath)
	}
	if len(list) != 2 {
		t.Fatalf("len = %d; want 2", len(list))
	}
}

// TestListKubernetesClusterSgRules_UnprovisionedScope verifies a scope with no
// SG provisioned on the cluster decodes to an empty slice (not an error) -
// the controller returns 200 {rules:[],security_group:null} rather than 404.
func TestListKubernetesClusterSgRules_UnprovisionedScope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"rules":[],"security_group":null}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	list, err := c.ListKubernetesClusterSgRules(context.Background(), "cl-1", "worker")
	if err != nil {
		t.Fatalf("ListKubernetesClusterSgRules returned error: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("len = %d; want 0", len(list))
	}
}

// TestListKubernetesClusterSgRules_EmptyIDs guards the path arguments.
func TestListKubernetesClusterSgRules_EmptyIDs(t *testing.T) {
	c := New("http://unused/api", "tok", time.Second, false)
	if _, err := c.ListKubernetesClusterSgRules(context.Background(), "", "cp"); err == nil {
		t.Fatal("expected error for empty cluster id")
	}
	if _, err := c.ListKubernetesClusterSgRules(context.Background(), "cl-1", ""); err == nil {
		t.Fatal("expected error for empty scope")
	}
}

// ---------------------------------------------------------------------------
// GetKubernetesClusterSgRule (list-and-match - no SHOW endpoint exists)
// ---------------------------------------------------------------------------

// TestGetKubernetesClusterSgRule_Found verifies the read-by-scan over the rule
// list returns the matching rule, including its native security_group_id
// column (SecurityGroupRule is $guarded=[], so every column serialises).
func TestGetKubernetesClusterSgRule_Found(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"rules":[{"id":"a"},{"id":"b","security_group_id":"sg-1","protocol":"tcp"}],"security_group":{"id":"sg-1"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.GetKubernetesClusterSgRule(context.Background(), "cl-1", "cp", "b")
	if err != nil {
		t.Fatalf("GetKubernetesClusterSgRule returned error: %v", err)
	}
	if obj["id"] != "b" || obj["security_group_id"] != "sg-1" || obj["protocol"] != "tcp" {
		t.Errorf("got %+v; want rule b/sg-1/tcp", obj)
	}
}

// TestGetKubernetesClusterSgRule_NotFound verifies a rule id absent from the
// list surfaces as an IsNotFound error (so Read removes it from state).
func TestGetKubernetesClusterSgRule_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"rules":[{"id":"a"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.GetKubernetesClusterSgRule(context.Background(), "cl-1", "cp", "missing")
	if err == nil {
		t.Fatal("expected error for absent rule, got nil")
	}
	if !IsNotFound(err) {
		t.Errorf("expected IsNotFound, got %v", err)
	}
}

// TestGetKubernetesClusterSgRule_EmptyIDs guards every path argument.
func TestGetKubernetesClusterSgRule_EmptyIDs(t *testing.T) {
	c := New("http://unused/api", "tok", time.Second, false)
	if _, err := c.GetKubernetesClusterSgRule(context.Background(), "", "cp", "r"); err == nil {
		t.Fatal("expected error for empty cluster id")
	}
	if _, err := c.GetKubernetesClusterSgRule(context.Background(), "cl-1", "", "r"); err == nil {
		t.Fatal("expected error for empty scope")
	}
	if _, err := c.GetKubernetesClusterSgRule(context.Background(), "cl-1", "cp", ""); err == nil {
		t.Fatal("expected error for empty rule id")
	}
}

// ---------------------------------------------------------------------------
// DeleteKubernetesClusterSgRule
// ---------------------------------------------------------------------------

// TestDeleteKubernetesClusterSgRule_Success verifies the delete route and the
// Idempotency-Key header.
func TestDeleteKubernetesClusterSgRule_Success(t *testing.T) {
	var gotMethod, gotPath, gotIdemKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotIdemKey = r.Header.Get("Idempotency-Key")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Rule removed"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DeleteKubernetesClusterSgRule(context.Background(), "cl-1", "cp", "rule-1", "idem-del"); err != nil {
		t.Fatalf("DeleteKubernetesClusterSgRule returned error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s; want DELETE", gotMethod)
	}
	if gotPath != "/api/kubernetes/cluster/cl-1/security-group/cp/rule/rule-1" {
		t.Errorf("path = %s; want /api/kubernetes/cluster/cl-1/security-group/cp/rule/rule-1", gotPath)
	}
	if gotIdemKey != "idem-del" {
		t.Errorf("Idempotency-Key = %q; want idem-del", gotIdemKey)
	}
}

// TestDeleteKubernetesClusterSgRule_SuccessFalse verifies a 200 success:false
// (e.g. "rule does not belong to this scope") maps to an error (C3).
func TestDeleteKubernetesClusterSgRule_SuccessFalse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"rule does not belong to this scope"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	err := c.DeleteKubernetesClusterSgRule(context.Background(), "cl-1", "cp", "rule-1", "k")
	if err == nil {
		t.Fatal("expected error for 200 success:false")
	}
	if !contains(err.Error(), "does not belong to this scope") {
		t.Errorf("error = %v; want it to mention the message", err)
	}
}

// TestDeleteKubernetesClusterSgRule_GeneratesKeyWhenEmpty verifies an empty
// idempotency key still sends a non-empty header.
func TestDeleteKubernetesClusterSgRule_GeneratesKeyWhenEmpty(t *testing.T) {
	var gotIdemKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotIdemKey = r.Header.Get("Idempotency-Key")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DeleteKubernetesClusterSgRule(context.Background(), "cl-1", "cp", "rule-1", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotIdemKey == "" {
		t.Error("expected a generated non-empty Idempotency-Key header when none supplied")
	}
}

// TestDeleteKubernetesClusterSgRule_EmptyIDs guards every path argument.
func TestDeleteKubernetesClusterSgRule_EmptyIDs(t *testing.T) {
	c := New("http://unused/api", "tok", time.Second, false)
	if err := c.DeleteKubernetesClusterSgRule(context.Background(), "", "cp", "r", "k"); err == nil {
		t.Fatal("expected error for empty cluster id")
	}
	if err := c.DeleteKubernetesClusterSgRule(context.Background(), "cl-1", "", "r", "k"); err == nil {
		t.Fatal("expected error for empty scope")
	}
	if err := c.DeleteKubernetesClusterSgRule(context.Background(), "cl-1", "cp", "", "k"); err == nil {
		t.Fatal("expected error for empty rule id")
	}
}
