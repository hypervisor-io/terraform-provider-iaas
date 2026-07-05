package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// doItem - composes do + responseError + decodeItem.
// ---------------------------------------------------------------------------

// TestDoItem_UnwrapsKey verifies that a 200 response carrying a wrapped object
// is unwrapped under the requested key.
func TestDoItem_UnwrapsKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"ssh_key":{"id":"k1","name":"n"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.doItem(context.Background(), http.MethodGet, "/ssh-key/k1", nil, "ssh_key")
	if err != nil {
		t.Fatalf("doItem returned error: %v", err)
	}
	if obj["id"] != "k1" {
		t.Errorf("obj[id] = %v; want k1", obj["id"])
	}
	if obj["name"] != "n" {
		t.Errorf("obj[name] = %v; want n", obj["name"])
	}
}

// TestDoItem_200SuccessFalse_IsError verifies that a 200 response with
// success:false is mapped to an error carrying the API message (C3).
func TestDoItem_200SuccessFalse_IsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"bad key"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.doItem(context.Background(), http.MethodPost, "/ssh-keys", map[string]any{"name": "n"}, "ssh_key")
	if err == nil {
		t.Fatal("doItem: expected error for 200+success:false, got nil")
	}
	if !contains(err.Error(), "bad key") {
		t.Errorf("error = %q; want it to contain %q", err.Error(), "bad key")
	}
}

// TestDoItem_422_IsAPIError verifies that a 422 is surfaced as *APIError before
// any decode is attempted.
func TestDoItem_422_IsAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"Validation failed","errors":{"name":["required"]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.doItem(context.Background(), http.MethodPatch, "/ssh-key/k1", map[string]any{"name": ""}, "ssh_key")
	if err == nil {
		t.Fatal("doItem: expected error for 422, got nil")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError; got %T (%v)", err, err)
	}
	if apiErr.Status != 422 {
		t.Errorf("Status = %d; want 422", apiErr.Status)
	}
}

// TestDoItem_404_IsNotFound verifies a 404 maps to an *APIError recognised by IsNotFound.
func TestDoItem_404_IsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not found."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.doItem(context.Background(), http.MethodGet, "/ssh-key/missing", nil, "ssh_key")
	if err == nil {
		t.Fatal("doItem: expected error for 404, got nil")
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound = false; want true (err: %v)", err)
	}
}

// ---------------------------------------------------------------------------
// doList - composes do + responseError + decodeList.
// ---------------------------------------------------------------------------

// TestDoList_Paginator verifies that a Laravel paginator body yields the data slice.
func TestDoList_Paginator(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"current_page":1,"data":[{"id":"k1"},{"id":"k2"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	items, err := c.doList(context.Background(), http.MethodGet, "/ssh-keys", nil)
	if err != nil {
		t.Fatalf("doList returned error: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d; want 2", len(items))
	}
	if items[0]["id"] != "k1" || items[1]["id"] != "k2" {
		t.Errorf("items = %v; want ids k1,k2", items)
	}
}

// TestDoList_422_IsAPIError verifies a non-2xx on a list endpoint is an *APIError.
func TestDoList_422_IsAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"nope"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.doList(context.Background(), http.MethodGet, "/ssh-keys", nil)
	if err == nil {
		t.Fatal("doList: expected error for 422, got nil")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError; got %T", err)
	}
}

// ---------------------------------------------------------------------------
// doVoid - request expecting no object; maps non-2xx and 200+success:false.
// ---------------------------------------------------------------------------

// TestDoVoid_Success verifies a 200 success:true returns nil.
func TestDoVoid_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"deleted"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.doVoid(context.Background(), http.MethodDelete, "/ssh-keys/k1", nil); err != nil {
		t.Fatalf("doVoid returned error: %v", err)
	}
}

// TestDoVoid_200SuccessFalse_IsError verifies a 200 success:false is an error (C3).
func TestDoVoid_200SuccessFalse_IsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"nope"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	err := c.doVoid(context.Background(), http.MethodDelete, "/ssh-keys/k1", nil)
	if err == nil {
		t.Fatal("doVoid: expected error for 200+success:false, got nil")
	}
	if !contains(err.Error(), "nope") {
		t.Errorf("error = %q; want it to contain %q", err.Error(), "nope")
	}
}

// TestDoVoid_422_IsAPIError verifies a 422 maps to *APIError.
func TestDoVoid_422_IsAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"unprocessable"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	err := c.doVoid(context.Background(), http.MethodDelete, "/ssh-keys/k1", nil)
	if err == nil {
		t.Fatal("doVoid: expected error for 422, got nil")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError; got %T", err)
	}
}

// ---------------------------------------------------------------------------
// doList - auto-pagination of Laravel paginators.
// ---------------------------------------------------------------------------

