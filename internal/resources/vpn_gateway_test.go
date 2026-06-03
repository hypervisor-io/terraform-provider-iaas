package resources_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/iaas/terraform-provider-iaas/internal/acctest"
)

// ---------------------------------------------------------------------------
// TestAccVpnGateway_basic — LIVE acceptance test (manual staging gate).
//
// Auto-skips unless TF_ACC is set. Requires a reachable panel + IP-locked token,
// a VPC in a VPN-gateway-enabled location with a PUBLIC subnet that has free IPs,
// a valid VPN gateway plan id, and the account's VPN gateway quota not exhausted:
//
//	IAAS_TEST_VPC_ID         — UUID of a VPC in a vpngw-enabled location
//	IAAS_TEST_VPC_SUBNET_ID  — UUID of a PUBLIC subnet in that VPC with a free IP
//	IAAS_TEST_VPNGW_PLAN_ID  — UUID of an enabled VPN gateway plan
//
// Skips cleanly when any var is absent so a bare TF_ACC=1 run does not fail.
// ---------------------------------------------------------------------------
func TestAccVpnGateway_basic(t *testing.T) {
	vpcID := os.Getenv("IAAS_TEST_VPC_ID")
	subnetID := os.Getenv("IAAS_TEST_VPC_SUBNET_ID")
	planID := os.Getenv("IAAS_TEST_VPNGW_PLAN_ID")
	if vpcID == "" || subnetID == "" || planID == "" {
		t.Skip("TestAccVpnGateway_basic: set IAAS_TEST_VPC_ID, IAAS_TEST_VPC_SUBNET_ID and IAAS_TEST_VPNGW_PLAN_ID to run")
	}

	config := fmt.Sprintf(`
resource "iaas_vpn_gateway" "test" {
  vpc_id        = %q
  vpc_subnet_id = %q
  vpngw_plan_id = %q
  name          = "tf-acc-vpngw"
}
`, vpcID, subnetID, planID)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("iaas_vpn_gateway.test", "id"),
					resource.TestCheckResourceAttr("iaas_vpn_gateway.test", "status", "active"),
					resource.TestCheckResourceAttrSet("iaas_vpn_gateway.test", "public_key"),
				),
			},
			{
				ResourceName: "iaas_vpn_gateway.test",
				ImportState:  true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					return vpcID + "/" + s.RootModule().Resources["iaas_vpn_gateway.test"].Primary.ID, nil
				},
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"timeouts", "vpc_subnet_id"},
			},
		},
	})
}

