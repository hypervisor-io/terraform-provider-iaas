// Package acctest provides test helpers for the IaaS Terraform provider.
//
// # Mock server routing convention
//
// The IaaS client builds request URLs as:
//
//	baseURL + path
//
// where baseURL is the endpoint with any trailing slash trimmed (e.g.
// "http://127.0.0.1:PORT/api") and path begins with "/" (e.g. "/ssh-keys"),
// producing "http://127.0.0.1:PORT/api/ssh-keys".
//
// [MockServer.Endpoint] therefore returns the test server's base URL with the
// "/api" suffix appended (e.g. "http://127.0.0.1:PORT/api").
//
// [MockServer.Handle] (and [MockServer.HandleJSON]) accept the bare resource
// path WITHOUT the "/api" prefix (e.g. "/ssh-keys"). Internally the server
// prepends "/api" before storing the handler key so that the lookup against the
// actual request path — which includes "/api" — succeeds.
//
// Example:
//
//	srv := acctest.NewMockServer(t)
//	srv.HandleJSON("POST", "/ssh-keys", 201, `{"id":"k1"}`)
//	// Client calls: POST srv.Endpoint()+"/ssh-keys"
//	//   = POST http://127.0.0.1:PORT/api/ssh-keys  ← matched by the server.
package acctest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// MockServer is a test HTTP server that dispatches requests by "METHOD /path".
// It is safe for concurrent use.
type MockServer struct {
	*httptest.Server

	mu       sync.RWMutex
	handlers map[string]http.HandlerFunc
}

// NewMockServer starts a new test HTTP server and registers t.Cleanup to close
// it when the test finishes. The returned MockServer is ready to accept handler
// registrations via [MockServer.Handle] or [MockServer.HandleJSON].
func NewMockServer(t *testing.T) *MockServer {
	t.Helper()

	m := &MockServer{
		handlers: make(map[string]http.HandlerFunc),
	}

	m.Server = httptest.NewServer(http.HandlerFunc(m.dispatch))
	t.Cleanup(m.Server.Close)

	return m
}

// dispatch is the root handler. It builds the lookup key from the request
// method and path, finds a registered handler, and calls it. If no handler
// is found it responds with 404 and a JSON error.
func (m *MockServer) dispatch(w http.ResponseWriter, r *http.Request) {
	key := r.Method + " " + r.URL.Path

	m.mu.RLock()
	h, ok := m.handlers[key]
	m.mu.RUnlock()

	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"message": fmt.Sprintf("mock: no handler for %s %s", r.Method, r.URL.Path),
		})
		return
	}

	h(w, r)
}

// Handle registers a handler for the given method and bare resource path.
//
// path must begin with "/" and must NOT include the "/api" prefix — e.g.
// "/ssh-keys", not "/api/ssh-keys". The MockServer prepends "/api" internally
// so that lookup matches the actual request URL built by the provider client.
//
// Last registration wins (subsequent calls for the same method+path replace
// the previous handler).
func (m *MockServer) Handle(method, path string, h http.HandlerFunc) {
	key := method + " /api" + path

	m.mu.Lock()
	m.handlers[key] = h
	m.mu.Unlock()
}

// HandleJSON is a convenience wrapper around [MockServer.Handle] that responds
// with the given HTTP status code and a raw JSON body string.
//
// path follows the same convention as [MockServer.Handle]: bare resource path
// without the "/api" prefix (e.g. "/ssh-keys").
func (m *MockServer) HandleJSON(method, path string, status int, body string) {
	m.Handle(method, path, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
}

// Endpoint returns the base URL the provider client should use as its
// endpoint. It includes the "/api" suffix so that the client's URL construction
// (baseURL + resource path) produces paths the mock server dispatches correctly.
//
// Example: "http://127.0.0.1:54321/api"
func (m *MockServer) Endpoint() string {
	return m.Server.URL + "/api"
}
