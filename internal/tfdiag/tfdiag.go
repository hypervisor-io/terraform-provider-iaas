// Package tfdiag holds the single, shared translation from a client-layer error
// into a Terraform diagnostic. It lives in its own neutral package so that both
// internal/resources and internal/datasources can reuse it without
// datasources → resources coupling (DRY: one error→diagnostics mapping for the
// whole provider).
package tfdiag

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"

	"github.com/hypervisor-io/terraform-provider-iaas/internal/client"
)

// FromErr converts a client-layer error into a Terraform error diagnostic.
//
// It is the single place the provider translates *client.APIError (and plain
// errors) into diagnostics, so every resource and data source surfaces the same
// detail:
//   - the API message,
//   - any 422 field-validation errors (field: [msgs]), sorted for determinism,
//   - the request id (when present) for support correlation.
//
// summary is a short, context-specific headline (e.g. "Error reading location").
func FromErr(summary string, err error) diag.Diagnostic {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		var sb strings.Builder
		sb.WriteString(apiErr.Message)

		if len(apiErr.FieldErrors) > 0 {
			fields := make([]string, 0, len(apiErr.FieldErrors))
			for f := range apiErr.FieldErrors {
				fields = append(fields, f)
			}
			sort.Strings(fields)
			for _, f := range fields {
				fmt.Fprintf(&sb, "\n  - %s: %s", f, strings.Join(apiErr.FieldErrors[f], "; "))
			}
		}
		if apiErr.RequestID != "" {
			fmt.Fprintf(&sb, "\n(request id: %s)", apiErr.RequestID)
		}
		return diag.NewErrorDiagnostic(summary, sb.String())
	}

	return diag.NewErrorDiagnostic(summary, err.Error())
}
