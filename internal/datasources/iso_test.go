package datasources_test

import (
	"net/http"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/iaas/terraform-provider-iaas/internal/acctest"
)

// TestUnitISO_lookup - resolves an ISO by exact name from the paginator
// response, exposing the computed id/filename. The mock matches on path only,
// so the ?search= query the client sends is ignored by the dispatcher.
func TestUnitISO_lookup(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)
	srv.Handle("GET", "/isos", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"current_page": 1,
			"data": []any{
				map[string]any{"id": "iso-alma9", "name": "AlmaLinux 9", "filename": "alma9.iso", "public": true},
				map[string]any{"id": "iso-rocky9", "name": "Rocky 9", "filename": "rocky9.iso", "public": true},
			},
			"total": 2,
		})
	})

	cfg := acctest.ProviderConfig(srv.Endpoint()) + `
data "iaas_iso" "t" {
  name = "AlmaLinux 9"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.iaas_iso.t", "id", "iso-alma9"),
					resource.TestCheckResourceAttr("data.iaas_iso.t", "filename", "alma9.iso"),
				),
			},
		},
	})
}

// TestUnitISO_noMatch - a name matching no ISO errors clearly.
func TestUnitISO_noMatch(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)
	srv.Handle("GET", "/isos", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"data": []any{
				map[string]any{"id": "iso-alma9", "name": "AlmaLinux 9", "filename": "alma9.iso", "public": true},
			},
		})
	})

	cfg := acctest.ProviderConfig(srv.Endpoint()) + `
data "iaas_iso" "t" {
  name = "Windows Server 2022"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config:      cfg,
				ExpectError: regexp.MustCompile(`no iso matching`),
			},
		},
	})
}
