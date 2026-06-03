package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// NOTE: like the other client tests, this file uses net/http/httptest directly
// rather than internal/acctest.MockServer. acctest imports internal/provider
// which imports internal/client, so importing acctest here would create an
// import cycle.

// ---------------------------------------------------------------------------
// CreateSecurityGroup
// ---------------------------------------------------------------------------

// TestCreateSecurityGroup_Success verifies CreateSecurityGroup:
//   - POSTs to /security-groups (PLURAL)
//   - sends the prebuilt body
//   - unwraps the "security_group" envelope, returning the object WITH its id
func TestCreateSecurityGroup_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Security group created successfully","security_group":{"id":"sg-uuid-1","name":"web-sg","description":"web servers"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	body := map[string]any{
		"name":        "web-sg",
		"description": "web servers",
	}
	obj, err := c.CreateSecurityGroup(context.Background(), body)
	if err != nil {
		t.Fatalf("CreateSecurityGroup returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/security-groups" {
		t.Errorf("path = %s; want /api/security-groups (plural)", gotPath)
	}
	if obj["id"] != "sg-uuid-1" {
		t.Errorf("obj[id] = %v; want sg-uuid-1", obj["id"])
	}
	if gotBody["name"] != "web-sg" {
		t.Errorf("body[name] = %v; want web-sg", gotBody["name"])
	}
	if gotBody["description"] != "web servers" {
		t.Errorf("body[description] = %v; want web servers", gotBody["description"])
	}
}

// TestCreateSecurityGroup_Failure verifies a 200 success:false response surfaces
// the API message as an error (C3).
func TestCreateSecurityGroup_Failure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"The name field is required."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.CreateSecurityGroup(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("CreateSecurityGroup: expected error for success:false, got nil")
	}
	if !contains(err.Error(), "name field is required") {
		t.Errorf("error = %q; want it to contain %q", err.Error(), "name field is required")
	}
}

// ---------------------------------------------------------------------------
// GetSecurityGroup
// ---------------------------------------------------------------------------

// TestGetSecurityGroup_Success verifies GetSecurityGroup:
//   - GETs /security-group/{id} (SINGULAR)
//   - unwraps the "security_group" envelope (rules EMBEDDED so Read hydrates them)
func TestGetSecurityGroup_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"security_group":{"id":"sg-uuid-1","name":"web-sg","description":"web servers","rules":[{"id":"r1","direction":"ingress","protocol":"tcp","port_range_min":80,"port_range_max":80,"ip_version":"ipv4","cidr":"0.0.0.0/0","remote_group_id":null,"ip_set_id":null,"description":"http"},{"id":"r2","direction":"ingress","protocol":"icmp","port_range_min":null,"port_range_max":null,"ip_version":"ipv4","cidr":"0.0.0.0/0","remote_group_id":null,"ip_set_id":null,"description":null}],"rules_count":2}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.GetSecurityGroup(context.Background(), "sg-uuid-1")
	if err != nil {
		t.Fatalf("GetSecurityGroup returned error: %v", err)
	}
	if gotPath != "/api/security-group/sg-uuid-1" {
		t.Errorf("path = %s; want /api/security-group/sg-uuid-1 (singular)", gotPath)
	}
	if obj["name"] != "web-sg" {
		t.Errorf("obj[name] = %v; want web-sg", obj["name"])
	}
	rules, ok := obj["rules"].([]any)
	if !ok {
		t.Fatalf("obj[rules] is not an array; got %T", obj["rules"])
	}
	if len(rules) != 2 {
		t.Fatalf("len(rules) = %d; want 2", len(rules))
	}
	first, _ := rules[0].(map[string]any)
	if first["id"] != "r1" || first["protocol"] != "tcp" {
		t.Errorf("rules[0] = %v; want id=r1 protocol=tcp", first)
	}
}

