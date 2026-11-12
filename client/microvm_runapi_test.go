package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestMintMicrovmAccessRouteBodyAndAnswer(t *testing.T) {
	var method, path, body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		method, path, body = r.Method, r.URL.EscapedPath(), string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"base_url":"https://49983-abc.sbx.example.com/.virtconsole/run/v1","token":"v1.k2.eA.c2ln","token_type":"Bearer","expires_at":"2026-11-11T10:05:00+00:00","scopes":["exec"],"sandbox":{"id":"vm/1","state":"running"},"limits":{"concurrent_commands":16}}`))
	}))
	defer server.Close()
	c := New(server.URL+"/api", "token", 0, false)

	out, err := c.MintMicrovmAccess(context.Background(), "vm/1", MicrovmAccessRequest{Scopes: []string{"exec", "files.read"}, TTLSeconds: 120, AutoResume: true})
	if err != nil {
		t.Fatalf("MintMicrovmAccess: %v", err)
	}
	if method != "POST" || path != "/api/microvm/vm/vm%2F1/access" {
		t.Fatalf("request = %s %s", method, path)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(body), &sent); err != nil {
		t.Fatalf("body %q: %v", body, err)
	}
	want := map[string]any{"scopes": []any{"exec", "files.read"}, "ttl_seconds": float64(120), "auto_resume": true}
	if !reflect.DeepEqual(sent, want) {
		t.Fatalf("body = %v, want %v", sent, want)
	}
	if out["token"] != "v1.k2.eA.c2ln" || out["base_url"] != "https://49983-abc.sbx.example.com/.virtconsole/run/v1" {
		t.Fatalf("answer = %v", out)
	}

	// Defaults are left to the API: no ttl_seconds, no auto_resume key.
	if _, err := c.MintMicrovmAccess(context.Background(), "vm/1", MicrovmAccessRequest{Scopes: []string{"exec"}}); err != nil {
		t.Fatalf("defaults: %v", err)
	}
	if strings.Contains(body, "ttl_seconds") || strings.Contains(body, "auto_resume") {
		t.Fatalf("defaults sent anyway: %s", body)
	}
}

func TestMintMicrovmAccessRefusalsAndGuards(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "paused"):
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"success":false,"reason":"sandbox_paused","message":"the sandbox is paused"}`))
		case strings.Contains(r.URL.Path, "empty"):
			_, _ = w.Write([]byte(`{"success":true}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"success":false,"reason":"sandbox_not_found","message":"Sandbox not found."}`))
		}
	}))
	defer server.Close()
	c := New(server.URL+"/api", "token", 0, false)
	ctx := context.Background()

	var apiErr *APIError
	_, err := c.MintMicrovmAccess(ctx, "paused", MicrovmAccessRequest{Scopes: []string{"exec"}})
	if !errors.As(err, &apiErr) || apiErr.Status != 409 || apiErr.Reason != "sandbox_paused" {
		t.Fatalf("paused = %v", err)
	}
	_, err = c.MintMicrovmPreview(ctx, "gone", 3000, 0)
	if !errors.As(err, &apiErr) || apiErr.Status != 404 || apiErr.Reason != "sandbox_not_found" {
		t.Fatalf("missing = %v", err)
	}
	if _, err := c.MintMicrovmAccess(ctx, "empty", MicrovmAccessRequest{Scopes: []string{"exec"}}); err == nil {
		t.Fatalf("a 200 without a token must be an error")
	}
	if _, err := c.MintMicrovmPreview(ctx, "empty", 3000, 0); err == nil {
		t.Fatalf("a 200 without a url must be an error")
	}
	if _, err := c.MintMicrovmAccess(ctx, "", MicrovmAccessRequest{Scopes: []string{"exec"}}); err == nil {
		t.Fatalf("empty id")
	}
	if _, err := c.MintMicrovmAccess(ctx, "x", MicrovmAccessRequest{}); err == nil {
		t.Fatalf("no scopes")
	}
	if _, err := c.MintMicrovmPreview(ctx, "", 3000, 0); err == nil {
		t.Fatalf("empty id")
	}
}

func TestMintMicrovmPreviewRouteAndBody(t *testing.T) {
	var method, path, body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		method, path, body = r.Method, r.URL.EscapedPath(), string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"url":"https://3000-abc.sbx.example.com/?vc_preview=v1.k2.eA.c2ln","host":"3000-abc.sbx.example.com","port":3000,"expires_at":"2026-11-11T11:00:00+00:00","key_id":"k2"}`))
	}))
	defer server.Close()
	c := New(server.URL+"/api", "token", 0, false)

	out, err := c.MintMicrovmPreview(context.Background(), "vm-1", 3000, 600)
	if err != nil {
		t.Fatalf("MintMicrovmPreview: %v", err)
	}
	if method != "POST" || path != "/api/microvm/vm/vm-1/preview" {
		t.Fatalf("request = %s %s", method, path)
	}
	var sent map[string]any
	_ = json.Unmarshal([]byte(body), &sent)
	if !reflect.DeepEqual(sent, map[string]any{"port": float64(3000), "ttl_seconds": float64(600)}) {
		t.Fatalf("body = %s", body)
	}
	if out["host"] != "3000-abc.sbx.example.com" || out["key_id"] != "k2" {
		t.Fatalf("answer = %v", out)
	}
	if _, err := c.MintMicrovmPreview(context.Background(), "vm-1", 3000, 0); err != nil || strings.Contains(body, "ttl_seconds") {
		t.Fatalf("default ttl must be left to the API: %v %s", err, body)
	}
}
