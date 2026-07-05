package datasources_test

import (
	"net/http"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/hypervisor-io/terraform-provider-iaas/internal/acctest"
)

const (
	planLocationID = "loc-nyc"
	planGroupID    = "pg-general"
	planGroup2ID   = "pg-compute"
)

// registerPlanCatalog wires the nested plan-groups → plans walk the data source
// performs: GET .../plan-groups (raw array) then GET .../plan-group/{pg}/plans
// (raw array) per group.
func registerPlanCatalog(srv *acctest.MockServer) {
	srv.Handle("GET", "/cloud-service/location/"+planLocationID+"/plan-groups",
		func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, []any{
				map[string]any{"id": planGroupID, "name": "general", "display_name": "General Purpose"},
				map[string]any{"id": planGroup2ID, "name": "compute", "display_name": "Compute Optimised"},
			})
		})
	srv.Handle("GET", "/cloud-service/location/"+planLocationID+"/plan-group/"+planGroupID+"/plans",
		func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, []any{
				map[string]any{"id": "plan-small", "name": "s1.small", "cpu_cores": 1, "ram": 1024, "storage": 25, "bandwidth": 1000},
				map[string]any{"id": "plan-large", "name": "s1.large", "cpu_cores": 4, "ram": 8192, "storage": 80, "bandwidth": 4000},
			})
		})
	srv.Handle("GET", "/cloud-service/location/"+planLocationID+"/plan-group/"+planGroup2ID+"/plans",
		func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, []any{
				map[string]any{"id": "plan-c1", "name": "c1.large", "cpu_cores": 8, "ram": 16384, "storage": 100, "bandwidth": 8000},
			})
		})
}

// TestUnitPlan_lookup - resolves a plan by name across plan groups, exposing the
// computed resource attributes plus plan_group_id.
func TestUnitPlan_lookup(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)
	registerPlanCatalog(srv)

	cfg := acctest.ProviderConfig(srv.Endpoint()) + `
data "iaas_plan" "t" {
  location_id = "` + planLocationID + `"
  name        = "s1.large"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.iaas_plan.t", "id", "plan-large"),
					resource.TestCheckResourceAttr("data.iaas_plan.t", "cpu_cores", "4"),
					resource.TestCheckResourceAttr("data.iaas_plan.t", "ram", "8192"),
					resource.TestCheckResourceAttr("data.iaas_plan.t", "storage", "80"),
					resource.TestCheckResourceAttr("data.iaas_plan.t", "bandwidth", "4000"),
					resource.TestCheckResourceAttr("data.iaas_plan.t", "plan_group_id", planGroupID),
				),
			},
		},
	})
}

// TestUnitPlan_noMatch - a plan name matching nothing errors clearly.
func TestUnitPlan_noMatch(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)
	registerPlanCatalog(srv)

	cfg := acctest.ProviderConfig(srv.Endpoint()) + `
data "iaas_plan" "t" {
  location_id = "` + planLocationID + `"
  name        = "does.not.exist"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config:      cfg,
				ExpectError: regexp.MustCompile(`no plan matching`),
			},
		},
	})
}
