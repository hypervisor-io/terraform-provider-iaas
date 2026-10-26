package client

import (
	"context"
	"fmt"
	"net/url"
)

// VPN gateway IPsec site-to-site connection endpoints (spec 17, task 337-IPSEC;
// verified against the user API VpnIpsecController, the shared
// HandlesIpsecConnections trait, IpsecPresenter and IpsecErrorResponse).
//
// A connection is a CHILD of a VPN gateway: every route lives under
// /vpn-gateway/{gatewayId}/ipsec and the connection has its own SHOW route.
//
//	LIST    GET    /vpn-gateway/{gw}/ipsec                       -> {success, connections:[...],
//	                                                               gateway:{ipsec_state, ipsec_version,
//	                                                               ipsec_apply_state, ipsec_apply_error,
//	                                                               ipsec_generation,
//	                                                               ipsec_applied_generation}}
//	CREATE  POST   /vpn-gateway/{gw}/ipsec                       -> 201 {success, message, connection,
//	                                                               warnings}
//	SHOW    GET    /vpn-gateway/{gw}/ipsec/{id}                  -> {success, connection}
//	UPDATE  PATCH  /vpn-gateway/{gw}/ipsec/{id}                  -> {success, message, connection, warnings}
//	DELETE  DELETE /vpn-gateway/{gw}/ipsec/{id}                  -> {success, message, connection}
//	RESTART POST   /vpn-gateway/{gw}/ipsec/{id}/restart          -> {success, message, connection}
//	                                                               (503 node_dispatch_unconfirmed)
//	SETTINGS GET   /vpn-gateway/{gw}/ipsec/{id}/remote-settings  -> {success, remote_settings}
//	                                                               (?format=text answers text/plain)
//
// Async behaviour: every write is recorded at once and APPLIED to the gateway
// asynchronously. The connection carries a derived apply_state (pending ->
// applied, or failed); sa_state (unknown, down, connecting, partial,
// established) is the live tunnel status and depends on the remote peer, so it
// is never a convergence condition. A delete removes the row from every read at
// once (SHOW 404s) but the gateway keeps the removed networks reserved until the
// node confirms the generation: the LIST gateway block (ipsec_generation ==
// ipsec_applied_generation) is the only signal for that.
//
// The pre-shared key (psk) is write-only: it is never returned by any read.
//
// Typed refusals carry a machine-readable "reason" (APIError.Reason): 422 input
// reasons (invalid_psk, invalid_remote_subnet, destination_overlap, ...), 409
// state reasons (ipsec_installing, ipsec_install_failed, connection_limit_reached,
// name_taken, ...) and 404 not_found.

// ReasonIpsecInstalling is the typed 409 reason a write gets while the gateway's
// IPsec packages are still being installed; it is a definite refusal (nothing was
// recorded) and safe to retry after a wait.
const ReasonIpsecInstalling = "ipsec_installing"

func vpnIpsecBase(gatewayID string) string {
	return "/vpn-gateway/" + url.PathEscape(gatewayID) + "/ipsec"
}

// ListVpnIpsecConnections returns the gateway's live connections and the gateway
// IPsec state block (install state, apply state, generations). Tombstoned
// (deleted) connections are not listed.
func (c *Client) ListVpnIpsecConnections(ctx context.Context, gatewayID string) ([]map[string]any, map[string]any, error) {
	if gatewayID == "" {
		return nil, nil, fmt.Errorf("ListVpnIpsecConnections: empty gatewayID")
	}
	top, err := c.doItem(ctx, "GET", vpnIpsecBase(gatewayID), nil, "")
	if err != nil {
		return nil, nil, err
	}
	var conns []map[string]any
	if raw, ok := top["connections"].([]any); ok {
		for _, e := range raw {
			if m, ok := e.(map[string]any); ok {
				conns = append(conns, m)
			}
		}
	}
	gw, _ := top["gateway"].(map[string]any)
	return conns, gw, nil
}

// GetVpnIpsecConnection fetches one connection (with its live status). A missing
// or deleted connection, or a gateway owned by another account, is a 404
// (IsNotFound).
func (c *Client) GetVpnIpsecConnection(ctx context.Context, gatewayID, id string) (map[string]any, error) {
	if gatewayID == "" {
		return nil, fmt.Errorf("GetVpnIpsecConnection: empty gatewayID")
	}
	if id == "" {
		return nil, fmt.Errorf("GetVpnIpsecConnection: empty id")
	}
	return c.doItem(ctx, "GET", vpnIpsecBase(gatewayID)+"/"+url.PathEscape(id), nil, "connection")
}

// CreateVpnIpsecConnection creates a connection from the prebuilt body. It is
// sent in exactly ONE attempt: a 429/5xx or transport error cannot establish that
// nothing was recorded, so it is returned as-is and the caller resolves it by
// reading the list back. The second return value is the response "warnings"
// (machine codes such as security_group_blocks_esp).
func (c *Client) CreateVpnIpsecConnection(ctx context.Context, gatewayID string, body map[string]any) (map[string]any, []string, error) {
	if gatewayID == "" {
		return nil, nil, fmt.Errorf("CreateVpnIpsecConnection: empty gatewayID")
	}
	resp, raw, err := c.doWithAttempts(ctx, "POST", vpnIpsecBase(gatewayID), body, nil, 1)
	if err != nil {
		return nil, nil, err
	}
	if err := responseError(resp, raw); err != nil {
		return nil, nil, err
	}
	return decodeConnectionWithWarnings(raw)
}

