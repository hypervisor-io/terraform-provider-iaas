package client

import (
	"context"
	"fmt"
)

// Sandbox terminal (user_api, bearer token).
//
//	MINT      POST /microvm/vm/{id}/terminal           body {mode, session?, ttl_seconds?}
//	                                                   -> {success, url, token, expires_at, protocol, session, mode, limits}
//	SESSIONS  GET  /microvm/vm/{id}/terminal/sessions  -> {success, sessions:[{name, epoch, pid, state, offset_start,
//	                                                      offset_end, holder, viewers, created_at, last_input_at}],
//	                                                      support:{allowed, grant}}
//
// The mint answers a CREDENTIAL (a short-lived token and the node websocket URL to use it on). It is not state:
// no resource and no data source of the OpenTofu provider uses these calls (the manifest excludes every terminal
// endpoint with a reason), and the token must never be written to state, a log or a plan. They exist for the MCP
// server, which mints a viewer token inside one tool call and reads the node's text tail itself.
//
// Refusals carry a typed reason: 404 sandbox_not_found; 409 sandbox_paused, sandbox_not_running, sandbox_killed;
// 422 terminal_disabled (or validation errors); 429 rate_limited (more than 60 mints a minute for one sandbox);
// 502 terminal_node_unavailable; 503 node_unsupported, node_domain_missing, node_missing, signing_unavailable.

// MicrovmTerminalRequest is the body of the terminal mint.
type MicrovmTerminalRequest struct {
	Mode       string // "writer" or "viewer"
	Session    string // "" = the API default (main)
	TTLSeconds int    // 0 = the API default (300)
}

// MintMicrovmTerminal mints a terminal token for the sandbox and returns the answer object: url (the node
// websocket address), token, expires_at, protocol, session, mode and limits. The token is secret: do not log or
// persist it.
func (c *Client) MintMicrovmTerminal(ctx context.Context, id string, req MicrovmTerminalRequest) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("MintMicrovmTerminal: empty id")
	}
	if req.Mode != "writer" && req.Mode != "viewer" {
		return nil, fmt.Errorf("MintMicrovmTerminal: mode must be writer or viewer")
	}
	body := map[string]any{"mode": req.Mode}
	if req.Session != "" {
		body["session"] = req.Session
	}
	if req.TTLSeconds != 0 {
		body["ttl_seconds"] = req.TTLSeconds
	}
	out, err := c.doItem(ctx, "POST", microvm(id)+"/terminal", body, "")
	if err != nil {
		return nil, err
	}
	if s, _ := out["token"].(string); s == "" {
		return nil, fmt.Errorf("MintMicrovmTerminal: the answer has no token")
	}
	if s, _ := out["url"].(string); s == "" {
		return nil, fmt.Errorf("MintMicrovmTerminal: the answer has no url")
	}
	return out, nil
}

// MicrovmTerminalSessions returns the answer object of the session list: sessions (metadata only) and support.
func (c *Client) MicrovmTerminalSessions(ctx context.Context, id string) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("MicrovmTerminalSessions: empty id")
	}
	out, err := c.doItem(ctx, "GET", microvm(id)+"/terminal/sessions", nil, "")
	if err != nil {
		return nil, err
	}
	if _, ok := out["sessions"].([]any); !ok {
		return nil, fmt.Errorf("MicrovmTerminalSessions: the answer has no sessions list")
	}
	return out, nil
}
