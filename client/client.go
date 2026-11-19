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

	// Busy refusals (HTTP 409 reason address_reservation_busy or concurrent_update_retry,
	// always with a Retry-After header, normally 1 second). The Master states
	// that the request changed nothing ("another request is in progress, safe
	// to retry"), so replaying it can neither duplicate a create nor repeat a
	// delete; that holds for every verb, including the single-attempt actions
	// that never replay a 429/5xx. They are the ONLY 409 reasons retried here:
	// a real conflict (plan_in_use, load_balancer_destroy_failed,
	// vpc_delete_incomplete, ...) is returned at once.
	maxBusyAttempts     = 5                // 1 initial + up to 4 retries
	defaultBusyWait     = 1 * time.Second  // when the header is absent or unparsable
	defaultBusyWaitCap  = 10 * time.Second // upper bound of ONE wait, whatever Retry-After says
	defaultBusyTotalCap = 30 * time.Second // upper bound of all busy waits of one call
)

// Reasons of the typed busy refusals (see maxBusyAttempts).
const (
	ReasonAddressReservationBusy = "address_reservation_busy"
	ReasonConcurrentUpdateRetry  = "concurrent_update_retry"
	ReasonDatabaseError          = "database_error"
	// ReasonDeleteInProgress: a repeated instance delete while one is running.
	// Nothing was started or changed, so for a caller that wants the instance
	// gone it is the same as "accepted".
	ReasonDeleteInProgress = "delete_in_progress"
	// ReasonDeleteState: a deploy or reinstall refused (before any task) for an
	// instance whose delete is running or failed.
	ReasonDeleteState = "delete_state"
	// ReasonPublicIPOperationInFlight: an instance delete refused while a public
	// address change is on its way to the host (Retry-After 120); it clears by itself.
	ReasonPublicIPOperationInFlight = "public_ip_operation_in_flight"
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

	// busyDefaultWait, busyWaitCap and busyTotalCap bound the busy-refusal
	// retry (see maxBusyAttempts). Unexported so tests can shrink them.
	busyDefaultWait time.Duration
	busyWaitCap     time.Duration
	busyTotalCap    time.Duration
	busyAttempts    int
}

// WithBusyRetry sets the bounds of the busy-refusal retry (HTTP 409
// address_reservation_busy / concurrent_update_retry): attempts is the TOTAL number of
// tries (1 means never retry), perWait caps one wait and total caps all waits
// of one call. Values below 1 or 0 keep the defaults (5 tries, 10 s, 30 s).
// Interactive callers such as the MCP server use a short budget so a tool call
// never sits silently for long. It returns c for chaining; call it before the
// client is shared between goroutines.
func (c *Client) WithBusyRetry(attempts int, perWait, total time.Duration) *Client {
	if attempts >= 1 {
		c.busyAttempts = attempts
	}
	if perWait > 0 {
		c.busyWaitCap = perWait
	}
	if total > 0 {
		c.busyTotalCap = total
	}
	return c
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
		retryBaseDelay:  defaultRetryBaseDelay,
		busyDefaultWait: defaultBusyWait,
		busyWaitCap:     defaultBusyWaitCap,
		busyTotalCap:    defaultBusyTotalCap,
		busyAttempts:    maxBusyAttempts,
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
	return c.doWithAttempts(ctx, method, path, body, extraHeaders, maxRetryAttempts)
}

// doWithAttempts lets non-idempotent actions opt out of replay while sharing the
// same authentication, body reading, and error handling as ordinary requests.
//
// On top of the 429/5xx replay it waits out the two typed busy refusals (HTTP
// 409 address_reservation_busy / concurrent_update_retry) up to busyAttempts times (default maxBusyAttempts),
// honouring Retry-After within busyWaitCap per wait and busyTotalCap overall.
// When it gives up, the last 409 is returned unchanged, so the caller's error
// carries the server's own message and reason.
func (c *Client) doWithAttempts(ctx context.Context, method, path string, body any, extraHeaders map[string]string, attempts int) (*http.Response, []byte, error) {
	var waited time.Duration
	for busy := 1; ; busy++ {
		resp, data, err := c.doAttempts(ctx, method, path, body, extraHeaders, attempts)
		if err != nil || busy >= c.busyAttempts || !isBusyRefusal(resp.StatusCode, resp.Header, data) {
			return resp, data, err
		}
		wait := c.busyWait(resp.Header.Get("Retry-After"))
		if waited+wait > c.busyTotalCap {
			return resp, data, nil
		}
		waited += wait
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// isBusyRefusal reports whether the response is one of the two typed 409 busy
// refusals that guarantee nothing was created, changed or deleted.
//
// The Retry-After header is REQUIRED as well as the reason: the panel sends it
// on every such answer, and the reason `database_busy` is a DIFFERENT refusal, sent without
// that header, by the managed-database backup and restore admission ("another
// operation is running on this database": a real conflict that lasts until the
// other operation ends, and the MCP server sends a backup once). Retrying
// those would be wrong, so a 409 without Retry-After is never retried.
func isBusyRefusal(status int, header http.Header, body []byte) bool {
	if status != http.StatusConflict || parseRetryAfter(header.Get("Retry-After"), time.Now()) <= 0 {
		return false
	}
	var parsed struct {
		Reason json.RawMessage `json:"reason"`
	}
	if json.Unmarshal(body, &parsed) != nil {
		return false
	}
	var reason string
	if json.Unmarshal(parsed.Reason, &reason) != nil {
		return false
	}
	return reason == ReasonAddressReservationBusy || reason == ReasonConcurrentUpdateRetry
}

// busyWait is the Retry-After the server named (default busyDefaultWait),
// capped at busyWaitCap, plus up to 25 % jitter so parallel resources of one
// apply do not retry in lockstep.
func (c *Client) busyWait(header string) time.Duration {
	wait := parseRetryAfter(header, time.Now())
	if wait <= 0 {
		wait = c.busyDefaultWait
	}
	if wait > c.busyWaitCap {
		wait = c.busyWaitCap
	}
	return wait + time.Duration(rand.Int64N(int64(wait/4)+1))
}

// doAttempts is one pass of the 429/5xx retry loop (see doWithAttempts).
func (c *Client) doAttempts(ctx context.Context, method, path string, body any, extraHeaders map[string]string, attempts int) (*http.Response, []byte, error) {
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

	for attempt := 0; attempt < attempts; attempt++ {
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

		if !isRetryable || attempt == attempts-1 {
			// Either not retryable, or this was the final attempt - return.
			return resp, data, nil
		}
		// Retryable and more attempts remain - loop.
	}

	// Unreachable (loop always returns on last attempt), but satisfies compiler.
	return nil, nil, fmt.Errorf("do: exhausted %d attempts for %s %s", attempts, method, rawURL)
}
