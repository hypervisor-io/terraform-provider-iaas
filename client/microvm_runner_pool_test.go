package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// NOTE: like the other client tests, this file uses net/http/httptest directly
// rather than internal/acctest.MockServer (see static_ip_test.go for why).

// poolPage renders a bare Laravel paginator page (the shape the MV3-23 index
// route returns: a top-level {data:[...]} object, no named key, no success
// envelope) for the given pool ids.
func poolPage(currentPage, lastPage int, ids ...string) []byte {
	items := make([]string, 0, len(ids))
	for _, id := range ids {
		items = append(items, `{"id":"`+id+`","provider":"gitlab","hypervisor_group_id":"hg1","plan_id":"pl1"}`)
	}
	return []byte(fmt.Sprintf(`{"current_page":%d,"data":[%s],"last_page":%d,"per_page":25,"total":%d}`,
		currentPage, strings.Join(items, ","), lastPage, len(ids)))
}

// ---------------------------------------------------------------------------
// ListRunnerPools
// ---------------------------------------------------------------------------

// TestListRunnerPools_PaginatesAllPages verifies the bare server-side
// paginate() response is unwrapped AND paged through: a pool living on page 2
// must be returned (the read path filters this listing client-side, so a
// page-1-only fetch would silently 404 pools past the first page).
func TestListRunnerPools_PaginatesAllPages(t *testing.T) {
	var gotURIs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURIs = append(gotURIs, r.RequestURI)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if r.URL.Query().Get("page") == "2" {
			_, _ = w.Write(poolPage(2, 2, "pool-3", "pool-4"))
			return
		}
		_, _ = w.Write(poolPage(1, 2, "pool-1", "pool-2"))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	items, err := c.ListRunnerPools(context.Background())
	if err != nil {
		t.Fatalf("ListRunnerPools returned error: %v", err)
	}
	if len(gotURIs) != 2 {
		t.Fatalf("requests = %v; want exactly 2 (page 1 + page 2)", gotURIs)
	}
	if gotURIs[0] != "/api/microvm/runners/pools" {
		t.Errorf("first request = %s; want /api/microvm/runners/pools", gotURIs[0])
	}
	if gotURIs[1] != "/api/microvm/runners/pools?page=2" {
		t.Errorf("second request = %s; want /api/microvm/runners/pools?page=2", gotURIs[1])
	}
	if len(items) != 4 {
		t.Fatalf("len(items) = %d; want 4 (both pages accumulated)", len(items))
	}
	want := []string{"pool-1", "pool-2", "pool-3", "pool-4"}
	for i, id := range want {
		if items[i]["id"] != id {
			t.Errorf("items[%d][id] = %v; want %s", i, items[i]["id"], id)
		}
	}
}

// ---------------------------------------------------------------------------
// GetRunnerPool
// ---------------------------------------------------------------------------

// TestGetRunnerPool_FoundOnLaterPage verifies the list-and-filter read path
// (READ-PATH GAP: there is no pool-show route) reaches pools past the first
// page and returns the matching pool object.
func TestGetRunnerPool_FoundOnLaterPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if r.URL.Query().Get("page") == "2" {
			_, _ = w.Write(poolPage(2, 2, "pool-3"))
			return
		}
		_, _ = w.Write(poolPage(1, 2, "pool-1", "pool-2"))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	pool, err := c.GetRunnerPool(context.Background(), "pool-3")
	if err != nil {
		t.Fatalf("GetRunnerPool returned error: %v", err)
	}
	if pool["id"] != "pool-3" {
		t.Errorf("pool[id] = %v; want pool-3", pool["id"])
	}
	if pool["hypervisor_group_id"] != "hg1" {
		t.Errorf("pool[hypervisor_group_id] = %v; want hg1", pool["hypervisor_group_id"])
	}
}

