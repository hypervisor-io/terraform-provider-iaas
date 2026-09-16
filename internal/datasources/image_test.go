package datasources_test

import (
	"net/http"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/hypervisor-io/terraform-provider-iaas/internal/acctest"
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

// TestUnitImage_locationIDAccepted - the canonical location_id input scopes the
// search: the client forwards it as the location_id query param.
func TestUnitImage_locationIDAccepted(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)
	srv.Handle("GET", "/images/search", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("location_id"); got != "hg-loc" {
			t.Errorf("query[location_id] = %q; want %q", got, "hg-loc")
		}
		writeJSON(w, http.StatusOK, imageSearchEnvelope())
	})

	cfg := acctest.ProviderConfig(srv.Endpoint()) + `
data "iaas_image" "t" {
  name        = "Ubuntu 24.04"
  location_id = "hg-loc"
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
					resource.TestCheckResourceAttr("data.iaas_image.t", "location_id", "hg-loc"),
				),
			},
		},
	})
}

// TestUnitImage_legacyHypervisorGroupIDAlias - the deprecated
// hypervisor_group_id input still scopes the search during the overlap (the
// client still forwards it as location_id, per spec 17 C1).
func TestUnitImage_legacyHypervisorGroupIDAlias(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)
	srv.Handle("GET", "/images/search", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("location_id"); got != "hg-legacy" {
			t.Errorf("query[location_id] = %q; want %q", got, "hg-legacy")
		}
		writeJSON(w, http.StatusOK, imageSearchEnvelope())
	})

	cfg := acctest.ProviderConfig(srv.Endpoint()) + `
data "iaas_image" "t" {
  name                = "Ubuntu 24.04"
  hypervisor_group_id = "hg-legacy"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.iaas_image.t", "id", "img-2404"),
					resource.TestCheckResourceAttr("data.iaas_image.t", "hypervisor_group_id", "hg-legacy"),
				),
			},
		},
	})
}

// TestUnitImage_bothSetErrors proves that configuring both location_id and the
// deprecated hypervisor_group_id (the decoy case for spec 17 C4) fails
// validation with a message naming the conflict, rather than silently picking
// a winner - this is a search filter data source, so no HTTP call should ever
// be made.
func TestUnitImage_bothSetErrors(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)

	cfg := acctest.ProviderConfig(srv.Endpoint()) + `
data "iaas_image" "t" {
  name                = "Ubuntu 24.04"
  location_id         = "hg-loc"
  hypervisor_group_id = "hg-legacy"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config:      cfg,
				ExpectError: regexp.MustCompile("Invalid Attribute Combination"),
			},
		},
	})
}
