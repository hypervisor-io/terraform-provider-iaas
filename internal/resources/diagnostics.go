// Package resources holds the Terraform resource implementations for the IaaS
// provider. ssh_key.go is the canonical/golden resource — every later resource
// copies its structure (model, Metadata/Schema/Configure/CRUD/ImportState) and
// reuses the shared helpers in this file (diagFromErr).
package resources

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"

	"github.com/iaas/terraform-provider-iaas/internal/client"
)

// diagFromErr converts a client-layer error into a Terraform error diagnostic.
//
// It is the single place resources translate *client.APIError (and plain
// errors) into diagnostics, so every resource surfaces the same detail:
//   - the API message,
//   - any 422 field-validation errors (field: [msgs]), sorted for determinism,
//   - the request id (when present) for support correlation.
//
// summary is a short, resource-specific headline (e.g. "Error creating SSH key").
func diagFromErr(summary string, err error) diag.Diagnostic {
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
