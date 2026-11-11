package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

const egressReadBody = `{"success":true,"egress":{"policy":{"default_action":"deny","inbound_replies":"service_ports","rules":[{"id":"r_4f2a9c1e","action":"allow","target":"api.github.com","protocol":"any","ports":[443]}]},"generation":3,"applied_generation":3,"state":"applied","applied_at":"2026-11-11T10:00:00+00:00","last_error_code":null,"capability":{"available":true,"names":true},"service_ports":[{"proto":"tcp","port":22}]}}`

func TestMicrovmEgressRoutesVerbsAndBodies(t *testing.T) {
	type seen struct {
		method, path, body string
	}
	var got []seen
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		got = append(got, seen{r.Method, r.URL.EscapedPath(), string(raw)})
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodPut:
			// 202: stored, the node has not confirmed it yet. Still a success.
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"success":true,"egress":{"policy":{"default_action":"deny","inbound_replies":"service_ports","rules":[]},"generation":4,"applied_generation":3,"state":"pending"}}`))
		case http.MethodDelete:
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"success":true,"egress":{"policy":null,"generation":5,"applied_generation":3,"state":"pending"}}`))
		default:
			_, _ = w.Write([]byte(egressReadBody))
		}
	}))
	defer server.Close()
	c := New(server.URL+"/api", "token", 0, false)
	ctx := context.Background()

	read, err := c.GetMicrovmEgress(ctx, "vm/1")
	if err != nil {
		t.Fatalf("GetMicrovmEgress: %v", err)
	}
	if read["state"] != "applied" || read["generation"] != float64(3) {
		t.Fatalf("read model = %v", read)
	}
	policy, _ := read["policy"].(map[string]any)
	if policy["default_action"] != "deny" {
		t.Fatalf("policy = %v", read["policy"])
	}

	doc := map[string]any{"default_action": "deny", "rules": []any{map[string]any{"action": "allow", "target": "api.github.com", "ports": []any{443}}}}
	stored, err := c.SetMicrovmEgress(ctx, "vm/1", doc)
	if err != nil {
		t.Fatalf("SetMicrovmEgress on a 202: %v", err)
	}
	if stored["state"] != "pending" || stored["generation"] != float64(4) {
		t.Fatalf("stored = %v", stored)
	}

	cleared, err := c.ClearMicrovmEgress(ctx, "vm/1")
	if err != nil {
		t.Fatalf("ClearMicrovmEgress on a 202: %v", err)
	}
	if cleared["policy"] != nil || cleared["generation"] != float64(5) {
		t.Fatalf("cleared = %v", cleared)
	}

	want := []seen{
		{http.MethodGet, "/api/microvm/vm/vm%2F1/egress", ""},
		{http.MethodPut, "/api/microvm/vm/vm%2F1/egress", ""},
		{http.MethodDelete, "/api/microvm/vm/vm%2F1/egress", ""},
	}
	if len(got) != len(want) {
		t.Fatalf("requests = %v", got)
	}
	for i := range want {
		if got[i].method != want[i].method || got[i].path != want[i].path {
			t.Errorf("request %d = %s %s; want %s %s", i, got[i].method, got[i].path, want[i].method, want[i].path)
		}
	}
	// The PUT body is the policy document itself, not wrapped in a key.
	var sent map[string]any
	if err := json.Unmarshal([]byte(got[1].body), &sent); err != nil {
		t.Fatalf("PUT body is not JSON: %q", got[1].body)
	}
	var wantDoc map[string]any
	raw, _ := json.Marshal(doc)
	_ = json.Unmarshal(raw, &wantDoc)
	if !reflect.DeepEqual(sent, wantDoc) {
		t.Errorf("PUT body = %v; want the policy document %v", sent, wantDoc)
	}
	if got[0].body != "" || got[2].body != "" {
		t.Errorf("GET and DELETE carry no body; got %q and %q", got[0].body, got[2].body)
	}
}

func TestMicrovmEgressRefusalsKeepStatusReasonAndFieldErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		reason string
		field  string
	}{
		{"bad policy", 422, `{"success":false,"reason":"egress_target_not_permitted","message":"rules.0.target: this address is blocked by the platform (metadata)","errors":{"rules.0.target":["this address is blocked by the platform (metadata)"]},"violations":[{"code":"egress_target_not_permitted","field":"rules.0.target","message":"this address is blocked by the platform (metadata)"}]}`, "egress_target_not_permitted", "rules.0.target"},
		{"terminal", 409, `{"success":false,"reason":"egress_policy_terminal","message":"The outbound network policy of a stopped-for-good sandbox cannot be changed."}`, "egress_policy_terminal", ""},
		{"unsupported node", 409, `{"success":false,"reason":"egress_policy_unsupported_node","message":"This sandbox runs on a node that cannot enforce an outbound network policy."}`, "egress_policy_unsupported_node", ""},
		{"not found", 404, `{"success":false,"reason":"sandbox_not_found","message":"Sandbox not found."}`, "sandbox_not_found", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			c := New(server.URL+"/api", "token", 0, false)

			_, err := c.SetMicrovmEgress(context.Background(), "vm-1", map[string]any{"default_action": "deny"})
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("error = %v; want *APIError", err)
			}
			if apiErr.Status != tc.status || apiErr.Reason != tc.reason {
				t.Errorf("status/reason = %d/%q; want %d/%q", apiErr.Status, apiErr.Reason, tc.status, tc.reason)
			}
			if tc.field != "" && len(apiErr.FieldErrors[tc.field]) != 1 {
				t.Errorf("field errors = %v; want one under %q", apiErr.FieldErrors, tc.field)
			}
			if tc.status == 404 && !IsNotFound(err) {
				t.Errorf("a 404 must satisfy IsNotFound")
			}
			if calls != 1 {
				t.Errorf("a 4xx refusal was sent %d times; want 1", calls)
			}
		})
	}
}

func TestMicrovmEgressFailsClosedOnAMalformedAnswerAndEmptyInput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer server.Close()
	c := New(server.URL+"/api", "token", 0, false)
	ctx := context.Background()

	if _, err := c.GetMicrovmEgress(ctx, "vm-1"); err == nil {
		t.Error("a 200 without an egress object must be an error, not an empty policy")
	}
	if _, err := c.GetMicrovmEgress(ctx, ""); err == nil {
		t.Error("GetMicrovmEgress: empty id must be refused before any request")
	}
	if _, err := c.SetMicrovmEgress(ctx, "", map[string]any{}); err == nil {
		t.Error("SetMicrovmEgress: empty id must be refused")
	}
	if _, err := c.SetMicrovmEgress(ctx, "vm-1", nil); err == nil {
		t.Error("SetMicrovmEgress: a nil policy must be refused (it would send no body)")
	}
	if _, err := c.ClearMicrovmEgress(ctx, ""); err == nil {
		t.Error("ClearMicrovmEgress: empty id must be refused")
	}
}
