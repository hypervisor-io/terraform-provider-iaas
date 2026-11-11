package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

const egressLogBody = `{"success":true,"events":{"current_page":1,"data":[{"id":5012,"layer":"dns","class":"dns_name_denied","rule":"default","rule_id":"default","category":null,"destination":"telemetry.example.net","withheld":false,"protocol":null,"port":null,"qtype":"A","count":14,"sampled":false,"suppressed_keys":0,"first_at":"2026-11-12T09:14:02+00:00","last_at":"2026-11-12T09:14:11+00:00"}],"last_page":1,"per_page":50,"total":1},"collection":{"pulled_at":"2026-11-12T09:15:00+00:00","packet_layer":true,"retention_days":14,"max_rows":5000}}`

func TestMicrovmEgressLogRouteAndQuery(t *testing.T) {
	var path, query, method string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path, query = r.Method, r.URL.EscapedPath(), r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(egressLogBody))
	}))
	defer server.Close()
	c := New(server.URL+"/api", "token", 0, false)

	got, err := c.GetMicrovmEgressLog(context.Background(), "vm/1", map[string]string{
		"layer": "dns", "search": "a b&c", "per_page": "100", "since": "", "cursor": "7", "vm_id": "x",
	})
	if err != nil {
		t.Fatalf("GetMicrovmEgressLog: %v", err)
	}
	if method != http.MethodGet || path != "/api/microvm/vm/vm%2F1/egress/log" {
		t.Fatalf("request = %s %s", method, path)
	}
	// Only documented filters, empty ones left out, values escaped.
	if query != "layer=dns&per_page=100&search=a+b%26c" {
		t.Fatalf("query = %q", query)
	}
	events, _ := got["events"].(map[string]any)
	rows, _ := events["data"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["destination"] != "telemetry.example.net" {
		t.Fatalf("events = %v", got["events"])
	}
	if collection, _ := got["collection"].(map[string]any); collection["retention_days"] != float64(14) {
		t.Fatalf("collection = %v", got["collection"])
	}

	// No filters: no query string at all.
	if _, err := c.GetMicrovmEgressLog(context.Background(), "vm-1", nil); err != nil {
		t.Fatalf("GetMicrovmEgressLog without filters: %v", err)
	}
	if query != "" || path != "/api/microvm/vm/vm-1/egress/log" {
		t.Fatalf("unfiltered request = %s ?%s", path, query)
	}
}

func TestMicrovmEgressLogRefusalsAndMalformedAnswers(t *testing.T) {
	answers := map[string]struct {
		status int
		body   string
	}{
		"missing":  {http.StatusNotFound, `{"success":false,"reason":"sandbox_not_found","message":"Sandbox not found."}`},
		"badquery": {http.StatusUnprocessableEntity, `{"message":"The selected layer is invalid.","errors":{"layer":["The selected layer is invalid."]}}`},
		"noevents": {http.StatusOK, `{"success":true}`},
		"nodata":   {http.StatusOK, `{"success":true,"events":{"total":0}}`},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for id, a := range answers {
			if r.URL.Path == "/api/microvm/vm/"+id+"/egress/log" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(a.status)
				_, _ = w.Write([]byte(a.body))
				return
			}
		}
		w.WriteHeader(http.StatusTeapot)
	}))
	defer server.Close()
	c := New(server.URL+"/api", "token", 0, false)
	ctx := context.Background()

	_, err := c.GetMicrovmEgressLog(ctx, "missing", nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound || apiErr.Reason != "sandbox_not_found" {
		t.Fatalf("404 = %v", err)
	}
	_, err = c.GetMicrovmEgressLog(ctx, "badquery", map[string]string{"layer": "http"})
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnprocessableEntity {
		t.Fatalf("422 = %v", err)
	}
	if _, err := c.GetMicrovmEgressLog(ctx, "noevents", nil); err == nil {
		t.Error("a 200 without an events object must be an error, not an empty log")
	}
	if _, err := c.GetMicrovmEgressLog(ctx, "nodata", nil); err == nil {
		t.Error("a 200 without events.data must be an error, not an empty log")
	}
	if _, err := c.GetMicrovmEgressLog(ctx, "", nil); err == nil {
		t.Error("empty id must be refused before any request")
	}
}
