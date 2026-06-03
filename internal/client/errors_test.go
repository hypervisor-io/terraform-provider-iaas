package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// responseError — unit tests (no HTTP server needed)
// ---------------------------------------------------------------------------

// TestResponseError_422_FieldErrors verifies that a 422 response with the
// Laravel validation shape is parsed into FieldErrors.
func TestResponseError_422_FieldErrors(t *testing.T) {
	body := []byte(`{"message":"Validation failed","errors":{"name":["The name field is required."],"label":["Too short.","Must be unique."]}}`)
	resp := &http.Response{
		StatusCode: http.StatusUnprocessableEntity,
		Header:     http.Header{},
	}

	err := responseError(resp, body)
	if err == nil {
		t.Fatal("responseError: expected non-nil error for 422, got nil")
	}

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("responseError: expected *APIError; got %T", err)
	}

	if apiErr.Status != 422 {
		t.Errorf("Status = %d; want 422", apiErr.Status)
	}
	if apiErr.Message != "Validation failed" {
		t.Errorf("Message = %q; want %q", apiErr.Message, "Validation failed")
	}
	if len(apiErr.FieldErrors) == 0 {
		t.Fatal("FieldErrors is empty; expected populated map")
	}
	nameErrs, ok := apiErr.FieldErrors["name"]
	if !ok {
		t.Fatalf("FieldErrors[\"name\"] missing; got keys: %v", apiErr.FieldErrors)
	}
	if len(nameErrs) != 1 || nameErrs[0] != "The name field is required." {
		t.Errorf("FieldErrors[\"name\"] = %v; want [\"The name field is required.\"]", nameErrs)
	}
	labelErrs, ok := apiErr.FieldErrors["label"]
	if !ok {
		t.Fatal("FieldErrors[\"label\"] missing")
	}
	if len(labelErrs) != 2 {
		t.Errorf("FieldErrors[\"label\"] = %v; want 2 entries", labelErrs)
	}
}

// TestResponseError_401_Hint verifies that a 401 response produces an *APIError
// whose Error() string contains the IP-lock hint.
func TestResponseError_401_Hint(t *testing.T) {
	body := []byte(`{"message":"Unauthenticated."}`)
	resp := &http.Response{
		StatusCode: http.StatusUnauthorized,
		Header:     http.Header{"X-Request-Id": []string{"req-401"}},
	}

	err := responseError(resp, body)
	if err == nil {
		t.Fatal("responseError: expected non-nil error for 401, got nil")
	}

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("responseError: expected *APIError; got %T", err)
	}
	if apiErr.Status != 401 {
		t.Errorf("Status = %d; want 401", apiErr.Status)
	}
	if apiErr.RequestID != "req-401" {
		t.Errorf("RequestID = %q; want %q", apiErr.RequestID, "req-401")
	}

	// The Error() string must contain the IP-lock hint.
	errStr := apiErr.Error()
	if !strings.Contains(errStr, "IP") {
		t.Errorf("Error() = %q; expected IP-lock hint substring", errStr)
	}
}

// TestResponseError_403_Hint verifies the same hint for 403.
func TestResponseError_403_Hint(t *testing.T) {
	body := []byte(`{"message":"Forbidden."}`)
	resp := &http.Response{
		StatusCode: http.StatusForbidden,
		Header:     http.Header{},
	}

	err := responseError(resp, body)
	if err == nil {
		t.Fatal("responseError: expected non-nil error for 403, got nil")
	}

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError; got %T", err)
	}
	errStr := apiErr.Error()
	if !strings.Contains(errStr, "IP") {
		t.Errorf("Error() = %q; expected IP-lock hint substring", errStr)
	}
}

// TestResponseError_404_IsNotFound verifies that a 404 response satisfies IsNotFound.
func TestResponseError_404_IsNotFound(t *testing.T) {
	body := []byte(`{"message":"Not found."}`)
	resp := &http.Response{
		StatusCode: http.StatusNotFound,
		Header:     http.Header{},
	}

	err := responseError(resp, body)
	if err == nil {
		t.Fatal("responseError: expected non-nil error for 404, got nil")
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound = false; want true for 404 APIError (err: %v)", err)
	}
}

// TestResponseError_2xx_Nil verifies that 2xx responses return nil.
func TestResponseError_2xx_Nil(t *testing.T) {
	for _, code := range []int{200, 201, 204} {
		resp := &http.Response{StatusCode: code, Header: http.Header{}}
		if err := responseError(resp, []byte(`{}`)); err != nil {
			t.Errorf("responseError(%d) = %v; want nil", code, err)
		}
	}
}

