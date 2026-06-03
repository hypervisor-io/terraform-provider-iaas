package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// NOTE: like the other client tests, these use net/http/httptest directly rather
// than internal/acctest.MockServer (acctest imports internal/provider which
// imports internal/client → an import cycle).
//
// Autoscaling has two resources: the GROUP (collection PLURAL /scaling-groups,
// item ops SINGULAR /scaling-group/{id}) and the POLICY (child of the group;
// /scaling-group/{id}/policy[/{policyId}]). The create/update/pause/resume
// envelope key is "group"; the SHOW key is "scaling_group"; policies are embedded
// under scaling_group.policies[] (no individual policy SHOW).

// TestCreateAutoscalingGroup_Success verifies CreateAutoscalingGroup POSTs to the
// PLURAL /scaling-groups path, sends the prebuilt body, and unwraps the "group"
// envelope returning the object WITH its id.
func TestCreateAutoscalingGroup_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Autoscaling group created successfully.","group":{"id":"g1","name":"web-asg","status":"active","min_instances":1,"max_instances":5,"current_count":0}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	body := map[string]any{
		"name":                "web-asg",
		"hypervisor_group_id": "hg1",
		"plan_id":             "p1",
		"image_id":            "img1",
	}
	obj, err := c.CreateAutoscalingGroup(context.Background(), body)
	if err != nil {
		t.Fatalf("CreateAutoscalingGroup returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/scaling-groups" {
		t.Errorf("path = %s; want /api/scaling-groups (PLURAL collection)", gotPath)
	}
	if obj["id"] != "g1" {
		t.Errorf("obj[id] = %v; want g1", obj["id"])
	}
	if obj["status"] != "active" {
		t.Errorf("obj[status] = %v; want active", obj["status"])
	}
	if gotBody["name"] != "web-asg" {
		t.Errorf("body[name] = %v; want web-asg", gotBody["name"])
	}
	if gotBody["hypervisor_group_id"] != "hg1" {
		t.Errorf("body[hypervisor_group_id] = %v; want hg1", gotBody["hypervisor_group_id"])
	}
	// min/max omitted by the caller must NOT appear in the body.
	if _, present := gotBody["min_instances"]; present {
		t.Errorf("body must omit unset min_instances; body = %v", gotBody)
	}
}

// TestCreateAutoscalingGroup_Failure verifies a 200 success:false response (e.g.
// autoscaling not enabled on the hypervisor group, or min>max) surfaces the API
// message as an error (C3 — gating is NOT a 403, it is success:false at 200).
func TestCreateAutoscalingGroup_Failure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"Autoscaling is not enabled for this hypervisor group."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.CreateAutoscalingGroup(context.Background(), map[string]any{"name": "x"})
	if err == nil {
		t.Fatal("CreateAutoscalingGroup: expected error for success:false, got nil")
	}
	if !contains(err.Error(), "Autoscaling is not enabled") {
		t.Errorf("error = %q; want it to contain the gating message", err.Error())
	}
}

// TestGetAutoscalingGroup_Success verifies GET /scaling-group/{id} (SINGULAR)
// unwraps the "scaling_group" envelope (note the key differs from create's "group").
func TestGetAutoscalingGroup_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"scaling_group":{"id":"g1","name":"web-asg","status":"paused","min_instances":2,"max_instances":6,"current_count":2,"policies":[{"id":"pol1","metric":"cpu"}]},"instances":{"data":[]},"activities":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.GetAutoscalingGroup(context.Background(), "g1")
	if err != nil {
		t.Fatalf("GetAutoscalingGroup returned error: %v", err)
	}
	if gotPath != "/api/scaling-group/g1" {
		t.Errorf("path = %s; want /api/scaling-group/g1 (SINGULAR)", gotPath)
	}
	if obj["id"] != "g1" {
		t.Errorf("obj[id] = %v; want g1", obj["id"])
	}
	if obj["status"] != "paused" {
		t.Errorf("obj[status] = %v; want paused", obj["status"])
	}
}

