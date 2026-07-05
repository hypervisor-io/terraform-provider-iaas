package datasources_test

import (
	"net/http"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/iaas/terraform-provider-iaas/internal/acctest"
)

// imageSearchEnvelope is the Select2 grouped envelope GET /images/search returns.
func imageSearchEnvelope() map[string]any {
	return map[string]any{
		"results": []any{
			map[string]any{
				"text": "Ubuntu",
				"children": []any{
					map[string]any{"id": "img-2204", "text": "Ubuntu 22.04", "distro": "ubuntu"},
					map[string]any{"id": "img-2404", "text": "Ubuntu 24.04", "distro": "ubuntu"},
				},
			},
			map[string]any{
				"text": "Debian",
				"children": []any{
					map[string]any{"id": "img-deb12", "text": "Debian 12", "distro": "debian"},
				},
			},
		},
	}
}

// TestUnitImage_lookup - resolves an image by its Select2 child text (exact
// match), exposing the computed id/distro. The mock matches on path only, so
// the query string the client appends is ignored by the dispatcher.
func TestUnitImage_lookup(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)
	srv.Handle("GET", "/images/search", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, imageSearchEnvelope())
	})

	cfg := acctest.ProviderConfig(srv.Endpoint()) + `
data "iaas_image" "t" {
  name = "Ubuntu 24.04"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.iaas_image.t", "id", "img-2404"),
					resource.TestCheckResourceAttr("data.iaas_image.t", "distro", "ubuntu"),
				),
			},
		},
	})
}

// TestUnitImage_noMatch - a name matching no child errors clearly.
func TestUnitImage_noMatch(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)
	srv.Handle("GET", "/images/search", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, imageSearchEnvelope())
	})

	cfg := acctest.ProviderConfig(srv.Endpoint()) + `
data "iaas_image" "t" {
  name = "Windows 11"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config:      cfg,
				ExpectError: regexp.MustCompile(`no image matching`),
			},
		},
	})
}
