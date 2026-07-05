package datasources_test

import (
	"net/http"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/iaas/terraform-provider-iaas/internal/acctest"
)

// k8sVpcsBody is the FLAT Select2 envelope /kubernetes/search/vpcs returns.
const k8sVpcsBody = `{"results":[{"id":"vpc-1","text":"prod-vpc (10.0.0.0/16)","name":"prod-vpc","cidr":"10.0.0.0/16","hypervisor_group_id":"hg-1","has_nat_gateway":true,"nat_public_ip":"203.0.113.10"},{"id":"vpc-2","text":"stg-vpc (10.1.0.0/16)","name":"stg-vpc","cidr":"10.1.0.0/16","hypervisor_group_id":"hg-1","has_nat_gateway":false,"nat_public_ip":null}],"pagination":{"more":false}}`

func TestUnitKubernetesVPC_lookupByName(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)
	srv.Handle("GET", "/kubernetes/search/vpcs", func(w http.ResponseWriter, _ *http.Request) {
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
