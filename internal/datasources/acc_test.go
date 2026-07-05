package datasources_test

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/iaas/terraform-provider-iaas/internal/acctest"
)

// ---------------------------------------------------------------------------
// Data-source LIVE acceptance tests (manual staging gate).
//
// Data sources are read-only, so there is no create/import lifecycle to drive:
// each TestAcc here performs a single read against a REAL staging panel, looking
// up an existing catalog entry (by the name/slug/kind the operator supplies) or
// a child of an existing parent (by id), and asserts the computed output is
// populated. They auto-skip in two layers:
//
//  1. resource.Test no-ops entirely unless TF_ACC=1 is set (the manual gate).
//  2. envOrSkip skips cleanly when the per-data-source filter/id env var the
//     lookup needs is not supplied - so a partial env still runs what it can.
//
// All of these require IAAS_API_ENDPOINT + IAAS_API_TOKEN (an IP-locked Bearer
// token) via acctest.PreCheck, plus the extra env var(s) named per test. They
// are the data-source half of the id39 staging-acceptance suite; see
// docs/guides/acceptance-testing.md / ACCEPTANCE_TESTING.md for the runbook.
// ---------------------------------------------------------------------------

// TestAccLocation_basic - looks up a location (hypervisor group) by name/slug.
//
//	IAAS_TEST_LOCATION_NAME - slug or display name of an existing location
func TestAccLocation_basic(t *testing.T) {
	name := envOrSkip(t, "IAAS_TEST_LOCATION_NAME")

	config := fmt.Sprintf(`
data "iaas_location" "test" {
  name = %q
}
`, name)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.iaas_location.test", "id"),
				),
			},
		},
	})
}

// TestAccPlan_basic - looks up an instance plan by name within a location.
//
//	IAAS_TEST_PLAN_LOCATION_ID - UUID of a location (hypervisor group)
//	IAAS_TEST_PLAN_NAME        - name of a plan available in that location
func TestAccPlan_basic(t *testing.T) {
	locationID := envOrSkip(t, "IAAS_TEST_PLAN_LOCATION_ID")
	name := envOrSkip(t, "IAAS_TEST_PLAN_NAME")

	config := fmt.Sprintf(`
data "iaas_plan" "test" {
  location_id = %q
  name        = %q
}
`, locationID, name)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.iaas_plan.test", "id"),
				),
			},
		},
	})
}

// TestAccImage_basic - looks up an OS image by name.
//
//	IAAS_TEST_IMAGE_NAME - name of an available image (e.g. "Ubuntu 24.04")
func TestAccImage_basic(t *testing.T) {
	name := envOrSkip(t, "IAAS_TEST_IMAGE_NAME")

	config := fmt.Sprintf(`
data "iaas_image" "test" {
  name = %q
}
`, name)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.iaas_image.test", "id"),
				),
			},
		},
	})
}

// TestAccISO_basic - looks up a mountable ISO by its exact name.
//
//	IAAS_TEST_ISO_NAME - name of an available ISO (e.g. "AlmaLinux 9")
func TestAccISO_basic(t *testing.T) {
	name := envOrSkip(t, "IAAS_TEST_ISO_NAME")

	config := fmt.Sprintf(`
data "iaas_iso" "test" {
  name = %q
}
`, name)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.iaas_iso.test", "id"),
				),
			},
		},
	})
}

// TestAccKubernetesVersion_basic - resolves a Kubernetes version UUID by name.
//
//	IAAS_TEST_K8S_VERSION_NAME - semantic version of an active k8s version (e.g. "1.31.4")
func TestAccKubernetesVersion_basic(t *testing.T) {
	name := envOrSkip(t, "IAAS_TEST_K8S_VERSION_NAME")

	config := fmt.Sprintf(`
data "iaas_kubernetes_version" "test" {
  name = %q
}
`, name)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.iaas_kubernetes_version.test", "id"),
				),
			},
		},
	})
}

// TestAccKubernetesRegion_basic - resolves a Kubernetes region UUID by name/slug.
//
//	IAAS_TEST_K8S_REGION_NAME - name or slug of a k8s-eligible region (e.g. "nyc1")
func TestAccKubernetesRegion_basic(t *testing.T) {
	name := envOrSkip(t, "IAAS_TEST_K8S_REGION_NAME")

	config := fmt.Sprintf(`
data "iaas_kubernetes_region" "test" {
  name = %q
}
`, name)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.iaas_kubernetes_region.test", "id"),
				),
			},
		},
	})
}

// TestAccKubernetesPlan_basic - resolves a Kubernetes plan UUID by kind + name.
//
//	IAAS_TEST_K8S_PLAN_KIND - one of worker|cp|lb
//	IAAS_TEST_K8S_PLAN_NAME - name of a plan of that kind
func TestAccKubernetesPlan_basic(t *testing.T) {
	kind := envOrSkip(t, "IAAS_TEST_K8S_PLAN_KIND")
	name := envOrSkip(t, "IAAS_TEST_K8S_PLAN_NAME")

	config := fmt.Sprintf(`
data "iaas_kubernetes_plan" "test" {
  kind = %q
  name = %q
}
`, kind, name)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.iaas_kubernetes_plan.test", "id"),
				),
			},
		},
	})
}

// TestAccKubernetesKubeconfig_basic - downloads the admin kubeconfig for a
// running cluster. The output is Sensitive; the server mints a fresh cert on
// every read.
//
//	IAAS_TEST_K8S_CLUSTER_ID - UUID of a running iaas_kubernetes_cluster
func TestAccKubernetesKubeconfig_basic(t *testing.T) {
	clusterID := envOrSkip(t, "IAAS_TEST_K8S_CLUSTER_ID")

	config := fmt.Sprintf(`
data "iaas_kubernetes_kubeconfig" "test" {
  cluster_id = %q
}
`, clusterID)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.iaas_kubernetes_kubeconfig.test", "kubeconfig"),
				),
			},
		},
	})
}

// TestAccKubernetesAutoscalerManifest_basic - renders the cluster-autoscaler
// manifest for a running cluster with worker autoscaling enabled. The output is
// Sensitive (embeds a controller JWT); every read rotates the token.
//
//	IAAS_TEST_K8S_CLUSTER_ID - UUID of a running cluster with worker autoscaling enabled
func TestAccKubernetesAutoscalerManifest_basic(t *testing.T) {
	clusterID := envOrSkip(t, "IAAS_TEST_K8S_CLUSTER_ID")

	config := fmt.Sprintf(`
data "iaas_kubernetes_autoscaler_manifest" "test" {
  cluster_id = %q
}
`, clusterID)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.iaas_kubernetes_autoscaler_manifest.test", "manifest"),
				),
			},
		},
	})
}

// TestAccVpnPeerConfig_basic - downloads the WireGuard client config for a
// road_warrior VPN peer. The output is Sensitive.
//
//	IAAS_TEST_VPN_GATEWAY_ID - UUID of an active iaas_vpn_gateway
//	IAAS_TEST_VPN_PEER_ID    - UUID of a road_warrior iaas_vpn_peer on that gateway
func TestAccVpnPeerConfig_basic(t *testing.T) {
	gatewayID := envOrSkip(t, "IAAS_TEST_VPN_GATEWAY_ID")
	peerID := envOrSkip(t, "IAAS_TEST_VPN_PEER_ID")

	config := fmt.Sprintf(`
data "iaas_vpn_peer_config" "test" {
  gateway_id = %q
  peer_id    = %q
}
`, gatewayID, peerID)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.iaas_vpn_peer_config.test", "config"),
				),
			},
		},
	})
}