// TestGetAutoscalingGroup_NotFound verifies a 404 is recognised by IsNotFound.
func TestGetAutoscalingGroup_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"Autoscaling Group not found"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.GetAutoscalingGroup(context.Background(), "missing")
	if err == nil {
		t.Fatal("GetAutoscalingGroup: expected error for 404, got nil")
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound = false; want true (err: %v)", err)
	}
}

// TestUpdateAutoscalingGroup_Success verifies PATCH /scaling-group/{id} sends the
// mutable fields and unwraps the "group" envelope.
func TestUpdateAutoscalingGroup_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Autoscaling group updated successfully.","group":{"id":"g1","name":"renamed","status":"active","min_instances":3,"max_instances":8,"current_count":3}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.UpdateAutoscalingGroup(context.Background(), "g1", map[string]any{"name": "renamed", "min_instances": int64(3)})
	if err != nil {
		t.Fatalf("UpdateAutoscalingGroup returned error: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %s; want PATCH", gotMethod)
	}
	if gotPath != "/api/scaling-group/g1" {
		t.Errorf("path = %s; want /api/scaling-group/g1", gotPath)
	}
	if gotBody["name"] != "renamed" {
		t.Errorf("body[name] = %v; want renamed", gotBody["name"])
	}
	if obj["min_instances"] != float64(3) {
		t.Errorf("obj[min_instances] = %v; want 3", obj["min_instances"])
	}
}

// TestPauseAutoscalingGroup_Success verifies POST /scaling-group/{id}/pause.
func TestPauseAutoscalingGroup_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Autoscaling group paused.","group":{"id":"g1","status":"paused"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.PauseAutoscalingGroup(context.Background(), "g1")
	if err != nil {
		t.Fatalf("PauseAutoscalingGroup returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/scaling-group/g1/pause" {
		t.Errorf("path = %s; want /api/scaling-group/g1/pause", gotPath)
	}
	if obj["status"] != "paused" {
		t.Errorf("obj[status] = %v; want paused", obj["status"])
	}
}

// TestResumeAutoscalingGroup_Success verifies POST /scaling-group/{id}/resume.
func TestResumeAutoscalingGroup_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Autoscaling group resumed.","group":{"id":"g1","status":"active"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.ResumeAutoscalingGroup(context.Background(), "g1")
	if err != nil {
		t.Fatalf("ResumeAutoscalingGroup returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/scaling-group/g1/resume" {
		t.Errorf("path = %s; want /api/scaling-group/g1/resume", gotPath)
	}
	if obj["status"] != "active" {
		t.Errorf("obj[status] = %v; want active", obj["status"])
	}
}

// TestDeleteAutoscalingGroup_Success verifies DELETE /scaling-group/{id} with
// success:true (the actual teardown is a background job).
func TestDeleteAutoscalingGroup_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Scaling group is being destroyed. Instances will be removed in the background."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DeleteAutoscalingGroup(context.Background(), "g1"); err != nil {
		t.Fatalf("DeleteAutoscalingGroup returned error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s; want DELETE", gotMethod)
	}
	if gotPath != "/api/scaling-group/g1" {
		t.Errorf("path = %s; want /api/scaling-group/g1", gotPath)
	}
}

