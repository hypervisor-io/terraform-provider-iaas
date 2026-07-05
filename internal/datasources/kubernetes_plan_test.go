package datasources_test

import (
	"net/http"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/iaas/terraform-provider-iaas/internal/acctest"
)

const (
	// k8sWorkerPlansBody - FLAT Select2 from /kubernetes/search/plans (text
	// decorated with specs; "name" is the clean plan name to match on).
	k8sWorkerPlansBody = `{"results":[{"id":"ip-1","text":"std-2 - 2 CPU, 4096 MB, 80 GB","name":"std-2","cpu_cores":2,"ram":4096,"storage":80,"credit_value":1000}],"pagination":{"more":false}}`
	// k8sLbPlansBody - FLAT Select2 from /kubernetes/search/lb-plans (NO storage).
	k8sLbPlansBody = `{"results":[{"id":"lbp-1","text":"lb-2 - 2 CPU, 2048 MB","name":"lb-2","cpu_cores":2,"ram":2048,"credit_value":500}],"pagination":{"more":false}}`
)

// TestUnitKubernetesPlan_worker - kind="worker" hits /kubernetes/search/plans,
// matches on the clean plan name, and exposes id + specs.
func TestUnitKubernetesPlan_worker(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)
	srv.Handle("GET", "/kubernetes/search/plans", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(k8sWorkerPlansBody))
	})

	cfg := acctest.ProviderConfig(srv.Endpoint()) + `
data "iaas_kubernetes_plan" "t" {
  kind = "worker"
  name = "std-2"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.iaas_kubernetes_plan.t", "id", "ip-1"),
					resource.TestCheckResourceAttr("data.iaas_kubernetes_plan.t", "cpu_cores", "2"),
					resource.TestCheckResourceAttr("data.iaas_kubernetes_plan.t", "ram", "4096"),
					resource.TestCheckResourceAttr("data.iaas_kubernetes_plan.t", "storage", "80"),
					resource.TestCheckResourceAttr("data.iaas_kubernetes_plan.t", "credit_value", "1000"),
				),
			},
		},
	})
}

// TestUnitKubernetesPlan_cp - kind="cp" hits /kubernetes/search/cp-plans. The cp
// catalog is the IDENTICAL underlying instance-plan list as worker (the server
// splits the route only for semantic clarity), so it resolves the same specs.
func TestUnitKubernetesPlan_cp(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)
	srv.Handle("GET", "/kubernetes/search/cp-plans", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(k8sWorkerPlansBody))
	})

	cfg := acctest.ProviderConfig(srv.Endpoint()) + `
data "iaas_kubernetes_plan" "t" {
  kind = "cp"
  name = "std-2"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.iaas_kubernetes_plan.t", "id", "ip-1"),
					resource.TestCheckResourceAttr("data.iaas_kubernetes_plan.t", "cpu_cores", "2"),
					resource.TestCheckResourceAttr("data.iaas_kubernetes_plan.t", "ram", "4096"),
					resource.TestCheckResourceAttr("data.iaas_kubernetes_plan.t", "storage", "80"),
					resource.TestCheckResourceAttr("data.iaas_kubernetes_plan.t", "credit_value", "1000"),
				),
			},
		},
	})
}

// TestUnitKubernetesPlan_lb - kind="lb" hits the distinct /kubernetes/search/
// lb-plans route; LB plans carry no storage, so it settles to 0.
func TestUnitKubernetesPlan_lb(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)
	srv.Handle("GET", "/kubernetes/search/lb-plans", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(k8sLbPlansBody))
	})

	cfg := acctest.ProviderConfig(srv.Endpoint()) + `
data "iaas_kubernetes_plan" "t" {
  kind = "lb"
  name = "lb-2"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.iaas_kubernetes_plan.t", "id", "lbp-1"),
					resource.TestCheckResourceAttr("data.iaas_kubernetes_plan.t", "ram", "2048"),
					resource.TestCheckResourceAttr("data.iaas_kubernetes_plan.t", "storage", "0"),
				),
			},
		},
	})
}

// TestUnitKubernetesPlan_noMatch - a plan name matching nothing errors clearly.
func TestUnitKubernetesPlan_noMatch(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)
	srv.Handle("GET", "/kubernetes/search/plans", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(k8sWorkerPlansBody))
	})

	cfg := acctest.ProviderConfig(srv.Endpoint()) + `
data "iaas_kubernetes_plan" "t" {
  kind = "worker"
  name = "does-not-exist"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config:      cfg,
				ExpectError: regexp.MustCompile(`no kubernetes plan matching`),
			},
		},
	})
}
