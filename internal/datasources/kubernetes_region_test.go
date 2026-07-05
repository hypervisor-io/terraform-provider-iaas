package datasources_test

import (
	"net/http"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/iaas/terraform-provider-iaas/internal/acctest"
)

// k8sRegionsBody is the FLAT Select2 envelope the real /kubernetes/search/
// regions endpoint returns (text == region name, plus slug + feature flags).
const k8sRegionsBody = `{"results":[{"id":"hg-1","text":"NYC1","slug":"nyc1","kubernetes_enabled":1,"vpc_enabled":1,"lb_enabled":1},{"id":"hg-2","text":"LON1","slug":"lon1","kubernetes_enabled":1,"vpc_enabled":1,"lb_enabled":1}],"pagination":{"more":false}}`

// TestUnitKubernetesRegion_lookupBySlug - a lookup matching the region slug
// resolves the id + display name.
func TestUnitKubernetesRegion_lookupBySlug(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)
	srv.Handle("GET", "/kubernetes/search/regions", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(k8sRegionsBody))
	})

	cfg := acctest.ProviderConfig(srv.Endpoint()) + `
data "iaas_kubernetes_region" "t" {
  name = "nyc1"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.iaas_kubernetes_region.t", "id", "hg-1"),
					resource.TestCheckResourceAttr("data.iaas_kubernetes_region.t", "slug", "nyc1"),
				),
			},
		},
	})
}

// TestUnitKubernetesRegion_lookupByName - a lookup matching the region display
// name (text) also resolves.
func TestUnitKubernetesRegion_lookupByName(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)
	srv.Handle("GET", "/kubernetes/search/regions", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(k8sRegionsBody))
	})

	cfg := acctest.ProviderConfig(srv.Endpoint()) + `
data "iaas_kubernetes_region" "t" {
  name = "LON1"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check:  resource.TestCheckResourceAttr("data.iaas_kubernetes_region.t", "id", "hg-2"),
			},
		},
	})
}

// TestUnitKubernetesRegion_noMatch - a region matching nothing errors clearly.
func TestUnitKubernetesRegion_noMatch(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)
	srv.Handle("GET", "/kubernetes/search/regions", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(k8sRegionsBody))
	})

	cfg := acctest.ProviderConfig(srv.Endpoint()) + `
data "iaas_kubernetes_region" "t" {
  name = "nowhere"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config:      cfg,
				ExpectError: regexp.MustCompile(`no kubernetes region matching`),
			},
		},
	})
}
