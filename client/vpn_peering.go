package client

import (
	"context"
	"fmt"
	"net/url"
)

// VPC-to-VPC "peering" endpoint (verified against the real UserApi
// VpnGatewayController::createPeering + VpnGatewayService::createVpcPeering +
// routes/user_api.php). This is DISTINCT from a "site_to_site" PEER created via
// AddVpnPeer/addPeer (internal/client/vpn_gateway.go) - that flavour lets the
// caller supply an arbitrary remote endpoint/public_key/allowed_ips for a
// third-party (non-platform) gateway. "Peering", by contrast, links TWO
// iaas_vpn_gateway resources OWNED BY THE SAME ACCOUNT, in DIFFERENT VPCs: the
// caller supplies only the remote gateway's id and the service derives
// everything else (endpoint, public_key, allowed_ips, a shared preshared_key,
// and a tunnel IP on each side) and materialises it as a SYMMETRIC PAIR of rows
// in the SAME vpn_gateway_peers table used by AddVpnPeer, tagged
// type="vpc_peering" - there is no separate "peering" table/model.
//
//	CREATE POST /vpn-gateway/{gatewayId}/peering  body {remote_gateway_id (req,
//	                                               uuid of another gateway owned
//	                                               by the same account, in a
//	                                               DIFFERENT vpc)}
//	                                               → {success,message,
//	                                               peers:[localPeer,remotePeer]}
//	                                               (peers[0] belongs to the
//	                                               gatewayId in the path; peers[1]
//	                                               is the symmetric peer created
//	                                               on the remote gateway)
//
// DEVIATION FROM THE PLAN: the plan assumed a create body carrying a remote
// gateway address / CIDRs / PSK (a classic third-party site-to-site shape).
// The controller (UserApi\VpnGatewayController::createPeering, confirmed by
// Read of the source) instead validates only `remote_gateway_id`: uuid, required
// - the rest is 100% server-derived from the two existing gateway rows. There
// is no dedicated "peering" SHOW/DELETE/UPDATE route at all; every read/delete
// goes through the generic peer machinery (GetVpnGateway's embedded peers[] /
// RemoveVpnPeer) because a "peering" IS a peer row.
//
// Async behaviour: createPeering is SYNCHRONOUS - createVpcPeering does its
// work (CIDR/tunnel overlap checks, tunnel IP allocation, peer creation) inside
// one DB transaction and returns; the WireGuard config push to each gateway VM
// afterwards is best-effort (a push failure is logged, not fatal, and does not
// fail the request). There is NO waiter/poll for this resource.
//
// The preshared_key generated for the pairing is $hidden + encrypted
// server-side (same as any other peer's preshared_key - see
// app/Models/VpnGatewayPeer.php) and is NEVER returned by ANY response
// (create or SHOW), so it is not modelled as a resource attribute at all (there
// is nothing to read, and it is not an accepted create input either - the
// server always generates its own).

// CreateVpnPeering creates a VPC-to-VPC peering from gatewayID (the LOCAL side,
// taken from the resource's parent path) to remoteGatewayID (another
// iaas_vpn_gateway owned by the same account, in a different VPC). The
// envelope key is "peers", an array of the two symmetric peer rows created;
// this returns ONLY peers[0] - the row that belongs to gatewayID - since that
// is the row this resource tracks (the symmetric peers[1] row belongs
// conceptually to the OTHER gateway's own iaas_vpn_peering resource, created by
// peering that gateway back to this one, or read out-of-band). A validation
// failure (same VPC, CIDR/tunnel overlap, duplicate peering, remote gateway not
// found/not owned) surfaces as a 200 success:false (or 404) → error.
func (c *Client) CreateVpnPeering(ctx context.Context, gatewayID, remoteGatewayID string) (map[string]any, error) {
	if gatewayID == "" {
		return nil, fmt.Errorf("CreateVpnPeering: empty gatewayID")
	}
	if remoteGatewayID == "" {
		return nil, fmt.Errorf("CreateVpnPeering: empty remoteGatewayID")
	}
	body := map[string]any{"remote_gateway_id": remoteGatewayID}
	// The "peers" envelope key is a JSON ARRAY, not a single object, so doItem
	// (single-object unwrap) can't be used directly; passing "" returns the bare
	// top-level map (after the success:false check) so we can pull "peers" out
	// ourselves.
	top, err := c.doItem(ctx, "POST", "/vpn-gateway/"+url.PathEscape(gatewayID)+"/peering", body, "")
	if err != nil {
		return nil, err
	}
	peersRaw, ok := top["peers"].([]any)
	if !ok || len(peersRaw) == 0 {
		return nil, fmt.Errorf("CreateVpnPeering: response missing a non-empty peers array")
	}
	local, ok := peersRaw[0].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("CreateVpnPeering: peers[0] is not an object (got %T)", peersRaw[0])
	}
	return local, nil
}

// GetVpnPeering resolves a single peering by scanning the parent gateway's
// embedded peers[] array (there is no dedicated peering SHOW route - same
// read-by-scan shape as GetVpnPeer) and additionally requires
// type == "vpc_peering", so a plain road_warrior/site_to_site peer id never
// satisfies a peering lookup (and vice versa). Returns a 404-shaped *APIError
// (IsNotFound = true) when absent or type-mismatched. A 404 on the parent
// gateway propagates (the peering is gone too).
func (c *Client) GetVpnPeering(ctx context.Context, gatewayID, peeringID string) (map[string]any, error) {
	if gatewayID == "" {
		return nil, fmt.Errorf("GetVpnPeering: empty gatewayID")
	}
	if peeringID == "" {
		return nil, fmt.Errorf("GetVpnPeering: empty peeringID")
	}
	gw, err := c.GetVpnGateway(ctx, gatewayID)
	if err != nil {
		return nil, err
	}
	for _, p := range vpnGatewayPeers(gw) {
		id, _ := p["id"].(string)
		if id != peeringID {
			continue
		}
		if t, _ := p["type"].(string); t != "vpc_peering" {
			break // id matches but it's a different peer flavour - treat as not found
		}
		return p, nil
	}
	return nil, &APIError{Status: 404, Message: "VPN gateway peering not found"}
}

// DeleteVpnPeering removes a peering. There is NO dedicated peering-delete
// route: a peering is a row in the same vpn_gateway_peers table as any other
// peer, so deletion reuses the generic peer-removal endpoint
// (DELETE /vpn-gateway/{id}/peer/{peerId} - RemoveVpnPeer). Deleting one side
// does NOT delete the symmetric row on the remote gateway (removePeer only
// scopes by vpn_gateway_id + peer id, and does not follow remote_gateway_id) -
// the remote gateway's own iaas_vpn_peering resource (if configured) manages
// that row independently.
func (c *Client) DeleteVpnPeering(ctx context.Context, gatewayID, peeringID string) error {
	if gatewayID == "" {
		return fmt.Errorf("DeleteVpnPeering: empty gatewayID")
	}
	if peeringID == "" {
		return fmt.Errorf("DeleteVpnPeering: empty peeringID")
	}
	return c.RemoveVpnPeer(ctx, gatewayID, peeringID)
}
