package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// NOTE: This test file uses net/http/httptest directly rather than
// internal/acctest.MockServer. acctest imports internal/provider which imports
// internal/client, so importing acctest from a client test would create an
// import cycle.

// projectObject builds a minimal project API object for test responses.
func projectObject(id, name, description, color string) map[string]any {
	obj := map[string]any{
		"id":   id,
		"name": name,
	}
	if description != "" {
		obj["description"] = description
	} else {
		obj["description"] = nil
	}
	if color != "" {
		obj["color"] = color
	} else {
		obj["color"] = nil
	}
	return obj
}

// TestListProjects_Success verifies that ListProjects:
//   - GETs /projects (plural)
//   - unwraps the Laravel paginator ({data:[...]}) into a flat []map[string]any.
func TestListProjects_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusOK)
		resp, _ := json.Marshal(map[string]any{
			"success": true,
			"data": []any{
				projectObject("p1", "First", "desc", "#3B82F6"),
				projectObject("p2", "Second", "", ""),
			},
		})
		_, _ = w.Write(resp)
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	projects, err := c.ListProjects(context.Background())
	if err != nil {
		t.Fatalf("ListProjects returned error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %s; want GET", gotMethod)
	}
	if gotPath != "/api/projects" {
		t.Errorf("path = %s; want /api/projects (plural)", gotPath)
	}
	if len(projects) != 2 {
		t.Fatalf("len(projects) = %d; want 2", len(projects))
	}
	if projects[0]["id"] != "p1" || projects[1]["id"] != "p2" {
		t.Errorf("ids = %v, %v; want p1, p2", projects[0]["id"], projects[1]["id"])
	}
}

// TestCreateProject_Success verifies that CreateProject:
//   - POSTs to /projects (plural)
//   - sends name + optional description and color
//   - returns the unwrapped project object.
func TestCreateProject_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		resp, _ := json.Marshal(map[string]any{
			"success": true,
			"message": "Project created successfully",
			"project": projectObject("p1", "MyProject", "Prod infra", "#3B82F6"),
		})
		_, _ = w.Write(resp)
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	body := map[string]any{
		"name":        "MyProject",
		"description": "Prod infra",
		"color":       "#3B82F6",
	}
	obj, err := c.CreateProject(context.Background(), body)
	if err != nil {
		t.Fatalf("CreateProject returned error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/projects" {
		t.Errorf("path = %s; want /api/projects (plural)", gotPath)
	}
	if obj["id"] != "p1" {
		t.Errorf("obj[id] = %v; want p1", obj["id"])
	}
	if obj["name"] != "MyProject" {
		t.Errorf("obj[name] = %v; want MyProject", obj["name"])
	}
	if gotBody["name"] != "MyProject" {
		t.Errorf("body[name] = %v; want MyProject", gotBody["name"])
	}
	if gotBody["description"] != "Prod infra" {
		t.Errorf("body[description] = %v; want 'Prod infra'", gotBody["description"])
	}
}

// TestCreateProject_SuccessMinimal verifies CreateProject works with name-only body.
func TestCreateProject_SuccessMinimal(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		resp, _ := json.Marshal(map[string]any{
			"success": true,
			"message": "Project created successfully",
			"project": projectObject("p2", "Minimal", "", ""),
		})
		_, _ = w.Write(resp)
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.CreateProject(context.Background(), map[string]any{"name": "Minimal"})
	if err != nil {
		t.Fatalf("CreateProject returned error: %v", err)
	}
	if obj["id"] != "p2" {
		t.Errorf("obj[id] = %v; want p2", obj["id"])
	}
	// Only name was sent.
	if gotBody["name"] != "Minimal" {
		t.Errorf("body[name] = %v; want Minimal", gotBody["name"])
	}
}

// TestCreateProject_Failure verifies that a 200 success:false response surfaces
// the API message as an error (C3).
func TestCreateProject_Failure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"name too long"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.CreateProject(context.Background(), map[string]any{"name": "x"})
	if err == nil {
		t.Fatal("CreateProject: expected error for success:false, got nil")
	}
	if !contains(err.Error(), "name too long") {
		t.Errorf("error = %q; want it to contain 'name too long'", err.Error())
	}
}

