package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// ipLockHint is appended to all 401/403 error messages.
// The token is IP-locked (only works from the IP it was registered with).
// Callers should also verify the token is enabled, not expired, and has
// the required scope / subuser permission.
const ipLockHint = "check that your token is registered from this IP address (tokens are IP-locked), " +
	"the token is enabled and not expired, and has the required scope / subuser permission"

// APIError represents a non-2xx response from the IaaS API.
// It is a plain Go error — no terraform-plugin-framework dependency.
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
}

// Error implements the error interface.
// Format: "<status>: <message>[; <field>: [msg, ...]...][; (request id: <id>)]"
func (e *APIError) Error() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%d: %s", e.Status, e.Message)
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
		Status:    resp.StatusCode,
		RequestID: resp.Header.Get("X-Request-Id"),
	}

	// Try to parse the body as JSON to extract message / errors.
	var parsed struct {
		Message string                     `json:"message"`
		Errors  map[string]json.RawMessage `json:"errors"`
	}
	_ = json.Unmarshal(body, &parsed) // best-effort; ignore unmarshal failures

	// Base message from the parsed body, or a status-based fallback.
	msg := parsed.Message
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

// IsNotFound reports whether err is an *APIError with HTTP status 404.
// Resource Read handlers use this to remove a resource from state when the API
// returns 404 (resource destroyed outside Terraform).
func IsNotFound(err error) bool {
	var e *APIError
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}
