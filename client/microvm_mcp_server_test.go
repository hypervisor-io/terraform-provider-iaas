package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const microvmMcpFixture = `{"success":true,"data":{"id":"m1","provider":"custom","name":"tool","label":"Tool","enabled":true,"credential_state":"saved_token","credential_label":"Saved token","connection":null,"revision":{"id":"r1","revision":1,"url":"https://tools.example/mcp?x=1","resource":"https://tools.example/mcp?x=1","auth_kind":"bearer","headers":[],"static_token_preview":"masked"},"connected_members":0,"connected_member_names":[],"probe":{"state":"checking","checked_at":null},"bindings":{"clearing":0,"clearing_info":[],"residual_until":null,"bound_sandboxes":[]},"kit":{"supported":null,"mode":null}}}`

func TestMicrovmMcpServerContracts(t *testing.T) {
	tests := []struct {
		name, method, path, response string
		call                         func(*Client) (map[string]any, error)
	}{
		{"create", "POST", "/microvm/mcp-servers", microvmMcpFixture, func(c *Client) (map[string]any, error) {
			return c.CreateMicrovmMcpServer(context.Background(), map[string]any{"provider": "custom", "static_token": "sentinel"})
		}},
		{"get", "GET", "/microvm/mcp-server/m1", microvmMcpFixture, func(c *Client) (map[string]any, error) { return c.GetMicrovmMcpServer(context.Background(), "m1") }},
		{"update", "PUT", "/microvm/mcp-server/m1", microvmMcpFixture, func(c *Client) (map[string]any, error) {
			return c.UpdateMicrovmMcpServer(context.Background(), "m1", map[string]any{"enabled": false, "headers": map[string]any{}})
		}},
		{"rotate", "POST", "/microvm/mcp-server/m1/rotate-token", microvmMcpFixture, func(c *Client) (map[string]any, error) {
			return c.RotateMicrovmMcpServerToken(context.Background(), "m1", "sentinel")
		}},
		{"disconnect", "POST", "/microvm/mcp-server/m1/disconnect", microvmMcpFixture, func(c *Client) (map[string]any, error) {
			return c.DisconnectMicrovmMcpServer(context.Background(), "m1")
		}},
		{"connect", "POST", "/microvm/mcp-server/m1/connect", `{"success":true,"authorize_url":"https://example.test/authorize?state=abc"}`, func(c *Client) (map[string]any, error) {
			preset := "read write"
			return c.ConnectMicrovmMcpServer(context.Background(), "m1", &preset)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != test.method || r.RequestURI != test.path {
					t.Errorf("route %s %s", r.Method, r.RequestURI)
				}
				if test.name == "rotate" || test.name == "update" || test.name == "connect" {
					var body map[string]any
					_ = json.NewDecoder(r.Body).Decode(&body)
					if test.name == "rotate" && body["static_token"] != "sentinel" {
						t.Error("rotation omitted token")
					}
					if test.name == "update" && body["enabled"] != false {
						t.Error("false dropped")
					}
					if test.name == "connect" && body["scope_preset"] != "read write" {
						t.Error("scope lost")
					}
				}
				_, _ = w.Write([]byte(test.response))
			}))
			defer server.Close()
			row, err := test.call(New(server.URL, "token", time.Second, false))
			if err != nil {
				t.Fatal(err)
			}
			if test.name == "connect" {
				if row["authorize_url"] != "https://example.test/authorize?state=abc" {
					t.Fatal("authorization URL corrupted")
				}
			} else if row["id"] != "m1" || row["revision"] == nil {
				t.Fatal("real presenter shape not decoded")
			}
			raw, _ := json.Marshal(row)
			if strings.Contains(string(raw), "sentinel") {
				t.Fatal("token leaked")
			}
		})
	}
}
func TestMicrovmMcpListProvidersAndDelete(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/microvm/mcp-providers":
			_, _ = w.Write([]byte(`{"success":true,"data":[{"slug":"custom","url":null,"scope_presets":[]}]}`))
		case "/microvm/mcp-servers":
			_, _ = w.Write([]byte(`{"success":true,"data":[{"id":"m1","connection":null}]}`))
		default:
			if r.Method != "DELETE" || r.RequestURI != "/microvm/mcp-server/a%2Fb" {
				t.Error("delete route")
			}
			_, _ = w.Write([]byte(`{"success":true}`))
		}
	}))
	defer server.Close()
	c := New(server.URL, "token", time.Second, false)
	if rows, err := c.ListMicrovmMcpProviders(context.Background()); err != nil || len(rows) != 1 {
		t.Fatalf("providers: %v", err)
	}
	if rows, err := c.ListMicrovmMcpServers(context.Background()); err != nil || len(rows) != 1 {
		t.Fatalf("servers: %v", err)
	}
	if err := c.DeleteMicrovmMcpServer(context.Background(), "a/b"); err != nil {
		t.Fatal(err)
	}
}
