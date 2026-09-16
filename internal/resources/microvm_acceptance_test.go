package resources_test

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/hypervisor-io/terraform-provider-iaas/internal/acctest"
)

func TestAccMicrovmImage_basic(t *testing.T) {
	groupID := envOrSkip(t, "IAAS_TEST_MICROVM_HG_ID")
	config := fmt.Sprintf(`
resource "iaas_microvm_image" "test" {
  name                = "tfacc-image"
  source_kind         = "dockerfile"
  dockerfile          = "FROM alpine:3.22\nRUN true"
  hypervisor_group_id = %q
}
`, groupID)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{{
			Config: config,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttrSet("iaas_microvm_image.test", "id"),
				resource.TestCheckResourceAttr("iaas_microvm_image.test", "source_kind", "dockerfile"),
			),
		}},
	})
}

func TestAccMicrovm_basic(t *testing.T) {
	groupID := envOrSkip(t, "IAAS_TEST_MICROVM_HG_ID")
	imageID := envOrSkip(t, "IAAS_TEST_MICROVM_IMAGE_ID")
	config := fmt.Sprintf(`
resource "iaas_microvm" "test" {
  name                = "tfacc-microvm"
  hypervisor_group_id = %q
  image_id            = %q

  // C1: isolated is retired - public/vpc only. A bare public entry with no
  // subnet_id lets the platform auto-assign a subnet with free capacity
  // (C8.1).
  network = [{ kind = "public" }]
}
`, groupID, imageID)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{{
			Config: config,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttrSet("iaas_microvm.test", "id"),
				resource.TestCheckResourceAttrSet("iaas_microvm.test", "state"),
			),
		}},
	})
}

func TestAccMicrovmConnector_basic(t *testing.T) {
	sourceID := envOrSkip(t, "IAAS_TEST_MICROVM_GITHUB_SOURCE_ID")
	config := fmt.Sprintf(`
resource "iaas_microvm_connector" "test" {
  kind          = "github_app"
  name          = "tfacc-connector"
  git_source_id = %q
  enabled       = false
}
`, sourceID)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{{
			Config: config,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttrSet("iaas_microvm_connector.test", "id"),
				resource.TestCheckResourceAttr("iaas_microvm_connector.test", "kind", "github_app"),
			),
		}},
	})
}
