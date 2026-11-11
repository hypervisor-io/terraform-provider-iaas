package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

const egressPresetsBody = `{"success":true,"presets":[{"id":"no_outbound","version":1,"source":"preset:no_outbound@1","name":"No outbound access","description":"Blocks every outbound connection the sandbox starts.","rule_count":0,"policy":{"default_action":"deny","rules":[]}},{"id":"model_apis","version":1,"source":"preset:model_apis@1","name":"AI provider APIs","description":"Allows the listed APIs.","rule_count":1,"policy":{"default_action":"deny","rules":[{"action":"allow","target":"api.anthropic.com","protocol":"tcp","ports":[443],"source":"preset:model_apis@1"}]}}]}`

func TestMicrovmEgressPresetsRouteAndShape(t *testing.T) {
	var method, path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.EscapedPath()+"?"+r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(egressPresetsBody))
	}))
	defer server.Close()
	c := New(server.URL+"/api", "token", 0, false)

	presets, err := c.ListMicrovmEgressPresets(context.Background())
	if err != nil {
		t.Fatalf("ListMicrovmEgressPresets: %v", err)
	}
	if method != http.MethodGet || path != "/api/microvm/egress/presets?" {
		t.Fatalf("request = %s %s", method, path)
	}
	if len(presets) != 2 || presets[0]["id"] != "no_outbound" || presets[1]["source"] != "preset:model_apis@1" {
		t.Fatalf("presets = %v", presets)
	}
	// The policy is the document SetMicrovmEgress takes, untouched.
	policy := presets[1]["policy"].(map[string]any)
	rules := policy["rules"].([]any)
	if policy["default_action"] != "deny" || len(rules) != 1 || rules[0].(map[string]any)["target"] != "api.anthropic.com" {
		t.Fatalf("policy = %v", policy)
	}
	if empty := presets[0]["policy"].(map[string]any)["rules"].([]any); len(empty) != 0 {
		t.Fatalf("no_outbound rules = %v", empty)
	}
}

func TestMicrovmEgressPresetsFailClosedOnAMalformedAnswer(t *testing.T) {
	bodies := []struct {
		status int
		body   string
	}{
		{http.StatusOK, `{"success":true}`},
		{http.StatusOK, `{"success":true,"presets":{"a":1}}`},
		{http.StatusOK, `{"success":true,"presets":["model_apis"]}`},
		{http.StatusOK, `{"success":true,"presets":[{"id":"model_apis"}]}`},
		{http.StatusForbidden, `{"success":false,"reason":"billing_disabled","message":"Billing is switched off."}`},
	}
	next := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(bodies[next].status)
		_, _ = w.Write([]byte(bodies[next].body))
	}))
	defer server.Close()
	c := New(server.URL+"/api", "token", 0, false)

	for i := range bodies {
		next = i
		presets, err := c.ListMicrovmEgressPresets(context.Background())
		if err == nil {
			t.Errorf("answer %d (%s): want an error, got %v", i, bodies[i].body, presets)
		}
	}
	var apiErr *APIError
	if _, err := c.ListMicrovmEgressPresets(context.Background()); !errors.As(err, &apiErr) || apiErr.Reason != "billing_disabled" {
		t.Errorf("a refusal keeps its reason: %v", err)
	}
}
