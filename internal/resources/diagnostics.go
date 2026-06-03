// Package resources holds the Terraform resource implementations for the IaaS
// provider. ssh_key.go is the canonical/golden resource — every later resource
// copies its structure (model, Metadata/Schema/Configure/CRUD/ImportState).
//
// The error→diagnostics translation now lives in the neutral internal/tfdiag
// package (shared with internal/datasources); diagFromErr is a thin alias kept
// so existing resource call sites stay unchanged.
package resources

import (
	"github.com/hashicorp/terraform-plugin-framework/diag"

	"github.com/iaas/terraform-provider-iaas/internal/tfdiag"
)

// diagFromErr converts a client-layer error into a Terraform error diagnostic.
// It delegates to tfdiag.FromErr so the mapping is shared with data sources.
//
// summary is a short, resource-specific headline (e.g. "Error creating SSH key").
func diagFromErr(summary string, err error) diag.Diagnostic {
	return tfdiag.FromErr(summary, err)
}
