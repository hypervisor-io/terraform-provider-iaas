package client

import (
	"context"
	"fmt"
	"net/url"
)

// MicroVM API key endpoints (user_api, bearer token).
//
//	INDEX  GET    /microvm/api-keys                     -> {success,keys:[...]}
//	CREATE POST   /microvm/api-keys        body {name}  -> {success,key:{...},plaintext}
//	DELETE DELETE /microvm/api-key/{id}                 -> {success,message}
//
// plaintext is returned ONLY by CreateMicrovmApiKey and never again by any
// other call - the provider resource must persist it to state at create
// time and never attempt to refresh it on Read.

// ListMicrovmApiKeys returns every MicroVM Sandboxes API key on the account
// (id/name/prefix only; the listing never carries key material).
func (c *Client) ListMicrovmApiKeys(ctx context.Context) ([]map[string]any, error) {
	return c.namedList(ctx, "GET", "/microvm/api-keys", "keys")
}

// CreateMicrovmApiKey creates a MicroVM Sandboxes API key with the given
// display name. The bare envelope is returned so the caller can read both the
// created "key" object (id/name/prefix) and the shown-once "plaintext" secret.
func (c *Client) CreateMicrovmApiKey(ctx context.Context, name string) (map[string]any, error) {
	return c.doItem(ctx, "POST", "/microvm/api-keys", map[string]any{"name": name}, "")
}

// DeleteMicrovmApiKey deletes a MicroVM Sandboxes API key by id. A failure is
// signalled with success:false at HTTP 200, so doVoid checks the flag.
func (c *Client) DeleteMicrovmApiKey(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("DeleteMicrovmApiKey: empty id")
	}
	return c.doVoid(ctx, "DELETE", "/microvm/api-key/"+url.PathEscape(id), nil)
}
