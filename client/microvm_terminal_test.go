package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestMintMicrovmTerminalRouteBodyAndAnswer(t *testing.T) {
	var method, path, body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		method, path, body = r.Method, r.URL.EscapedPath(), string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"url":"wss://49983-abc.sbx.example.com/.virtconsole/terminal/v1","token":"v1.k2.eA.c2ln","expires_at":"2026-11-12T10:05:00+00:00","protocol":"vc-terminal.v1","session":"dev","mode":"viewer","limits":{"viewers_per_session":8}}`))
	}))
	defer server.Close()
	c := New(server.URL+"/api", "token", 0, false)

	out, err := c.MintMicrovmTerminal(context.Background(), "vm/1", MicrovmTerminalRequest{Mode: "viewer", Session: "dev", TTLSeconds: 600})
	if err != nil {
		t.Fatalf("MintMicrovmTerminal: %v", err)
	}
	if method != "POST" || path != "/api/microvm/vm/vm%2F1/terminal" {
		t.Fatalf("request = %s %s", method, path)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(body), &sent); err != nil {
		t.Fatalf("body %q: %v", body, err)
	}
	want := map[string]any{"mode": "viewer", "session": "dev", "ttl_seconds": float64(600)}
	if !reflect.DeepEqual(sent, want) {
		t.Fatalf("body = %v, want %v", sent, want)
	}
	if out["token"] != "v1.k2.eA.c2ln" || out["url"] != "wss://49983-abc.sbx.example.com/.virtconsole/terminal/v1" {
		t.Fatalf("answer = %v", out)
	}
	// Defaults are left to the API.
	if _, err := c.MintMicrovmTerminal(context.Background(), "vm/1", MicrovmTerminalRequest{Mode: "writer"}); err != nil {
		t.Fatalf("defaults: %v", err)
	}
	if strings.Contains(body, "session") || strings.Contains(body, "ttl_seconds") {
		t.Fatalf("defaults sent anyway: %s", body)
	}
}

func TestMintMicrovmTerminalRefusalsAndGuards(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "paused"):
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"success":false,"reason":"sandbox_paused","message":"the sandbox is paused"}`))
		case strings.Contains(r.URL.Path, "empty"):
			_, _ = w.Write([]byte(`{"success":true}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"success":false,"reason":"sandbox_not_found","message":"Sandbox not found."}`))
		}
	}))
	defer server.Close()
	c := New(server.URL+"/api", "token", 0, false)
	ctx := context.Background()

	_, err := c.MintMicrovmTerminal(ctx, "paused", MicrovmTerminalRequest{Mode: "writer"})
	var api *APIError
	if !errors.As(err, &api) || api.Status != 409 || !strings.Contains(err.Error(), "sandbox_paused") {
		t.Fatalf("paused: %v", err)
	}
	if _, err = c.MicrovmTerminalSessions(ctx, "gone"); err == nil || !strings.Contains(err.Error(), "sandbox_not_found") {
		t.Fatalf("gone: %v", err)
	}
	if _, err := c.MintMicrovmTerminal(ctx, "empty", MicrovmTerminalRequest{Mode: "writer"}); err == nil {
		t.Fatal("an answer without a token must be an error")
	}
	if _, err := c.MicrovmTerminalSessions(ctx, "empty"); err == nil {
		t.Fatal("an answer without a sessions list must be an error")
	}
	if _, err := c.MintMicrovmTerminal(ctx, "", MicrovmTerminalRequest{Mode: "writer"}); err == nil {
		t.Fatal("empty id")
	}
	for _, mode := range []string{"", "support", "admin", "WRITER"} {
		if _, err := c.MintMicrovmTerminal(ctx, "x", MicrovmTerminalRequest{Mode: mode}); err == nil {
			t.Fatalf("mode %q was accepted", mode)
		}
	}
	if _, err := c.MicrovmTerminalSessions(ctx, ""); err == nil {
		t.Fatal("empty id")
	}
}

func TestMicrovmTerminalSessionsRouteAndAnswer(t *testing.T) {
	var method, path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.EscapedPath()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"sessions":[{"name":"main","epoch":1,"pid":412,"state":"running","offset_start":0,"offset_end":900,"holder":true,"viewers":0}],"support":{"allowed":false,"grant":null}}`))
	}))
	defer server.Close()
	c := New(server.URL+"/api", "token", 0, false)

	out, err := c.MicrovmTerminalSessions(context.Background(), "vm/1")
	if err != nil {
		t.Fatal(err)
	}
	if method != "GET" || path != "/api/microvm/vm/vm%2F1/terminal/sessions" {
		t.Fatalf("request = %s %s", method, path)
	}
	list, _ := out["sessions"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["name"] != "main" {
		t.Fatalf("answer = %v", out)
	}
}
