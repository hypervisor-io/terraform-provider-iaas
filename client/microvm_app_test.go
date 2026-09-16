package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// NOTE: like the other client tests, this file uses net/http/httptest directly
// rather than internal/acctest.MockServer (see static_ip_test.go for why).

// ---------------------------------------------------------------------------
// ListApps
// ---------------------------------------------------------------------------

// TestListApps_UnwrapsPaginatorUnderAppsKey verifies GET /microvm/apps unwraps
// the Laravel paginator nested under the "apps" key. The merged MV2-25
// AppsController@index returns paginate(25) under "apps", which serialises to
// {success,apps:{data:[...],current_page,...}} - NOT a bare {apps:[...]}
// collection, so the unwrap must go through the named-paginator shape.
func TestListApps_UnwrapsPaginatorUnderAppsKey(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"apps":{"current_page":1,"data":[{"id":"a1","slug":"demo"},{"id":"a2","slug":"demo2"}],"last_page":1,"per_page":25,"total":2}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	items, err := c.ListApps(context.Background())
	if err != nil {
		t.Fatalf("ListApps returned error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %s; want GET", gotMethod)
	}
	if gotPath != "/api/microvm/apps" {
		t.Errorf("path = %s; want /api/microvm/apps", gotPath)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d; want 2", len(items))
	}
	if items[0]["id"] != "a1" || items[1]["slug"] != "demo2" {
		t.Errorf("items = %v; want the two app objects from the paginator data array", items)
	}
}

// ---------------------------------------------------------------------------
// GetApp
// ---------------------------------------------------------------------------

