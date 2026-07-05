package client

import (
	"context"
	"fmt"
	"net/url"
)

// VPC VPN Gateway endpoints (verified against the real UserApi
// VpnGatewayController + VpnGatewayService + routes/user_api.php). A VPN gateway
// is a WireGuard endpoint backed by a real VM instance, deployed INTO a VPC's
// PUBLIC subnet, giving remote clients (road-warrior) and remote sites
// (site-to-site / VPC peering) encrypted access to the VPC. Each VPC can have AT
// MOST ONE VPN gateway. The gateway holds a server-generated WireGuard keypair
// (the PRIVATE key is encrypted at rest and $hidden - never returned by SHOW;
// the PUBLIC key IS returned). Peers are children of the gateway, each holding
// the remote side's public key + allowed-ips + tunnel ip; a road-warrior peer's
// client configuration is rendered on demand as a downloadable WireGuard .conf.
//
// ROUTE PATH ASYMMETRY (controller-verified, important): only CREATE is nested
// under the parent VPC - every other operation uses the FLAT /vpn-gateway/{id}
// path (the gateway id alone is sufficient and the controller resolves the VPC
// from the gateway). This differs from the NAT gateway, where ALL operations are
// nested under /vpc/{vpcId}. Consequently the resource needs the parent vpc_id
// ONLY for create; Read/Delete/peer ops use the gateway id alone.
//
//	CREATE  POST   /vpc/{vpcId}/vpn-gateway              body {vpngw_plan_id (req),
//	                                                      vpc_subnet_id (req), name?,
//	                                                      tunnel_subnet?, listen_port?}
//	                                                      → {success,message,gateway:{id,
//	                                                      status:"deploying",...}}
//	SHOW    GET    /vpn-gateway/{id}                     → {success,gateway:{...,peers:[],
//	                                                      vpc:{},instance:{ips:[]}},
//	                                                      other_gateways:[]}
//	DELETE  DELETE /vpn-gateway/{id}                     → {success,message}
//	RETRY   POST   /vpn-gateway/{id}/retry               → {success,message,gateway}
//	                                                      (only for status="error")
//	ADDPEER POST   /vpn-gateway/{id}/peer                body {type (req:road_warrior|
//	                                                      site_to_site), name?, public_key?,
//	                                                      endpoint?, tunnel_ip?, allowed_ips?:[],
//	                                                      preshared_key?, dns?, keepalive?}
//	                                                      → {success,message,peer:{id,...}}
//	UPDPEER PATCH  /vpn-gateway/{id}/peer/{peerId}       body {name?, public_key?, endpoint?,
//	                                                      allowed_ips?:[], preshared_key?,
//	                                                      keepalive?, enabled?}
//	                                                      → {success,message,peer:{...}}
//	DELPEER DELETE /vpn-gateway/{id}/peer/{peerId}       → {success,message}
//	CONFIG  GET    /vpn-gateway/{id}/peer/{peerId}/config → text/plain WireGuard .conf
//	                                                      (NOT JSON; road_warrior peers only,
//	                                                      else 422 success:false)
//
// Async behaviour (controller + service verified):
//   - CREATE is ASYNC: the service records the gateway row with status="deploying"
//     (allocates a public IP, creates the backing VPN-gateway instance, sends a
//     deploy command to the slave) and returns the gateway immediately. The slave
//     provisions the VM and reports back; status flips "deploying" → "active" when
//     healthy, or "error" on failure. There is NO task_id in the create response
//     (the create returns {success,message,gateway}); the async signal is the
//     gateway's own "status" field, polled via the SHOW endpoint. GetVpnGateway IS
//     the poll. Ready value is "active"; the fail value is "error" (a failed
//     gateway can be retried via the /retry endpoint).
//   - DELETE bills final hours, destroys the backing instance (releasing its public
//     IP), deletes peers, and SOFT-DELETES the row, so a subsequent SHOW 404s right
//     away - no delete waiter is required.
//   - Peer add/update/remove are SYNCHRONOUS: the service mutates the peer row and
//     pushes the regenerated WireGuard config to the gateway VM in-line (best
//     effort; a push failure is logged, not fatal). The peer create returns the
//     peer WITH its id under the "peer" key.
//
// Feature gating & errors (controller-verified):
//   - These routes are NOT wrapped in the billing.enabled middleware, so there is
//     NO HTTP 403 billing gate of the volume/static_ip kind. The controller gates
//     the feature in-line: HTTP 403 when the VPC's hypervisor group lacks
//     vpngw_enabled; HTTP 422 when the VPC already has a gateway or the per-account
//     max_vpn_gateways quota is reached; HTTP 200 success:false for any service
//     exception during deploy (no public IP, subnet not public, tunnel overlap,
//     no VPN image, etc.). responseError maps 403/422; doItem surfaces 200
//     success:false (C3).
//   - Empty-id guards are applied on every path-id argument (consistency).
//   - Every id is url.PathEscape'd into the path.

