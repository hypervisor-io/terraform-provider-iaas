package resources_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/iaas/terraform-provider-iaas/internal/acctest"
)

// ---------------------------------------------------------------------------
// TestAccStaticIP_basic — LIVE acceptance test (manual staging gate).
//
// Auto-skips unless TF_ACC is set (resource.Test enforces this), so it never
// runs or blocks CI. Requires a reachable panel + IP-locked token via
// IAAS_API_ENDPOINT / IAAS_API_TOKEN (checked by acctest.PreCheck).
// Also requires billing to be enabled on the target panel and real UUIDs for
// ip_id and hypervisor_group_id supplied via the env vars below; the test
// skips cleanly when either var is absent so a bare TF_ACC=1 run does not
// fail with a confusing 404/422.
//
//	IAAS_TEST_STATIC_IP_ID   — UUID of an available (unallocated) IP
//	IAAS_TEST_HG_ID          — UUID of the hypervisor group that owns the IP
//
// ---------------------------------------------------------------------------
func TestAccStaticIP_basic(t *testing.T) {
	ipID := os.Getenv("IAAS_TEST_STATIC_IP_ID")
	hgID := os.Getenv("IAAS_TEST_HG_ID")
	if ipID == "" || hgID == "" {
		t.Skip("TestAccStaticIP_basic: set IAAS_TEST_STATIC_IP_ID and IAAS_TEST_HG_ID to run this acceptance test")
	}

	config := fmt.Sprintf(`
resource "iaas_static_ip" "test" {
  ip_id               = %q
  hypervisor_group_id = %q
}
`, ipID, hgID)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("iaas_static_ip.test", "id"),
					resource.TestCheckResourceAttrSet("iaas_static_ip.test", "address"),
					resource.TestCheckResourceAttr("iaas_static_ip.test", "status", "allocated"),
				),
			},
			{
				ResourceName:      "iaas_static_ip.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// ---------------------------------------------------------------------------
// TestUnitStaticIP_lifecycle — MOCK-backed lifecycle proof.
//
// Drives the full resource lifecycle against canned API responses, with no
// live panel. The Steps execute in this order:
//
//  1. Create + read-back — applies createCfg; checks id, address, status,
//     hypervisor_group_id, ip_id, hypervisor_group_name.
//  2. Import — imports the resource by UUID and verifies state matches.
//
// There is NO update step: Static IP has no UPDATE route, so every configurable
// attribute is RequiresReplace and the resource is effectively immutable in place.
// Delete is implicit teardown after the final step, not an explicit Step.
//
// resource.UnitTest needs a terraform/opentofu binary on PATH or via
// TF_ACC_TERRAFORM_PATH; if none is found the test is skipped with a clear
// binary-not-found message (see ensureTFBinary in ssh_key_test.go).
// ---------------------------------------------------------------------------
func TestUnitStaticIP_lifecycle(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)

	const (
		staticIPID = "44444444-4444-4444-4444-444444444444"
		ipID       = "55555555-5555-5555-5555-555555555555"
		groupID    = "66666666-6666-6666-6666-666666666666"
		groupName  = "US East"
		address    = "203.0.113.10"
		subnetID   = "77777777-7777-7777-7777-777777777777"
	)

	// currentStatus tracks server-side status so READ always reflects the current value.
	currentStatus := "allocated"

	// ALLOCATE — POST /static-ips/allocate returns 200 + {success,message,static_ip}.
	// The static_ip object carries id, status, and the nested ip + hypervisor_group objects.
	srv.Handle("POST", "/static-ips/allocate", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success":   true,
			"message":   "Static IP 203.0.113.10 allocated successfully.",
			"static_ip": staticIPObject(staticIPID, ipID, groupID, address, subnetID, groupName, currentStatus),
		})
	})

	// READ — GET /static-ips (paginator) — the resource uses list+scan-by-id
	// because there is no individual SHOW route.
	srv.Handle("GET", "/static-ips", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"current_page": 1,
			"data": []any{
				staticIPObject(staticIPID, ipID, groupID, address, subnetID, groupName, currentStatus),
			},
			"total": 1,
		})
	})

	// DELETE — DELETE /static-ip/{id} (singular) succeeds at 200.
	srv.Handle("DELETE", "/static-ip/"+staticIPID, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Static IP deallocated successfully.",
		})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	createCfg := providerCfg + `
resource "iaas_static_ip" "test" {
  ip_id               = "` + ipID + `"
  hypervisor_group_id = "` + groupID + `"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			// Create + read-back.
			{
				Config: createCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_static_ip.test", "id", staticIPID),
					resource.TestCheckResourceAttr("iaas_static_ip.test", "ip_id", ipID),
					resource.TestCheckResourceAttr("iaas_static_ip.test", "hypervisor_group_id", groupID),
					resource.TestCheckResourceAttr("iaas_static_ip.test", "address", address),
					resource.TestCheckResourceAttr("iaas_static_ip.test", "status", "allocated"),
					resource.TestCheckResourceAttr("iaas_static_ip.test", "hypervisor_group_name", groupName),
				),
			},
			// Import the existing resource and verify state matches.
			{
				ResourceName:      "iaas_static_ip.test",
				ImportState:       true,
				ImportStateId:     staticIPID,
				ImportStateVerify: true,
			},
		},
	})

	// Assert the allocate request sent ip_id and hypervisor_group_id (required fields)
	// and NOT stray server-only fields (id, address, status).
	allocates := srv.Requests("POST", "/static-ips/allocate")
	if len(allocates) == 0 {
		t.Fatal("expected at least one POST /static-ips/allocate")
	}
	var createBody map[string]any
	if err := json.Unmarshal(allocates[0].Body, &createBody); err != nil {
		t.Fatalf("decoding allocate body: %v", err)
	}
	if createBody["ip_id"] != ipID {
		t.Errorf("allocate body ip_id = %v; want %q", createBody["ip_id"], ipID)
	}
	if createBody["hypervisor_group_id"] != groupID {
		t.Errorf("allocate body hypervisor_group_id = %v; want %q", createBody["hypervisor_group_id"], groupID)
	}
	// The resource must not leak computed/server-only fields into the allocate body.
	for _, stray := range []string{"id", "address", "status", "hypervisor_group_name"} {
		if _, present := createBody[stray]; present {
			t.Errorf("allocate body must NOT include %q; got %v", stray, createBody)
		}
	}
}

// staticIPObject builds a serialised static_ip object matching the API ALLOCATE/LIST shape.
// The nested ip and hypervisor_group objects are included as they appear in real responses.
func staticIPObject(id, ipID, groupID, address, subnetID, groupName, status string) map[string]any {
	return map[string]any{
		"id":                  id,
		"ip_id":               ipID,
		"hypervisor_group_id": groupID,
		"status":              status,
		"ip": map[string]any{
			"id":        ipID,
			"ip":        address,
			"subnet_id": subnetID,
		},
		"hypervisor_group": map[string]any{
			"id":   groupID,
			"name": groupName,
		},
	}
}
