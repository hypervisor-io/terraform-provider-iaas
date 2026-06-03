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

// testServer sets up an httptest server that captures the incoming request and
// writes a canned response. It returns the server and a pointer to the captured
// request (populated after the first call).
func newTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(handler)
}

// TestDo_BearerAuthHeader verifies that every request carries
// Authorization: Bearer <token> and Accept: application/json.
func TestDo_BearerAuthHeader(t *testing.T) {
	const token = "test-token-abc123"

	var gotAuth, gotAccept string

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	defer srv.Close()

	// endpoint = server URL + "/api", path starts with "/"
	c := New(srv.URL+"/api", token, 10*time.Second, false)

	resp, body, err := c.do(context.Background(), http.MethodGet, "/ssh-keys", nil)
	if err != nil {
		t.Fatalf("do returned error: %v", err)
	}
	defer resp.Body.Close()

	if gotAuth != "Bearer "+token {
		t.Errorf("Authorization header = %q; want %q", gotAuth, "Bearer "+token)
	}
	if gotAccept != "application/json" {
		t.Errorf("Accept header = %q; want %q", gotAccept, "application/json")
	}
	if string(body) != `{"ok":true}` {
		t.Errorf("body = %q; want %q", string(body), `{"ok":true}`)
	}
}

// TestDo_PostWithBody verifies that when a body is supplied:
//   - Content-Type: application/json is set
//   - The JSON is transmitted and round-trips correctly
//   - The response body bytes are returned
func TestDo_PostWithBody(t *testing.T) {
	const token = "token-xyz"

	type payload struct {
		Name string `json:"name"`
	}

	var received payload
	var gotContentType string

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &received)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"created":true}`))
	})
	defer srv.Close()

	c := New(srv.URL+"/api", token, 10*time.Second, false)

	reqBody := map[string]any{"name": "my-key"}
	resp, body, err := c.do(context.Background(), http.MethodPost, "/ssh-keys", reqBody)
	if err != nil {
		t.Fatalf("do returned error: %v", err)
	}
	defer resp.Body.Close()

	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q; want %q", gotContentType, "application/json")
	}
	if received.Name != "my-key" {
		t.Errorf("server received name = %q; want %q", received.Name, "my-key")
	}
	if string(body) != `{"created":true}` {
		t.Errorf("body = %q; want %q", string(body), `{"created":true}`)
	}
}

// TestDo_BaseURLJoining verifies the full URL construction:
// endpoint (with "/api" suffix) + path is called correctly.
func TestDo_BaseURLJoining(t *testing.T) {
	var gotPath string

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})
	defer srv.Close()

	// Endpoint has trailing slash — the constructor must strip it.
	c := New(srv.URL+"/api/", "tok", 10*time.Second, false)

	_, _, err := c.do(context.Background(), http.MethodGet, "/instances", nil)
	if err != nil {
		t.Fatalf("do returned error: %v", err)
	}

	if gotPath != "/api/instances" {
		t.Errorf("server saw path %q; want %q", gotPath, "/api/instances")
	}
}

// TestDo_DefaultTimeout verifies that passing zero timeout uses the default (non-zero).
func TestDo_DefaultTimeout(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 0, false)
	if c.http.Timeout == 0 {
		t.Error("expected non-zero default timeout when zero is passed")
	}
}

// TestDo_InsecureSkipVerify verifies that insecure=true sets TLS skip on transport.
func TestDo_InsecureSkipVerify(t *testing.T) {
	c := New("https://example.com/api", "tok", 10*time.Second, true)
	tr, ok := c.http.Transport.(*http.Transport)
	if !ok {
		t.Fatal("transport is not *http.Transport")
	}
	if tr.TLSClientConfig == nil || !tr.TLSClientConfig.InsecureSkipVerify {
		t.Error("expected InsecureSkipVerify=true when insecure=true")
	}
}

// TestDo_SecureDefaultTransport verifies that insecure=false does NOT set InsecureSkipVerify.
func TestDo_SecureDefaultTransport(t *testing.T) {
	c := New("https://example.com/api", "tok", 10*time.Second, false)
	// Transport may be nil (default) or explicit — either way InsecureSkipVerify must be false.
	if tr, ok := c.http.Transport.(*http.Transport); ok {
		if tr.TLSClientConfig != nil && tr.TLSClientConfig.InsecureSkipVerify {
			t.Error("InsecureSkipVerify must be false when insecure=false")
		}
	}
	// if Transport is nil, that's the safe default — pass.
}

// TestDo_RequestIDHeader verifies that the request-id from the response is accessible.
func TestDo_RequestIDHeader(t *testing.T) {
	const wantID = "req-abc-123"

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", wantID)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	resp, _, err := c.do(context.Background(), http.MethodGet, "/test", nil)
	if err != nil {
		t.Fatalf("do returned error: %v", err)
	}
	defer resp.Body.Close()

	gotID := resp.Header.Get("X-Request-Id")
	if gotID != wantID {
		t.Errorf("X-Request-Id = %q; want %q", gotID, wantID)
	}
}
