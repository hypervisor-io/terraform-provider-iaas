package datasources_test

import (
	"net/http"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/iaas/terraform-provider-iaas/internal/acctest"
)

// TestUnitLocation_lookup - mock-backed data-source proof.
//
// data "iaas_location" "t" { name = "nyc" } reads GET
// /cloud-service/locations (paginator), matches the SLUG name, and exposes the
// computed id/display_name/country.
func TestUnitLocation_lookup(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)
	srv.Handle("GET", "/cloud-service/locations", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"current_page": 1,
			"data": []any{
				map[string]any{"id": "loc-nyc", "name": "nyc", "display_name": "New York", "country": "US"},
				map[string]any{"id": "loc-lon", "name": "lon", "display_name": "London", "country": "GB"},
			},
			"total": 2,
		})
	})

	cfg := acctest.ProviderConfig(srv.Endpoint()) + `
data "iaas_location" "t" {
  name = "nyc"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.iaas_location.t", "id", "loc-nyc"),
					resource.TestCheckResourceAttr("data.iaas_location.t", "display_name", "New York"),
					resource.TestCheckResourceAttr("data.iaas_location.t", "country", "US"),
				),
			},
		},
	})
}

// TestUnitLocation_matchByDisplayName - a lookup that matches display_name
// rather than the slug name still resolves.
func TestUnitLocation_matchByDisplayName(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)
	srv.Handle("GET", "/cloud-service/locations", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"data": []any{
				map[string]any{"id": "loc-nyc", "name": "nyc", "display_name": "New York", "country": "US"},
			},
		})
	})

	cfg := acctest.ProviderConfig(srv.Endpoint()) + `
data "iaas_location" "t" {
  name = "New York"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check:  resource.TestCheckResourceAttr("data.iaas_location.t", "id", "loc-nyc"),
			},
		},
	})
}

// TestUnitLocation_ambiguousOR - a name that matches one location's slug AND a
// different location's display_name produces a "multiple * match" error.
//
// locA has slug "london" (matches by slug); locB has display_name "london"
// (matches by display_name). The findUnique OR predicate accepts both, so two
// items pass the filter → "multiple location match" error, not a spurious
// single-match result.
func TestUnitLocation_ambiguousOR(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)
	srv.Handle("GET", "/cloud-service/locations", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"current_page": 1,
			"data": []any{
				// locA: slug "london" matches the filter by name-field equality.
				map[string]any{"id": "loc-a", "name": "london", "display_name": "London City", "country": "GB"},
				// locB: display_name "london" matches the filter by display_name equality.
				map[string]any{"id": "loc-b", "name": "ldn", "display_name": "london", "country": "GB"},
			},
			"total": 2,
		})
	})

	cfg := acctest.ProviderConfig(srv.Endpoint()) + `
data "iaas_location" "t" {
  name = "london"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config:      cfg,
				ExpectError: regexp.MustCompile(`multiple .* match`),
			},
		},
	})
}

// TestUnitLocation_noMatch - a name matching nothing errors clearly.
func TestUnitLocation_noMatch(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)
	srv.Handle("GET", "/cloud-service/locations", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"data": []any{
				map[string]any{"id": "loc-nyc", "name": "nyc", "display_name": "New York", "country": "US"},
			},
		})
	})

	cfg := acctest.ProviderConfig(srv.Endpoint()) + `
data "iaas_location" "t" {
  name = "nowhere"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config:      cfg,
				ExpectError: regexp.MustCompile(`no location matching`),
			},
		},
	})
}