// UpdateVpnIpsecConnection patches a connection with the changed fields only (an
// omitted psk keeps the stored key, a new psk replaces it). A replay of the same
// PATCH is harmless (an unchanged value changes nothing), so the transport retry
// applies.
func (c *Client) UpdateVpnIpsecConnection(ctx context.Context, gatewayID, id string, body map[string]any) (map[string]any, []string, error) {
	if gatewayID == "" {
		return nil, nil, fmt.Errorf("UpdateVpnIpsecConnection: empty gatewayID")
	}
	if id == "" {
		return nil, nil, fmt.Errorf("UpdateVpnIpsecConnection: empty id")
	}
	resp, raw, err := c.do(ctx, "PATCH", vpnIpsecBase(gatewayID)+"/"+url.PathEscape(id), body)
	if err != nil {
		return nil, nil, err
	}
	if err := responseError(resp, raw); err != nil {
		return nil, nil, err
	}
	return decodeConnectionWithWarnings(raw)
}

// DeleteVpnIpsecConnection deletes a connection. The row disappears from reads at
// once; removal from the gateway is asynchronous (see the file comment). A replay
// after a recorded delete answers 404, which callers treat as done.
func (c *Client) DeleteVpnIpsecConnection(ctx context.Context, gatewayID, id string) error {
	if gatewayID == "" {
		return fmt.Errorf("DeleteVpnIpsecConnection: empty gatewayID")
	}
	if id == "" {
		return fmt.Errorf("DeleteVpnIpsecConnection: empty id")
	}
	return c.doVoid(ctx, "DELETE", vpnIpsecBase(gatewayID)+"/"+url.PathEscape(id), nil)
}

// RestartVpnIpsecConnection terminates and re-initiates the tunnel. Sent once: a
// 503 node_dispatch_unconfirmed means the restart is stored but the node did not
// confirm it yet.
func (c *Client) RestartVpnIpsecConnection(ctx context.Context, gatewayID, id string) (map[string]any, error) {
	if gatewayID == "" {
		return nil, fmt.Errorf("RestartVpnIpsecConnection: empty gatewayID")
	}
	if id == "" {
		return nil, fmt.Errorf("RestartVpnIpsecConnection: empty id")
	}
	return c.doItemOnce(ctx, "POST", vpnIpsecBase(gatewayID)+"/"+url.PathEscape(id)+"/restart", nil, "connection")
}

// GetVpnIpsecRemoteSettings returns the values to enter on the other device
// (identities, proposals, networks, lifetimes, NAT traversal, route hint). The
// pre-shared key is never part of it.
func (c *Client) GetVpnIpsecRemoteSettings(ctx context.Context, gatewayID, id string) (map[string]any, error) {
	if gatewayID == "" {
		return nil, fmt.Errorf("GetVpnIpsecRemoteSettings: empty gatewayID")
	}
	if id == "" {
		return nil, fmt.Errorf("GetVpnIpsecRemoteSettings: empty id")
	}
	return c.doItem(ctx, "GET", vpnIpsecBase(gatewayID)+"/"+url.PathEscape(id)+"/remote-settings", nil, "remote_settings")
}

// GetVpnIpsecRemoteSettingsText returns the same values as a plain-text copy
// (?format=text answers text/plain, not JSON). It never contains the key.
func (c *Client) GetVpnIpsecRemoteSettingsText(ctx context.Context, gatewayID, id string) (string, error) {
	if gatewayID == "" {
		return "", fmt.Errorf("GetVpnIpsecRemoteSettingsText: empty gatewayID")
	}
	if id == "" {
		return "", fmt.Errorf("GetVpnIpsecRemoteSettingsText: empty id")
	}
	return c.doRaw(ctx, "GET", vpnIpsecBase(gatewayID)+"/"+url.PathEscape(id)+"/remote-settings?format=text")
}

// decodeConnectionWithWarnings unwraps {connection, warnings} (also the admin
// pre-nested {data:{connection, warnings}} shape, which a user call never gets
// but costs nothing to accept).
func decodeConnectionWithWarnings(raw []byte) (map[string]any, []string, error) {
	top, err := decodeItem(raw, "")
	if err != nil {
		return nil, nil, err
	}
	if data, ok := top["data"].(map[string]any); ok {
		if _, has := data["connection"]; has {
			top = data
		}
	}
	conn, ok := top["connection"].(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("expected object under %q in the IPsec response", "connection")
	}
	var warnings []string
	if w, ok := top["warnings"].([]any); ok {
		for _, e := range w {
			if s, ok := e.(string); ok {
				warnings = append(warnings, s)
			}
		}
	}
	return conn, warnings, nil
}
