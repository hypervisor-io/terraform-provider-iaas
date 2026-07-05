package datasources_test

import (
	"net/http"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/hypervisor-io/terraform-provider-iaas/internal/acctest"
)

// k8sVpcsBody is the FLAT Select2 envelope /kubernetes/search/vpcs returns.
const k8sVpcsBody = `{"results":[{"id":"vpc-1","text":"prod-vpc (10.0.0.0/16)","name":"prod-vpc","cidr":"10.0.0.0/16","hypervisor_group_id":"hg-1","has_nat_gateway":true,"nat_public_ip":"203.0.113.10"},{"id":"vpc-2","text":"stg-vpc (10.1.0.0/16)","name":"stg-vpc","cidr":"10.1.0.0/16","hypervisor_group_id":"hg-1","has_nat_gateway":false,"nat_public_ip":null}],"pagination":{"more":false}}`

func TestUnitKubernetesVPC_lookupByName(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)
	srv.Handle("GET", "/kubernetes/search/vpcs", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("search"); got != "prod-vpc" {
			t.Errorf("query[search] = %q; want %q (the name filter must be forwarded)", got, "prod-vpc")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(k8sVpcsBody))
	})

	cfg := acctest.ProviderConfig(srv.Endpoint()) + `
data "iaas_kubernetes_vpc" "t" {
  name = "prod-vpc"
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.iaas_kubernetes_vpc.t", "id", "vpc-1"),
					resource.TestCheckResourceAttr("data.iaas_kubernetes_vpc.t", "cidr", "10.0.0.0/16"),
					resource.TestCheckResourceAttr("data.iaas_kubernetes_vpc.t", "has_nat_gateway", "true"),
				),
			},
		},
	})
}

func TestUnitKubernetesVPC_lookupByNameAndHypervisorGroupID(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)
	srv.Handle("GET", "/kubernetes/search/vpcs", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("search"); got != "prod-vpc" {
			t.Errorf("query[search] = %q; want %q (the name filter must be forwarded)", got, "prod-vpc")
		}
		if got := r.URL.Query().Get("hypervisor_group_id"); got != "hg-1" {
			t.Errorf("query[hypervisor_group_id] = %q; want %q", got, "hg-1")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(k8sVpcsBody))
	})

	cfg := acctest.ProviderConfig(srv.Endpoint()) + `
data "iaas_kubernetes_vpc" "t" {
  name                = "prod-vpc"
  hypervisor_group_id = "hg-1"
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.iaas_kubernetes_vpc.t", "id", "vpc-1"),
					resource.TestCheckResourceAttr("data.iaas_kubernetes_vpc.t", "cidr", "10.0.0.0/16"),
				),
			},
		},
	})
}

func TestUnitKubernetesVPC_noMatch(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)
	srv.Handle("GET", "/kubernetes/search/vpcs", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(k8sVpcsBody))
	})

	cfg := acctest.ProviderConfig(srv.Endpoint()) + `
data "iaas_kubernetes_vpc" "t" {
  name = "nope"
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config:      cfg,
				ExpectError: regexp.MustCompile(`no kubernetes vpc matching`),
			},
		},
	})
}