// TestGetSecurityGroup_WithAttachedInstances verifies the SHOW envelope's
// top-level "attached_instances" array is surfaced (the doItem key="" path),
// so the resource can rebuild instance_ids. Here we test via the bare envelope.
func TestGetSecurityGroup_WithAttachedInstances(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"security_group":{"id":"sg-uuid-1","name":"web-sg","rules":[]},"attached_instances":[{"id":"inst-1","name":"web1"},{"id":"inst-2","name":"web2"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	env, err := c.GetSecurityGroupEnvelope(context.Background(), "sg-uuid-1")
	if err != nil {
		t.Fatalf("GetSecurityGroupEnvelope returned error: %v", err)
	}
	insts, ok := env["attached_instances"].([]any)
	if !ok {
		t.Fatalf("env[attached_instances] is not an array; got %T", env["attached_instances"])
	}
	if len(insts) != 2 {
		t.Fatalf("len(attached_instances) = %d; want 2", len(insts))
	}
	first, _ := insts[0].(map[string]any)
	if first["id"] != "inst-1" {
		t.Errorf("attached_instances[0][id] = %v; want inst-1", first["id"])
	}
	sg, ok := env["security_group"].(map[string]any)
	if !ok {
		t.Fatalf("env[security_group] is not an object; got %T", env["security_group"])
	}
	if sg["id"] != "sg-uuid-1" {
		t.Errorf("env[security_group][id] = %v; want sg-uuid-1", sg["id"])
	}
}

// TestGetSecurityGroup_NotFound verifies a 404 maps to an *APIError
// (IsNotFound=true).
func TestGetSecurityGroup_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"Security Group not found"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.GetSecurityGroup(context.Background(), "missing")
	if err == nil {
		t.Fatal("GetSecurityGroup: expected error for 404, got nil")
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound = false; want true (err: %v)", err)
	}
}

// TestGetSecurityGroup_EmptyID verifies the empty-id guard.
func TestGetSecurityGroup_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if _, err := c.GetSecurityGroup(context.Background(), ""); err == nil {
		t.Fatal("GetSecurityGroup: expected error for empty id, got nil")
	}
	if _, err := c.GetSecurityGroupEnvelope(context.Background(), ""); err == nil {
		t.Fatal("GetSecurityGroupEnvelope: expected error for empty id, got nil")
	}
}

// ---------------------------------------------------------------------------
// UpdateSecurityGroup
// ---------------------------------------------------------------------------

// TestUpdateSecurityGroup_Success verifies UpdateSecurityGroup PATCHes
// /security-group/{id} (SINGULAR) and tolerates the body-less {success,message}
// response (no security_group wrapper → resource reads back).
func TestUpdateSecurityGroup_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Security group updated successfully"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.UpdateSecurityGroup(context.Background(), "sg-uuid-1", map[string]any{"name": "renamed"})
	if err != nil {
		t.Fatalf("UpdateSecurityGroup returned error: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %s; want PATCH", gotMethod)
	}
	if gotPath != "/api/security-group/sg-uuid-1" {
		t.Errorf("path = %s; want /api/security-group/sg-uuid-1 (singular)", gotPath)
	}
	if gotBody["name"] != "renamed" {
		t.Errorf("body[name] = %v; want renamed", gotBody["name"])
	}
}

// TestUpdateSecurityGroup_GlobalFailure verifies a 200 success:false (modifying
// a global SG) surfaces an error (C3).
func TestUpdateSecurityGroup_GlobalFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"Cannot modify a global security group."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.UpdateSecurityGroup(context.Background(), "sg-uuid-1", map[string]any{"name": "x"})
	if err == nil {
		t.Fatal("UpdateSecurityGroup: expected error for success:false, got nil")
	}
	if !contains(err.Error(), "global security group") {
		t.Errorf("error = %q; want it to contain %q", err.Error(), "global security group")
	}
}

// TestUpdateSecurityGroup_EmptyID verifies the empty-id guard.
func TestUpdateSecurityGroup_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if _, err := c.UpdateSecurityGroup(context.Background(), "", map[string]any{"name": "x"}); err == nil {
		t.Fatal("UpdateSecurityGroup: expected error for empty id, got nil")
	}
}

// ---------------------------------------------------------------------------
// DeleteSecurityGroup
// ---------------------------------------------------------------------------

// TestDeleteSecurityGroup_Success verifies DELETE /security-group/{id}
// (SINGULAR).
func TestDeleteSecurityGroup_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Security group deleted successfully"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DeleteSecurityGroup(context.Background(), "sg-uuid-1"); err != nil {
		t.Fatalf("DeleteSecurityGroup returned error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s; want DELETE", gotMethod)
	}
	if gotPath != "/api/security-group/sg-uuid-1" {
		t.Errorf("path = %s; want /api/security-group/sg-uuid-1 (singular)", gotPath)
	}
}

// TestDeleteSecurityGroup_GlobalFailure verifies a 200 success:false (deleting a
// global SG) surfaces an error (C3).
func TestDeleteSecurityGroup_GlobalFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"Cannot delete a global security group."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	err := c.DeleteSecurityGroup(context.Background(), "sg-uuid-1")
	if err == nil {
		t.Fatal("DeleteSecurityGroup: expected error for success:false, got nil")
	}
	if !contains(err.Error(), "global security group") {
		t.Errorf("error = %q; want it to contain %q", err.Error(), "global security group")
	}
}

