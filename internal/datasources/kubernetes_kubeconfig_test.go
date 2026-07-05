package datasources_test

import (
	"context"
	"net/http"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/iaas/terraform-provider-iaas/internal/acctest"
	"github.com/iaas/terraform-provider-iaas/internal/datasources"
)

// TestUnitKubernetesKubeconfig_download - mock-backed data-source proof.
//
// data "iaas_kubernetes_kubeconfig" "t" { cluster_id = ... } reads GET
// /kubernetes/cluster/{id}/kubeconfig, which returns a RAW application/yaml body
// (NOT JSON), and exposes it as the sensitive computed `kubeconfig`.
func TestUnitKubernetesKubeconfig_download(t *testing.T) {
	ensureTFBinary(t)

	const (
		clusterID = "11111111-1111-1111-1111-111111111111"
		yaml      = "apiVersion: v1\nkind: Config\nclusters:\n- cluster:\n    server: https://10.0.1.5:6443\n  name: prod\nusers:\n- name: kubernetes-admin\n  user:\n    client-certificate-data: Q0xJRU5U\n    client-key-data: S0VZ\n"
	)

	srv := acctest.NewMockServer(t)
	srv.Handle("GET", "/kubernetes/cluster/"+clusterID+"/kubeconfig", func(w http.ResponseWriter, _ *http.Request) {
		// RAW YAML body - the kubeconfig endpoint is an attachment download,
		// NOT a JSON envelope.
		w.Header().Set("Content-Type", "application/yaml")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(yaml))
	})

	cfg := acctest.ProviderConfig(srv.Endpoint()) + `
data "iaas_kubernetes_kubeconfig" "t" {
  cluster_id = "` + clusterID + `"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.iaas_kubernetes_kubeconfig.t", "cluster_id", clusterID),
					resource.TestCheckResourceAttr("data.iaas_kubernetes_kubeconfig.t", "kubeconfig", yaml),
				),
			},
		},
	})
}

// TestUnitKubernetesKubeconfig_notBootstrapped - a 404 (cluster has not finished
// bootstrap, no CA yet) surfaces as a clear error.
func TestUnitKubernetesKubeconfig_notBootstrapped(t *testing.T) {
	ensureTFBinary(t)

	const clusterID = "11111111-1111-1111-1111-111111111111"

	srv := acctest.NewMockServer(t)
	srv.Handle("GET", "/kubernetes/cluster/"+clusterID+"/kubeconfig", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error": "kubeconfig not yet available - cluster has not finished bootstrap. Retry after cluster reaches running state.",
		})
	})

	cfg := acctest.ProviderConfig(srv.Endpoint()) + `
data "iaas_kubernetes_kubeconfig" "t" {
  cluster_id = "` + clusterID + `"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config:      cfg,
				ExpectError: regexp.MustCompile(`not yet available`),
			},
		},
	})
}

// TestUnitKubernetesKubeconfig_schemaSensitive asserts the `kubeconfig` attribute
// is declared Sensitive in the schema - it embeds a live cluster-admin client
// certificate, so it must never be printed in plan/apply output.
func TestUnitKubernetesKubeconfig_schemaSensitive(t *testing.T) {
	ds := datasources.NewKubernetesKubeconfigDataSource()
	var resp datasource.SchemaResponse
	ds.Schema(context.Background(), datasource.SchemaRequest{}, &resp)

	attr, ok := resp.Schema.Attributes["kubeconfig"].(dschema.StringAttribute)
	if !ok {
		t.Fatalf("kubeconfig attribute missing or not a StringAttribute: %T", resp.Schema.Attributes["kubeconfig"])
	}
	if !attr.Sensitive {
		t.Error("kubeconfig attribute must be Sensitive (it embeds an admin client certificate)")
	}
	if !attr.Computed {
		t.Error("kubeconfig attribute must be Computed")
	}
}
