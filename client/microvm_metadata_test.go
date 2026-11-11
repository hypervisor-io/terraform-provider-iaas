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

// SBX-SMALL: sandbox create carries an Idempotency-Key, metadata is merge-patched
// through PATCH /microvm/vm/{id}/metadata, and the list takes metadata[key]=value.

func TestCreateMicrovm_SendsAFreshIdempotencyKeyPerCall(t *testing.T) {
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"success":true,"microvm":{"id":"vm-1"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	for i := 0; i < 2; i++ {
		if _, err := c.CreateMicrovm(context.Background(), map[string]any{"name": "x"}); err != nil {
			t.Fatal(err)
		}
	}
	if len(keys) != 2 || keys[0] == "" || keys[1] == "" {
		t.Fatalf("every create must carry an Idempotency-Key, got %q", keys)
	}
	if keys[0] == keys[1] {
		t.Fatalf("two separate creates must not share a key: %q", keys)
	}
}

func TestCreateMicrovm_KeepsTheKeyAcrossTransportRetries(t *testing.T) {
	var keys []string
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		if calls == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"success":false,"message":"slow down"}`))
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"success":true,"microvm":{"id":"vm-1"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if _, err := c.CreateMicrovm(context.Background(), map[string]any{"name": "x"}); err != nil {
		t.Fatal(err)
	}
	if len(keys) < 2 || keys[0] == "" || keys[0] != keys[1] {
		t.Fatalf("a retried attempt must reuse the same key, got %q", keys)
	}
}

func TestUpdateMicrovmMetadata_PatchesTheMergeBody(t *testing.T) {
	var method, path string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		_, _ = w.Write([]byte(`{"success":true,"metadata":{"team":"pay"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	got, err := c.UpdateMicrovmMetadata(context.Background(), "vm-1", map[string]any{"team": "pay", "old": nil})
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPatch || path != "/api/microvm/vm/vm-1/metadata" {
		t.Fatalf("request = %s %s", method, path)
	}
	meta, _ := body["metadata"].(map[string]any)
	if meta["team"] != "pay" {
		t.Fatalf("body = %v", body)
	}
	if v, present := meta["old"]; !present || v != nil {
		t.Fatalf("a removal must be sent as an explicit null, body = %v", body)
	}
	if got["team"] != "pay" {
		t.Fatalf("returned map = %v", got)
	}
	if _, err := c.UpdateMicrovmMetadata(context.Background(), "", nil); err == nil {
		t.Fatal("empty id must be refused before any request")
	}
}

func TestListMicrovmsByMetadata_SendsBracketedFilters(t *testing.T) {
	var q map[string][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.Query()
		_, _ = w.Write([]byte(`{"success":true,"microvms":{"data":[],"last_page":1}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if _, err := c.ListMicrovmsByMetadata(context.Background(), "", "running", "", "", map[string]string{"team": "pay", "env": "prod"}); err != nil {
		t.Fatal(err)
	}
	if q["metadata[team]"][0] != "pay" || q["metadata[env]"][0] != "prod" || q["state"][0] != "running" {
		t.Fatalf("query = %v", q)
	}
}

func TestRemoveMicrovmTimeout_PostsNoLimit(t *testing.T) {
	var path string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.RemoveMicrovmTimeout(context.Background(), "vm-1"); err != nil {
		t.Fatal(err)
	}
	if path != "/api/microvm/vm/vm-1/timeout" || body["no_limit"] != true {
		t.Fatalf("path %s body %v", path, body)
	}
	if _, has := body["timeout"]; has {
		t.Fatal("no_limit and timeout are mutually exclusive")
	}
}

func TestBuildMicrovmImage_SendsAnIdempotencyKey(t *testing.T) {
	var key string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key = r.Header.Get("Idempotency-Key")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"success":true,"version":{"id":"v1"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if _, err := c.BuildMicrovmImage(context.Background(), "img-1", "grp-1"); err != nil {
		t.Fatal(err)
	}
	if key == "" {
		t.Fatal("a kit build must carry an Idempotency-Key")
	}
}

func TestCreateMicrovmImage_SendsAnIdempotencyKey(t *testing.T) {
	var key string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key = r.Header.Get("Idempotency-Key")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"success":true,"image":{"id":"img-1"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if _, err := c.CreateMicrovmImage(context.Background(), map[string]any{"name": "k"}); err != nil {
		t.Fatal(err)
	}
	if key == "" {
		t.Fatal("a kit create must carry an Idempotency-Key")
	}
}
