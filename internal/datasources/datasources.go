// Package datasources holds the Terraform data-source implementations for the
// IaaS provider. location.go is the GOLDEN data source — it establishes the
// read-only lookup-by-name pattern every later data source copies:
//
//  1. an input filter attribute (the human name, Required) + Computed outputs;
//  2. Configure pulls *client.Client from req.ProviderData (nil-guard + typed
//     error), identically to resources;
//  3. Read lists from the API, finds the UNIQUE match, and errors clearly on
//     zero ("no <type> matching name %q") or multiple ("multiple <type> match
//     name %q; refine") matches, then sets state.
//
// API errors are surfaced via the shared internal/tfdiag.FromErr so resources
// and data sources translate client errors identically (no datasources →
// resources coupling).
package datasources

import (
	"fmt"

	"github.com/iaas/terraform-provider-iaas/internal/client"
)

// configureClient is the body every data source's Configure shares: it pulls
// the shared *client.Client from req.ProviderData, tolerating the nil
// ProviderData the framework passes before the provider's own Configure runs,
// and surfacing a typed-mismatch as a provider-bug error.
//
// It returns (client, problem). When problem is non-empty the caller adds it as
// an error diagnostic. A nil client with empty problem means "not configured
// yet" — the caller should simply return.
func configureClient(providerData any) (c *client.Client, problem string) {
	if providerData == nil {
		return nil, ""
	}
	cl, ok := providerData.(*client.Client)
	if !ok {
		return nil, fmt.Sprintf("Expected *client.Client, got: %T. This is a provider bug; please report it.", providerData)
	}
	return cl, ""
}

// findUnique returns the single item from items for which match(item) is true.
// It implements the data-source matching contract: exactly one match wins; zero
// or multiple matches are an error keyed off noun (e.g. "location"):
//
//	zero     → error "no <noun> matching name %q"
//	multiple → error "multiple <noun> match name %q; refine your filter"
//
// name is the user-supplied filter value, used only for the error messages.
func findUnique(items []map[string]any, noun, name string, match func(map[string]any) bool) (map[string]any, error) {
	var found []map[string]any
	for _, it := range items {
		if match(it) {
			found = append(found, it)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return nil, fmt.Errorf("no %s matching name %q", noun, name)
	default:
		return nil, fmt.Errorf("multiple %s match name %q; refine your filter", noun, name)
	}
}

// strField reads a string field from an API object map. A present string wins;
// a present null or absent key yields "". Non-string JSON scalars are coerced
// defensively so an int id never panics the lookup.
func strField(obj map[string]any, key string) string {
	raw, ok := obj[key]
	if !ok || raw == nil {
		return ""
	}
	if s, ok := raw.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", raw)
}

// int64Field reads a numeric field from an API object map and returns it as an
// int64 (JSON numbers decode to float64). Absent/non-numeric yields 0.
func int64Field(obj map[string]any, key string) int64 {
	raw, ok := obj[key]
	if !ok || raw == nil {
		return 0
	}
	switch v := raw.(type) {
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	default:
		return 0
	}
}

// boolField reads a boolean field from an API object map. JSON booleans decode
// to bool; a present numeric 1/0 (some endpoints emit int flags) is coerced.
// Absent/other yields false.
func boolField(obj map[string]any, key string) bool {
	raw, ok := obj[key]
	if !ok || raw == nil {
		return false
	}
	switch v := raw.(type) {
	case bool:
		return v
	case float64:
		return v != 0
	default:
		return false
	}
}
