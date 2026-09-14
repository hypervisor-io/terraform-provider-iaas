package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// NOTE: like the other client tests, this file uses net/http/httptest directly
// rather than internal/acctest.MockServer (see static_ip_test.go for why).

// ---------------------------------------------------------------------------
// Images
// ---------------------------------------------------------------------------

// TestListMicrovmImages_Success verifies GET /microvm/images unwraps the
// paginator nested under "images" and forwards the search/kind/status filters
// as query parameters.
func TestListMicrovmImages_Success(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"images":{"current_page":1,"last_page":1,"data":[{"id":"img-1","name":"ci-runner","status":"ready"}]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	items, err := c.ListMicrovmImages(context.Background(), "ci", "dockerfile", "ready")
	if err != nil {
		t.Fatalf("ListMicrovmImages returned error: %v", err)
	}
	if len(items) != 1 || items[0]["id"] != "img-1" {
		t.Fatalf("items = %v; want one row img-1", items)
	}
	if gotQuery.Get("search") != "ci" || gotQuery.Get("kind") != "dockerfile" || gotQuery.Get("status") != "ready" {
		t.Errorf("query = %v; want search=ci&kind=dockerfile&status=ready", gotQuery)
	}
}

// TestGetMicrovmImage_Success verifies SHOW returns the bare envelope with
// the image, its versions and microvms_count.
func TestGetMicrovmImage_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/microvm/image/img-1" {
			t.Errorf("path = %s; want /api/microvm/image/img-1", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"image":{"id":"img-1"},"versions":[{"id":"v-2","version":2}],"microvms_count":3}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	env, err := c.GetMicrovmImage(context.Background(), "img-1")
	if err != nil {
		t.Fatalf("GetMicrovmImage returned error: %v", err)
	}
	if env["microvms_count"] != float64(3) {
		t.Errorf("microvms_count = %v; want 3", env["microvms_count"])
	}
	if _, ok := env["versions"].([]any); !ok {
		t.Errorf("versions missing or not an array: %v", env)
	}
}

// TestBuildMicrovmImage_SendsBuilderLocation verifies BUILD posts
// hypervisor_group_id and unwraps the new version.
func TestBuildMicrovmImage_SendsBuilderLocation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["hypervisor_group_id"] != "hg-1" {
			t.Errorf("body = %v; want hypervisor_group_id hg-1", body)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"success":true,"version":{"id":"v-3","version":3,"status":"pending"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	version, err := c.BuildMicrovmImage(context.Background(), "img-1", "hg-1")
	if err != nil {
		t.Fatalf("BuildMicrovmImage returned error: %v", err)
	}
	if version["version"] != float64(3) {
		t.Errorf("version = %v; want 3", version["version"])
	}
}

// TestDeleteMicrovmImage_InUseConflict verifies a 409 surfaces as an
// *APIError, not a silent success.
func TestDeleteMicrovmImage_InUseConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"success":false,"message":"image_in_use"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	err := c.DeleteMicrovmImage(context.Background(), "img-1")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict {
		t.Fatalf("err = %v; want 409 *APIError", err)
	}
}

// ---------------------------------------------------------------------------
// MicroVMs
// ---------------------------------------------------------------------------

