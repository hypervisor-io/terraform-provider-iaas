package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ipLockHint is appended to all 401/403 error messages.
// The token is IP-locked (only works from the IP it was registered with).
// Callers should also verify the token is enabled, not expired, and has
// the required scope / subuser permission.
const ipLockHint = "check that your token is registered from this IP address (tokens are IP-locked), " +
	"the token is enabled and not expired, and has the required scope / subuser permission"

// APIError represents a non-2xx response from the IaaS API.
// It is a plain Go error - no terraform-plugin-framework dependency.
// Resource-layer code translates *APIError → Terraform diagnostics.
type APIError struct {
	// Status is the HTTP status code (e.g. 404, 422).
	Status int
	// Message is the human-readable summary from the API's "message" field,
	// or a status-based default when absent.
	// For 401/403 it also includes the IP-lock/scope hint (ipLockHint).
	Message string
	// FieldErrors is populated for 422 Unprocessable Entity responses;
	// keys are field names, values are slices of validation messages.
	FieldErrors map[string][]string
	// RequestID holds the X-Request-Id response header when present.
	RequestID string
	// Reason is the API's machine-readable "reason" code when the body carries
	// one (e.g. "volume_operation_refused", "volume_attached"). Empty when the
	// API sent none. It is the typed refusal callers branch on; never parse
	// Message for it.
	Reason string
	// RetryAfter is the wait the API asked for in a Retry-After response
	// header (delta-seconds, or an HTTP date converted to the time left), for
	// example on a volume cooldown (HTTP 429, reason volume_cooldown). Zero
	// when the header is absent, unparsable, zero or in the past. It is the
	// raw server value; callers apply their own bounds.
	RetryAfter time.Duration
}

// Error implements the error interface.
// Format: "<status>: <message>[ (reason: <code>)][; <field>: [msg, ...]...][; (request id: <id>)]"
func (e *APIError) Error() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%d: %s", e.Status, e.Message)
	if e.Reason != "" {
		fmt.Fprintf(&sb, " (reason: %s)", e.Reason)
	}
	fields := make([]string, 0, len(e.FieldErrors))
	for f := range e.FieldErrors {
		fields = append(fields, f)
	}
	sort.Strings(fields)
	for _, f := range fields {
		fmt.Fprintf(&sb, "; %s: [%s]", f, strings.Join(e.FieldErrors[f], ", "))
	}
	if e.RequestID != "" {
		fmt.Fprintf(&sb, " (request id: %s)", e.RequestID)
	}
	return sb.String()
}

// responseError returns a *APIError for non-2xx responses, or nil for 2xx.
//
// Parsing rules:
//   - 2xx → nil.
//   - 422 → parse {"errors":{field:[...]}} into FieldErrors, plus top-level "message".
//   - 401/403 → Message includes ipLockHint.
//   - others (404, generic 4xx, 5xx) → Message from body "message" field if present,
//     else a status-based default.
//
// RequestID is set from the X-Request-Id response header when present.
func responseError(resp *http.Response, body []byte) error {
	// 2xx is not an error.
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	apiErr := &APIError{
		Status:     resp.StatusCode,
		RequestID:  resp.Header.Get("X-Request-Id"),
		RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
	}

	// Try to parse the body as JSON to extract message / errors. Most
	// controllers return the Laravel-standard {"message":…} envelope, but a few
	// (notably the Kubernetes KubeconfigController) return {"error":…} instead -
	// fall back to that key when "message" is absent so the specific text is not
	// lost.
	var parsed struct {
		Message string                     `json:"message"`
		Error   string                     `json:"error"`
		Errors  map[string]json.RawMessage `json:"errors"`
		Reason  json.RawMessage            `json:"reason"`
	}
	_ = json.Unmarshal(body, &parsed) // best-effort; ignore unmarshal failures

	// reason is a string code on every typed refusal; any other JSON shape is
	// ignored rather than failing the whole parse.
	var reason string
	if len(parsed.Reason) > 0 && json.Unmarshal(parsed.Reason, &reason) == nil {
		apiErr.Reason = reason
	}

	// Base message from the parsed body, or a status-based fallback.
	msg := parsed.Message
	if msg == "" {
		msg = parsed.Error
	}
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
	}

	switch resp.StatusCode {
	case http.StatusUnprocessableEntity: // 422
		apiErr.Message = msg
		if len(parsed.Errors) > 0 {
			fieldErrs := make(map[string][]string, len(parsed.Errors))
			for field, raw := range parsed.Errors {
				var msgs []string
				if err := json.Unmarshal(raw, &msgs); err == nil {
					fieldErrs[field] = msgs
				}
			}
			apiErr.FieldErrors = fieldErrs
		}

	case http.StatusUnauthorized, http.StatusForbidden: // 401, 403
		apiErr.Message = msg + "; " + ipLockHint

	default:
		apiErr.Message = msg
	}

	return apiErr
}

// maxRetryAfter caps a parsed Retry-After so a hostile or broken value can
// never overflow a time.Duration; callers bound it further.
const maxRetryAfter = 24 * time.Hour

// parseRetryAfter reads a Retry-After header value (RFC 9110 section 10.2.3):
// a non-negative integer number of seconds, or an HTTP date. It returns 0 for
// an absent, malformed, zero or past value.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if v[0] >= '0' && v[0] <= '9' {
		for i := 0; i < len(v); i++ {
			if v[i] < '0' || v[i] > '9' {
				return 0
			}
		}
		secs, err := strconv.ParseInt(v, 10, 64)
		if err != nil || secs > int64(maxRetryAfter/time.Second) {
			// Out of range for an int64: absurdly large, cap it.
			return maxRetryAfter
		}
		return time.Duration(secs) * time.Second
	}
	t, err := http.ParseTime(v)
	if err != nil {
		return 0
	}
	d := t.Sub(now)
	if d <= 0 {
		return 0
	}
	if d > maxRetryAfter {
		return maxRetryAfter
	}
	return d
}

// IsNotFound reports whether err is an *APIError with HTTP status 404.
// Resource Read handlers use this to remove a resource from state when the API
// returns 404 (resource destroyed outside Terraform).
func IsNotFound(err error) bool {
	var e *APIError
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}
