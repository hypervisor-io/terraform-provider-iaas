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

	"github.com/hypervisor-io/terraform-provider-iaas/client"
)

// FromErr converts a client-layer error into a Terraform error diagnostic.
//
// It is the single place the provider translates *client.APIError (and plain
// errors) into diagnostics, so every resource and data source surfaces the same
// detail:
//   - the API message,
//   - any 422 field-validation errors (field: [msgs]), sorted for determinism,
//   - the NAT gateways a VPC delete is waiting for, when the API listed them,
//   - the typed refusal reason code (when the API sent one),
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
		if len(apiErr.PendingNatGateways) > 0 {
			fmt.Fprintf(&sb, "\nNAT gateways still being removed: %s", strings.Join(apiErr.PendingNatGateways, ", "))
		}
		if apiErr.Reason == client.ReasonVpcDeleting {
			sb.WriteString("\nThe target VPC is being deleted, so nothing can be created in it. " +
				"The request was not retried; use another VPC, or create the VPC again once the delete has finished.")
		}
		switch apiErr.Reason {
		case client.ReasonAddressReservationBusy, client.ReasonDatabaseBusy:
			sb.WriteString("\nAnother request was in progress and the provider retried several times. " +
				"Nothing was created, changed or deleted, so it is safe to run the same command again.")
		case client.ReasonDatabaseError:
			sb.WriteString("\nThe platform could not complete the request because of an internal database error. " +
				"Try again later; if it persists, contact the platform administrator with the request id.")
		}
		if apiErr.Reason != "" {
			fmt.Fprintf(&sb, "\n(reason: %s)", apiErr.Reason)
		}
		if apiErr.RequestID != "" {
			fmt.Fprintf(&sb, "\n(request id: %s)", apiErr.RequestID)
		}
		return diag.NewErrorDiagnostic(summary, sb.String())
	}

	return diag.NewErrorDiagnostic(summary, err.Error())
}
