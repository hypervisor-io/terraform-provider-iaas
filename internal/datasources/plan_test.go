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

// registerGPUPlanCatalog serves the user_api plan list in the exact shape Master
// answers (UserApi\CloudServiceController::planGroupPlans): a GPU plan carries
// the `gpu` object including `available`, a plan without a GPU carries gpu:null.
func registerGPUPlanCatalog(srv *acctest.MockServer) {
	srv.Handle("GET", "/cloud-service/location/"+planLocationID+"/plan-groups",
		func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, []any{map[string]any{"id": planGroupID, "name": "general"}})
		})
	srv.Handle("GET", "/cloud-service/location/"+planLocationID+"/plan-group/"+planGroupID+"/plans",
		func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, []any{
				map[string]any{"id": "plan-gpu", "name": "g1.gpu", "cpu_cores": 8, "ram": 32768, "storage": 200, "bandwidth": 4000, "gpu_count": 1,
					"gpu": map[string]any{"count": 1, "vendor": "nvidia", "vram_min_gb": 24, "mode": "passthrough", "profile": nil,
						"label": "1 × NVIDIA RTX A5000 · 24 GB", "available": true}},
				map[string]any{"id": "plan-plain", "name": "g1.plain", "cpu_cores": 1, "ram": 1024, "storage": 25, "bandwidth": 1000, "gpu_count": 0, "gpu": nil},
			})
		})
}

// TestUnitPlan_gpuBlock - a GPU plan exposes the gpu block; a non-GPU plan in
// the same group leaves it null (no attribute in state).
func TestUnitPlan_gpuBlock(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)
	registerGPUPlanCatalog(srv)

	cfg := acctest.ProviderConfig(srv.Endpoint()) + `
data "iaas_plan" "gpu" {
  location_id = "` + planLocationID + `"
  name        = "g1.gpu"
}
data "iaas_plan" "plain" {
  location_id = "` + planLocationID + `"
  name        = "g1.plain"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.iaas_plan.gpu", "gpu.count", "1"),
					resource.TestCheckResourceAttr("data.iaas_plan.gpu", "gpu.vendor", "nvidia"),
					resource.TestCheckResourceAttr("data.iaas_plan.gpu", "gpu.vram_min_gb", "24"),
					resource.TestCheckResourceAttr("data.iaas_plan.gpu", "gpu.mode", "passthrough"),
					resource.TestCheckNoResourceAttr("data.iaas_plan.gpu", "gpu.profile"),
					resource.TestCheckResourceAttr("data.iaas_plan.gpu", "gpu.label", "1 × NVIDIA RTX A5000 · 24 GB"),
					resource.TestCheckResourceAttr("data.iaas_plan.gpu", "gpu.available", "true"),
					resource.TestCheckNoResourceAttr("data.iaas_plan.plain", "gpu.count"),
					resource.TestCheckNoResourceAttr("data.iaas_plan.plain", "gpu.label"),
				),
			},
		},
	})
}
