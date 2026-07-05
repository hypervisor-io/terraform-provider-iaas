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
// TestAccVpnPeer_basic - LIVE acceptance test (manual staging gate).
//
// Auto-skips unless TF_ACC is set. Requires an EXISTING active VPN gateway:
//
//	IAAS_TEST_VPN_GATEWAY_ID - UUID of an active iaas_vpn_gateway
//
// Skips cleanly when absent.
// ---------------------------------------------------------------------------
func TestAccVpnPeer_basic(t *testing.T) {
	gwID := os.Getenv("IAAS_TEST_VPN_GATEWAY_ID")
	if gwID == "" {
		t.Skip("TestAccVpnPeer_basic: set IAAS_TEST_VPN_GATEWAY_ID to run this acceptance test")
	}

	config := fmt.Sprintf(`
resource "iaas_vpn_peer" "test" {
  vpn_gateway_id = %q
  type           = "road_warrior"
  name           = "tf-acc-peer"
  public_key     = "Y2xpZW50cHVibGlja2V5MTIzNDU2Nzg5MGFiY2RlZmc9"
}
`, gwID)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("iaas_vpn_peer.test", "id"),
					resource.TestCheckResourceAttrSet("iaas_vpn_peer.test", "tunnel_ip"),
				),
			},
		},
	})
}

