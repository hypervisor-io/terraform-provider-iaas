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
// which imports internal/client, creating an import cycle if acctest were used here.

// ---------------------------------------------------------------------------
// ListAlertRules
// ---------------------------------------------------------------------------

func TestListAlertRules_Success(t *testing.T) {
	var gotMethod, gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"alert_rules":{"current_page":1,"data":[` +
			`{"id":"ar-uuid-1","name":"High CPU","resource_type":"instance","metric":"cpu_pct","operator":"gt","threshold":80,"status":"ok","channels":[]},` +
			`{"id":"ar-uuid-2","name":"Low Disk","resource_type":"instance","metric":"disk_pct","operator":"gt","threshold":90,"status":"ok","channels":[]}` +
			`],"per_page":25,"total":2}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	items, err := c.ListAlertRules(context.Background())
	if err != nil {
		t.Fatalf("ListAlertRules returned error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %s; want GET", gotMethod)
	}
	if gotPath != "/api/alert-rules" {
		t.Errorf("path = %s; want /api/alert-rules (plural)", gotPath)
	}
	if len(items) != 2 {
		t.Errorf("len(items) = %d; want 2", len(items))
	}
	if items[0]["id"] != "ar-uuid-1" {
		t.Errorf("items[0][id] = %v; want ar-uuid-1", items[0]["id"])
	}
}

func TestListAlertRules_Empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"alert_rules":{"current_page":1,"data":[],"per_page":25,"total":0}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	items, err := c.ListAlertRules(context.Background())
	if err != nil {
		t.Fatalf("ListAlertRules returned error: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("len(items) = %d; want 0", len(items))
	}
}

// ---------------------------------------------------------------------------
// CreateAlertRule
// ---------------------------------------------------------------------------

