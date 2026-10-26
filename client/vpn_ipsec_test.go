package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestVpnIpsec_listAndGet(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/api/vpn-gateway/gw-1/ipsec":
			_, _ = w.Write([]byte(`{"success":true,"connections":[{"id":"c1","name":"office"}],"gateway":{"ipsec_generation":3,"ipsec_applied_generation":3,"ipsec_apply_state":"idle"}}`))
		case "/api/vpn-gateway/gw-1/ipsec/c1":
			_, _ = w.Write([]byte(`{"success":true,"connection":{"id":"c1","apply_state":"applied"}}`))
		default:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"success":false,"reason":"not_found","message":"IPsec connection not found."}`))
		}
	}))
	defer srv.Close()
	c := New(srv.URL+"/api", "tok", 5*time.Second, false)

	conns, gw, err := c.ListVpnIpsecConnections(context.Background(), "gw-1")
	if err != nil || len(conns) != 1 || conns[0]["id"] != "c1" || gw["ipsec_apply_state"] != "idle" {
		t.Fatalf("list = %v %v %v", conns, gw, err)
	}
	conn, err := c.GetVpnIpsecConnection(context.Background(), "gw-1", "c1")
	if err != nil || conn["apply_state"] != "applied" {
		t.Fatalf("get = %v %v", conn, err)
	}
	_, err = c.GetVpnIpsecConnection(context.Background(), "gw-1", "gone")
	if !IsNotFound(err) {
		t.Fatalf("expected not found, got %v", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Reason != "not_found" {
		t.Fatalf("reason = %v", err)
	}
}

func TestVpnIpsec_createIsSentOnceAndReturnsWarnings(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.Method != "POST" || r.URL.Path != "/api/vpn-gateway/gw-1/ipsec" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"success":true,"message":"IPsec connection created.","connection":{"id":"c1"},"warnings":["security_group_blocks_esp"]}`))
	}))
	defer srv.Close()
	c := New(srv.URL+"/api", "tok", 5*time.Second, false)
	conn, warnings, err := c.CreateVpnIpsecConnection(context.Background(), "gw-1", map[string]any{"name": "office"})
	if err != nil || conn["id"] != "c1" || len(warnings) != 1 || warnings[0] != "security_group_blocks_esp" {
		t.Fatalf("create = %v %v %v", conn, warnings, err)
	}

	// A 500 is returned after ONE attempt (never replayed).
	atomic.StoreInt32(&calls, 0)
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"success":false,"message":"boom"}`))
	}))
	defer srv2.Close()
	c2 := New(srv2.URL+"/api", "tok", 5*time.Second, false)
	if _, _, err := c2.CreateVpnIpsecConnection(context.Background(), "gw-1", map[string]any{"name": "office"}); err == nil {
		t.Fatal("expected an error")
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("create attempts = %d; want 1", n)
	}
}

func TestVpnIpsec_typedRefusalsKeepTheirReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(409)
		_, _ = w.Write([]byte(`{"success":false,"reason":"ipsec_installing","message":"IPsec is still being set up on this gateway; try again in a minute."}`))
	}))
	defer srv.Close()
	c := New(srv.URL+"/api", "tok", 5*time.Second, false)
	_, _, err := c.CreateVpnIpsecConnection(context.Background(), "gw-1", map[string]any{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Reason != ReasonIpsecInstalling || apiErr.Status != 409 {
		t.Fatalf("err = %v", err)
	}
}

func TestVpnIpsec_updateDeleteRestartSettings(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == "PATCH":
			_, _ = w.Write([]byte(`{"success":true,"connection":{"id":"c1","name":"new"},"warnings":[]}`))
		case r.Method == "DELETE":
			_, _ = w.Write([]byte(`{"success":true,"message":"IPsec connection deleted.","connection":{"id":"c1"}}`))
		case r.URL.Path == "/api/vpn-gateway/gw-1/ipsec/c1/restart":
			_, _ = w.Write([]byte(`{"success":true,"connection":{"id":"c1"}}`))
		case r.URL.Path == "/api/vpn-gateway/gw-1/ipsec/c1/remote-settings" && r.URL.Query().Get("format") == "text":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("Our public IP: 203.0.113.10\n"))
		case r.URL.Path == "/api/vpn-gateway/gw-1/ipsec/c1/remote-settings":
			_, _ = w.Write([]byte(`{"success":true,"remote_settings":{"our_public_ip":"203.0.113.10"}}`))
		}
	}))
	defer srv.Close()
	c := New(srv.URL+"/api", "tok", 5*time.Second, false)
	ctx := context.Background()
	if conn, _, err := c.UpdateVpnIpsecConnection(ctx, "gw-1", "c1", map[string]any{"name": "new"}); err != nil || conn["name"] != "new" {
		t.Fatalf("update = %v %v", conn, err)
	}
	if err := c.DeleteVpnIpsecConnection(ctx, "gw-1", "c1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if conn, err := c.RestartVpnIpsecConnection(ctx, "gw-1", "c1"); err != nil || conn["id"] != "c1" {
		t.Fatalf("restart = %v %v", conn, err)
	}
	if rs, err := c.GetVpnIpsecRemoteSettings(ctx, "gw-1", "c1"); err != nil || rs["our_public_ip"] != "203.0.113.10" {
		t.Fatalf("settings = %v %v", rs, err)
	}
	if txt, err := c.GetVpnIpsecRemoteSettingsText(ctx, "gw-1", "c1"); err != nil || txt != "Our public IP: 203.0.113.10\n" {
		t.Fatalf("text settings = %q %v", txt, err)
	}
	want := []string{
		"PATCH /api/vpn-gateway/gw-1/ipsec/c1", "DELETE /api/vpn-gateway/gw-1/ipsec/c1",
		"POST /api/vpn-gateway/gw-1/ipsec/c1/restart", "GET /api/vpn-gateway/gw-1/ipsec/c1/remote-settings",
		"GET /api/vpn-gateway/gw-1/ipsec/c1/remote-settings",
	}
	for i, w := range want {
		if i >= len(seen) || seen[i] != w {
			t.Errorf("request %d = %v; want %s", i, seen, w)
		}
	}
}

func TestVpnIpsec_emptyIDsAreRejected(t *testing.T) {
	c := New("http://127.0.0.1:1/api", "tok", time.Second, false)
	ctx := context.Background()
	if _, _, err := c.ListVpnIpsecConnections(ctx, ""); err == nil {
		t.Error("list: expected an error")
	}
	if _, err := c.GetVpnIpsecConnection(ctx, "g", ""); err == nil {
		t.Error("get: expected an error")
	}
	if err := c.DeleteVpnIpsecConnection(ctx, "", "c"); err == nil {
		t.Error("delete: expected an error")
	}
}
