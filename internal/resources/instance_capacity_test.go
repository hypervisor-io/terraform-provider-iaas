package resources_test

import (
	"net/http"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hypervisor-io/terraform-provider-iaas/internal/acctest"
)

func TestUnitInstanceCapacityReason(t *testing.T) {
	ensureTFBinary(t)
	for _, reason := range []string{"ipv6_not_available", "storage_backend_not_offered", "no_capacity"} {
		t.Run(reason, func(t *testing.T) {
			srv := acctest.NewMockServer(t)
			srv.Handle("POST", "/cloud-service/instances", func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"success": false, "reason": reason, "message": "Try another plan or location."})
			})
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: acctest.Factories,
				Steps: []resource.TestStep{{Config: acctest.ProviderConfig(srv.Endpoint()) + `
resource "iaas_instance" "test" {
 location_id = "location-1"
 plan_id = "plan-1"
 image_id = "image-1"
}
`, ExpectError: regexp.MustCompile(reason)}},
			})
			if calls := srv.Requests("POST", "/cloud-service/instances"); len(calls) != 1 {
				t.Fatalf("create attempts=%d", len(calls))
			}
		})
	}
}