// ---------------------------------------------------------------------------
// TestUnitVpnGateway_lifecycle — MOCK-backed lifecycle proof.
//
// Drives the full CHILD + ASYNC resource lifecycle against canned API responses:
//
//  1. Create — POST /vpc/{vpcId}/vpn-gateway returns {gateway:{id,status:"deploying"}};
//     the FLAT SHOW (GET /vpn-gateway/{id}) immediately returns status="active"
//     (ready on the FIRST poll → the waiter converges instantly, no sleep).
//     Asserts the create body carries vpngw_plan_id + vpc_subnet_id + name and
//     omits server-only computed fields.
//  2. Import — by COMPOSITE id "<vpc_id>/<gateway_id>", ignoring the write-only
//     vpc_subnet_id (not recoverable from SHOW) + timeouts.
//  3. Delete — implicit teardown; DELETE soft-deletes and the next SHOW 404s.
//
// There is NO update step: the gateway has no update endpoint, so every input is
// RequiresReplace (the vpc no-update pattern). The IAAS_INSTANCE_POLL_INTERVAL
// seam is set tiny so the waiter cannot hang.
// ---------------------------------------------------------------------------
func TestUnitVpnGateway_lifecycle(t *testing.T) {
	ensureTFBinary(t)

	// TEST-ONLY poll-interval seam: instant convergence.
	t.Setenv("IAAS_INSTANCE_POLL_INTERVAL", "1ms")

	srv := acctest.NewMockServer(t)

	const (
		vpcID    = "11111111-1111-1111-1111-111111111111"
		subnetID = "55555555-5555-5555-5555-555555555555"
		planID   = "66666666-6666-6666-6666-666666666666"
		gwID     = "22222222-2222-2222-2222-222222222222"
		gwName   = "vpngw-prod"
		pubKey   = "Z2F0ZXdheXB1YmtleQ=="
		vpcIP    = "192.168.0.2"
		pubIP    = "203.0.113.9"
	)

	createPath := "/vpc/" + vpcID + "/vpn-gateway"
	itemPath := "/vpn-gateway/" + gwID

	var mu sync.Mutex
	deleted := false

	showGateway := func() map[string]any {
		return map[string]any{
			"id":            gwID,
			"name":          gwName,
			"status":        "active",
			"vpngw_plan_id": planID,
			"public_key":    pubKey,
			"tunnel_subnet": "10.99.0.0/24",
			"listen_port":   float64(51820),
			"vpc_ip":        vpcIP,
			"vpc":           map[string]any{"id": vpcID},
			"peers":         []any{},
			"instance": map[string]any{
				"ips": []any{
					map[string]any{"ip": pubIP, "subnet": map[string]any{"type": "public"}},
				},
			},
		}
	}

	// CREATE — record the row (status "deploying" in the create response); the first
	// SHOW already reports "active" so the waiter converges on the first poll.
	srv.Handle("POST", createPath, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "VPN gateway is being deployed",
			"gateway": map[string]any{
				"id":     gwID,
				"name":   gwName,
				"status": "deploying",
			},
		})
	})

	// SHOW — FLAT path; 404 once delete has been enqueued.
	srv.Handle("GET", itemPath, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gone := deleted
		mu.Unlock()
		if gone {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "VPN Gateway not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "gateway": showGateway(), "other_gateways": []any{}})
	})

	// DELETE — soft-delete; the next SHOW 404s.
	srv.Handle("DELETE", itemPath, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		deleted = true
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "VPN gateway deleted successfully"})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	createCfg := providerCfg + `
resource "iaas_vpn_gateway" "test" {
  vpc_id        = "` + vpcID + `"
  vpc_subnet_id = "` + subnetID + `"
  vpngw_plan_id = "` + planID + `"
  name          = "` + gwName + `"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			// Create + read-back (async wait converges immediately).
			{
				Config: createCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_vpn_gateway.test", "id", gwID),
					resource.TestCheckResourceAttr("iaas_vpn_gateway.test", "vpc_id", vpcID),
					resource.TestCheckResourceAttr("iaas_vpn_gateway.test", "vpc_subnet_id", subnetID),
					resource.TestCheckResourceAttr("iaas_vpn_gateway.test", "name", gwName),
					resource.TestCheckResourceAttr("iaas_vpn_gateway.test", "status", "active"),
					resource.TestCheckResourceAttr("iaas_vpn_gateway.test", "public_key", pubKey),
					resource.TestCheckResourceAttr("iaas_vpn_gateway.test", "vpc_ip", vpcIP),
					resource.TestCheckResourceAttr("iaas_vpn_gateway.test", "public_ip", pubIP),
					resource.TestCheckResourceAttr("iaas_vpn_gateway.test", "tunnel_subnet", "10.99.0.0/24"),
					resource.TestCheckResourceAttr("iaas_vpn_gateway.test", "listen_port", "51820"),
				),
			},
			// Import by composite id; ignore the write-only vpc_subnet_id + timeouts.
			{
				ResourceName:            "iaas_vpn_gateway.test",
				ImportState:             true,
				ImportStateId:           vpcID + "/" + gwID,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"timeouts", "vpc_subnet_id"},
			},
		},
	})

	// Assert the CREATE body carried vpngw_plan_id + vpc_subnet_id + name and NOT
	// server-only computed fields.
	creates := srv.Requests("POST", createPath)
	if len(creates) == 0 {
		t.Fatal("expected at least one POST " + createPath)
	}
	var createBody map[string]any
	if err := json.Unmarshal(creates[0].Body, &createBody); err != nil {
		t.Fatalf("decoding create body: %v", err)
	}
	if createBody["vpngw_plan_id"] != planID {
		t.Errorf("create body vpngw_plan_id = %v; want %q", createBody["vpngw_plan_id"], planID)
	}
	if createBody["vpc_subnet_id"] != subnetID {
		t.Errorf("create body vpc_subnet_id = %v; want %q", createBody["vpc_subnet_id"], subnetID)
	}
	if createBody["name"] != gwName {
		t.Errorf("create body name = %v; want %q", createBody["name"], gwName)
	}
	for _, stray := range []string{"id", "status", "public_key", "vpc_ip", "public_ip"} {
		if _, present := createBody[stray]; present {
			t.Errorf("create body must NOT include %q; got %v", stray, createBody)
		}
	}
}
