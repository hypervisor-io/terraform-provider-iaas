// Package client provides a minimal HTTP transport for the IaaS API.
// It handles base-URL construction, bearer-token auth, and raw response
// body reading. Envelope decoding and error mapping live in separate files.
package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

const defaultTimeout = 30 * time.Second

// Client is the HTTP transport for the IaaS REST API.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// New constructs a Client.
//
//   - endpoint: API base URL ending in "/api" (trailing slash stripped).
//   - token: bearer token sent in every request's Authorization header.
//   - timeout: http.Client.Timeout; if zero, defaults to 30 s.
//   - insecure: when true the TLS transport skips certificate verification
//     (useful for self-signed staging certificates).
func New(endpoint, token string, timeout time.Duration, insecure bool) *Client {
	if timeout == 0 {
		timeout = defaultTimeout
	}

	var transport http.RoundTripper
	if insecure {
		transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // intentional; caller opts in
		}
	}

	return &Client{
		baseURL: strings.TrimRight(endpoint, "/"),
		token:   token,
		http: &http.Client{
			Timeout:   timeout,
			Transport: transport,
		},
	}
}

// do executes an HTTP request against the API.
//
//   - method: HTTP verb (GET, POST, …).
//   - path: resource path starting with "/" (e.g. "/ssh-keys").
//   - body: if non-nil, JSON-marshalled and sent as the request body.
//
// Returns the raw *http.Response (caller must close Body if needed, though
// the body bytes are already drained), the full body bytes, and any error.
// The response's X-Request-Id header is preserved on the returned *http.Response
// for diagnostics (http.Header.Get is case-insensitive, so both
// "X-Request-Id" and "X-Request-ID" are accessible).
func (c *Client) do(ctx context.Context, method, path string, body any) (*http.Response, []byte, error) {
	url := c.baseURL + path

	var reqBody io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, nil, err
		}
		reqBody = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, nil, err
	}

	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp, nil, err
	}

	// Return the response with its headers intact (including X-Request-Id).
	// Body is already drained; replace it with an empty reader so callers
	// that close resp.Body don't panic.
	resp.Body = io.NopCloser(bytes.NewReader(nil))

	return resp, data, nil
}
