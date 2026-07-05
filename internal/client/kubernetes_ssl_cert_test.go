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
// CreateKubernetesSslCert
// ---------------------------------------------------------------------------

// TestCreateKubernetesSslCert_Success verifies CreateKubernetesSslCert:
//   - POSTs to /kubernetes/cluster/{clusterID}/ssl-certificates (cluster id in
//     the path, PLURAL "ssl-certificates" segment, matching the LIST path)
//   - sends the prebuilt body verbatim
//   - attaches the supplied Idempotency-Key request header (idempotency.user)
//   - unwraps the "certificate" envelope, returning the object WITH its id.
func TestCreateKubernetesSslCert_Success(t *testing.T) {
	var gotMethod, gotPath, gotIdemKey string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotIdemKey = r.Header.Get("Idempotency-Key")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		// private_key is $hidden - NOT echoed back. "type" is ALSO absent here on
		// purpose: create() only populates attributes explicitly passed, so a
		// DB-defaulted column (type) is absent from the in-memory model until the
		// row is re-queried by the LIST endpoint.
		_, _ = w.Write([]byte(`{"success":true,"message":"Certificate added.","certificate":{"id":"cert-1","name":"api.example.com","domain":"api.example.com","certificate":"-----CERT-----"},"sync":{}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	body := map[string]any{
		"source":      "custom",
		"domain":      "api.example.com",
		"certificate": "-----CERT-----",
		"private_key": "-----KEY-----",
	}
	obj, err := c.CreateKubernetesSslCert(context.Background(), "cl-1", body, "idem-key-abc")
	if err != nil {
		t.Fatalf("CreateKubernetesSslCert returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/kubernetes/cluster/cl-1/ssl-certificates" {
		t.Errorf("path = %s; want /api/kubernetes/cluster/cl-1/ssl-certificates", gotPath)
	}
	if gotIdemKey != "idem-key-abc" {
		t.Errorf("Idempotency-Key header = %q; want %q", gotIdemKey, "idem-key-abc")
	}
	if gotBody["source"] != "custom" || gotBody["domain"] != "api.example.com" || gotBody["private_key"] != "-----KEY-----" {
		t.Errorf("body did not round-trip: %+v", gotBody)
	}
	if obj["id"] != "cert-1" {
		t.Errorf("returned id = %v; want cert-1", obj["id"])
	}
	if _, present := obj["private_key"]; present {
		t.Errorf("create response must NOT echo private_key (it is $hidden): %v", obj)
	}
}

// TestCreateKubernetesSslCert_GeneratesKeyWhenEmpty verifies that an empty
// idempotency key still sends a NON-empty Idempotency-Key header.
func TestCreateKubernetesSslCert_GeneratesKeyWhenEmpty(t *testing.T) {
	var gotIdemKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotIdemKey = r.Header.Get("Idempotency-Key")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"certificate":{"id":"c"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if _, err := c.CreateKubernetesSslCert(context.Background(), "cl-1", map[string]any{"source": "custom"}, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotIdemKey == "" {
		t.Error("expected a generated non-empty Idempotency-Key header when none supplied")
	}
}

// TestCreateKubernetesSslCert_ValidationError verifies a 422 (e.g.
// required_if:source,custom) surfaces as an error carrying the field message.
func TestCreateKubernetesSslCert_ValidationError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"errors":{"certificate":["The certificate field is required when source is custom."]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.CreateKubernetesSslCert(context.Background(), "cl-1", map[string]any{"source": "custom"}, "k")
	if err == nil {
		t.Fatal("expected error for 422 validation, got nil")
	}
	if !contains(err.Error(), "certificate field is required") {
		t.Errorf("error = %v; want it to mention the field error", err)
	}
}

// TestCreateKubernetesSslCert_EmptyClusterID guards the empty cluster-id path
// argument.
func TestCreateKubernetesSslCert_EmptyClusterID(t *testing.T) {
	c := New("http://unused/api", "tok", time.Second, false)
	if _, err := c.CreateKubernetesSslCert(context.Background(), "", map[string]any{"source": "custom"}, "k"); err == nil {
		t.Fatal("expected error for empty cluster id")
	}
}

// ---------------------------------------------------------------------------
// ListKubernetesSslCerts
// ---------------------------------------------------------------------------

// TestListKubernetesSslCerts_Success verifies the index unwraps the named
// "certs" key, which is a BARE ARRAY (not a Laravel paginator) - controller
// `index` returns `{"success":true,"certs":[...]}`.
func TestListKubernetesSslCerts_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"certs":[{"id":"a","name":"one","type":"manual"},{"id":"b","name":"two","type":"letsencrypt"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	list, err := c.ListKubernetesSslCerts(context.Background(), "cl-1")
	if err != nil {
		t.Fatalf("ListKubernetesSslCerts returned error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %s; want GET", gotMethod)
	}
	if gotPath != "/api/kubernetes/cluster/cl-1/ssl-certificates" {
		t.Errorf("path = %s; want /api/kubernetes/cluster/cl-1/ssl-certificates", gotPath)
	}
	if len(list) != 2 {
		t.Fatalf("len = %d; want 2", len(list))
	}
	if list[0]["id"] != "a" || list[1]["id"] != "b" {
		t.Errorf("unexpected list contents: %+v", list)
	}
	if _, present := list[0]["certificate"]; present {
		t.Errorf("cluster-scoped LIST must NOT contain certificate: %+v", list[0])
	}
}

// TestListKubernetesSslCerts_Empty verifies an empty cert set (e.g. the
// cluster has no CP LB yet) decodes to an empty slice, not an error.
func TestListKubernetesSslCerts_Empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"certs":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	list, err := c.ListKubernetesSslCerts(context.Background(), "cl-1")
	if err != nil {
		t.Fatalf("ListKubernetesSslCerts returned error: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("len = %d; want 0", len(list))
	}
}

// TestListKubernetesSslCerts_EmptyClusterID guards the path argument.
func TestListKubernetesSslCerts_EmptyClusterID(t *testing.T) {
	c := New("http://unused/api", "tok", time.Second, false)
	if _, err := c.ListKubernetesSslCerts(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty cluster id")
	}
}

// ---------------------------------------------------------------------------
// GetKubernetesSslCert (list-and-match - no SHOW endpoint exists)
// ---------------------------------------------------------------------------

// TestGetKubernetesSslCert_Found verifies the read-by-scan over the cert list
// returns the matching cert. The user-API surface has NO per-cert SHOW route.
func TestGetKubernetesSslCert_Found(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"certs":[{"id":"a","name":"one"},{"id":"b","name":"two","domain":"api.example.com"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.GetKubernetesSslCert(context.Background(), "cl-1", "b")
	if err != nil {
		t.Fatalf("GetKubernetesSslCert returned error: %v", err)
	}
	if obj["id"] != "b" || obj["name"] != "two" {
		t.Errorf("got %+v; want cert b/two", obj)
	}
}

// TestGetKubernetesSslCert_NotFound verifies a cert id absent from the list
// surfaces as an IsNotFound error (so Read removes it from state).
func TestGetKubernetesSslCert_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"certs":[{"id":"a","name":"one"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.GetKubernetesSslCert(context.Background(), "cl-1", "missing")
	if err == nil {
		t.Fatal("expected error for absent cert, got nil")
	}
	if !IsNotFound(err) {
		t.Errorf("expected IsNotFound, got %v", err)
	}
}

// TestGetKubernetesSslCert_EmptyIDs guards both path arguments.
func TestGetKubernetesSslCert_EmptyIDs(t *testing.T) {
	c := New("http://unused/api", "tok", time.Second, false)
	if _, err := c.GetKubernetesSslCert(context.Background(), "", "c"); err == nil {
		t.Fatal("expected error for empty cluster id")
	}
	if _, err := c.GetKubernetesSslCert(context.Background(), "cl-1", ""); err == nil {
		t.Fatal("expected error for empty certificate id")
	}
}

// ---------------------------------------------------------------------------
// DeleteKubernetesSslCert
// ---------------------------------------------------------------------------

// TestDeleteKubernetesSslCert_Success verifies the delete route is a DELETE to
// the SINGULAR "ssl-certificate" path and carries the Idempotency-Key header.
func TestDeleteKubernetesSslCert_Success(t *testing.T) {
	var gotMethod, gotPath, gotIdemKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotIdemKey = r.Header.Get("Idempotency-Key")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Certificate deleted."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DeleteKubernetesSslCert(context.Background(), "cl-1", "cert-1", "idem-del"); err != nil {
		t.Fatalf("DeleteKubernetesSslCert returned error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s; want DELETE", gotMethod)
	}
	if gotPath != "/api/kubernetes/cluster/cl-1/ssl-certificate/cert-1" {
		t.Errorf("path = %s; want /api/kubernetes/cluster/cl-1/ssl-certificate/cert-1 (SINGULAR)", gotPath)
	}
	if gotIdemKey != "idem-del" {
		t.Errorf("Idempotency-Key = %q; want idem-del", gotIdemKey)
	}
}

// TestDeleteKubernetesSslCert_SuccessFalse verifies a 200 success:false maps to
// an error (C3).
func TestDeleteKubernetesSslCert_SuccessFalse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"cluster has no CP load balancer"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	err := c.DeleteKubernetesSslCert(context.Background(), "cl-1", "cert-1", "k")
	if err == nil {
		t.Fatal("expected error for 200 success:false")
	}
	if !contains(err.Error(), "no CP load balancer") {
		t.Errorf("error = %v; want it to mention the message", err)
	}
}

// TestDeleteKubernetesSslCert_GeneratesKeyWhenEmpty verifies an empty
// idempotency key still sends a non-empty header.
func TestDeleteKubernetesSslCert_GeneratesKeyWhenEmpty(t *testing.T) {
	var gotIdemKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotIdemKey = r.Header.Get("Idempotency-Key")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DeleteKubernetesSslCert(context.Background(), "cl-1", "cert-1", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotIdemKey == "" {
		t.Error("expected a generated non-empty Idempotency-Key header when none supplied")
	}
}

// TestDeleteKubernetesSslCert_EmptyIDs guards the path arguments.
func TestDeleteKubernetesSslCert_EmptyIDs(t *testing.T) {
	c := New("http://unused/api", "tok", time.Second, false)
	if err := c.DeleteKubernetesSslCert(context.Background(), "", "c", "k"); err == nil {
		t.Fatal("expected error for empty cluster id")
	}
	if err := c.DeleteKubernetesSslCert(context.Background(), "cl-1", "", "k"); err == nil {
		t.Fatal("expected error for empty certificate id")
	}
}
