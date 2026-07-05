package datasources_test

import (
	"net/http"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/iaas/terraform-provider-iaas/internal/acctest"
)

// TestUnitVpnPeerConfig_download - mock-backed data-source proof.
//
// data "iaas_vpn_peer_config" "t" { gateway_id = ...; peer_id = ... } reads
// GET /vpn-gateway/{id}/peer/{peerId}/config, which returns a RAW text/plain
// WireGuard .conf (NOT JSON), and exposes it as the sensitive computed `config`.
func TestUnitVpnPeerConfig_download(t *testing.T) {
	ensureTFBinary(t)

	const (
		gwID   = "22222222-2222-2222-2222-222222222222"
		peerID = "77777777-7777-7777-7777-777777777777"
		wgConf = "[Interface]\nPrivateKey = [YOUR_PRIVATE_KEY]\nAddress = 10.99.0.2/32\n\n[Peer]\nPublicKey = Z2F0ZXdheXB1YmtleQ==\nAllowedIPs = 192.168.0.0/16, 10.99.0.0/24\nEndpoint = 203.0.113.9:51820\nPersistentKeepalive = 25\n"
	)

	srv := acctest.NewMockServer(t)
	srv.Handle("GET", "/vpn-gateway/"+gwID+"/peer/"+peerID+"/config", func(w http.ResponseWriter, _ *http.Request) {
		// RAW text/plain body - the config endpoint is an attachment download,
		// NOT a JSON envelope.
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(wgConf))
	})

	cfg := acctest.ProviderConfig(srv.Endpoint()) + `
data "iaas_vpn_peer_config" "t" {
  gateway_id = "` + gwID + `"
  peer_id    = "` + peerID + `"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.iaas_vpn_peer_config.t", "gateway_id", gwID),
					resource.TestCheckResourceAttr("data.iaas_vpn_peer_config.t", "peer_id", peerID),
					resource.TestCheckResourceAttr("data.iaas_vpn_peer_config.t", "config", wgConf),
				),
			},
		},
	})
}

// TestUnitVpnPeerConfig_notRoadWarrior - a 422 (config download is
// road-warrior-only) surfaces as a clear error.
func TestUnitVpnPeerConfig_notRoadWarrior(t *testing.T) {
	ensureTFBinary(t)

	const (
		gwID   = "22222222-2222-2222-2222-222222222222"
		peerID = "88888888-8888-8888-8888-888888888888"
	)

	srv := acctest.NewMockServer(t)
	srv.Handle("GET", "/vpn-gateway/"+gwID+"/peer/"+peerID+"/config", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"success": false,
			"message": "Config download is only available for road_warrior peers",
		})
	})

	cfg := acctest.ProviderConfig(srv.Endpoint()) + `
data "iaas_vpn_peer_config" "t" {
  gateway_id = "` + gwID + `"
  peer_id    = "` + peerID + `"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config:      cfg,
				ExpectError: regexp.MustCompile(`road_warrior`),
			},
		},
	})
}