// TestListMicrovms_Filters verifies the state/image_id/connector_id filters
// reach the query string.
func TestListMicrovms_Filters(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"microvms":{"current_page":1,"last_page":1,"data":[{"id":"vm-1","state":"running"}]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	items, err := c.ListMicrovms(context.Background(), "", "running", "img-1", "con-1")
	if err != nil {
		t.Fatalf("ListMicrovms returned error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %v; want one row", items)
	}
	if gotQuery.Get("state") != "running" || gotQuery.Get("image_id") != "img-1" || gotQuery.Get("connector_id") != "con-1" {
		t.Errorf("query = %v; want state=running&image_id=img-1&connector_id=con-1", gotQuery)
	}
	if gotQuery.Has("search") {
		t.Errorf("empty search must be omitted, got %v", gotQuery)
	}
}

// TestMicrovmVerb_Envelope verifies the pause/resume/stop/start/kill verbs POST
// to /microvm/vm/{id}/<verb> and accept the bare {success} envelope.
func TestMicrovmVerb_Envelope(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.PauseMicrovm(context.Background(), "vm-1"); err != nil {
		t.Fatalf("PauseMicrovm returned error: %v", err)
	}
	if gotPath != "/api/microvm/vm/vm-1/pause" {
		t.Errorf("path = %s; want /api/microvm/vm/vm-1/pause", gotPath)
	}
}

// TestDeployMicrovm_SendsVersion verifies DEPLOY posts image_version_id and
// unwraps the new run.
func TestDeployMicrovm_SendsVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["image_version_id"] != "v-2" {
			t.Errorf("body = %v; want image_version_id v-2", body)
		}
		if !strings.HasSuffix(r.URL.Path, "/deploy") {
			t.Errorf("path = %s; want suffix /deploy", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"run":{"id":"run-1","status":"creating"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	run, err := c.DeployMicrovm(context.Background(), "vm-1", "v-2")
	if err != nil {
		t.Fatalf("DeployMicrovm returned error: %v", err)
	}
	if run["id"] != "run-1" {
		t.Errorf("run = %v; want run-1", run)
	}
}

// TestSetMicrovmEnv_ReturnsKeys verifies PUT env returns only key names.
func TestSetMicrovmEnv_ReturnsKeys(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if _, ok := body["env"].(map[string]any); !ok {
			t.Errorf("body = %v; want an env object", body)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"keys":["FOO","BAR"]}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	keys, err := c.SetMicrovmEnv(context.Background(), "vm-1", map[string]string{"FOO": "x", "BAR": "y"})
	if err != nil {
		t.Fatalf("SetMicrovmEnv returned error: %v", err)
	}
	if len(keys) != 2 || keys[0] != "FOO" {
		t.Errorf("keys = %v; want [FOO BAR]", keys)
	}
}

// TestGetMicrovmLogs_Limit verifies LOGS forwards ?limit= and unwraps "lines".
func TestGetMicrovmLogs_Limit(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"lines":[{"line":"booting"},{"line":"ready"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	lines, err := c.GetMicrovmLogs(context.Background(), "vm-1", 50)
	if err != nil {
		t.Fatalf("GetMicrovmLogs returned error: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("lines = %v; want 2", lines)
	}
	if gotQuery.Get("limit") != "50" {
		t.Errorf("limit = %v; want 50", gotQuery.Get("limit"))
	}
}

// ---------------------------------------------------------------------------
// Connectors
// ---------------------------------------------------------------------------

// TestListMicrovmConnectors_Envelope verifies INDEX returns the bare envelope
// carrying the paginator plus the create-context lists.
func TestListMicrovmConnectors_Envelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"connectors":{"current_page":1,"last_page":1,"data":[{"id":"con-1","kind":"gitlab_runner"}]},"git_sources":[],"github_app_configured":false}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	env, err := c.ListMicrovmConnectors(context.Background())
	if err != nil {
		t.Fatalf("ListMicrovmConnectors returned error: %v", err)
	}
	pag, ok := env["connectors"].(map[string]any)
	if !ok {
		t.Fatalf("connectors = %v; want a paginator object", env)
	}
	if data, _ := pag["data"].([]any); len(data) != 1 {
		t.Errorf("connectors.data = %v; want one row", pag["data"])
	}
	if env["github_app_configured"] != false {
		t.Errorf("github_app_configured = %v; want false", env["github_app_configured"])
	}
}

// TestUpdateMicrovmConnector_NeedsPlan verifies a 422 needs_plan surfaces as
// an *APIError carrying the field error.
func TestUpdateMicrovmConnector_NeedsPlan(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"needs_plan","errors":{"enabled":["needs_plan"]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.UpdateMicrovmConnector(context.Background(), "con-1", map[string]any{"enabled": true})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnprocessableEntity {
		t.Fatalf("err = %v; want 422 *APIError", err)
	}
}

// TestListMicrovmConnectorJobs_Paginator verifies JOBS unwraps the paginator
// nested under "jobs".
func TestListMicrovmConnectorJobs_Paginator(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/microvm/connectors/jobs" {
			t.Errorf("path = %s; want /api/microvm/connectors/jobs", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"jobs":{"current_page":1,"last_page":1,"data":[{"id":"job-1","status":"completed"}]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	jobs, err := c.ListMicrovmConnectorJobs(context.Background())
	if err != nil {
		t.Fatalf("ListMicrovmConnectorJobs returned error: %v", err)
	}
	if len(jobs) != 1 || jobs[0]["id"] != "job-1" {
		t.Fatalf("jobs = %v; want one row job-1", jobs)
	}
}
