package client

import (
	"context"
	"fmt"
)

// Sandbox run API mints (user_api, bearer token).
//
//	ACCESS  POST /microvm/vm/{id}/access   body {scopes, ttl_seconds?, auto_resume?}
//	                                       -> {success, base_url, token, token_type, expires_at, scopes,
//	                                           sandbox:{id,state}, limits:{...}}
//	PREVIEW POST /microvm/vm/{id}/preview  body {port, ttl_seconds?}
//	                                       -> {success, url, host, port, expires_at, key_id}
//
// Both answer a CREDENTIAL (a short-lived token, or a link that carries one). It is not state: no resource and
// no data source of the OpenTofu provider uses these calls (the manifest excludes both endpoints with a reason),
// and the values must never be written to state, a log or a plan. They exist for the MCP server, which mints a
// token inside one tool call and then calls the node itself.
//
// Refusals carry a typed reason: 404 sandbox_not_found; 409 sandbox_paused, sandbox_not_running, sandbox_killed,
// envd_not_available, billing_hold; 422 port_not_allowed (or validation errors); 503 node_unsupported,
// node_domain_missing, node_missing, signing_unavailable; 429 when more than 30 mints a minute are made with one
// API key.

// MicrovmAccessRequest is the body of the access mint.
type MicrovmAccessRequest struct {
	Scopes     []string
	TTLSeconds int  // 0 = the API default (300)
	AutoResume bool // resume a paused sandbox first
}

// MintMicrovmAccess mints a run token for the sandbox and returns the answer object: base_url (where to call),
// token, token_type, expires_at, scopes, sandbox and limits. The token is secret: do not log or persist it.
func (c *Client) MintMicrovmAccess(ctx context.Context, id string, req MicrovmAccessRequest) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("MintMicrovmAccess: empty id")
	}
	if len(req.Scopes) == 0 {
		return nil, fmt.Errorf("MintMicrovmAccess: no scopes")
	}
	body := map[string]any{"scopes": req.Scopes}
	if req.TTLSeconds != 0 {
		body["ttl_seconds"] = req.TTLSeconds
	}
	if req.AutoResume {
		body["auto_resume"] = true
	}
	out, err := c.doItem(ctx, "POST", microvm(id)+"/access", body, "")
	if err != nil {
		return nil, err
	}
	if s, _ := out["token"].(string); s == "" {
		return nil, fmt.Errorf("MintMicrovmAccess: the answer has no token")
	}
	if s, _ := out["base_url"].(string); s == "" {
		return nil, fmt.Errorf("MintMicrovmAccess: the answer has no base_url")
	}
	return out, nil
}

// MintMicrovmPreview mints a preview link for a port of the sandbox and returns the answer object: url (it
// carries the token: secret), host, port, expires_at and key_id. ttlSeconds 0 = the API default (3600).
func (c *Client) MintMicrovmPreview(ctx context.Context, id string, port, ttlSeconds int) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("MintMicrovmPreview: empty id")
	}
	body := map[string]any{"port": port}
	if ttlSeconds != 0 {
		body["ttl_seconds"] = ttlSeconds
	}
	out, err := c.doItem(ctx, "POST", microvm(id)+"/preview", body, "")
	if err != nil {
		return nil, err
	}
	if s, _ := out["url"].(string); s == "" {
		return nil, fmt.Errorf("MintMicrovmPreview: the answer has no url")
	}
	return out, nil
}