// TestGetProject_Success verifies GET /project/{id} (singular) unwraps project.
func TestGetProject_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		resp, _ := json.Marshal(map[string]any{
			"success": true,
			"project": projectObject("p1", "MyProject", "desc", "#3B82F6"),
			// SHOW also returns embedded resource lists — we ignore them.
			"instances": map[string]any{"data": []any{}},
			"vpcs":      map[string]any{"data": []any{}},
		})
		_, _ = w.Write(resp)
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.GetProject(context.Background(), "p1")
	if err != nil {
		t.Fatalf("GetProject returned error: %v", err)
	}
	if gotPath != "/api/project/p1" {
		t.Errorf("path = %s; want /api/project/p1 (singular)", gotPath)
	}
	if obj["id"] != "p1" {
		t.Errorf("obj[id] = %v; want p1", obj["id"])
	}
	if obj["name"] != "MyProject" {
		t.Errorf("obj[name] = %v; want MyProject", obj["name"])
	}
}

// TestGetProject_NotFound verifies a 404 is recognised by client.IsNotFound.
func TestGetProject_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not found."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.GetProject(context.Background(), "missing")
	if err == nil {
		t.Fatal("GetProject: expected error for 404, got nil")
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound = false; want true (err: %v)", err)
	}
}

// TestGetProject_EmptyID verifies an empty id returns an immediate error
// (empty-id guard) without making an HTTP request.
func TestGetProject_EmptyID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("unexpected HTTP request for empty id")
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.GetProject(context.Background(), "")
	if err == nil {
		t.Fatal("GetProject: expected error for empty id, got nil")
	}
}

// TestUpdateProject_Success verifies PATCH /project/{id} (singular) sends the
// supplied fields and unwraps the fresh project from the response.
func TestUpdateProject_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		resp, _ := json.Marshal(map[string]any{
			"success": true,
			"message": "Project updated successfully",
			"project": projectObject("p1", "Renamed", "new desc", "#F59E0B"),
		})
		_, _ = w.Write(resp)
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	fields := map[string]any{
		"name":        "Renamed",
		"description": "new desc",
		"color":       "#F59E0B",
	}
	obj, err := c.UpdateProject(context.Background(), "p1", fields)
	if err != nil {
		t.Fatalf("UpdateProject returned error: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %s; want PATCH", gotMethod)
	}
	if gotPath != "/api/project/p1" {
		t.Errorf("path = %s; want /api/project/p1 (singular)", gotPath)
	}
	if gotBody["name"] != "Renamed" {
		t.Errorf("body[name] = %v; want Renamed", gotBody["name"])
	}
	if obj["name"] != "Renamed" {
		t.Errorf("obj[name] = %v; want Renamed", obj["name"])
	}
}

// TestUpdateProject_Failure verifies a 422 surfaces as an error.
func TestUpdateProject_Failure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"success":false,"message":"name invalid"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.UpdateProject(context.Background(), "p1", map[string]any{"name": "INVALID!"})
	if err == nil {
		t.Fatal("UpdateProject: expected error for 422, got nil")
	}
}

// TestUpdateProject_EmptyID verifies an empty id returns an immediate error.
func TestUpdateProject_EmptyID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("unexpected HTTP request for empty id")
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.UpdateProject(context.Background(), "", map[string]any{"name": "x"})
	if err == nil {
		t.Fatal("UpdateProject: expected error for empty id, got nil")
	}
}

// TestDeleteProject_Success verifies DELETE /project/{id} (singular) with success:true.
func TestDeleteProject_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Project deleted successfully"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DeleteProject(context.Background(), "p1"); err != nil {
		t.Fatalf("DeleteProject returned error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s; want DELETE", gotMethod)
	}
	if gotPath != "/api/project/p1" {
		t.Errorf("path = %s; want /api/project/p1 (singular)", gotPath)
	}
}

// TestDeleteProject_Failure verifies a 200 success:false delete surfaces the
// message as an error (C3).
func TestDeleteProject_Failure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"cannot delete"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	err := c.DeleteProject(context.Background(), "p1")
	if err == nil {
		t.Fatal("DeleteProject: expected error for success:false, got nil")
	}
	if !contains(err.Error(), "cannot delete") {
		t.Errorf("error = %q; want it to contain 'cannot delete'", err.Error())
	}
}

// TestDeleteProject_EmptyID verifies an empty id returns an immediate error.
func TestDeleteProject_EmptyID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("unexpected HTTP request for empty id")
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	err := c.DeleteProject(context.Background(), "")
	if err == nil {
		t.Fatal("DeleteProject: expected error for empty id, got nil")
	}
}
