// Package client provides a minimal HTTP transport for the IaaS API.
// It handles base-URL construction, bearer-token auth, and raw response
// body reading. Envelope decoding and error mapping live in separate files.
package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"
)

const (
	defaultTimeout        = 30 * time.Second
	defaultRetryBaseDelay = 500 * time.Millisecond

	// maxRetryAttempts is the total number of attempts (1 initial + up to 3 retries).
	maxRetryAttempts = 4

	// maxRetryDelay caps the sleep between retries regardless of backoff calculation.
	maxRetryDelay = 30 * time.Second
)

// Client is the HTTP transport for the IaaS REST API.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client

	// retryBaseDelay is the base sleep between retry attempts.
	// Exposed as an unexported field so tests can set it to a tiny value
	// (e.g. 1 ms) to keep the suite fast without sleeping for real seconds.
	// New() sets it to defaultRetryBaseDelay (500 ms).
	retryBaseDelay time.Duration
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
		base := http.DefaultTransport.(*http.Transport).Clone()
		base.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // intentional; caller opts in
		transport = base
	}

	return &Client{
		baseURL: strings.TrimRight(endpoint, "/"),
		token:   token,
		httpClient: &http.Client{
			Timeout:   timeout,
			Transport: transport,
		},
		retryBaseDelay: defaultRetryBaseDelay,
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
//
// Retry behaviour (C6):
//   - Retries apply to HTTP 429 and 5xx responses only; all other status codes
//     (including 401/403/404) are returned immediately without retry.
//     Transport-level errors (e.g. connection refused, TLS failure) are also
//     returned immediately without retry.
//   - Retried requests use exponential back-off + jitter, up to maxRetryAttempts
//     total tries (1 initial + 3 retries).
//   - The back-off sleep is cancellable via ctx.
//   - On the final attempt the last response is returned as-is so callers can
//     inspect the status; callers call responseError to turn it into an error.
func (c *Client) do(ctx context.Context, method, path string, body any) (*http.Response, []byte, error) {
	return c.doWithHeaders(ctx, method, path, body, nil)
}

// doWithHeaders is do with optional per-request extra headers. The fixed
// transport headers (Authorization, Accept, Content-Type) are always set; the
// extra headers are applied on top (and may override them, by design - callers
// only pass application headers such as Idempotency-Key). The retry/backoff and
// body-handling semantics are identical to do.
//
// This is the minimal seam the create path uses to attach an Idempotency-Key
// header: the Master's idempotency.user middleware reads the "Idempotency-Key"
// request header and replays the cached 2xx response for 24h, so a retried
// create that reuses the same key is deduplicated server-side. Every extra
// header is re-applied on each retry attempt (a fresh request is built per
// attempt), so the idempotency key survives a 429/5xx retry.
func (c *Client) doWithHeaders(ctx context.Context, method, path string, body any, extraHeaders map[string]string) (*http.Response, []byte, error) {
	rawURL := c.baseURL + path

	// Pre-marshal the body once; each attempt creates a new bytes.Reader.
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			return nil, nil, fmt.Errorf("marshalling request body: %w", err)
		}
	}

	for attempt := 0; attempt < maxRetryAttempts; attempt++ {
		// If this is a retry, sleep with exponential back-off + jitter,
		// but honour ctx cancellation.
		if attempt > 0 {
			// delay = base * 2^(attempt-1) + small jitter, capped at maxRetryDelay.
			shift := attempt - 1
			if shift > 10 {
				shift = 10 // guard against overflow for large maxRetryAttempts
			}
			delay := c.retryBaseDelay * (1 << uint(shift))
			if delay > maxRetryDelay {
				delay = maxRetryDelay
			}
			// Add up to 10 % jitter (deterministic range; tiny base makes it negligible in tests).
			jitter := time.Duration(rand.Int64N(int64(delay/10) + 1))
			delay += jitter

			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, nil, ctx.Err()
			case <-timer.C:
			}
		}

		// Build a fresh request for each attempt (body reader must be reset).
		var reqBody io.Reader
		if encoded != nil {
			reqBody = bytes.NewReader(encoded)
		}

		req, err := http.NewRequestWithContext(ctx, method, rawURL, reqBody)
		if err != nil {
			return nil, nil, fmt.Errorf("building request %s %s: %w", method, rawURL, err)
		}

		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Accept", "application/json")
		if encoded != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		// Apply optional per-request headers (e.g. Idempotency-Key) AFTER the
		// fixed headers so a fresh request per retry attempt always carries them.
		for k, v := range extraHeaders {
			req.Header.Set(k, v)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, nil, fmt.Errorf("executing request %s %s: %w", method, rawURL, err)
		}

		data, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return nil, nil, fmt.Errorf("reading response body: %w", readErr)
		}

		// Replace the drained body with an empty reader so callers that close
		// resp.Body don't panic.
		resp.Body = io.NopCloser(bytes.NewReader(nil))

		// Decide whether to retry.
		isRetryable := resp.StatusCode == http.StatusTooManyRequests || // 429
			(resp.StatusCode >= 500 && resp.StatusCode < 600) // 5xx

		if !isRetryable || attempt == maxRetryAttempts-1 {
			// Either not retryable, or this was the final attempt - return.
			return resp, data, nil
		}
		// Retryable and more attempts remain - loop.
	}

	// Unreachable (loop always returns on last attempt), but satisfies compiler.
	return nil, nil, fmt.Errorf("do: exhausted %d attempts for %s %s", maxRetryAttempts, method, rawURL)
}