// CreateVpnGateway deploys the VPC's VPN gateway from the supplied prebuilt body
// (vpngw_plan_id + vpc_subnet_id required; name/tunnel_subnet/listen_port
// optional). Create is ASYNC: the returned gateway carries the id but
// status="deploying"; the caller must poll GetVpnGateway until status="active".
// The "gateway" envelope is unwrapped. A feature-gate failure (vpngw not enabled
// → 403, one-per-vpc / quota → 422, service exception → 200 success:false)
// surfaces as an error.
func (c *Client) CreateVpnGateway(ctx context.Context, vpcID string, body map[string]any) (map[string]any, error) {
	if vpcID == "" {
		return nil, fmt.Errorf("CreateVpnGateway: empty vpcID")
	}
	return c.doItem(ctx, "POST", "/vpc/"+url.PathEscape(vpcID)+"/vpn-gateway", body, "gateway")
}

// GetVpnGateway fetches the VPN gateway by its id via the FLAT /vpn-gateway/{id}
// path (the parent VPC is NOT in the path - see the route asymmetry note above).
// It unwraps the "gateway" envelope. The returned object carries the embedded
// "peers" array, the "vpc" object, and the backing "instance" (with its ips).
// The encrypted WireGuard private key is $hidden and is NEVER present. A 404
// (absent, or owned by a different account) is an *APIError recognised by
// IsNotFound. GetVpnGateway is the async poll source for create (scan "status"
// for "active").
func (c *Client) GetVpnGateway(ctx context.Context, id string) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("GetVpnGateway: empty id")
	}
	return c.doItem(ctx, "GET", "/vpn-gateway/"+url.PathEscape(id), nil, "gateway")
}

// DeleteVpnGateway deletes the VPN gateway: the service bills final hours,
// destroys the backing instance (releasing its public IP), deletes the peers, and
// soft-deletes the row immediately, so a subsequent SHOW 404s right away - no
// delete waiter is required. A failure is signalled with success:false at HTTP
// 200, so doVoid checks the flag.
func (c *Client) DeleteVpnGateway(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("DeleteVpnGateway: empty id")
	}
	return c.doVoid(ctx, "DELETE", "/vpn-gateway/"+url.PathEscape(id), nil)
}

// AddVpnPeer adds a WireGuard peer to the gateway from the supplied prebuilt body
// (type required; name/public_key/endpoint/tunnel_ip/allowed_ips/preshared_key/
// dns/keepalive optional). The "peer" envelope is unwrapped, returning the
// created peer WITH its id. A validation/service failure surfaces as 200
// success:false (or 422) → error.
func (c *Client) AddVpnPeer(ctx context.Context, gatewayID string, body map[string]any) (map[string]any, error) {
	if gatewayID == "" {
		return nil, fmt.Errorf("AddVpnPeer: empty gatewayID")
	}
	return c.doItem(ctx, "POST", "/vpn-gateway/"+url.PathEscape(gatewayID)+"/peer", body, "peer")
}

