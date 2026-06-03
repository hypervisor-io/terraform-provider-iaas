package client

import "context"

// This file holds the shared request helpers every resource client method
// reuses. Each helper performs the same canonical sequence:
//
//  1. c.do(...)                 — transport (auth, retry, body read).
//  2. responseError(resp, body) — map any non-2xx to *APIError and return.
//  3. decode*                   — unwrap the envelope; decodeItem/decodeList
//                                 also surface HTTP-200 + success:false (C3).
//
// Keeping this sequence in one place means resource methods are one-liners
// (e.g. CreateSSHKey → doItem(...,"ssh_key")) and the error-mapping order is
// identical across every resource.

// doItem performs the request, maps a non-2xx response to *APIError, then
// unwraps the single object stored under key. A 200 response carrying
// success:false is also surfaced as an error (handled inside decodeItem).
//
// When key is "" the bare top-level envelope is returned (used by endpoints
// whose success payload has no nested object).
func (c *Client) doItem(ctx context.Context, method, path string, body any, key string) (map[string]any, error) {
	resp, raw, err := c.do(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	if err := responseError(resp, raw); err != nil {
		return nil, err
	}
	return decodeItem(raw, key)
}

// doList performs the request, maps a non-2xx response to *APIError, then
// unwraps a list (Laravel paginator {"data":[...]} or a bare top-level array).
// A 200 response carrying success:false is surfaced as an error.
func (c *Client) doList(ctx context.Context, method, path string, body any) ([]map[string]any, error) {
	resp, raw, err := c.do(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	if err := responseError(resp, raw); err != nil {
		return nil, err
	}
	return decodeList(raw)
}

// doVoid performs the request expecting no object in the response. It maps a
// non-2xx response to *APIError and treats a 200 response carrying
// success:false as an error (C3). Used by delete-style endpoints.
func (c *Client) doVoid(ctx context.Context, method, path string, body any) error {
	resp, raw, err := c.do(ctx, method, path, body)
	if err != nil {
		return err
	}
	if err := responseError(resp, raw); err != nil {
		return err
	}
	// Reuse the success-flag logic: decodeItem with an empty key returns the
	// bare envelope and short-circuits on success:false. We discard the object.
	_, err = decodeItem(raw, "")
	return err
}
