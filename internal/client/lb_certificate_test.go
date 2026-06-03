package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestCreateLBCertificate_Success verifies the POST path + envelope + body
// (name/certificate/private_key/chain).
func TestCreateLBCertificate_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		// private_key is $hidden — NOT echoed back.
		_, _ = w.Write([]byte(`{"success":true,"message":"Certificate added.","certificate":{"id":"ct-1","name":"my-cert","certificate":"-----CERT-----"},"sync":{}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.CreateLBCertificate(context.Background(), "lb-1", map[string]any{
		"name":        "my-cert",
		"certificate": "-----CERT-----",
		"private_key": "-----KEY-----",
	})
	if err != nil {
		t.Fatalf("CreateLBCertificate returned error: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/load-balancer/lb-1/certificates" {
		t.Errorf("method/path = %s %s; want POST .../certificates", gotMethod, gotPath)
	}
	if obj["id"] != "ct-1" {
		t.Errorf("obj[id] = %v; want ct-1", obj["id"])
	}
	if gotBody["name"] != "my-cert" || gotBody["private_key"] != "-----KEY-----" {
		t.Errorf("create body = %v; want name + private_key", gotBody)
	}
	// The SHOW (and create) response must not surface the private key.
	if _, present := obj["private_key"]; present {
		t.Errorf("create response must NOT echo private_key (it is $hidden): %v", obj)
	}
}

// TestCreateLBCertificate_SuccessFalse verifies a 200 success:false → error (C3).
func TestCreateLBCertificate_SuccessFalse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"Invalid certificate."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if _, err := c.CreateLBCertificate(context.Background(), "lb-1", map[string]any{"name": "x"}); err == nil {
		t.Fatal("expected error on 200 success:false, got nil")
	}
}

func TestDeleteLBCertificate_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Certificate deleted."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DeleteLBCertificate(context.Background(), "lb-1", "ct-1"); err != nil {
		t.Fatalf("DeleteLBCertificate returned error: %v", err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/api/load-balancer/lb-1/certificate/ct-1" {
		t.Errorf("method/path = %s %s; want DELETE .../certificate/ct-1", gotMethod, gotPath)
	}
}

func TestGetLBCertificate_ScanFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"load_balancer":{"id":"lb-1","certificates":[{"id":"ct-1","name":"my-cert","certificate":"-----CERT-----"},{"id":"ct-2","name":"other"}]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.GetLBCertificate(context.Background(), "lb-1", "ct-1")
	if err != nil {
		t.Fatalf("GetLBCertificate returned error: %v", err)
	}
	if obj["name"] != "my-cert" {
		t.Errorf("obj[name] = %v; want my-cert", obj["name"])
	}
	if _, present := obj["private_key"]; present {
		t.Errorf("SHOW must NOT contain private_key: %v", obj)
	}
}

func TestGetLBCertificate_ScanAbsent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"load_balancer":{"id":"lb-1","certificates":[]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if _, err := c.GetLBCertificate(context.Background(), "lb-1", "missing"); err == nil || !IsNotFound(err) {
		t.Errorf("expected IsNotFound for absent certificate, got %v", err)
	}
}

func TestLBCertificate_EmptyIDGuards(t *testing.T) {
	c := New("http://unused/api", "tok", time.Second, false)
	ctx := context.Background()
	if _, err := c.CreateLBCertificate(ctx, "", map[string]any{}); err == nil {
		t.Error("CreateLBCertificate: expected empty-lbID error")
	}
	if err := c.DeleteLBCertificate(ctx, "lb", ""); err == nil {
		t.Error("DeleteLBCertificate: expected empty-certificateID error")
	}
	if err := c.DeleteLBCertificate(ctx, "", "ct"); err == nil {
		t.Error("DeleteLBCertificate: expected empty-lbID error")
	}
	if _, err := c.GetLBCertificate(ctx, "lb", ""); err == nil {
		t.Error("GetLBCertificate: expected empty-certificateID error")
	}
}
