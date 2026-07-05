package datasources_test

import (
	"net/http"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/iaas/terraform-provider-iaas/internal/acctest"
)

// k8sSubnetsBody is the FLAT Select2 envelope /kubernetes/search/subnets returns.
const k8sSubnetsBody = `{"results":[{"id":"sn-1","text":"cp-subnet (10.0.1.0/24) - PRIVATE","name":"cp-subnet","cidr":"10.0.1.0/24","type":"private"},{"id":"sn-2","text":"pub-subnet (10.0.2.0/24) - PUBLIC","name":"pub-subnet","cidr":"10.0.2.0/24","type":"public"}],"pagination":{"more":false}}`

func TestUnitKubernetesSubnet_lookupByName(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)
	srv.Handle("GET", "/kubernetes/search/subnets", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("vpc_id"); got != "vpc-1" {
			t.Errorf("query[vpc_id] = %q; want %q (the required vpc_id must be sent)", got, "vpc-1")
		}
		if got := r.URL.Query().Get("search"); got != "cp-subnet" {
			t.Errorf("query[search] = %q; want %q (the name filter must be forwarded)", got, "cp-subnet")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(k8sSubnetsBody))
	})

	cfg := acctest.ProviderConfig(srv.Endpoint()) + `
data "iaas_kubernetes_subnet" "t" {
  vpc_id = "vpc-1"
  name   = "cp-subnet"
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.iaas_kubernetes_subnet.t", "id", "sn-1"),
					resource.TestCheckResourceAttr("data.iaas_kubernetes_subnet.t", "cidr", "10.0.1.0/24"),
					resource.TestCheckResourceAttr("data.iaas_kubernetes_subnet.t", "type", "private"),
				),
			},
		},
	})
}

func TestUnitKubernetesSubnet_lookupByNameAndType(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)
	srv.Handle("GET", "/kubernetes/search/subnets", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("vpc_id"); got != "vpc-1" {
			t.Errorf("query[vpc_id] = %q; want %q (the required vpc_id must be sent)", got, "vpc-1")
		}
		if got := r.URL.Query().Get("type"); got != "private" {
			t.Errorf("query[type] = %q; want %q (the type filter must be forwarded)", got, "private")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(k8sSubnetsBody))
	})

	cfg := acctest.ProviderConfig(srv.Endpoint()) + `
data "iaas_kubernetes_subnet" "t" {
  vpc_id = "vpc-1"
  name   = "cp-subnet"
  type   = "private"
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.iaas_kubernetes_subnet.t", "id", "sn-1"),
					resource.TestCheckResourceAttr("data.iaas_kubernetes_subnet.t", "cidr", "10.0.1.0/24"),
					resource.TestCheckResourceAttr("data.iaas_kubernetes_subnet.t", "type", "private"),
				),
			},
		},
	})
}

func TestUnitKubernetesSubnet_noMatch(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)
	srv.Handle("GET", "/kubernetes/search/subnets", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(k8sSubnetsBody))
	})

	cfg := acctest.ProviderConfig(srv.Endpoint()) + `
data "iaas_kubernetes_subnet" "t" {
  vpc_id = "vpc-1"
  name   = "nope"
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config:      cfg,
				ExpectError: regexp.MustCompile(`no kubernetes subnet matching`),
			},
		},
	})
}