// TestDeleteSecurityGroup_EmptyID verifies the empty-id guard.
func TestDeleteSecurityGroup_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if err := c.DeleteSecurityGroup(context.Background(), ""); err == nil {
		t.Fatal("DeleteSecurityGroup: expected error for empty id, got nil")
	}
}

// ---------------------------------------------------------------------------
// AddSecurityGroupRule
// ---------------------------------------------------------------------------

// TestAddSecurityGroupRule_Success verifies AddSecurityGroupRule:
//   - POSTs to /security-group/{id}/rules
//   - sends the prebuilt rule body
//   - unwraps the "rule" envelope, returning the rule WITH its server id
func TestAddSecurityGroupRule_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Rule added successfully","rule":{"id":"rule-uuid-1","direction":"ingress","protocol":"tcp","port_range_min":443,"port_range_max":443,"ip_version":"ipv4","cidr":"0.0.0.0/0"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	rule, err := c.AddSecurityGroupRule(context.Background(), "sg-uuid-1", map[string]any{
		"direction":      "ingress",
		"protocol":       "tcp",
		"port_range_min": 443,
		"port_range_max": 443,
		"ip_version":     "ipv4",
		"cidr":           "0.0.0.0/0",
	})
	if err != nil {
		t.Fatalf("AddSecurityGroupRule returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/security-group/sg-uuid-1/rules" {
		t.Errorf("path = %s; want /api/security-group/sg-uuid-1/rules", gotPath)
	}
	if rule["id"] != "rule-uuid-1" {
		t.Errorf("rule[id] = %v; want rule-uuid-1", rule["id"])
	}
	if gotBody["protocol"] != "tcp" {
		t.Errorf("body[protocol] = %v; want tcp", gotBody["protocol"])
	}
	if gotBody["cidr"] != "0.0.0.0/0" {
		t.Errorf("body[cidr] = %v; want 0.0.0.0/0", gotBody["cidr"])
	}
}

// TestAddSecurityGroupRule_DuplicateFailure verifies a 200 success:false
// (duplicate rule) surfaces an error (C3).
func TestAddSecurityGroupRule_DuplicateFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"This rule already exists."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.AddSecurityGroupRule(context.Background(), "sg-uuid-1", map[string]any{"direction": "ingress"})
	if err == nil {
		t.Fatal("AddSecurityGroupRule: expected error for success:false, got nil")
	}
	if !contains(err.Error(), "already exists") {
		t.Errorf("error = %q; want it to contain %q", err.Error(), "already exists")
	}
}

// TestAddSecurityGroupRule_EmptySgID verifies the empty-sgID guard.
func TestAddSecurityGroupRule_EmptySgID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if _, err := c.AddSecurityGroupRule(context.Background(), "", map[string]any{"direction": "ingress"}); err == nil {
		t.Fatal("AddSecurityGroupRule: expected error for empty sgID, got nil")
	}
}

// ---------------------------------------------------------------------------
// DeleteSecurityGroupRule
// ---------------------------------------------------------------------------

// TestDeleteSecurityGroupRule_Success verifies DELETE
// /security-group/{sgID}/rule/{ruleID} (singular "rule" segment).
func TestDeleteSecurityGroupRule_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Rule removed successfully"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DeleteSecurityGroupRule(context.Background(), "sg-uuid-1", "rule-uuid-1"); err != nil {
		t.Fatalf("DeleteSecurityGroupRule returned error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s; want DELETE", gotMethod)
	}
	if gotPath != "/api/security-group/sg-uuid-1/rule/rule-uuid-1" {
		t.Errorf("path = %s; want /api/security-group/sg-uuid-1/rule/rule-uuid-1", gotPath)
	}
}

// TestDeleteSecurityGroupRule_EmptyIDs verifies the empty-id guards on both args.
func TestDeleteSecurityGroupRule_EmptyIDs(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if err := c.DeleteSecurityGroupRule(context.Background(), "", "r1"); err == nil {
		t.Fatal("DeleteSecurityGroupRule: expected error for empty sgID, got nil")
	}
	if err := c.DeleteSecurityGroupRule(context.Background(), "sg-uuid-1", ""); err == nil {
		t.Fatal("DeleteSecurityGroupRule: expected error for empty ruleID, got nil")
	}
}