func TestCreateAlertRule_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"alert_rule":{"id":"ar-uuid-1","name":"High CPU on web-01",` +
			`"resource_type":"instance","resource_id":"inst-uuid-1","metric":"cpu_pct","operator":"gt",` +
			`"threshold":80,"duration":300,"reminder_interval":3600,"status":"ok","enabled":1,` +
			`"channels":[{"id":"nc-uuid-1","name":"Ops Slack"}]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	body := map[string]any{
		"name":              "High CPU on web-01",
		"resource_type":     "instance",
		"resource_id":       "inst-uuid-1",
		"metric":            "cpu_pct",
		"operator":          "gt",
		"threshold":         80,
		"duration":          300,
		"reminder_interval": 3600,
		"channel_ids":       []string{"nc-uuid-1"},
	}
	obj, err := c.CreateAlertRule(context.Background(), body)
	if err != nil {
		t.Fatalf("CreateAlertRule returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/alert-rules" {
		t.Errorf("path = %s; want /api/alert-rules (plural)", gotPath)
	}
	if obj["id"] != "ar-uuid-1" {
		t.Errorf("obj[id] = %v; want ar-uuid-1", obj["id"])
	}
	if gotBody["name"] != "High CPU on web-01" {
		t.Errorf("body[name] = %v; want High CPU on web-01", gotBody["name"])
	}
	if gotBody["metric"] != "cpu_pct" {
		t.Errorf("body[metric] = %v; want cpu_pct", gotBody["metric"])
	}
	if gotBody["operator"] != "gt" {
		t.Errorf("body[operator] = %v; want gt", gotBody["operator"])
	}
}

func TestCreateAlertRule_NoResourceID(t *testing.T) {
	// resource_id is optional - when omitted the rule applies to ALL instances.
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"alert_rule":{"id":"ar-uuid-2","name":"Global CPU Alert",` +
			`"resource_type":"instance","resource_id":null,"metric":"cpu_pct","operator":"gt",` +
			`"threshold":90,"status":"ok","enabled":1,"channels":[]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	body := map[string]any{
		"name":          "Global CPU Alert",
		"resource_type": "instance",
		"metric":        "cpu_pct",
		"operator":      "gt",
		"threshold":     90,
	}
	obj, err := c.CreateAlertRule(context.Background(), body)
	if err != nil {
		t.Fatalf("CreateAlertRule returned error: %v", err)
	}
	if obj["id"] != "ar-uuid-2" {
		t.Errorf("obj[id] = %v; want ar-uuid-2", obj["id"])
	}
	// resource_id not in body means rule applies globally
	if _, present := gotBody["resource_id"]; present {
		t.Errorf("body should not contain resource_id when omitted, but got %v", gotBody["resource_id"])
	}
}

func TestCreateAlertRule_Failure_ValidationError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"errors":{"metric":["The metric field is required."],"operator":["The operator field is required."]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.CreateAlertRule(context.Background(), map[string]any{"name": "x", "resource_type": "instance", "threshold": 50})
	if err == nil {
		t.Fatal("CreateAlertRule: expected error for 422, got nil")
	}
	if !contains(err.Error(), "metric field is required") {
		t.Errorf("error = %q; want it to contain %q", err.Error(), "metric field is required")
	}
}

func TestCreateAlertRule_Failure_SuccessFalse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"Invalid load balancer."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.CreateAlertRule(context.Background(), map[string]any{
		"name": "x", "resource_type": "load_balancer", "resource_id": "bad-id",
		"metric": "cpu_pct", "operator": "gt", "threshold": 80,
	})
	if err == nil {
		t.Fatal("CreateAlertRule: expected error for success:false, got nil")
	}
}

// ---------------------------------------------------------------------------
// GetAlertRule
// ---------------------------------------------------------------------------

func TestGetAlertRule_Success(t *testing.T) {
	var gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"alert_rule":{"id":"ar-uuid-1","name":"High CPU",` +
			`"resource_type":"instance","resource_id":"inst-uuid-1","metric":"cpu_pct","operator":"gt",` +
			`"threshold":80,"duration":300,"reminder_interval":3600,"status":"ok","enabled":1,` +
			`"channels":[{"id":"nc-uuid-1","name":"Ops Slack","type":"slack"}]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.GetAlertRule(context.Background(), "ar-uuid-1")
	if err != nil {
		t.Fatalf("GetAlertRule returned error: %v", err)
	}
	if gotPath != "/api/alert-rule/ar-uuid-1" {
		t.Errorf("path = %s; want /api/alert-rule/ar-uuid-1 (singular)", gotPath)
	}
	if obj["id"] != "ar-uuid-1" {
		t.Errorf("obj[id] = %v; want ar-uuid-1", obj["id"])
	}
	channels, _ := obj["channels"].([]any)
	if len(channels) != 1 {
		t.Errorf("len(channels) = %d; want 1", len(channels))
	}
}

func TestGetAlertRule_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"Alert Rule not found"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.GetAlertRule(context.Background(), "ar-missing")
	if err == nil {
		t.Fatal("GetAlertRule: expected error for 404, got nil")
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound = false; want true for 404 response, err = %v", err)
	}
}

func TestGetAlertRule_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	_, err := c.GetAlertRule(context.Background(), "")
	if err == nil {
		t.Fatal("GetAlertRule: expected error for empty id, got nil")
	}
	if !contains(err.Error(), "empty id") {
		t.Errorf("error = %q; want it to mention empty id", err.Error())
	}
}

// ---------------------------------------------------------------------------
// UpdateAlertRule
// ---------------------------------------------------------------------------

func TestUpdateAlertRule_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"alert_rule":{"id":"ar-uuid-1","name":"High CPU Updated",` +
			`"resource_type":"instance","metric":"cpu_pct","operator":"gte","threshold":85,` +
			`"duration":600,"reminder_interval":7200,"status":"ok","enabled":1,` +
			`"channels":[{"id":"nc-uuid-1","name":"Ops Slack"},{"id":"nc-uuid-2","name":"Discord"}]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	body := map[string]any{
		"name":              "High CPU Updated",
		"resource_type":     "instance",
		"metric":            "cpu_pct",
		"operator":          "gte",
		"threshold":         85,
		"duration":          600,
		"reminder_interval": 7200,
		"enabled":           true,
		"channel_ids":       []string{"nc-uuid-1", "nc-uuid-2"},
	}
	obj, err := c.UpdateAlertRule(context.Background(), "ar-uuid-1", body)
	if err != nil {
		t.Fatalf("UpdateAlertRule returned error: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %s; want PATCH", gotMethod)
	}
	if gotPath != "/api/alert-rule/ar-uuid-1" {
		t.Errorf("path = %s; want /api/alert-rule/ar-uuid-1 (singular)", gotPath)
	}
	if obj["id"] != "ar-uuid-1" {
		t.Errorf("obj[id] = %v; want ar-uuid-1", obj["id"])
	}
	if gotBody["name"] != "High CPU Updated" {
		t.Errorf("body[name] = %v; want High CPU Updated", gotBody["name"])
	}
	if gotBody["operator"] != "gte" {
		t.Errorf("body[operator] = %v; want gte", gotBody["operator"])
	}
}

func TestUpdateAlertRule_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	_, err := c.UpdateAlertRule(context.Background(), "", map[string]any{"name": "x", "resource_type": "instance", "metric": "cpu_pct", "operator": "gt", "threshold": 80})
	if err == nil {
		t.Fatal("UpdateAlertRule: expected error for empty id, got nil")
	}
	if !contains(err.Error(), "empty id") {
		t.Errorf("error = %q; want it to mention empty id", err.Error())
	}
}

// ---------------------------------------------------------------------------
// DeleteAlertRule
// ---------------------------------------------------------------------------

func TestDeleteAlertRule_Success(t *testing.T) {
	var gotMethod, gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	err := c.DeleteAlertRule(context.Background(), "ar-uuid-1")
	if err != nil {
		t.Fatalf("DeleteAlertRule returned error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s; want DELETE", gotMethod)
	}
	if gotPath != "/api/alert-rule/ar-uuid-1" {
		t.Errorf("path = %s; want /api/alert-rule/ar-uuid-1 (singular)", gotPath)
	}
}

func TestDeleteAlertRule_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"Alert Rule not found"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	err := c.DeleteAlertRule(context.Background(), "ar-missing")
	if err == nil {
		t.Fatal("DeleteAlertRule: expected error for 404, got nil")
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound = false; want true for 404 response, err = %v", err)
	}
}

func TestDeleteAlertRule_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	err := c.DeleteAlertRule(context.Background(), "")
	if err == nil {
		t.Fatal("DeleteAlertRule: expected error for empty id, got nil")
	}
	if !contains(err.Error(), "empty id") {
		t.Errorf("error = %q; want it to mention empty id", err.Error())
	}
}
