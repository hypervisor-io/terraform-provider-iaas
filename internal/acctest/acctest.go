// Package acctest provides acceptance-test helpers for the IaaS Terraform provider.
//
// It exposes:
//   - [Factories]: provider factory map for terraform-plugin-testing resource tests.
//   - [PreCheck]: gate function for live acceptance tests that require a real panel.
//   - [MockServer], [NewMockServer]: registerable mock HTTP server for unit-style
//     resource tests that run against canned API responses (see mockserver.go).
//   - [ProviderConfig]: HCL helper that returns a provider block pointing at a
//     mock server endpoint.
package acctest

import (
	"fmt"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"

	"github.com/hypervisor-io/terraform-provider-iaas/internal/provider"
)

// Factories maps the provider name to a protocol-v6 server built from our
// provider. Pass this map to resource.UnitTest or resource.Test as the
// ProtoV6ProviderFactories field.
//
//	resource.UnitTest(t, resource.TestCase{
//	    ProtoV6ProviderFactories: acctest.Factories,
//	    Steps: []resource.TestStep{ … },
//	})
var Factories = map[string]func() (tfprotov6.ProviderServer, error){
	"iaas": providerserver.NewProtocol6WithError(provider.New("test")()),
}

// PreCheck fails the test if the environment variables required for live
// acceptance tests are missing.
//
// Live acceptance tests are additionally gated by the TF_ACC environment
// variable (handled automatically by resource.Test). They require a reachable
// panel and an IP-locked API token. CI does NOT set TF_ACC or these variables -
// live acceptance tests are a manual gate only.
//
// Usage:
//
//	func TestAccSomeResource(t *testing.T) {
//	    resource.Test(t, resource.TestCase{
//	        PreCheck:                 func() { acctest.PreCheck(t) },
//	        ProtoV6ProviderFactories: acctest.Factories,
//	        Steps: []resource.TestStep{ … },
//	    })
//	}
func PreCheck(t *testing.T) {
	t.Helper()
	if os.Getenv("IAAS_API_ENDPOINT") == "" {
		t.Fatal("IAAS_API_ENDPOINT must be set for acceptance tests")
	}
	if os.Getenv("IAAS_API_TOKEN") == "" {
		t.Fatal("IAAS_API_TOKEN must be set for acceptance tests")
	}
}

// ProviderConfig returns the HCL `provider "iaas"` block pointing at endpoint
// (typically a MockServer's Endpoint()) with a fixed test token. Prepend it to
// each resource.TestCase Config string.
func ProviderConfig(endpoint string) string {
	return fmt.Sprintf(`
provider "iaas" {
  endpoint = %q
  token    = "test-token"
}
`, endpoint)
}
