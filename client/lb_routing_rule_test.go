package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestCreateLBRoutingRule_Success verifies the POST path + envelope + body
// (match_type/match_value/lb_backend_id, NOT condition_type/condition_value/backend_id).
func TestCreateLBRoutingRule_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Routing rule created.","rule":{"id":"rl-1","match_type":"path_prefix","match_value":"/api","lb_backend_id":"be-1","priority":100},"sync":{}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.CreateLBRoutingRule(context.Background(), "lb-1", "fe-1", map[string]any{
		"match_type":    "path_prefix",
		"match_value":   "/api",
		"lb_backend_id": "be-1",
	})
	if err != nil {
		t.Fatalf("CreateLBRoutingRule returned error: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/load-balancer/lb-1/frontend/fe-1/rules" {
		t.Errorf("method/path = %s %s; want POST .../frontend/fe-1/rules", gotMethod, gotPath)
	}
	if obj["id"] != "rl-1" {
		t.Errorf("obj[id] = %v; want rl-1", obj["id"])
	}
	if gotBody["match_value"] != "/api" || gotBody["lb_backend_id"] != "be-1" {
		t.Errorf("create body = %v; want match_value=/api lb_backend_id=be-1", gotBody)
	}
	for _, stray := range []string{"condition_type", "condition_value", "backend_id"} {
		if _, present := gotBody[stray]; present {
			t.Errorf("create body must NOT use legacy field %q: %v", stray, gotBody)
		}
	}
}

func TestUpdateLBRoutingRule_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"rule":{"id":"rl-1","match_value":"/v2"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.UpdateLBRoutingRule(context.Background(), "lb-1", "fe-1", "rl-1", map[string]any{"match_value": "/v2"})
	if err != nil {
		t.Fatalf("UpdateLBRoutingRule returned error: %v", err)
	}
	if gotMethod != http.MethodPatch || gotPath != "/api/load-balancer/lb-1/frontend/fe-1/rule/rl-1" {
		t.Errorf("method/path = %s %s; want PATCH .../rule/rl-1", gotMethod, gotPath)
	}
	if obj["match_value"] != "/v2" {
		t.Errorf("obj[match_value] = %v; want /v2", obj["match_value"])
	}
}

func TestDeleteLBRoutingRule_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Routing rule deleted."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DeleteLBRoutingRule(context.Background(), "lb-1", "fe-1", "rl-1"); err != nil {
		t.Fatalf("DeleteLBRoutingRule returned error: %v", err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/api/load-balancer/lb-1/frontend/fe-1/rule/rl-1" {
		t.Errorf("method/path = %s %s; want DELETE .../rule/rl-1", gotMethod, gotPath)
	}
}

func TestGetLBRoutingRule_ScanFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"load_balancer":{"id":"lb-1","frontends":[{"id":"fe-1","routing_rules":[{"id":"rl-1","match_value":"/api"}]},{"id":"fe-2","routing_rules":[]}]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.GetLBRoutingRule(context.Background(), "lb-1", "fe-1", "rl-1")
	if err != nil {
		t.Fatalf("GetLBRoutingRule returned error: %v", err)
	}
	if obj["match_value"] != "/api" {
		t.Errorf("obj[match_value] = %v; want /api", obj["match_value"])
	}
}

func TestGetLBRoutingRule_ScanAbsent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"load_balancer":{"id":"lb-1","frontends":[{"id":"fe-1","routing_rules":[]}]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if _, err := c.GetLBRoutingRule(context.Background(), "lb-1", "fe-1", "missing"); err == nil || !IsNotFound(err) {
		t.Errorf("expected IsNotFound for absent rule, got %v", err)
	}
	if _, err := c.GetLBRoutingRule(context.Background(), "lb-1", "fe-missing", "rl-1"); err == nil || !IsNotFound(err) {
		t.Errorf("expected IsNotFound for wrong frontend, got %v", err)
	}
}

func TestLBRoutingRule_EmptyIDGuards(t *testing.T) {
	c := New("http://unused/api", "tok", time.Second, false)
	ctx := context.Background()
	if _, err := c.CreateLBRoutingRule(ctx, "", "fe", map[string]any{}); err == nil {
		t.Error("CreateLBRoutingRule: expected empty-lbID error")
	}
	if _, err := c.CreateLBRoutingRule(ctx, "lb", "", map[string]any{}); err == nil {
		t.Error("CreateLBRoutingRule: expected empty-frontendID error")
	}
	if _, err := c.UpdateLBRoutingRule(ctx, "lb", "fe", "", map[string]any{}); err == nil {
		t.Error("UpdateLBRoutingRule: expected empty-ruleID error")
	}
	if err := c.DeleteLBRoutingRule(ctx, "lb", "fe", ""); err == nil {
		t.Error("DeleteLBRoutingRule: expected empty-ruleID error")
	}
	if _, err := c.GetLBRoutingRule(ctx, "lb", "fe", ""); err == nil {
		t.Error("GetLBRoutingRule: expected empty-ruleID error")
	}
}
