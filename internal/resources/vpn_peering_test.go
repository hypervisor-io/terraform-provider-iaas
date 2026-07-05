package resources_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/iaas/terraform-provider-iaas/internal/acctest"
)

// ---------------------------------------------------------------------------
// TestAccVpnPeering_basic — LIVE acceptance test (manual staging gate).
//
// Auto-skips unless TF_ACC is set. Requires TWO existing active VPN gateways
// in DIFFERENT VPCs, owned by the same account:
//
//	IAAS_TEST_VPN_GATEWAY_ID        — UUID of the LOCAL iaas_vpn_gateway
//	IAAS_TEST_VPN_GATEWAY_REMOTE_ID — UUID of the REMOTE iaas_vpn_gateway
//
// Skips cleanly when absent.
// ---------------------------------------------------------------------------
func TestAccVpnPeering_basic(t *testing.T) {
	gwID := os.Getenv("IAAS_TEST_VPN_GATEWAY_ID")
	remoteID := os.Getenv("IAAS_TEST_VPN_GATEWAY_REMOTE_ID")
	if gwID == "" || remoteID == "" {
		t.Skip("TestAccVpnPeering_basic: set IAAS_TEST_VPN_GATEWAY_ID and IAAS_TEST_VPN_GATEWAY_REMOTE_ID to run this acceptance test")
	}

	config := fmt.Sprintf(`
resource "iaas_vpn_peering" "test" {
  vpn_gateway_id    = %q
  remote_gateway_id = %q
}
`, gwID, remoteID)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("iaas_vpn_peering.test", "id"),
					resource.TestCheckResourceAttr("iaas_vpn_peering.test", "type", "vpc_peering"),
					resource.TestCheckResourceAttrSet("iaas_vpn_peering.test", "tunnel_ip"),
				),
			},
		},
	})
}

