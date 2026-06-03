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

// TestUnitKubernetesAutoscalerManifest_render — mock-backed data-source proof.
//
// data "iaas_kubernetes_autoscaler_manifest" "t" { cluster_id = ... } reads GET
// /kubernetes/cluster/{id}/autoscaler-manifest, which returns a RAW text/yaml
// body (NOT JSON), and exposes it as the sensitive computed `manifest`.
func TestUnitKubernetesAutoscalerManifest_render(t *testing.T) {
	ensureTFBinary(t)

	const (
		clusterID = "11111111-1111-1111-1111-111111111111"
		manifest  = "apiVersion: v1\nkind: ServiceAccount\nmetadata:\n  name: cluster-autoscaler\n---\napiVersion: v1\nkind: Secret\nmetadata:\n  name: cluster-autoscaler-token\ndata:\n  token: SldUX0JBU0U2NA==\n"
	)

	srv := acctest.NewMockServer(t)
	srv.Handle("GET", "/kubernetes/cluster/"+clusterID+"/autoscaler-manifest", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/yaml")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(manifest))
	})

	cfg := acctest.ProviderConfig(srv.Endpoint()) + `
data "iaas_kubernetes_autoscaler_manifest" "t" {
  cluster_id = "` + clusterID + `"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.iaas_kubernetes_autoscaler_manifest.t", "cluster_id", clusterID),
					resource.TestCheckResourceAttr("data.iaas_kubernetes_autoscaler_manifest.t", "manifest", manifest),
				),
			},
		},
	})
}

// TestUnitKubernetesAutoscalerManifest_notEnabled — a 422 (autoscaling not
// enabled on the cluster) surfaces as a clear error.
func TestUnitKubernetesAutoscalerManifest_notEnabled(t *testing.T) {
	ensureTFBinary(t)

	const clusterID = "11111111-1111-1111-1111-111111111111"

	srv := acctest.NewMockServer(t)
	srv.Handle("GET", "/kubernetes/cluster/"+clusterID+"/autoscaler-manifest", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error": "autoscaling not enabled on this cluster",
		})
	})

	cfg := acctest.ProviderConfig(srv.Endpoint()) + `
data "iaas_kubernetes_autoscaler_manifest" "t" {
  cluster_id = "` + clusterID + `"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config:      cfg,
				ExpectError: regexp.MustCompile(`autoscaling not enabled`),
			},
		},
	})
}

// TestUnitKubernetesAutoscalerManifest_schemaSensitive asserts the `manifest`
// attribute is declared Sensitive — it embeds a freshly-minted controller JWT
// (a live bearer credential) inline as a Secret.
func TestUnitKubernetesAutoscalerManifest_schemaSensitive(t *testing.T) {
	ds := datasources.NewKubernetesAutoscalerManifestDataSource()
	var resp datasource.SchemaResponse
	ds.Schema(context.Background(), datasource.SchemaRequest{}, &resp)

	attr, ok := resp.Schema.Attributes["manifest"].(dschema.StringAttribute)
	if !ok {
		t.Fatalf("manifest attribute missing or not a StringAttribute: %T", resp.Schema.Attributes["manifest"])
	}
	if !attr.Sensitive {
		t.Error("manifest attribute must be Sensitive (it embeds a controller JWT bearer credential)")
	}
	if !attr.Computed {
		t.Error("manifest attribute must be Computed")
	}
}
