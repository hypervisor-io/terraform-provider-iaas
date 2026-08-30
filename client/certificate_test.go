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
// rather than internal/acctest.MockServer (see static_ip_test.go for why).

// ---------------------------------------------------------------------------
// ListCertificates
// ---------------------------------------------------------------------------

// TestListCertificates_Success verifies GET /certificates unwraps the
// "certificates" key (a bare Eloquent collection, not a paginator).
func TestListCertificates_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"certificates":[{"id":"cert-uuid-1","name":"example","domain":"example.test","status":"active"},{"id":"cert-uuid-2","name":"other","domain":"other.test","status":"expiring"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	items, err := c.ListCertificates(context.Background())
	if err != nil {
		t.Fatalf("ListCertificates returned error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %s; want GET", gotMethod)
	}
	if gotPath != "/api/certificates" {
		t.Errorf("path = %s; want /api/certificates", gotPath)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d; want 2", len(items))
	}
	if items[0]["id"] != "cert-uuid-1" {
		t.Errorf("items[0][id] = %v; want cert-uuid-1", items[0]["id"])
	}
}

// ---------------------------------------------------------------------------
// GetCertificate
// ---------------------------------------------------------------------------

// TestGetCertificate_Success verifies GET /certificate/{id} unwraps the
// "certificate" key, including the nested usages[] array, and never carries
// the certificate/private_key/chain PEM bodies.
func TestGetCertificate_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"certificate":{"id":"cert-uuid-1","name":"example","domain":"example.test","san_domains":["www.example.test"],"type":"manual","status":"active","expires_at":"2027-01-01T00:00:00Z","fingerprint_sha256":"AA:BB:CC","usage_count":1,"usages":[{"load_balancer_id":"lb-uuid-1","load_balancer_name":"web-lb","port":443,"is_default":true}]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.GetCertificate(context.Background(), "cert-uuid-1")
	if err != nil {
		t.Fatalf("GetCertificate returned error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %s; want GET", gotMethod)
	}
	if gotPath != "/api/certificate/cert-uuid-1" {
		t.Errorf("path = %s; want /api/certificate/cert-uuid-1", gotPath)
	}
	if obj["id"] != "cert-uuid-1" {
		t.Errorf("obj[id] = %v; want cert-uuid-1", obj["id"])
	}
	usages, ok := obj["usages"].([]any)
	if !ok || len(usages) != 1 {
		t.Fatalf("obj[usages] = %v; want a 1-element array", obj["usages"])
	}
	for _, stray := range []string{"certificate", "private_key", "chain"} {
		if _, present := obj[stray]; present {
			t.Errorf("GetCertificate response must NOT include %q; got %v", stray, obj)
		}
	}
}

// TestGetCertificate_NotFound verifies a 404 response surfaces as an
// *APIError with IsNotFound = true.
func TestGetCertificate_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Certificate not found."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.GetCertificate(context.Background(), "missing-uuid")
	if err == nil {
		t.Fatal("GetCertificate: expected error for 404, got nil")
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound = false; want true (err: %v)", err)
	}
}

// TestGetCertificate_EmptyID verifies the empty-id guard.
func TestGetCertificate_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	_, err := c.GetCertificate(context.Background(), "")
	if err == nil {
		t.Fatal("GetCertificate: expected error for empty id, got nil")
	}
}

// ---------------------------------------------------------------------------
// CreateCertificate
// ---------------------------------------------------------------------------