// TestGetRunnerPool_NotFoundIs404 verifies an id absent from every page
// surfaces as a 404 *APIError so the resource layer's client.IsNotFound(err)
// removes the pool from state (matching the SHOW-endpoint resources).
func TestGetRunnerPool_NotFoundIs404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(poolPage(1, 1, "pool-1"))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.GetRunnerPool(context.Background(), "pool-missing")
	if err == nil {
		t.Fatal("GetRunnerPool: expected error for a missing id, got nil")
	}
	if !IsNotFound(err) {
		t.Errorf("error = %v; want a 404 *APIError (IsNotFound must be true)", err)
	}
}

// TestGetRunnerPool_EmptyID verifies the empty-id guard fires BEFORE any
// HTTP request is attempted.
func TestGetRunnerPool_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	_, err := c.GetRunnerPool(context.Background(), "")
	if err == nil {
		t.Fatal("GetRunnerPool: expected error for empty id, got nil")
	}
	if !strings.Contains(err.Error(), "empty id") {
		t.Errorf("error = %q; want the empty-id guard error (no request should be attempted)", err.Error())
	}
}

// ---------------------------------------------------------------------------
// CreateGitlabRunnerPool
// ---------------------------------------------------------------------------

// TestCreateGitlabRunnerPool_Success verifies POST
// /microvm/runners/pools/gitlab sends the body verbatim and returns the BARE
// pool object (the route returns the model directly, no envelope).
func TestCreateGitlabRunnerPool_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"p1","provider":"gitlab","hypervisor_group_id":"hg1","plan_id":"pl1","max_concurrent":4,"enabled":true,"warm_count":2}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	pool, err := c.CreateGitlabRunnerPool(context.Background(), map[string]any{
		"hypervisor_group_id": "hg1",
		"plan_id":             "pl1",
		"gitlab_url":          "https://gitlab.example.com",
		"gitlab_token":        "glrt-secret",
		"warm_count":          2,
	})
	if err != nil {
		t.Fatalf("CreateGitlabRunnerPool returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/microvm/runners/pools/gitlab" {
		t.Errorf("path = %s; want /api/microvm/runners/pools/gitlab", gotPath)
	}
	if gotBody["gitlab_url"] != "https://gitlab.example.com" || gotBody["gitlab_token"] != "glrt-secret" {
		t.Errorf("body = %v; want the gitlab_url and gitlab_token to reach the API verbatim", gotBody)
	}
	if pool["id"] != "p1" {
		t.Errorf("pool[id] = %v; want p1 (bare pool object returned)", pool["id"])
	}
	if _, present := pool["gitlab_token"]; present {
		t.Errorf("pool must NOT echo gitlab_token ($hidden on the Master model); got %v", pool)
	}
}

// ---------------------------------------------------------------------------
// UpdateRunnerPool
// ---------------------------------------------------------------------------

// TestUpdateRunnerPool_ProviderInPath verifies the provider segment is part
// of the request path for both providers: PUT
// /microvm/runners/pools/{provider}/{id}. Dropping the segment (e.g. PUT
// /microvm/runners/pools/{id}) must fail this test - the Master only routes
// the provider-scoped paths.
func TestUpdateRunnerPool_ProviderInPath(t *testing.T) {
	for _, providerType := range []string{"gitlab", "github"} {
		t.Run(providerType, func(t *testing.T) {
			var gotMethod, gotPath string
			var gotBody map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				gotPath = r.URL.Path
				_ = json.NewDecoder(r.Body).Decode(&gotBody)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"id":"p1","provider":"` + providerType + `"}`))
			}))
			defer srv.Close()

			c := New(srv.URL+"/api", "tok", 10*time.Second, false)
			body := map[string]any{"hypervisor_group_id": "hg1", "plan_id": "pl1", "max_concurrent": 5}
			pool, err := c.UpdateRunnerPool(context.Background(), providerType, "p1", body)
			if err != nil {
				t.Fatalf("UpdateRunnerPool returned error: %v", err)
			}
			if gotMethod != http.MethodPut {
				t.Errorf("method = %s; want PUT", gotMethod)
			}
			want := "/api/microvm/runners/pools/" + providerType + "/p1"
			if gotPath != want {
				t.Errorf("path = %s; want %s", gotPath, want)
			}
			if gotBody["max_concurrent"] != float64(5) {
				t.Errorf("body[max_concurrent] = %v; want 5", gotBody["max_concurrent"])
			}
			if pool["provider"] != providerType {
				t.Errorf("pool[provider] = %v; want %s (bare updated pool object returned)", pool["provider"], providerType)
			}
		})
	}
}

// TestUpdateRunnerPool_RejectsUnknownProvider verifies a provider_type other
// than github/gitlab is rejected client-side before any request (the Master
// has no route for it).
func TestUpdateRunnerPool_RejectsUnknownProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("no request expected for an unknown provider, got %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.UpdateRunnerPool(context.Background(), "bitbucket", "p1", map[string]any{})
	if err == nil {
		t.Fatal("UpdateRunnerPool: expected error for provider_type bitbucket, got nil")
	}
	if !strings.Contains(err.Error(), "github or gitlab") {
		t.Errorf("error = %q; want the provider guard error", err.Error())
	}
}

// TestUpdateRunnerPool_PathEscapes verifies the id is url.PathEscape'd into
// the request path so it cannot break out of its segment.
func TestUpdateRunnerPool_PathEscapes(t *testing.T) {
	var gotURI string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURI = r.RequestURI
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"a/b"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if _, err := c.UpdateRunnerPool(context.Background(), "gitlab", "a/b", map[string]any{}); err != nil {
		t.Fatalf("UpdateRunnerPool returned error: %v", err)
	}
	if gotURI != "/api/microvm/runners/pools/gitlab/a%2Fb" {
		t.Errorf("RequestURI = %s; want /api/microvm/runners/pools/gitlab/a%%2Fb", gotURI)
	}
}

// TestUpdateRunnerPool_EmptyID verifies the empty-id guard fires before any
// HTTP request is attempted.
func TestUpdateRunnerPool_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	_, err := c.UpdateRunnerPool(context.Background(), "gitlab", "", map[string]any{})
	if err == nil {
		t.Fatal("UpdateRunnerPool: expected error for empty id, got nil")
	}
	if !strings.Contains(err.Error(), "empty id") {
		t.Errorf("error = %q; want the empty-id guard error (no request should be attempted)", err.Error())
	}
}

// ---------------------------------------------------------------------------
// DeleteRunnerPool
// ---------------------------------------------------------------------------

// TestDeleteRunnerPool_Success verifies DELETE /microvm/runners/pools/{id}
// with success:true.
func TestDeleteRunnerPool_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DeleteRunnerPool(context.Background(), "p1"); err != nil {
		t.Fatalf("DeleteRunnerPool returned error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s; want DELETE", gotMethod)
	}
	if gotPath != "/api/microvm/runners/pools/p1" {
		t.Errorf("path = %s; want /api/microvm/runners/pools/p1", gotPath)
	}
}

// TestDeleteRunnerPool_PathEscapes verifies the id is url.PathEscape'd into
// the request path.
func TestDeleteRunnerPool_PathEscapes(t *testing.T) {
	var gotURI string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURI = r.RequestURI
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DeleteRunnerPool(context.Background(), "a/b"); err != nil {
		t.Fatalf("DeleteRunnerPool returned error: %v", err)
	}
	if gotURI != "/api/microvm/runners/pools/a%2Fb" {
		t.Errorf("RequestURI = %s; want /api/microvm/runners/pools/a%%2Fb", gotURI)
	}
}

// TestDeleteRunnerPool_EmptyID verifies the empty-id guard fires before any
// HTTP request is attempted.
func TestDeleteRunnerPool_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	err := c.DeleteRunnerPool(context.Background(), "")
	if err == nil {
		t.Fatal("DeleteRunnerPool: expected error for empty id, got nil")
	}
	if !strings.Contains(err.Error(), "empty id") {
		t.Errorf("error = %q; want the empty-id guard error (no request should be attempted)", err.Error())
	}
}
