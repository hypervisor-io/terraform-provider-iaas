package client

import (
	"context"
	"fmt"
	"net/url"
)

// SSH key endpoints (verified against the real controller):
//
//	INDEX   GET    /ssh-keys        → raw Laravel paginator {data:[...]}
//	CREATE  POST   /ssh-keys        body {name,public_key} → 200 {success,ssh_key}
//	SHOW    GET    /ssh-key/{id}    (singular) → {ssh_key:{...}}; 404 when absent
//	UPDATE  PATCH  /ssh-key/{id}    (singular) body {name?,comments?} → 200/422
//	DELETE  DELETE /ssh-keys/{id}   (plural) → 200 {success}; 200 success:false on failure
//
// Notes:
//   - Create is HTTP 200 (not 201) and signals failure with success:false at 200.
//   - comments is server-derived: never sent on create (a controller bug would
//     store "" if provided), so CreateSSHKey only sends name + public_key.
//   - public_key cannot be updated.

// ListSSHKeys returns all SSH keys visible to the token (paginator-aware).
func (c *Client) ListSSHKeys(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/ssh-keys", nil)
}

// CreateSSHKey creates an SSH key. Only name and public_key are sent; comments
// is derived by the server (see package notes). Returns the created object.
func (c *Client) CreateSSHKey(ctx context.Context, name, publicKey string) (map[string]any, error) {
	body := map[string]any{
		"name":       name,
		"public_key": publicKey,
	}
	return c.doItem(ctx, "POST", "/ssh-keys", body, "ssh_key")
}

// GetSSHKey fetches a single SSH key by id. The SHOW route is singular.
// A 404 is returned as an *APIError recognised by IsNotFound.
func (c *Client) GetSSHKey(ctx context.Context, id string) (map[string]any, error) {
	return c.doItem(ctx, "GET", "/ssh-key/"+url.PathEscape(id), nil, "ssh_key")
}

// UpdateSSHKey patches an SSH key. fields may contain "name" and/or "comments"
// (both optional); public_key cannot be updated. The UPDATE route is singular.
// Returns the fresh object.
func (c *Client) UpdateSSHKey(ctx context.Context, id string, fields map[string]any) (map[string]any, error) {
	return c.doItem(ctx, "PATCH", "/ssh-key/"+url.PathEscape(id), fields, "ssh_key")
}

// DeleteSSHKey deletes an SSH key by id. The DELETE route is plural. A failure
// is signalled with success:false at HTTP 200, so doVoid checks the flag.
func (c *Client) DeleteSSHKey(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("DeleteSSHKey: empty id")
	}
	return c.doVoid(ctx, "DELETE", "/ssh-keys/"+url.PathEscape(id), nil)
}
