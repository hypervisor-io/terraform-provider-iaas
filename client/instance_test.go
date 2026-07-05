package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// NOTE: like the other client tests, this uses net/http/httptest directly
// rather than internal/acctest (which would create an import cycle).
//
// The instance resource is the GOLDEN ASYNC resource: create is TWO-PHASE
// (phase 1 records the row synchronously and returns the id; phase 2 deploys the
// OS asynchronously and returns a task_id), readiness converges via a task
// poller, and DELETE converges by polling SHOW until 404. These client methods
// are the thin transport layer the resource composes.

// TestCreateCSInstance_Success verifies that CreateCSInstance:
//   - POSTs to /cloud-service/instances
//   - sends the prebuilt body the caller supplied (location_id, plan_id, …)
//   - returns the unwrapped instance object WITH its id (read from "instance").
//
// Phase 1 is SYNCHRONOUS and HTTP 200: the controller returns the full instance
// model (with id) under key "instance". There is NO task in phase 1.
func TestCreateCSInstance_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK) // create returns 200, not 201
		_, _ = w.Write([]byte(`{"success":true,"message":"Instance record created","instance":{"id":"i1","display_name":"web-01","cpu_cores":2,"ram":2048}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	body := map[string]any{
		"location_id": "g1",
		"plan_id":     "p1",
	}
	obj, err := c.CreateCSInstance(context.Background(), body)
	if err != nil {
		t.Fatalf("CreateCSInstance returned error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/cloud-service/instances" {
		t.Errorf("path = %s; want /api/cloud-service/instances", gotPath)
	}
	if obj["id"] != "i1" {
		t.Errorf("obj[id] = %v; want i1", obj["id"])
	}
	if gotBody["location_id"] != "g1" {
		t.Errorf("body[location_id] = %v; want g1", gotBody["location_id"])
	}
	if gotBody["plan_id"] != "p1" {
		t.Errorf("body[plan_id] = %v; want p1", gotBody["plan_id"])
	}
}

// TestCreateCSInstance_Failure verifies a 200 success:false response surfaces the
// API message as an error (C3 - e.g. plan_id not offered at location_id).
func TestCreateCSInstance_Failure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"plan is not offered at this location"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.CreateCSInstance(context.Background(), map[string]any{"location_id": "g1", "plan_id": "p1"})
	if err == nil {
		t.Fatal("CreateCSInstance: expected error for success:false, got nil")
	}
	if !contains(err.Error(), "plan is not offered at this location") {
		t.Errorf("error = %q; want it to contain the API message", err.Error())
	}
}

// TestDeployInstance_Success verifies phase 2: POST /instance/{id}/deploy returns
// 200 with a TOP-LEVEL task_id (no nested object). DeployInstance must return the
// bare envelope so the caller can read task_id.
func TestDeployInstance_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Deploy queued","task_id":"t1"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	body := map[string]any{
		"image_id": "img1",
		"ssh_keys": []string{"k1"},
	}
	obj, err := c.DeployInstance(context.Background(), "i1", body)
	if err != nil {
		t.Fatalf("DeployInstance returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/instance/i1/deploy" {
		t.Errorf("path = %s; want /api/instance/i1/deploy", gotPath)
	}
	if obj["task_id"] != "t1" {
		t.Errorf("obj[task_id] = %v; want t1", obj["task_id"])
	}
	if gotBody["image_id"] != "img1" {
		t.Errorf("body[image_id] = %v; want img1", gotBody["image_id"])
	}
	// ssh_keys must travel as an array under the key "ssh_keys" (NOT ssh_key_id).
	keys, ok := gotBody["ssh_keys"].([]any)
	if !ok || len(keys) != 1 || keys[0] != "k1" {
		t.Errorf("body[ssh_keys] = %v; want [k1]", gotBody["ssh_keys"])
	}
}

// TestDeployInstance_Failure verifies a 200 success:false deploy surfaces the
// message as an error (e.g. image_id missing/invalid).
func TestDeployInstance_Failure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"image not found"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.DeployInstance(context.Background(), "i1", map[string]any{"image_id": "bad"})
	if err == nil {
		t.Fatal("DeployInstance: expected error for success:false, got nil")
	}
	if !contains(err.Error(), "image not found") {
		t.Errorf("error = %q; want it to contain the API message", err.Error())
	}
}

// TestGetInstance_Success verifies GET /instance/{id} returns the BARE instance
// model (no envelope, no success wrapper). GetInstance must return the top-level
// map unchanged.
func TestGetInstance_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"i1","display_name":"web-01","cpu_cores":2,"ram":2048,"status":1,"deployed":1,"vnc_password":"secret","primary_public_ip":{"ip":"1.2.3.4"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.GetInstance(context.Background(), "i1")
	if err != nil {
		t.Fatalf("GetInstance returned error: %v", err)
	}
	if gotPath != "/api/instance/i1" {
		t.Errorf("path = %s; want /api/instance/i1", gotPath)
	}
	if obj["id"] != "i1" {
		t.Errorf("obj[id] = %v; want i1", obj["id"])
	}
	if obj["vnc_password"] != "secret" {
		t.Errorf("obj[vnc_password] = %v; want secret", obj["vnc_password"])
	}
	// primary_public_ip is an appended nested object.
	ipObj, ok := obj["primary_public_ip"].(map[string]any)
	if !ok || ipObj["ip"] != "1.2.3.4" {
		t.Errorf("obj[primary_public_ip] = %v; want {ip:1.2.3.4}", obj["primary_public_ip"])
	}
}

// TestGetInstance_NotFound verifies a 404 is recognised by client.IsNotFound
// (used by both Read drift-handling and the DELETE convergence waiter).
func TestGetInstance_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not found."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.GetInstance(context.Background(), "missing")
	if err == nil {
		t.Fatal("GetInstance: expected error for 404, got nil")
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound = false; want true (err: %v)", err)
	}
}

// TestGetInstanceTask_Success verifies GET /instance/{id}/task/{taskId} unwraps
// the "task" object (carrying the authoritative status used by the create waiter).
func TestGetInstanceTask_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"logs":[{"message":"booting"}],"task":{"id":"t1","status":"completed","progress":100}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.GetInstanceTask(context.Background(), "i1", "t1")
	if err != nil {
		t.Fatalf("GetInstanceTask returned error: %v", err)
	}
	if gotPath != "/api/instance/i1/task/t1" {
		t.Errorf("path = %s; want /api/instance/i1/task/t1", gotPath)
	}
	if obj["status"] != "completed" {
		t.Errorf("obj[status] = %v; want completed", obj["status"])
	}
}

// TestDeleteCSInstance_Success verifies DELETE /cloud-service/instances/{id} with
// success:true returns no error (the slave finalizes the row asynchronously).
func TestDeleteCSInstance_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Instance deletion queued"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DeleteCSInstance(context.Background(), "i1"); err != nil {
		t.Fatalf("DeleteCSInstance returned error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s; want DELETE", gotMethod)
	}
	if gotPath != "/api/cloud-service/instances/i1" {
		t.Errorf("path = %s; want /api/cloud-service/instances/i1", gotPath)
	}
}

// TestDeleteCSInstance_Failure verifies a 200 success:false delete surfaces the
// message as an error (e.g. protection_enabled).
func TestDeleteCSInstance_Failure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"protection is enabled on this instance"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	err := c.DeleteCSInstance(context.Background(), "i1")
	if err == nil {
		t.Fatal("DeleteCSInstance: expected error for success:false, got nil")
	}
	if !contains(err.Error(), "protection is enabled") {
		t.Errorf("error = %q; want it to contain the API message", err.Error())
	}
}

// TestUpdateInstance_Success verifies PATCH /instance/{id} sends only the changed
// metadata fields and returns the refreshed object.
func TestUpdateInstance_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Instance updated","instance":{"id":"i1","display_name":"renamed"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.UpdateInstance(context.Background(), "i1", map[string]any{"display_name": "renamed"})
	if err != nil {
		t.Fatalf("UpdateInstance returned error: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %s; want PATCH", gotMethod)
	}
	if gotPath != "/api/instance/i1" {
		t.Errorf("path = %s; want /api/instance/i1", gotPath)
	}
	if gotBody["display_name"] != "renamed" {
		t.Errorf("body[display_name] = %v; want renamed", gotBody["display_name"])
	}
	if obj["display_name"] != "renamed" {
		t.Errorf("obj[display_name] = %v; want renamed", obj["display_name"])
	}
}