// UpdateVpnPeer patches an existing peer (name/public_key/endpoint/allowed_ips/
// preshared_key/keepalive/enabled). The "peer" envelope is unwrapped, returning
// the fresh peer.
func (c *Client) UpdateVpnPeer(ctx context.Context, gatewayID, peerID string, body map[string]any) (map[string]any, error) {
	if gatewayID == "" {
		return nil, fmt.Errorf("UpdateVpnPeer: empty gatewayID")
	}
	if peerID == "" {
		return nil, fmt.Errorf("UpdateVpnPeer: empty peerID")
	}
	path := "/vpn-gateway/" + url.PathEscape(gatewayID) + "/peer/" + url.PathEscape(peerID)
	return c.doItem(ctx, "PATCH", path, body, "peer")
}

// RemoveVpnPeer deletes a peer from the gateway. doVoid checks the success flag
// (a failure surfaces as 200 success:false).
func (c *Client) RemoveVpnPeer(ctx context.Context, gatewayID, peerID string) error {
	if gatewayID == "" {
		return fmt.Errorf("RemoveVpnPeer: empty gatewayID")
	}
	if peerID == "" {
		return fmt.Errorf("RemoveVpnPeer: empty peerID")
	}
	path := "/vpn-gateway/" + url.PathEscape(gatewayID) + "/peer/" + url.PathEscape(peerID)
	return c.doVoid(ctx, "DELETE", path, nil)
}

// GetVpnPeer resolves a single peer by scanning the parent gateway's embedded
// peers[] array (there is NO individual peer SHOW route - the gateway SHOW embeds
// the peers). It returns the matching peer object or a 404-shaped *APIError
// (IsNotFound = true) when the id is absent. This is the read-by-scan source for
// the peer resource's Read. A 404 on the parent gateway propagates (the peer is
// gone too).
func (c *Client) GetVpnPeer(ctx context.Context, gatewayID, peerID string) (map[string]any, error) {
	if gatewayID == "" {
		return nil, fmt.Errorf("GetVpnPeer: empty gatewayID")
	}
	if peerID == "" {
		return nil, fmt.Errorf("GetVpnPeer: empty peerID")
	}
	gw, err := c.GetVpnGateway(ctx, gatewayID)
	if err != nil {
		return nil, err
	}
	for _, p := range vpnGatewayPeers(gw) {
		if id, ok := p["id"].(string); ok && id == peerID {
			return p, nil
		}
	}
	return nil, &APIError{Status: 404, Message: "VPN gateway peer not found"}
}

// DownloadVpnPeerConfig fetches a road-warrior peer's WireGuard client
// configuration. The endpoint returns the config as text/plain (NOT JSON - it is
// an attachment download), so this uses the raw transport (doRaw) rather than
// doItem. The rendered config uses a "[YOUR_PRIVATE_KEY]" placeholder for the
// client's own private key (the server does NOT generate or store it), but it
// DOES contain the gateway's public key + endpoint, so callers treat it as
// sensitive. A non-road-warrior peer yields 422 success:false (JSON) → error.
func (c *Client) DownloadVpnPeerConfig(ctx context.Context, gatewayID, peerID string) (string, error) {
	if gatewayID == "" {
		return "", fmt.Errorf("DownloadVpnPeerConfig: empty gatewayID")
	}
	if peerID == "" {
		return "", fmt.Errorf("DownloadVpnPeerConfig: empty peerID")
	}
	path := "/vpn-gateway/" + url.PathEscape(gatewayID) + "/peer/" + url.PathEscape(peerID) + "/config"
	return c.doRaw(ctx, "GET", path)
}

// vpnGatewayPeers extracts the embedded "peers" array from a gateway SHOW object,
// coercing each element to a map. A missing/empty/malformed array yields nil.
func vpnGatewayPeers(gw map[string]any) []map[string]any {
	raw, ok := gw["peers"].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, e := range raw {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}
