package client

import (
	"context"
	"fmt"
	"net/url"
)

// MicroVM sandbox endpoints (MV1-25; user_api, bearer token). A sandbox is a
// short-lived MicroVM in a hypervisor group with an auto-pause/kill TTL.
//
//	INDEX   GET    /microvm/sandboxes            (?state=) -> {success,sandboxes:[...]}
//	SHOW    GET    /microvm/sandbox/{id}                   -> {success,sandbox:{...}}
//	CREATE  POST   /microvm/sandboxes   body {hypervisor_group_id,template,
//	                                       timeout?,metadata?,env_vars?,secure?,on_timeout?}
//	                                                      -> {success,sandbox:{...}}
//	PAUSE   POST   /microvm/sandbox/{id}/pause             -> {success,sandbox:{...}}
//	RESUME  POST   /microvm/sandbox/{id}/resume            -> {success,sandbox:{...}}
//	TIMEOUT POST   /microvm/sandbox/{id}/timeout  body {timeout}
//	                                                      -> {success,sandbox:{...}}
//	KILL    DELETE /microvm/sandbox/{id}                   -> {success,message}; success:false
//	                                                         at HTTP 200 on refusal
//	METRICS GET    /microvm/sandbox/{id}/metrics           -> {success,metrics:{...}}
//	LOGS    GET    /microvm/sandbox/{id}/logs              -> {success,logs:[...]}

// ListSandboxes returns the account's sandboxes, optionally filtered by state
// (running, paused, killed).
func (c *Client) ListSandboxes(ctx context.Context, state string) ([]map[string]any, error) {
	path := "/microvm/sandboxes"
	if state != "" {
		path += "?state=" + url.QueryEscape(state)
	}
	return c.namedList(ctx, "GET", path, "sandboxes")
}

// GetSandbox fetches a single sandbox by UUID. A missing id surfaces as a
// 404 *APIError (IsNotFound = true).
func (c *Client) GetSandbox(ctx context.Context, id string) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("GetSandbox: empty id")
	}
	return c.doItem(ctx, "GET", "/microvm/sandbox/"+url.PathEscape(id), nil, "sandbox")
}

// CreateSandbox creates a sandbox. body must contain "hypervisor_group_id"
// and "template"; "timeout", "metadata", "env_vars", "secure" and
// "on_timeout" are optional and omitted keys fall back to the API defaults.
func (c *Client) CreateSandbox(ctx context.Context, body map[string]any) (map[string]any, error) {
	return c.doItem(ctx, "POST", "/microvm/sandboxes", body, "sandbox")
}

// PauseSandbox pauses a running sandbox (snapshots it on the node).
func (c *Client) PauseSandbox(ctx context.Context, id string) (map[string]any, error) {
	return c.doItem(ctx, "POST", "/microvm/sandbox/"+url.PathEscape(id)+"/pause", nil, "sandbox")
}

// ResumeSandbox resumes a paused sandbox.
func (c *Client) ResumeSandbox(ctx context.Context, id string) (map[string]any, error) {
	return c.doItem(ctx, "POST", "/microvm/sandbox/"+url.PathEscape(id)+"/resume", nil, "sandbox")
}

// SetSandboxTimeout overwrites the sandbox's auto-pause/kill TTL in seconds.
func (c *Client) SetSandboxTimeout(ctx context.Context, id string, timeoutSeconds int) (map[string]any, error) {
	return c.doItem(ctx, "POST", "/microvm/sandbox/"+url.PathEscape(id)+"/timeout", map[string]any{"timeout": timeoutSeconds}, "sandbox")
}

// KillSandbox permanently destroys a sandbox. A failure is signalled with
// success:false at HTTP 200, so doVoid checks the flag.
func (c *Client) KillSandbox(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("KillSandbox: empty id")
	}
	return c.doVoid(ctx, "DELETE", "/microvm/sandbox/"+url.PathEscape(id), nil)
}

// GetSandboxMetrics returns the latest CPU/memory/disk sample for a sandbox.
func (c *Client) GetSandboxMetrics(ctx context.Context, id string) (map[string]any, error) {
	return c.doItem(ctx, "GET", "/microvm/sandbox/"+url.PathEscape(id)+"/metrics", nil, "metrics")
}

// GetSandboxLogs returns the recent console log lines for a sandbox.
func (c *Client) GetSandboxLogs(ctx context.Context, id string) ([]map[string]any, error) {
	return c.namedList(ctx, "GET", "/microvm/sandbox/"+url.PathEscape(id)+"/logs", "logs")
}