// TestCreateCertificate_Success verifies POST /certificates sends the plan
// body and returns the created certificate with its id, WITHOUT echoing the
// PEM bodies back (the API never returns them).
func TestCreateCertificate_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Certificate uploaded successfully.","certificate":{"id":"cert-uuid-1","name":"example","domain":"example.test","san_domains":[],"type":"manual","status":"active","fingerprint_sha256":"AA:BB:CC"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	body := map[string]any{
		"name":        "example",
		"certificate": "-----BEGIN CERTIFICATE-----\nMIIB...\n-----END CERTIFICATE-----",
		"private_key": "-----BEGIN PRIVATE KEY-----\nMIIE...\n-----END PRIVATE KEY-----",
	}
	obj, err := c.CreateCertificate(context.Background(), body)
	if err != nil {
		t.Fatalf("CreateCertificate returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/certificates" {
		t.Errorf("path = %s; want /api/certificates", gotPath)
	}
	if obj["id"] != "cert-uuid-1" {
		t.Errorf("obj[id] = %v; want cert-uuid-1", obj["id"])
	}
	if gotBody["name"] != "example" {
		t.Errorf("body[name] = %v; want example", gotBody["name"])
	}
	if gotBody["private_key"] == nil {
		t.Errorf("body[private_key] missing; want the PEM key to be sent")
	}
}

// ---------------------------------------------------------------------------
// RequestLetsEncryptCertificate
// ---------------------------------------------------------------------------

// TestRequestLetsEncryptCertificate_Success verifies POST
// /certificates/letsencrypt sends domains + via_load_balancer_id and returns
// the (pending) certificate.
func TestRequestLetsEncryptCertificate_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"certificate":{"id":"cert-uuid-2","name":"le-cert","domain":"le.test","san_domains":["www.le.test"],"type":"letsencrypt","status":"pending","letsencrypt_status":"pending"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	body := map[string]any{
		"domains":              []string{"le.test", "www.le.test"},
		"via_load_balancer_id": "lb-uuid-1",
	}
	obj, err := c.RequestLetsEncryptCertificate(context.Background(), body)
	if err != nil {
		t.Fatalf("RequestLetsEncryptCertificate returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/certificates/letsencrypt" {
		t.Errorf("path = %s; want /api/certificates/letsencrypt", gotPath)
	}
	if obj["id"] != "cert-uuid-2" {
		t.Errorf("obj[id] = %v; want cert-uuid-2", obj["id"])
	}
	if gotBody["via_load_balancer_id"] != "lb-uuid-1" {
		t.Errorf("body[via_load_balancer_id] = %v; want lb-uuid-1", gotBody["via_load_balancer_id"])
	}
}

// ---------------------------------------------------------------------------
// RetryCertificate
// ---------------------------------------------------------------------------

// TestRetryCertificate_Success verifies POST /certificate/{id}/retry.
func TestRetryCertificate_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Retry queued."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.RetryCertificate(context.Background(), "cert-uuid-1"); err != nil {
		t.Fatalf("RetryCertificate returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/certificate/cert-uuid-1/retry" {
		t.Errorf("path = %s; want /api/certificate/cert-uuid-1/retry", gotPath)
	}
}

// TestRetryCertificate_EmptyID verifies the empty-id guard.
func TestRetryCertificate_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if err := c.RetryCertificate(context.Background(), ""); err == nil {
		t.Fatal("RetryCertificate: expected error for empty id, got nil")
	}
}

// ---------------------------------------------------------------------------
// DeleteCertificate
// ---------------------------------------------------------------------------

// TestDeleteCertificate_Success verifies DELETE /certificate/{id} with success:true.
func TestDeleteCertificate_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Certificate deleted."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DeleteCertificate(context.Background(), "cert-uuid-1"); err != nil {
		t.Fatalf("DeleteCertificate returned error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s; want DELETE", gotMethod)
	}
	if gotPath != "/api/certificate/cert-uuid-1" {
		t.Errorf("path = %s; want /api/certificate/cert-uuid-1", gotPath)
	}
}

// TestDeleteCertificate_InUseFailure verifies a 200 success:false response
// (e.g. the certificate is still attached to an LB frontend) is surfaced as
// an error (C3).
func TestDeleteCertificate_InUseFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"This certificate is in use by 1 load balancer frontend and cannot be deleted."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	err := c.DeleteCertificate(context.Background(), "cert-uuid-1")
	if err == nil {
		t.Fatal("DeleteCertificate: expected error for success:false, got nil")
	}
	if !contains(err.Error(), "in use") {
		t.Errorf("error = %q; want it to contain %q", err.Error(), "in use")
	}
}

// TestDeleteCertificate_EmptyID verifies the empty-id guard.
func TestDeleteCertificate_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if err := c.DeleteCertificate(context.Background(), ""); err == nil {
		t.Fatal("DeleteCertificate: expected error for empty id, got nil")
	}
}