// TestCreateAutoscalingPolicy_Success verifies POST /scaling-group/{groupId}/policy
// sends the body and unwraps the "policy" envelope returning the object WITH its id.
func TestCreateAutoscalingPolicy_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Scaling policy created successfully.","policy":{"id":"pol1","metric":"cpu","scale_up_threshold":80,"scale_down_threshold":30}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	body := map[string]any{
		"metric":               "cpu",
		"scale_up_threshold":   int64(80),
		"scale_down_threshold": int64(30),
	}
	obj, err := c.CreateAutoscalingPolicy(context.Background(), "g1", body)
	if err != nil {
		t.Fatalf("CreateAutoscalingPolicy returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/scaling-group/g1/policy" {
		t.Errorf("path = %s; want /api/scaling-group/g1/policy", gotPath)
	}
	if obj["id"] != "pol1" {
		t.Errorf("obj[id] = %v; want pol1", obj["id"])
	}
	if gotBody["metric"] != "cpu" {
		t.Errorf("body[metric] = %v; want cpu", gotBody["metric"])
	}
}

// TestUpdateAutoscalingPolicy_WrongGroup verifies a 403 success:false (policy
// belongs to a different group) surfaces as an error.
func TestUpdateAutoscalingPolicy_WrongGroup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"success":false,"message":"Policy does not belong to this scaling group."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.UpdateAutoscalingPolicy(context.Background(), "g1", "pol1", map[string]any{"metric": "memory"})
	if err == nil {
		t.Fatal("UpdateAutoscalingPolicy: expected error for 403 success:false, got nil")
	}
	if !contains(err.Error(), "does not belong to this scaling group") {
		t.Errorf("error = %q; want the wrong-group message", err.Error())
	}
}

// TestDeleteAutoscalingPolicy_Success verifies DELETE /scaling-group/{groupId}/policy/{policyId}.
func TestDeleteAutoscalingPolicy_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Scaling policy deleted successfully."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DeleteAutoscalingPolicy(context.Background(), "g1", "pol1"); err != nil {
		t.Fatalf("DeleteAutoscalingPolicy returned error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s; want DELETE", gotMethod)
	}
	if gotPath != "/api/scaling-group/g1/policy/pol1" {
		t.Errorf("path = %s; want /api/scaling-group/g1/policy/pol1", gotPath)
	}
}

// TestGetAutoscalingPolicy_ReadByScan verifies GetAutoscalingPolicy resolves a
// policy by scanning the group SHOW's embedded policies[] (no individual SHOW).
func TestGetAutoscalingPolicy_ReadByScan(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"scaling_group":{"id":"g1","policies":[{"id":"pol1","metric":"cpu","scale_up_threshold":80},{"id":"pol2","metric":"memory","scale_up_threshold":70}]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.GetAutoscalingPolicy(context.Background(), "g1", "pol2")
	if err != nil {
		t.Fatalf("GetAutoscalingPolicy returned error: %v", err)
	}
	// Read-by-scan goes through the group SHOW endpoint.
	if gotPath != "/api/scaling-group/g1" {
		t.Errorf("path = %s; want /api/scaling-group/g1 (read-by-scan via group SHOW)", gotPath)
	}
	if obj["id"] != "pol2" {
		t.Errorf("obj[id] = %v; want pol2", obj["id"])
	}
	if obj["metric"] != "memory" {
		t.Errorf("obj[metric] = %v; want memory", obj["metric"])
	}
}

// TestGetAutoscalingPolicy_NotFoundInScan verifies that an absent policy id in the
// group's policies[] yields an IsNotFound error (so Read can RemoveResource).
func TestGetAutoscalingPolicy_NotFoundInScan(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"scaling_group":{"id":"g1","policies":[{"id":"pol1","metric":"cpu"}]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.GetAutoscalingPolicy(context.Background(), "g1", "missing")
	if err == nil {
		t.Fatal("GetAutoscalingPolicy: expected IsNotFound error, got nil")
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound = false; want true (err: %v)", err)
	}
}

// TestGetAutoscalingPolicy_GroupGone verifies a 404 on the group propagates as
// IsNotFound (the policy can't exist without its group).
func TestGetAutoscalingPolicy_GroupGone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"Autoscaling Group not found"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.GetAutoscalingPolicy(context.Background(), "gone", "pol1")
	if err == nil {
		t.Fatal("GetAutoscalingPolicy: expected error when group is gone, got nil")
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound = false; want true (err: %v)", err)
	}
}