// ---------------------------------------------------------------------------
// AttachSecurityGroupInstances
// ---------------------------------------------------------------------------

// TestAttachSecurityGroupInstances_Success verifies AttachSecurityGroupInstances:
//   - POSTs to /security-group/{id}/attach-instances
//   - sends {instance_ids:[...]} (array of strings)
func TestAttachSecurityGroupInstances_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Security group attached to instances"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.AttachSecurityGroupInstances(context.Background(), "sg-uuid-1", []string{"inst-1", "inst-2"}); err != nil {
		t.Fatalf("AttachSecurityGroupInstances returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/security-group/sg-uuid-1/attach-instances" {
		t.Errorf("path = %s; want /api/security-group/sg-uuid-1/attach-instances", gotPath)
	}
	ids, ok := gotBody["instance_ids"].([]any)
	if !ok || len(ids) != 2 {
		t.Fatalf("body[instance_ids] = %v; want 2-element array", gotBody["instance_ids"])
	}
	if ids[0] != "inst-1" {
		t.Errorf("body[instance_ids][0] = %v; want inst-1", ids[0])
	}
}

// TestAttachSecurityGroupInstances_Failure verifies a 200 success:false (e.g.
// max groups exceeded) surfaces an error (C3).
func TestAttachSecurityGroupInstances_Failure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"Maximum of 10 security groups per instance exceeded."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	err := c.AttachSecurityGroupInstances(context.Background(), "sg-uuid-1", []string{"inst-1"})
	if err == nil {
		t.Fatal("AttachSecurityGroupInstances: expected error for success:false, got nil")
	}
	if !contains(err.Error(), "Maximum of 10") {
		t.Errorf("error = %q; want it to contain %q", err.Error(), "Maximum of 10")
	}
}

// TestAttachSecurityGroupInstances_EmptyID verifies the empty-id guard.
func TestAttachSecurityGroupInstances_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if err := c.AttachSecurityGroupInstances(context.Background(), "", []string{"inst-1"}); err == nil {
		t.Fatal("AttachSecurityGroupInstances: expected error for empty sgID, got nil")
	}
}

// ---------------------------------------------------------------------------
// DetachSecurityGroupInstances
// ---------------------------------------------------------------------------

// TestDetachSecurityGroupInstances_Success verifies DetachSecurityGroupInstances:
//   - POSTs to /security-group/{id}/detach-instances (POST, not DELETE)
//   - sends {instance_ids:[...]} (array of strings)
func TestDetachSecurityGroupInstances_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Security group detached from instances"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DetachSecurityGroupInstances(context.Background(), "sg-uuid-1", []string{"inst-3"}); err != nil {
		t.Fatalf("DetachSecurityGroupInstances returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/security-group/sg-uuid-1/detach-instances" {
		t.Errorf("path = %s; want /api/security-group/sg-uuid-1/detach-instances", gotPath)
	}
	ids, ok := gotBody["instance_ids"].([]any)
	if !ok || len(ids) != 1 {
		t.Fatalf("body[instance_ids] = %v; want 1-element array", gotBody["instance_ids"])
	}
	if ids[0] != "inst-3" {
		t.Errorf("body[instance_ids][0] = %v; want inst-3", ids[0])
	}
}

// TestDetachSecurityGroupInstances_EmptyID verifies the empty-id guard.
func TestDetachSecurityGroupInstances_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	if err := c.DetachSecurityGroupInstances(context.Background(), "", []string{"inst-1"}); err == nil {
		t.Fatal("DetachSecurityGroupInstances: expected error for empty sgID, got nil")
	}
}

// ---------------------------------------------------------------------------
// ListSecurityGroups
// ---------------------------------------------------------------------------

// TestListSecurityGroups_Success verifies GET /security-groups returns the
// paginator list.
func TestListSecurityGroups_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"current_page":1,"data":[{"id":"sg-uuid-1","name":"web-sg"},{"id":"sg-uuid-2","name":"db-sg"}],"total":2}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	items, err := c.ListSecurityGroups(context.Background())
	if err != nil {
		t.Fatalf("ListSecurityGroups returned error: %v", err)
	}
	if gotPath != "/api/security-groups" {
		t.Errorf("path = %s; want /api/security-groups", gotPath)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d; want 2", len(items))
	}
	if items[0]["id"] != "sg-uuid-1" {
		t.Errorf("items[0][id] = %v; want sg-uuid-1", items[0]["id"])
	}
}
