package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// doItem — composes do + responseError + decodeItem.
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
// doList — composes do + responseError + decodeList.
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
// doVoid — request expecting no object; maps non-2xx and 200+success:false.
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