// ---------------------------------------------------------------------------
// TestUnitVpnPeer_lifecycle - MOCK-backed lifecycle proof.
//
// Drives the full CHILD (read-by-scan) resource lifecycle against canned API
// responses. The peer has NO individual SHOW route - it is read by scanning the
// gateway SHOW's embedded peers[]; a stateful mock embeds the peer after create,
// reflects the update, and drops it after delete:
//
//  1. Create - POST /vpn-gateway/{id}/peer returns {peer:{id,...}} (with the
//     server-allocated tunnel_ip + default allowed_ips). Asserts the create body
//     carries type + name + public_key + the write-only preshared_key, and that
//     the SHOW never leaks the preshared_key.
//  2. Read-back - scans the gateway SHOW peers[]; the write-only preshared_key is
//     echoed from the plan.
//  3. Import - COMPOSITE id "<gateway_id>/<peer_id>", ignoring the unrecoverable
//     write-only preshared_key.
//  4. Update - rename + disable (PATCH). Asserts the PATCH fired.
//  5. Delete - implicit teardown; the peer drops out of the embedded peers[].
//
// Peer writes are synchronous → NO waiter, NO poll-interval seam, NO hang.
// ---------------------------------------------------------------------------
func TestUnitVpnPeer_lifecycle(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)

	const (
		gwID      = "22222222-2222-2222-2222-222222222222"
		peerID    = "77777777-7777-7777-7777-777777777777"
		peerName  = "laptop"
		peerName2 = "laptop-renamed"
		clientPK  = "Y2xpZW50cHVibGlja2V5MTIzNDU2Nzg5MGFiY2RlZmc9"
		psk       = "cHJlc2hhcmVka2V5MTIzNDU2Nzg5MGFiY2RlZmdoaWo9"
		tunnelIP  = "10.99.0.2"
	)

	gwPath := "/vpn-gateway/" + gwID
	peerPath := gwPath + "/peer"
	peerItemPath := peerPath + "/" + peerID

	// Stateful peer fields mutated by the update.
	var mu sync.Mutex
	created := false
	currentName := peerName
	currentEnabled := 1

	peerObject := func() map[string]any {
		// NOTE: the embedded peer NEVER includes preshared_key (it is $hidden +
		// encrypted server-side), so the resource must preserve it from the plan.
		return map[string]any{
			"id":          peerID,
			"type":        "road_warrior",
			"name":        currentName,
			"public_key":  clientPK,
			"tunnel_ip":   tunnelIP,
			"allowed_ips": []any{tunnelIP + "/32"},
			"keepalive":   float64(25),
			"enabled":     float64(currentEnabled),
		}
	}

	// Gateway SHOW - embeds the peer once created (and not yet deleted).
	srv.Handle("GET", gwPath, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		peers := []any{}
		if created {
			peers = append(peers, peerObject())
		}
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"gateway": map[string]any{
				"id":     gwID,
				"status": "active",
				"peers":  peers,
			},
		})
	})

	// CREATE peer - returns {peer:{id,...}}.
	srv.Handle("POST", peerPath, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		created = true
		p := peerObject()
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Peer added successfully",
			"peer":    p,
		})
	})

	// UPDATE peer - PATCH name/enabled.
	srv.Handle("PATCH", peerItemPath, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		if n, ok := body["name"].(string); ok {
			currentName = n
		}
		if e, ok := body["enabled"].(bool); ok {
			if e {
				currentEnabled = 1
			} else {
				currentEnabled = 0
			}
		}
		p := peerObject()
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Peer updated successfully", "peer": p})
	})

	// DELETE peer - drops it from the embedded peers[].
	srv.Handle("DELETE", peerItemPath, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		created = false
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Peer removed successfully"})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	createCfg := providerCfg + `
resource "iaas_vpn_peer" "test" {
  vpn_gateway_id = "` + gwID + `"
  type           = "road_warrior"
  name           = "` + peerName + `"
  public_key     = "` + clientPK + `"
  preshared_key  = "` + psk + `"
}
`
	updateCfg := providerCfg + `
resource "iaas_vpn_peer" "test" {
  vpn_gateway_id = "` + gwID + `"
  type           = "road_warrior"
  name           = "` + peerName2 + `"
  public_key     = "` + clientPK + `"
  preshared_key  = "` + psk + `"
  enabled        = false
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			// Create + read-back by scan.
			{
				Config: createCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_vpn_peer.test", "id", peerID),
					resource.TestCheckResourceAttr("iaas_vpn_peer.test", "vpn_gateway_id", gwID),
					resource.TestCheckResourceAttr("iaas_vpn_peer.test", "type", "road_warrior"),
					resource.TestCheckResourceAttr("iaas_vpn_peer.test", "name", peerName),
					resource.TestCheckResourceAttr("iaas_vpn_peer.test", "public_key", clientPK),
					resource.TestCheckResourceAttr("iaas_vpn_peer.test", "tunnel_ip", tunnelIP),
					resource.TestCheckResourceAttr("iaas_vpn_peer.test", "keepalive", "25"),
					resource.TestCheckResourceAttr("iaas_vpn_peer.test", "enabled", "true"),
					// preshared_key is echoed from config (write-only; never in SHOW).
					resource.TestCheckResourceAttr("iaas_vpn_peer.test", "preshared_key", psk),
					resource.TestCheckResourceAttr("iaas_vpn_peer.test", "allowed_ips.#", "1"),
					resource.TestCheckTypeSetElemAttr("iaas_vpn_peer.test", "allowed_ips.*", tunnelIP+"/32"),
				),
			},
			// Import by composite id; ignore the unrecoverable write-only preshared_key.
			{
				ResourceName:            "iaas_vpn_peer.test",
				ImportState:             true,
				ImportStateId:           gwID + "/" + peerID,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"preshared_key"},
			},
			// Update: rename + disable.
			{
				Config: updateCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_vpn_peer.test", "name", peerName2),
					resource.TestCheckResourceAttr("iaas_vpn_peer.test", "enabled", "false"),
				),
			},
		},
	})

	// Assert the CREATE body carried type + name + public_key + preshared_key and
	// NOT server-only computed fields.
	creates := srv.Requests("POST", peerPath)
	if len(creates) == 0 {
		t.Fatal("expected at least one POST " + peerPath)
	}
	var createBody map[string]any
	if err := json.Unmarshal(creates[0].Body, &createBody); err != nil {
		t.Fatalf("decoding create body: %v", err)
	}
	if createBody["type"] != "road_warrior" {
		t.Errorf("create body type = %v; want road_warrior", createBody["type"])
	}
	if createBody["name"] != peerName {
		t.Errorf("create body name = %v; want %q", createBody["name"], peerName)
	}
	if createBody["public_key"] != clientPK {
		t.Errorf("create body public_key = %v; want %q", createBody["public_key"], clientPK)
	}
	if createBody["preshared_key"] != psk {
		t.Errorf("create body preshared_key = %v; want the configured psk", createBody["preshared_key"])
	}
	for _, stray := range []string{"id", "tunnel_ip"} {
		if _, present := createBody[stray]; present {
			t.Errorf("create body must NOT include %q; got %v", stray, createBody)
		}
	}

	// Assert the rename/disable PATCH fired.
	patches := srv.Requests("PATCH", peerItemPath)
	if len(patches) != 1 {
		t.Fatalf("expected exactly 1 PATCH %s, got %d", peerItemPath, len(patches))
	}
	var patchBody map[string]any
	if err := json.Unmarshal(patches[0].Body, &patchBody); err != nil {
		t.Fatalf("decoding patch body: %v", err)
	}
	if patchBody["name"] != peerName2 {
		t.Errorf("patch body name = %v; want %q", patchBody["name"], peerName2)
	}
	if patchBody["enabled"] != false {
		t.Errorf("patch body enabled = %v; want false", patchBody["enabled"])
	}
}
