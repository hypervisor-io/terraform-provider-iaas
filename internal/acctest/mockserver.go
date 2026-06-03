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
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// RecordedRequest holds the details of a single HTTP request received by the
// MockServer. It is captured before the registered handler is invoked, so the
// handler always sees the original body via r.Body (which is restored).
type RecordedRequest struct {
	Method string
	// Path is the request URL path as received by the server, including the
	// "/api" prefix (e.g. "/api/ssh-keys"). Use [MockServer.Requests] with the
	// bare path (e.g. "/ssh-keys") for a convenient filtered view.
	Path   string
	Body   []byte
	Header http.Header
}

// MockServer is a test HTTP server that dispatches requests by "METHOD /path".
// It is safe for concurrent use.
type MockServer struct {
	*httptest.Server

	mu       sync.RWMutex
	handlers map[string]http.HandlerFunc
	recorded []RecordedRequest
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

// dispatch is the root handler. It reads and records the full request body,
// restores it so the matched handler can read it again, then dispatches to
// the registered handler. Unmatched (404) requests are also recorded.
func (m *MockServer) dispatch(w http.ResponseWriter, r *http.Request) {
	// Read the entire request body for capture.
	var bodyBytes []byte
	if r.Body != nil {
		var err error
		bodyBytes, err = io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "mock: failed to read request body", http.StatusInternalServerError)
			return
		}
		r.Body.Close()
	}

	// Restore the body so handlers that read r.Body still see the full content.
	r.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	// Record the request under the write lock.
	rec := RecordedRequest{
		Method: r.Method,
		Path:   r.URL.Path,
		Body:   bodyBytes,
		Header: r.Header.Clone(),
	}
	m.mu.Lock()
	m.recorded = append(m.recorded, rec)
	h, ok := m.handlers[r.Method+" "+r.URL.Path]
	m.mu.Unlock()

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

// Requests returns all recorded requests for the given method and bare path.
// The bare path follows the same convention as [MockServer.Handle]: it must
// begin with "/" and must NOT include the "/api" prefix (e.g. "/ssh-keys").
// Internally the "/api" prefix is applied before matching, so this mirrors the
// routing convention used by Handle.
// Thread-safe.
func (m *MockServer) Requests(method, path string) []RecordedRequest {
	fullPath := "/api" + path

	m.mu.RLock()
	defer m.mu.RUnlock()

	var out []RecordedRequest
	for _, r := range m.recorded {
		if r.Method == method && r.Path == fullPath {
			out = append(out, r)
		}
	}
	return out
}

// AllRequests returns every recorded request in the order received.
// Thread-safe.
func (m *MockServer) AllRequests() []RecordedRequest {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]RecordedRequest, len(m.recorded))
	copy(out, m.recorded)
	return out
}
