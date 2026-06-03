package datasources_test

import (
	"net/http"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/iaas/terraform-provider-iaas/internal/acctest"
)

// k8sVersionsBody is the FLAT Select2 envelope the real /kubernetes/search/
// versions endpoint returns (text == semantic_version, no children optgroups).
const k8sVersionsBody = `{"results":[{"id":"ver-1","text":"1.31.4","semantic_version":"1.31.4"},{"id":"ver-2","text":"1.30.8","semantic_version":"1.30.8"}],"pagination":{"more":false}}`

// TestUnitKubernetesVersion_lookup — mock-backed data-source proof.
//
// data "iaas_kubernetes_version" "t" { name = "1.31.4" } reads GET
// /kubernetes/search/versions (flat Select2), matches on the semantic version,
// and exposes the computed id.
func TestUnitKubernetesVersion_lookup(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)
	srv.Handle("GET", "/kubernetes/search/versions", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(k8sVersionsBody))
	})

	cfg := acctest.ProviderConfig(srv.Endpoint()) + `
data "iaas_kubernetes_version" "t" {
  name = "1.31.4"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.iaas_kubernetes_version.t", "id", "ver-1"),
				),
			},
		},
	})
}

// TestUnitKubernetesVersion_noMatch — a version matching nothing errors clearly.
func TestUnitKubernetesVersion_noMatch(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)
	srv.Handle("GET", "/kubernetes/search/versions", func(w http.ResponseWriter, _ *http.Request) {
		// Server returns its (possibly filtered) list; the client still resolves
		// the unique match locally, so a body with no exact match must error.
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(k8sVersionsBody))
	})

	cfg := acctest.ProviderConfig(srv.Endpoint()) + `
data "iaas_kubernetes_version" "t" {
  name = "1.99.0"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config:      cfg,
				ExpectError: regexp.MustCompile(`no kubernetes version matching`),
			},
		},
	})
}
