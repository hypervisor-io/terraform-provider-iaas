package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestDo_BearerAuthHeader verifies that every request carries
// Authorization: Bearer <token> and Accept: application/json.
func TestDo_BearerAuthHeader(t *testing.T) {
	const token = "test-token-abc123"

	var gotAuth, gotAccept string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
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

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &received)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"created":true}`))
	}))
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

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	// Endpoint has trailing slash - the constructor must strip it.
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
	c := New("https://example.com/api", "tok", 0, false)
	if c.httpClient.Timeout != defaultTimeout {
		t.Errorf("expected default timeout %v; got %v", defaultTimeout, c.httpClient.Timeout)
	}
}

// TestDo_InsecureSkipVerify verifies that insecure=true sets TLS skip on transport.
func TestDo_InsecureSkipVerify(t *testing.T) {
	c := New("https://example.com/api", "tok", 10*time.Second, true)
	tr, ok := c.httpClient.Transport.(*http.Transport)
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
	// Transport may be nil (default) or explicit - either way InsecureSkipVerify must be false.
	if tr, ok := c.httpClient.Transport.(*http.Transport); ok {
		if tr.TLSClientConfig != nil && tr.TLSClientConfig.InsecureSkipVerify {
			t.Error("InsecureSkipVerify must be false when insecure=false")
		}
	}
	// if Transport is nil, that's the safe default - pass.
}

// TestDo_RequestIDHeader verifies that the request-id from the response is accessible.
func TestDo_RequestIDHeader(t *testing.T) {
	const wantID = "req-abc-123"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", wantID)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
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

// TestDo_ReadAllError verifies that when the response body cannot be fully read
// (Content-Length larger than bytes actually written, connection closed mid-body),
// do returns a non-nil error AND a nil *http.Response (per fix 1: no half-closed
// response is returned to callers).
//
// Implementation: we hijack the connection inside the handler, write a response
// with Content-Length: 100 but only a few body bytes, then close the connection.
// This causes io.ReadAll to receive io.ErrUnexpectedEOF.
func TestDo_ReadAllError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Hijack the connection so we can write a deliberately malformed response.
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("server does not support hijacking")
			http.Error(w, "no hijack", http.StatusInternalServerError)
			return
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack failed: %v", err)
			return
		}
		defer conn.Close()

		// Write a valid HTTP/1.1 response header claiming 100 body bytes,
		// but only write 5 bytes then close - yielding io.ErrUnexpectedEOF.
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\n")
		_, _ = buf.WriteString("Content-Type: application/json\r\n")
		_, _ = buf.WriteString("Content-Length: 100\r\n")
		_, _ = buf.WriteString("\r\n")
		_, _ = buf.WriteString("short") // only 5 of the promised 100 bytes
		_ = buf.Flush()
		// conn.Close() via defer - server abruptly ends the response
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	resp, data, err := c.do(context.Background(), http.MethodGet, "/test", nil)

	if err == nil {
		t.Fatal("expected error from truncated response body, got nil")
	}
	if resp != nil {
		t.Errorf("expected nil *http.Response on ReadAll error, got non-nil (status %d)", resp.StatusCode)
	}
	if data != nil {
		t.Errorf("expected nil body bytes on ReadAll error, got %q", data)
	}
	// Confirm it wraps the underlying I/O error (io.ErrUnexpectedEOF or net read error).
	t.Logf("ReadAll-error path returned (as expected): %v", err)
}

// TestDo_ContextCancellation verifies that when the request context is cancelled,
// do returns an error that wraps context.Canceled (confirming %w wrapping in
// the c.httpClient.Do error path preserves the context error chain).
func TestDo_ContextCancellation(t *testing.T) {
	// Use a server that blocks until the connection is closed, giving us time
	// to cancel the context before the response arrives.
	ready := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(ready) // signal that the request has been received
		// Block until the client disconnects (context cancelled).
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)

	errCh := make(chan error, 1)
	go func() {
		_, _, err := c.do(ctx, http.MethodGet, "/test", nil)
		errCh <- err
	}()

	// Wait until the server has received the request, then cancel.
	select {
	case <-ready:
		cancel()
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("timed out waiting for server to receive request")
	}

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected error after context cancellation, got nil")
		}
		if !errors.Is(err, context.Canceled) {
			// Check for net.Error wrapping a context error - on some Go versions
			// the context error is wrapped inside a *url.Error which is itself
			// a net.Error. errors.Is walks the chain via %w so this handles it.
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				t.Errorf("got timeout error instead of context.Canceled: %v", err)
			} else {
				t.Errorf("errors.Is(err, context.Canceled) = false; err = %v", err)
			}
		}
		t.Logf("context cancellation returned (as expected): %v", err)
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("timed out waiting for do() to return after context cancel")
	}
}
