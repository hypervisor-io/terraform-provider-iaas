package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// NOTE: this test deliberately uses net/http/httptest directly rather than
// internal/acctest.MockServer. acctest imports internal/provider which imports
// internal/client, so importing acctest from a client test would create an
// import cycle.

// TestAssignResourceToProject_Assign verifies that AssignResourceToProject:
//   - POSTs to /project/assign-resource
//   - sends {resource_type, resource_id, project_id} with the real project id
//   - a bare {success,message} response (no object) is NOT an error.
func TestAssignResourceToProject_Assign(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Resource assigned to project"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	err := c.AssignResourceToProject(context.Background(), "instance", "inst-1", "proj-1")
	if err != nil {
		t.Fatalf("AssignResourceToProject returned error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/project/assign-resource" {
		t.Errorf("path = %s; want /api/project/assign-resource", gotPath)
	}
	if gotBody["resource_type"] != "instance" {
		t.Errorf("body[resource_type] = %v; want instance", gotBody["resource_type"])
	}
	if gotBody["resource_id"] != "inst-1" {
		t.Errorf("body[resource_id] = %v; want inst-1", gotBody["resource_id"])
	}
	if gotBody["project_id"] != "proj-1" {
		t.Errorf("body[project_id] = %v; want proj-1", gotBody["project_id"])
	}
}

// TestAssignResourceToProject_Unassign verifies that passing projectID == ""
// sends an explicit JSON null for project_id (the documented unassign path).
func TestAssignResourceToProject_Unassign(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Resource assigned to project"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.AssignResourceToProject(context.Background(), "instance", "inst-1", ""); err != nil {
		t.Fatalf("AssignResourceToProject (unassign) returned error: %v", err)
	}

	projectIDVal, present := gotBody["project_id"]
	if !present {
		t.Fatalf("body must include project_id key (as null) on unassign; body = %v", gotBody)
	}
	if projectIDVal != nil {
		t.Errorf("body[project_id] = %v; want JSON null on unassign", projectIDVal)
	}
}

// TestAssignResourceToProject_Failure verifies a 200 success:false response
// (e.g. resource not owned by the caller, or invalid project) surfaces the
// API message as an error (C3).
func TestAssignResourceToProject_Failure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"Invalid resource type"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	err := c.AssignResourceToProject(context.Background(), "instance", "inst-1", "proj-1")
	if err == nil {
		t.Fatal("AssignResourceToProject: expected error for success:false, got nil")
	}
	if !strings.Contains(err.Error(), "Invalid resource type") {
		t.Errorf("error = %v; want it to contain the API message", err)
	}
}

// TestAssignResourceToProject_EmptyIDs verifies the client-side guard against
// empty resource_type/resource_id (never sent to the server).
func TestAssignResourceToProject_EmptyIDs(t *testing.T) {
	c := New("http://example.invalid/api", "tok", 10*time.Second, false)

	if err := c.AssignResourceToProject(context.Background(), "", "inst-1", "proj-1"); err == nil {
		t.Error("expected error for empty resource_type, got nil")
	}
	if err := c.AssignResourceToProject(context.Background(), "instance", "", "proj-1"); err == nil {
		t.Error("expected error for empty resource_id, got nil")
	}
}

// TestGetResourceProjectID_Dispatch verifies GetResourceProjectID routes each
// resource_type to the correct SHOW endpoint and reads project_id back from
// that type's actual envelope shape - including s3_bucket's nested "bucket".
func TestGetResourceProjectID_Dispatch(t *testing.T) {
	tests := []struct {
		name         string
		resourceType string
		path         string
		response     string
		wantProject  string
	}{
		{
			name:         "instance bare model",
			resourceType: "instance",
			path:         "/api/instance/r1",
			response:     `{"id":"r1","name":"web-1","project_id":"proj-1"}`,
			wantProject:  "proj-1",
		},
		{
			name:         "vpc unwrapped envelope",
			resourceType: "vpc",
			path:         "/api/vpc/r1",
			response:     `{"success":true,"vpc":{"id":"r1","name":"vpc-1","project_id":"proj-2"}}`,
			wantProject:  "proj-2",
		},
		{
			name:         "load_balancer unwrapped envelope",
			resourceType: "load_balancer",
			path:         "/api/load-balancer/r1",
			response:     `{"success":true,"load_balancer":{"id":"r1","status":"active","project_id":"proj-3"}}`,
			wantProject:  "proj-3",
		},
		{
			name:         "s3_bucket nested under bucket",
			resourceType: "s3_bucket",
			path:         "/api/object-storage/bucket/r1",
			response:     `{"bucket":{"id":"r1","name":"bkt-1","project_id":"proj-4"},"access_key":"ak","secret_key":"sk"}`,
			wantProject:  "proj-4",
		},
		{
			name:         "managed_database unwrapped envelope",
			resourceType: "managed_database",
			path:         "/api/database/r1",
			response:     `{"success":true,"managed_database":{"id":"r1","engine":"postgres","project_id":"proj-5"}}`,
			wantProject:  "proj-5",
		},
		{
			name:         "no project assigned (null project_id)",
			resourceType: "instance",
			path:         "/api/instance/r1",
			response:     `{"id":"r1","name":"web-1","project_id":null}`,
			wantProject:  "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(tc.response))
			}))
			defer srv.Close()

			c := New(srv.URL+"/api", "tok", 10*time.Second, false)
			got, err := c.GetResourceProjectID(context.Background(), tc.resourceType, "r1")
			if err != nil {
				t.Fatalf("GetResourceProjectID returned error: %v", err)
			}
			if gotPath != tc.path {
				t.Errorf("path = %s; want %s", gotPath, tc.path)
			}
			if got != tc.wantProject {
				t.Errorf("project_id = %q; want %q", got, tc.wantProject)
			}
		})
	}
}

// TestGetResourceProjectID_UnsupportedType verifies an unknown resource_type
// is rejected client-side instead of dispatching an unpredictable request.
func TestGetResourceProjectID_UnsupportedType(t *testing.T) {
	c := New("http://example.invalid/api", "tok", 10*time.Second, false)
	_, err := c.GetResourceProjectID(context.Background(), "bogus", "r1")
	if err == nil {
		t.Fatal("expected error for unsupported resource_type, got nil")
	}
}

// TestGetResourceProjectID_NotFound verifies a 404 from the target resource's
// own SHOW propagates as an *APIError recognised by IsNotFound (the resource
// layer distinguishes "resource gone" from "resource exists but unassigned").
func TestGetResourceProjectID_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Instance not found"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.GetResourceProjectID(context.Background(), "instance", "gone")
	if err == nil {
		t.Fatal("expected an error for 404, got nil")
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound(err) = false; want true (err = %v)", err)
	}
}

// TestIsValidProjectResourceType verifies the exported validator matches
// exactly ProjectController's $modelMap keys.
func TestIsValidProjectResourceType(t *testing.T) {
	valid := []string{"instance", "vpc", "load_balancer", "s3_bucket", "managed_database"}
	for _, rt := range valid {
		if !IsValidProjectResourceType(rt) {
			t.Errorf("IsValidProjectResourceType(%q) = false; want true", rt)
		}
	}
	invalid := []string{"", "volume", "kubernetes_cluster", "INSTANCE"}
	for _, rt := range invalid {
		if IsValidProjectResourceType(rt) {
			t.Errorf("IsValidProjectResourceType(%q) = true; want false", rt)
		}
	}
}