// TestDoList_MultiPage verifies that doList fetches ALL pages of a Laravel
// paginator and returns items from every page in order.
// The mock server serves page 1 (items [{id:"a"}]) and page 2 (items [{id:"b"}]).
func TestDoList_MultiPage(t *testing.T) {
	var hits int32 // atomic counter of server requests

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		pageParam := r.URL.Query().Get("page")
		w.WriteHeader(http.StatusOK)
		if pageParam == "2" {
			_, _ = w.Write([]byte(`{"current_page":2,"last_page":2,"data":[{"id":"b"}]}`))
		} else {
			// page 1 (no param or param == "1")
			_, _ = w.Write([]byte(`{"current_page":1,"last_page":2,"data":[{"id":"a"}]}`))
		}
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	items, err := c.doList(context.Background(), http.MethodGet, "/things", nil)
	if err != nil {
		t.Fatalf("doList returned error: %v", err)
	}

	if len(items) != 2 {
		t.Fatalf("len(items) = %d; want 2", len(items))
	}
	if items[0]["id"] != "a" {
		t.Errorf("items[0][id] = %v; want a", items[0]["id"])
	}
	if items[1]["id"] != "b" {
		t.Errorf("items[1][id] = %v; want b", items[1]["id"])
	}

	// Exactly 2 server hits: one for page 1, one for page 2.
	if n := atomic.LoadInt32(&hits); n != 2 {
		t.Errorf("server was hit %d times; want exactly 2", n)
	}
}

// TestDoList_SinglePagePaginator verifies that a paginator with last_page==1
// results in exactly ONE server request (no spurious page-2 fetch).
func TestDoList_SinglePagePaginator(t *testing.T) {
	var hits int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"current_page":1,"last_page":1,"data":[{"id":"only"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	items, err := c.doList(context.Background(), http.MethodGet, "/things", nil)
	if err != nil {
		t.Fatalf("doList returned error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d; want 1", len(items))
	}
	if items[0]["id"] != "only" {
		t.Errorf("items[0][id] = %v; want only", items[0]["id"])
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Errorf("server was hit %d times; want exactly 1 (no extra page fetch)", n)
	}
}

// TestDoList_TopLevelArray_Unchanged verifies that a top-level JSON array
// response (non-paginator) is returned as-is in a single fetch.
func TestDoList_TopLevelArray_Unchanged(t *testing.T) {
	var hits int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[{"id":"x"},{"id":"y"}]`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	items, err := c.doList(context.Background(), http.MethodGet, "/things", nil)
	if err != nil {
		t.Fatalf("doList returned error: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d; want 2", len(items))
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Errorf("server was hit %d times; want exactly 1 (array = no pagination)", n)
	}
}

// TestDoList_MultiPage_PreserveQueryString verifies that when the initial path
// carries an existing query string (e.g. ?search=x), the page-2 request retains
// that param alongside the injected page=2.
func TestDoList_MultiPage_PreserveQueryString(t *testing.T) {
	var page2RawQuery string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		w.WriteHeader(http.StatusOK)
		if q.Get("page") == "2" {
			page2RawQuery = r.URL.RawQuery
			_, _ = w.Write([]byte(`{"current_page":2,"last_page":2,"data":[{"id":"b"}]}`))
		} else {
			_, _ = w.Write([]byte(`{"current_page":1,"last_page":2,"data":[{"id":"a"}]}`))
		}
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	items, err := c.doList(context.Background(), http.MethodGet, "/things?search=x", nil)
	if err != nil {
		t.Fatalf("doList returned error: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d; want 2", len(items))
	}

	// The page-2 request must carry both search=x and page=2.
	u, _ := parseQuery(page2RawQuery)
	if u["search"] != "x" {
		t.Errorf("page-2 request missing search=x; raw query was %q", page2RawQuery)
	}
	if u["page"] != "2" {
		t.Errorf("page-2 request missing page=2; raw query was %q", page2RawQuery)
	}
}

// parseQuery is a small local helper that decodes a raw query string into a
// flat map (first value per key only). It avoids importing net/url in the test
// file itself (we already have it in request.go).
func parseQuery(raw string) (map[string]string, error) {
	// Parse "key=val&key2=val2" manually to avoid import.
	m := make(map[string]string)
	if raw == "" {
		return m, nil
	}
	// net/url is already a transitive import in the test binary - use it.
	import_url_parse := func(s string) map[string]string {
		r := make(map[string]string)
		for _, pair := range splitAmpersand(s) {
			kv := splitEq(pair)
			if len(kv) == 2 && kv[0] != "" {
				r[kv[0]] = kv[1]
			}
		}
		return r
	}
	return import_url_parse(raw), nil
}

func splitAmpersand(s string) []string {
	var out []string
	start := 0
	for i, ch := range s {
		if ch == '&' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

func splitEq(s string) []string {
	for i, ch := range s {
		if ch == '=' {
			return []string{s[:i], s[i+1:]}
		}
	}
	return []string{s}
}