// TestResponseError_RequestID verifies that X-Request-Id is captured in APIError.
func TestResponseError_RequestID(t *testing.T) {
	body := []byte(`{"message":"internal error"}`)
	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Header:     http.Header{"X-Request-Id": []string{"req-xyz"}},
	}

	err := responseError(resp, body)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError; got %T", err)
	}
	if apiErr.RequestID != "req-xyz" {
		t.Errorf("RequestID = %q; want %q", apiErr.RequestID, "req-xyz")
	}
	if !strings.Contains(apiErr.Error(), "req-xyz") {
		t.Errorf("Error() = %q; should mention request id", apiErr.Error())
	}
}

// TestResponseError_ErrorString_422 verifies the Error() string mentions field errors.
func TestResponseError_ErrorString_422(t *testing.T) {
	body := []byte(`{"message":"Validation failed","errors":{"name":["required"]}}`)
	resp := &http.Response{StatusCode: 422, Header: http.Header{}}

	err := responseError(resp, body)
	s := err.Error()
	if !strings.Contains(s, "422") {
		t.Errorf("Error() = %q; should contain status 422", s)
	}
	if !strings.Contains(s, "name") {
		t.Errorf("Error() = %q; should mention field 'name'", s)
	}
}

// ---------------------------------------------------------------------------
// IsNotFound helper
// ---------------------------------------------------------------------------

func TestIsNotFound_NonAPIError(t *testing.T) {
	if IsNotFound(fmt.Errorf("plain error")) {
		t.Error("IsNotFound(plain error) = true; want false")
	}
	if IsNotFound(nil) {
		t.Error("IsNotFound(nil) = true; want false")
	}
}

func TestIsNotFound_Non404APIError(t *testing.T) {
	apiErr := &APIError{Status: 500, Message: "oops"}
	if IsNotFound(apiErr) {
		t.Error("IsNotFound(500 APIError) = true; want false")
	}
}

// ---------------------------------------------------------------------------
// do — retry integration tests (via httptest)
// ---------------------------------------------------------------------------

// TestDo_429ThenOK verifies that do retries on 429 and returns the 200 response.
func TestDo_429ThenOK(t *testing.T) {
	var hits atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"message":"rate limited"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	c.retryBaseDelay = 1 * time.Millisecond // make test fast

	resp, body, err := c.do(context.Background(), http.MethodGet, "/test", nil)
	if err != nil {
		t.Fatalf("do returned error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d; want 200", resp.StatusCode)
	}
	if string(body) != `{"ok":true}` {
		t.Errorf("body = %q; want {\"ok\":true}", string(body))
	}
	if hits.Load() != 2 {
		t.Errorf("server hit %d times; want exactly 2", hits.Load())
	}
}

// TestDo_5xxThenOK verifies that do retries on 5xx and returns the 200 response.
func TestDo_5xxThenOK(t *testing.T) {
	var hits atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"server error"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	c.retryBaseDelay = 1 * time.Millisecond

	resp, _, err := c.do(context.Background(), http.MethodGet, "/test", nil)
	if err != nil {
		t.Fatalf("do returned error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d; want 200", resp.StatusCode)
	}
	if hits.Load() != 2 {
		t.Errorf("server hit %d times; want exactly 2", hits.Load())
	}
}

// TestDo_401_NotRetried verifies that do does NOT retry 401 responses.
func TestDo_401_NotRetried(t *testing.T) {
	var hits atomic.Int32

	body401 := []byte(`{"message":"Unauthenticated."}`)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write(body401)
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	c.retryBaseDelay = 1 * time.Millisecond

	resp, _, err := c.do(context.Background(), http.MethodGet, "/test", nil)
	if err != nil {
		t.Fatalf("do returned unexpected transport error: %v", err)
	}
	defer resp.Body.Close()

	if hits.Load() != 1 {
		t.Errorf("server hit %d times; want exactly 1 (no retry for 401)", hits.Load())
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("StatusCode = %d; want 401", resp.StatusCode)
	}
}

// TestDo_CtxCanceledDuringBackoff verifies that canceling the context during
// the retry backoff sleep causes do to return promptly with context.Canceled.
func TestDo_CtxCanceledDuringBackoff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Always return 429.
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"message":"rate limited"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	// Use a long base delay so the context cancel fires during the sleep.
	c.retryBaseDelay = 5 * time.Second

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		_, _, err := c.do(ctx, http.MethodGet, "/test", nil)
		errCh <- err
	}()

	// Give the first request time to land, then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("expected context.Canceled; got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("do did not return promptly after context cancel during backoff")
	}
}
