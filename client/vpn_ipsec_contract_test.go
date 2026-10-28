package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Contract pinned to Master 255ba16d: nullable diagnostics, live responder
// evidence, and per-subnet route objects survive the consumer boundary.
func TestVpnIpsecContract_LiveStatusAndSubnetRoutes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/vpn-gateway/g/ipsec":
			_, _ = w.Write([]byte(`{"success":true,"connections":[{"id":"c","role":"responder","remote_address":null,"remote_host":"198.51.100.7","children_expected":2,"children_installed":1,"bytes_in":123,"bytes_out":456,"established_at":null,"status_checked_at":"2026-10-28T12:00:00+00:00","last_error_code":"esp_proposal_mismatch","last_error":"The peers could not agree on an ESP proposal.","last_error_at":"2026-10-28T11:59:59+00:00"}],"gateway":{"ipsec_last_error_code":"no_proposal_chosen","ipsec_last_error_at":"2026-10-28T12:00:00+00:00","ipsec_version":null,"ipsec_error":null}}`))
		case "/api/vpn-gateway/g/ipsec/c/remote-settings":
			_, _ = w.Write([]byte(`{"success":true,"remote_settings":{"your_identity":{"type":"any","value":null},"instance_route_hint":{"commands":[{"subnet":"web","cidr":"10.60.1.0/24","gateway":"10.60.1.1","command":"ip route add 192.168.50.0/24 via 10.60.1.1"},{"subnet":"db","cidr":"10.60.2.0/24","gateway":"10.60.2.1","command":"ip route add 192.168.50.0/24 via 10.60.2.1"}],"note":"Use the instance subnet gateway."}}}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := New(srv.URL+"/api", "tok", time.Second, false)
	conns, gw, err := c.ListVpnIpsecConnections(context.Background(), "g")
	if err != nil || len(conns) != 1 {
		t.Fatalf("list failed: %v", err)
	}
	conn := conns[0]
	for key, want := range map[string]any{"remote_address": nil, "remote_host": "198.51.100.7", "children_expected": float64(2), "children_installed": float64(1), "bytes_in": float64(123), "bytes_out": float64(456), "established_at": nil, "last_error": "The peers could not agree on an ESP proposal.", "last_error_at": "2026-10-28T11:59:59+00:00", "status_checked_at": "2026-10-28T12:00:00+00:00"} {
		if got, ok := conn[key]; !ok || got != want {
			t.Errorf("connection %s = %v, want %v", key, got, want)
		}
	}
	if gw["ipsec_last_error_code"] != "no_proposal_chosen" || gw["ipsec_last_error_at"] == nil || gw["ipsec_error"] != nil || gw["ipsec_version"] != nil {
		t.Fatalf("gateway diagnosis/privacy drift: %v", gw)
	}
	settings, err := c.GetVpnIpsecRemoteSettings(context.Background(), "g", "c")
	if err != nil {
		t.Fatal(err)
	}
	hint := settings["instance_route_hint"].(map[string]any)
	commands := hint["commands"].([]any)
	if len(commands) != 2 {
		t.Fatalf("route commands = %v", commands)
	}
	second := commands[1].(map[string]any)
	if second["subnet"] != "db" || second["cidr"] != "10.60.2.0/24" || second["gateway"] != "10.60.2.1" || second["command"] != "ip route add 192.168.50.0/24 via 10.60.2.1" {
		t.Fatalf("second subnet hint drift: %v", second)
	}
	if _, exists := settings["psk"]; exists {
		t.Fatal("remote settings exposes psk")
	}
}

func TestVpnIpsecContract_TypedRefusalsAndPatchOmission(t *testing.T) {
	for _, tc := range []struct {
		status int
		reason string
		field  string
	}{
		{409, "remote_identity_in_use", ""}, {409, "node_tls_unpinned", ""},
		{409, "node_upgrade_required", ""}, {422, "validation_failed", "remote_ts"},
		{422, "destination_overlap", ""}, {422, "responder_requires_remote_id", "remote_id"},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "PATCH" || r.URL.Path != "/api/vpn-gateway/g/ipsec/c" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if len(body) != 1 || body["enabled"] != false {
					t.Errorf("patch changed omitted fields: %v", body)
				}
				w.WriteHeader(tc.status)
				response := map[string]any{"success": false, "reason": tc.reason, "message": "Request refused."}
				if tc.field != "" {
					response["errors"] = map[string][]string{tc.field: {"Invalid value."}}
				}
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer srv.Close()
			c := New(srv.URL+"/api", "tok", time.Second, false)
			_, _, err := c.UpdateVpnIpsecConnection(context.Background(), "g", "c", map[string]any{"enabled": false})
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.Status != tc.status || apiErr.Reason != tc.reason {
				t.Fatalf("typed refusal lost: %v", err)
			}
			if tc.field != "" && len(apiErr.FieldErrors[tc.field]) != 1 {
				t.Fatalf("field errors lost: %v", apiErr.FieldErrors)
			}
			if calls != 1 {
				t.Fatalf("refusal replayed %d times", calls)
			}
		})
	}
}

func TestVpnGatewayContract_DeleteRefusalRetainsChildrenUntilAccepted(t *testing.T) {
	deleted, attempts := false, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" && r.URL.Path == "/api/vpn-gateway/g" {
			attempts++
			if attempts == 1 {
				w.WriteHeader(409)
				_, _ = w.Write([]byte(`{"success":false,"reason":"vpn_gateway_destroy_failed","message":"The VPN gateway could not be deleted and has been kept; try deleting it again."}`))
				return
			}
			deleted = true
			_, _ = w.Write([]byte(`{"success":true}`))
			return
		}
		if r.Method == "DELETE" {
			t.Errorf("gateway deletion must not pre-delete children: %s", r.URL.Path)
		}
		if deleted {
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"success":false,"reason":"not_found"}`))
			return
		}
		switch r.URL.Path {
		case "/api/vpn-gateway/g":
			_, _ = w.Write([]byte(`{"success":true,"gateway":{"id":"g","status":"active"}}`))
		case "/api/vpn-gateway/g/ipsec":
			_, _ = w.Write([]byte(`{"success":true,"connections":[{"id":"c"}],"gateway":{"ipsec_generation":2,"ipsec_applied_generation":2}}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c := New(srv.URL+"/api", "tok", time.Second, false)
	ctx := context.Background()
	err := c.DeleteVpnGateway(ctx, "g")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 409 || apiErr.Reason != ReasonVpnGatewayDestroyFailed || attempts != 1 {
		t.Fatalf("delete refusal not preserved: %v, attempts %d", err, attempts)
	}
	gw, err := c.GetVpnGateway(ctx, "g")
	if err != nil || gw["status"] != "active" {
		t.Fatalf("gateway not retained: %v", err)
	}
	conns, _, err := c.ListVpnIpsecConnections(ctx, "g")
	if err != nil || len(conns) != 1 {
		t.Fatalf("children not retained: %v", err)
	}
	if err := c.DeleteVpnGateway(ctx, "g"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetVpnGateway(ctx, "g"); !IsNotFound(err) {
		t.Fatalf("accepted deletion not observed: %v", err)
	}
	if _, _, err := c.ListVpnIpsecConnections(ctx, "g"); !IsNotFound(err) {
		t.Fatalf("children remain after accepted deletion: %v", err)
	}
}

func TestVpnIpsecContract_RestartUnconfirmedIsNotReplayed(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/api/vpn-gateway/g/ipsec/c/restart" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(503)
		_, _ = w.Write([]byte(`{"success":false,"reason":"node_dispatch_unconfirmed","message":"The node has not confirmed the IPsec operation. It will be retried."}`))
	}))
	defer srv.Close()
	c := New(srv.URL+"/api", "tok", time.Second, false)
	_, err := c.RestartVpnIpsecConnection(context.Background(), "g", "c")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 503 || apiErr.Reason != "node_dispatch_unconfirmed" || calls != 1 {
		t.Fatalf("restart result %v after %d attempts", err, calls)
	}
}
