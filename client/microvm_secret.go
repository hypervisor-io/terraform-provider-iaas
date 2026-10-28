package client

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Secret metadata is an allowlist: the API never returns the vault value.
func (c *Client) ListMicrovmSecrets(ctx context.Context, kind string) ([]map[string]any, error) {
	if kind == "" {
		kind = "env"
	}
	if kind != "env" && kind != "registry" && kind != "all" {
		return nil, fmt.Errorf("invalid secret kind")
	}
	return c.microvmStrictList(ctx, "/microvm/secrets?kind="+url.QueryEscape(kind), "secrets")
}
func (c *Client) CreateMicrovmSecret(ctx context.Context, body map[string]any) (map[string]any, error) {
	row, err := c.doItemOnce(ctx, "POST", "/microvm/secrets", body, "secret")
	return microvmSafeResult(row, err, microvmBodySecret(body, "value"))
}
func (c *Client) UpdateMicrovmSecret(ctx context.Context, id string, body map[string]any) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("UpdateMicrovmSecret: empty id")
	}
	row, err := c.doItemOnce(ctx, "PUT", "/microvm/secret/"+url.PathEscape(id), body, "secret")
	return microvmSafeResult(row, err, microvmBodySecret(body, "value"))
}
func (c *Client) DeleteMicrovmSecret(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("DeleteMicrovmSecret: empty id")
	}
	return c.doVoidOnce(ctx, "DELETE", "/microvm/secret/"+url.PathEscape(id), nil)
}

// A malformed list must never be mistaken for deletion during resource refresh.
func (c *Client) microvmStrictList(ctx context.Context, path, key string) ([]map[string]any, error) {
	env, err := c.doItem(ctx, "GET", path, nil, "")
	if err != nil {
		return nil, err
	}
	values, ok := env[key].([]any)
	if !ok {
		return nil, fmt.Errorf("invalid microVM list response: expected %s array", key)
	}
	rows := make([]map[string]any, 0, len(values))
	for _, value := range values {
		row, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid microVM list response: expected objects")
		}
		identity := "id"
		if path == "/microvm/mcp-providers" {
			identity = "slug"
		}
		if id, ok := row[identity].(string); !ok || id == "" {
			return nil, fmt.Errorf("invalid microVM list response: missing identity")
		}
		microvmScrub(row)
		rows = append(rows, row)
	}
	return rows, nil
}
func microvmBodySecret(body map[string]any, key string) string {
	value, _ := body[key].(string)
	return value
}

// Sanitize before returning, including typed fields later consumed by diagnostics
// or MCP MapError. Never wrap an unsanitized cause. Secret write errors retain
// status and machine reason but discard server prose, field errors and request IDs.
func microvmSafeResult(row map[string]any, err error, secret string) (map[string]any, error) {
	if err != nil && secret != "" {
		var api *APIError
		if errors.As(err, &api) {
			return nil, &APIError{Status: api.Status, Reason: microvmSafeReason(api.Reason), Message: "MicroVM credential request failed; secret details omitted", RetryAfter: api.RetryAfter}
		}
		return nil, errors.New("MicroVM credential request failed; secret details omitted")
	}
	if err != nil {
		return nil, err
	}
	if id, ok := row["id"].(string); !ok || id == "" {
		return nil, fmt.Errorf("invalid microVM response: missing ID; check the account before retrying a write")
	}
	microvmScrub(row)
	return row, nil
}

// Keep only the pinned presenters' metadata. Never search-and-replace credential
// substrings in identities: even a one-character credential is valid. Unknown
// response prose and credential previews are deliberately not exposed.
func microvmScrub(row map[string]any) {
	microvmRemoveCredentialKeys(row)
	allowed := " id name kind registry username used_by images_count last_used_at created_at updated_at provider label enabled credential_state credential_label connection revision connected_members connected_member_names probe bindings kit slug url resource auth icon docs_url registration_hint scope_presets setup_notes headers transport setup_note "
	for key, value := range row {
		if !strings.Contains(allowed, " "+key+" ") {
			delete(row, key)
			continue
		}
		fields := ""
		switch key {
		case "connection":
			fields = "status connected_at identity_hint provider_revocation token_expires_at"
		case "revision":
			fields = "id revision url resource auth_kind headers"
		case "probe":
			fields = "state checked_at"
		case "bindings":
			fields = "clearing clearing_info residual_until bound_sandboxes"
		case "kit":
			fields = "supported mode"
		}
		if fields != "" {
			microvmProjectObject(value, fields)
		}
	}
}
func microvmProjectObject(value any, fields string) {
	row, ok := value.(map[string]any)
	if !ok {
		return
	}
	for key, item := range row {
		if !strings.Contains(" "+fields+" ", " "+key+" ") {
			delete(row, key)
			continue
		}
		if key == "clearing_info" || key == "bound_sandboxes" {
			if rows, ok := item.([]any); ok {
				for _, nested := range rows {
					if key == "clearing_info" {
						microvmProjectObject(nested, "binding_id microvm_id auth_kind residual_until")
					} else {
						microvmProjectObject(nested, "id name")
					}
				}
			}
		}
	}
}

func microvmRemoveCredentialKeys(value any) {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			switch strings.ToLower(key) {
			case "value", "preview", "static_token_preview", "static_token", "access_token", "refresh_token", "client_secret", "code_verifier", "plaintext", "authorization", "cookie", "api_key", "api-key":
				delete(v, key)
			default:
				microvmRemoveCredentialKeys(item)
			}
		}
	case []any:
		for _, item := range v {
			microvmRemoveCredentialKeys(item)
		}
	}
}

// Machine reasons are constants, never server-controlled prose. Unknown reasons
// are omitted rather than rewritten using credential substrings.
func microvmSafeReason(reason string) string {
	switch reason {
	case "secret_exists", "secret_kind_immutable", "secret_kind_fields", "registry_in_use", "registry_secrets_not_enabled", "validation_failed", "validation_error",
		"mcp_server_limit", "not_found", "mcp_servers_invalid", "mcp_server_disabled", "mcp_server_changed", "mcp_needs_sign_in", "mcp_server_name_taken", "mcp_provider_already_added", "mcp_server_name_immutable", "mcp_server_provider_immutable", "mcp_use_rotate_token", "mcp_resource_invalid", "mcp_server_in_use", "static_token_invalid", "mcp_not_bearer", "mcp_server_not_found", "mcp_server_invalid", "mcp_provider_invalid", "mcp_auth_kind_invalid", "mcp_headers_invalid":
		return reason
	default:
		return ""
	}
}