// TestGetApp_UnwrapsAppObject verifies GET /microvm/app/{id} unwraps the "app"
// key; the sibling "revisions" array in the SHOW envelope is not part of the
// returned object.
func TestGetApp_UnwrapsAppObject(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"app":{"id":"a1","slug":"demo","state":"running","fqdn":"demo.apps.example.test"},"revisions":[{"id":"r1"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	app, err := c.GetApp(context.Background(), "a1")
	if err != nil {
		t.Fatalf("GetApp returned error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %s; want GET", gotMethod)
	}
	if gotPath != "/api/microvm/app/a1" {
		t.Errorf("path = %s; want /api/microvm/app/a1", gotPath)
	}
	if app["slug"] != "demo" || app["fqdn"] != "demo.apps.example.test" {
		t.Errorf("app = %v; want the unwrapped app object", app)
	}
	if _, present := app["revisions"]; present {
		t.Errorf("app must be the unwrapped app object without the sibling revisions array; got %v", app)
	}
}

// TestGetApp_EmptyID verifies the empty-id guard fires BEFORE any HTTP request
// is attempted: the error must be the guard's, not a transport error from
// dialing the unreachable host.
func TestGetApp_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	_, err := c.GetApp(context.Background(), "")
	if err == nil {
		t.Fatal("GetApp: expected error for empty id, got nil")
	}
	if !strings.Contains(err.Error(), "empty id") {
		t.Errorf("error = %q; want the empty-id guard error (no request should be attempted)", err.Error())
	}
}

// ---------------------------------------------------------------------------
// CreateApp
// ---------------------------------------------------------------------------

// TestCreateApp_PostsToMicrovmApps verifies CreateApp posts the body to
// POST /microvm/apps (wire path /api/microvm/apps: the client base URL ends
// in /api, exactly like the sandbox and api-key methods) and unwraps the
// "app" key from the response.
func TestCreateApp_PostsToMicrovmApps(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"app":{"id":"a1","slug":"demo"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	app, err := c.CreateApp(context.Background(), map[string]any{"slug": "demo"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/microvm/apps" {
		t.Errorf("path = %s; want /api/microvm/apps", gotPath)
	}
	if gotBody["slug"] != "demo" {
		t.Errorf("body[slug] = %v; want demo", gotBody["slug"])
	}
	if app["slug"] != "demo" {
		t.Fatalf("unexpected app: %v", app)
	}
}

// ---------------------------------------------------------------------------
// DeployAppRevision / RollbackAppRevision
// ---------------------------------------------------------------------------

// TestDeployAppRevision_PostsRevisionID verifies POST /microvm/app/{id}/deploy
// carries {revision_id}.
func TestDeployAppRevision_PostsRevisionID(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DeployAppRevision(context.Background(), "a1", "r1"); err != nil {
		t.Fatalf("DeployAppRevision returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/microvm/app/a1/deploy" {
		t.Errorf("path = %s; want /api/microvm/app/a1/deploy", gotPath)
	}
	if gotBody["revision_id"] != "r1" {
		t.Errorf("body[revision_id] = %v; want r1", gotBody["revision_id"])
	}
}

// TestRollbackAppRevision_PostsRevisionID verifies POST
// /microvm/app/{id}/rollback carries {revision_id}.
func TestRollbackAppRevision_PostsRevisionID(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.RollbackAppRevision(context.Background(), "a1", "r0"); err != nil {
		t.Fatalf("RollbackAppRevision returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/microvm/app/a1/rollback" {
		t.Errorf("path = %s; want /api/microvm/app/a1/rollback", gotPath)
	}
	if gotBody["revision_id"] != "r0" {
		t.Errorf("body[revision_id] = %v; want r0", gotBody["revision_id"])
	}
}

// ---------------------------------------------------------------------------
// StopApp / StartApp
// ---------------------------------------------------------------------------

// TestStopApp_PostsToStop verifies POST /microvm/app/{id}/stop with no body.
func TestStopApp_PostsToStop(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.StopApp(context.Background(), "a1"); err != nil {
		t.Fatalf("StopApp returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/microvm/app/a1/stop" {
		t.Errorf("path = %s; want /api/microvm/app/a1/stop", gotPath)
	}
}

// TestStartApp_PostsToStart verifies POST /microvm/app/{id}/start with no body.
func TestStartApp_PostsToStart(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.StartApp(context.Background(), "a1"); err != nil {
		t.Fatalf("StartApp returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/microvm/app/a1/start" {
		t.Errorf("path = %s; want /api/microvm/app/a1/start", gotPath)
	}
}

// ---------------------------------------------------------------------------
// SetAppEnv
// ---------------------------------------------------------------------------

// TestSetAppEnv_PutsEnvMap verifies PUT /microvm/app/{id}/env carries the env
// map under the "env" key (the merged MV2-25 SetEnvVarsRequest validates
// {env: {K: V, ...}}).
func TestSetAppEnv_PutsEnvMap(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.SetAppEnv(context.Background(), "a1", map[string]string{"DATABASE_URL": "postgres://db/app"}); err != nil {
		t.Fatalf("SetAppEnv returned error: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %s; want PUT", gotMethod)
	}
	if gotPath != "/api/microvm/app/a1/env" {
		t.Errorf("path = %s; want /api/microvm/app/a1/env", gotPath)
	}
	env, ok := gotBody["env"].(map[string]any)
	if !ok {
		t.Fatalf("body[env] = %v; want an object", gotBody["env"])
	}
	if env["DATABASE_URL"] != "postgres://db/app" {
		t.Errorf("body[env][DATABASE_URL] = %v; want postgres://db/app", env["DATABASE_URL"])
	}
}

// ---------------------------------------------------------------------------
// DeleteApp
// ---------------------------------------------------------------------------

// TestDeleteApp_Success verifies DELETE /microvm/app/{id} with success:true.
func TestDeleteApp_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"App deleted."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DeleteApp(context.Background(), "a1"); err != nil {
		t.Fatalf("DeleteApp returned error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s; want DELETE", gotMethod)
	}
	if gotPath != "/api/microvm/app/a1" {
		t.Errorf("path = %s; want /api/microvm/app/a1", gotPath)
	}
}

// TestDeleteApp_ServerErrorSurfaces verifies a 500 from the delete endpoint is
// surfaced as a *APIError (retryBaseDelay shrunk to keep the retry loop fast;
// the client retries 5xx and returns the final response as an error). A
// swallowed server error here would let Delete report success for an app that
// still exists.
func TestDeleteApp_ServerErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"Server is on fire."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	c.retryBaseDelay = 1 * time.Millisecond // make test fast

	err := c.DeleteApp(context.Background(), "a1")
	if err == nil {
		t.Fatal("DeleteApp: expected error for HTTP 500, got nil")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v (%T); want a *APIError", err, err)
	}
	if apiErr.Status != http.StatusInternalServerError {
		t.Errorf("status = %d; want 500", apiErr.Status)
	}
}

// TestDeleteApp_SuccessFalseAt200IsError verifies the C3 convention: a 200
// response carrying success:false (refusal) is surfaced as an error.
func TestDeleteApp_SuccessFalseAt200IsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"App has an active deployment."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	err := c.DeleteApp(context.Background(), "a1")
	if err == nil {
		t.Fatal("DeleteApp: expected error for success:false, got nil")
	}
	if !strings.Contains(err.Error(), "App has an active deployment.") {
		t.Errorf("error = %q; want the API refusal message", err.Error())
	}
}