// ---------------------------------------------------------------------------
// TestUnitVpnPeering_lifecycle — MOCK-backed lifecycle proof.
//
// Drives the CHILD (read-by-scan) resource lifecycle against canned API
// responses. A peering has NO individual SHOW/DELETE route — it is read by
// scanning the LOCAL gateway's SHOW embedded peers[] (type == "vpc_peering"),
// and deleted via the generic peer-removal endpoint:
//
//  1. Create — POST /vpn-gateway/{id}/peering with body {remote_gateway_id}
//     returns {peers:[local,remote]}; asserts the create body carries ONLY
//     remote_gateway_id (no cidrs/psk — the plan's assumed shape was wrong,
//     confirmed by reading VpnGatewayController::createPeering) and that
//     peers[0] (the LOCAL side) is the one tracked.
//  2. Read-back — scans the local gateway SHOW's peers[]; matches by id AND
//     type == "vpc_peering".
//  3. Import — COMPOSITE id "<vpn_gateway_id>/<peering_id>"; remote_gateway_id
//     (Required, non-Computed) is populated by the automatic post-import Read,
//     not by ImportState.
//  4. Delete — implicit teardown via DELETE /vpn-gateway/{id}/peer/{peeringId}
//     (there is no dedicated peering-delete route); asserted afterward.
//
// Peering creation is SYNCHRONOUS → NO waiter, NO poll-interval seam, NO hang.
// ---------------------------------------------------------------------------
func TestUnitVpnPeering_lifecycle(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)

	const (
		localGwID    = "11111111-1111-1111-1111-111111111111"
		remoteGwID   = "22222222-2222-2222-2222-222222222222"
		peeringID    = "88888888-8888-8888-8888-888888888888"
		remotePeerID = "99999999-9999-9999-9999-999999999999"
		remotePubKey = "cmVtb3RlZ2F0ZXdheXB1YmxpY2tleTEyMzQ1Njc4OTA9"
		tunnelIP     = "10.100.0.2"
		endpoint     = "203.0.113.5:51820"
	)

	gwPath := "/vpn-gateway/" + localGwID
	peeringPath := gwPath + "/peering"
	peerItemPath := gwPath + "/peer/" + peeringID

	// Stateful: whether the peering currently exists (embedded in the local
	// gateway's SHOW).
	var mu sync.Mutex
	exists := false

	localPeerObject := func() map[string]any {
		// NOTE: no preshared_key — it is $hidden + encrypted server-side and
		// never returned by ANY response, so the resource must not (and does
		// not) expose it.
		return map[string]any{
			"id":                peeringID,
			"vpn_gateway_id":    localGwID,
			"type":              "vpc_peering",
			"name":              "vpc-peering-remotevpc",
			"public_key":        remotePubKey,
			"tunnel_ip":         tunnelIP,
			"allowed_ips":       []any{"10.20.0.0/16", "10.100.1.0/24"},
			"endpoint":          endpoint,
			"remote_gateway_id": remoteGwID,
			"dns":               nil,
			"keepalive":         float64(25),
			"enabled":           float64(1),
		}
	}

	// Local gateway SHOW — embeds the peering once created (and not yet deleted).
	srv.Handle("GET", gwPath, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		peers := []any{}
		if exists {
			peers = append(peers, localPeerObject())
		}
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"gateway": map[string]any{
				"id":     localGwID,
				"status": "active",
				"peers":  peers,
			},
		})
	})

	// CREATE peering — returns {peers:[local,remote]}.
	srv.Handle("POST", peeringPath, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		exists = true
		local := localPeerObject()
		mu.Unlock()
		remote := map[string]any{
			"id":                remotePeerID,
			"vpn_gateway_id":    remoteGwID,
			"type":              "vpc_peering",
			"name":              "vpc-peering-localvpc",
			"public_key":        "bG9jYWxnYXRld2F5cHVibGlja2V5MTIzNDU2Nzg5MD0=",
			"tunnel_ip":         "10.100.0.3",
			"allowed_ips":       []any{"10.10.0.0/16", "10.100.0.0/24"},
			"endpoint":          "198.51.100.9:51820",
			"remote_gateway_id": localGwID,
			"dns":               nil,
			"keepalive":         float64(25),
			"enabled":           float64(1),
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "VPC peering created successfully",
			"peers":   []any{local, remote},
		})
	})

	// DELETE — the generic peer-removal endpoint (no dedicated peering-delete route).
	srv.Handle("DELETE", peerItemPath, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		exists = false
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Peer removed successfully"})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	createCfg := providerCfg + `
resource "iaas_vpn_peering" "test" {
  vpn_gateway_id    = "` + localGwID + `"
  remote_gateway_id = "` + remoteGwID + `"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			// Create + read-back by scan.
			{
				Config: createCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_vpn_peering.test", "id", peeringID),
					resource.TestCheckResourceAttr("iaas_vpn_peering.test", "vpn_gateway_id", localGwID),
					resource.TestCheckResourceAttr("iaas_vpn_peering.test", "remote_gateway_id", remoteGwID),
					resource.TestCheckResourceAttr("iaas_vpn_peering.test", "type", "vpc_peering"),
					resource.TestCheckResourceAttr("iaas_vpn_peering.test", "public_key", remotePubKey),
					resource.TestCheckResourceAttr("iaas_vpn_peering.test", "tunnel_ip", tunnelIP),
					resource.TestCheckResourceAttr("iaas_vpn_peering.test", "endpoint", endpoint),
					resource.TestCheckResourceAttr("iaas_vpn_peering.test", "keepalive", "25"),
					resource.TestCheckResourceAttr("iaas_vpn_peering.test", "enabled", "true"),
					resource.TestCheckResourceAttr("iaas_vpn_peering.test", "allowed_ips.#", "2"),
					resource.TestCheckTypeSetElemAttr("iaas_vpn_peering.test", "allowed_ips.*", "10.20.0.0/16"),
					resource.TestCheckTypeSetElemAttr("iaas_vpn_peering.test", "allowed_ips.*", "10.100.1.0/24"),
				),
			},
			// Import by composite id; remote_gateway_id is populated by the
			// post-import Read (not recoverable from the id alone).
			{
				ResourceName:      "iaas_vpn_peering.test",
				ImportState:       true,
				ImportStateId:     localGwID + "/" + peeringID,
				ImportStateVerify: true,
			},
		},
	})

	// Assert the CREATE body carried ONLY remote_gateway_id — no cidrs/psk/etc
	// (the plan's assumed create shape was wrong; the controller only accepts
	// remote_gateway_id, confirmed by Read of VpnGatewayController::createPeering).
	creates := srv.Requests("POST", peeringPath)
	if len(creates) == 0 {
		t.Fatal("expected at least one POST " + peeringPath)
	}
	var createBody map[string]any
	if err := json.Unmarshal(creates[0].Body, &createBody); err != nil {
		t.Fatalf("decoding create body: %v", err)
	}
	if createBody["remote_gateway_id"] != remoteGwID {
		t.Errorf("create body remote_gateway_id = %v; want %q", createBody["remote_gateway_id"], remoteGwID)
	}
	if len(createBody) != 1 {
		t.Errorf("create body must contain ONLY remote_gateway_id; got %v", createBody)
	}

	// Assert the implicit teardown hit the generic peer-removal endpoint (no
	// dedicated peering-delete route exists).
	deletes := srv.Requests("DELETE", peerItemPath)
	if len(deletes) == 0 {
		t.Fatal("expected at least one DELETE " + peerItemPath + " (teardown)")
	}
}
