package client

import "context"

// MicroVM account default-network settings (MV7-2/MV7-18, contract C4/C8.6;
// user_api, bearer token). A per-account default lets a create omit
// `network`/`metadata.location_id` entirely: MicrovmService::create() and
// every E2B `POST /sandboxes` call resolve it via MicrovmNetworkDefaults.
//
//	GET /microvm/settings -> {"default_network": {"kind":"public"} | {"kind":"vpc","vpc_subnet_id":uuid} | null}
//	PUT /microvm/settings body {"default_network": {...} | null} -> same shape echoed back
//
// Unlike most microvm endpoints, both responses are a bare object with no
// "success"/wrapper key - decodeItem's key="" path returns it unwrapped.

// GetMicrovmSettings returns the account's stored default microVM network,
// or a nil "default_network" key when none is set.
func (c *Client) GetMicrovmSettings(ctx context.Context) (map[string]any, error) {
	return c.doItem(ctx, "GET", "/microvm/settings", nil, "")
}

// UpdateMicrovmSettings sets (or, given a nil defaultNetwork, clears) the
// account's default microVM network and returns the stored value. A `kind:
// public` default needs no subnet_id (C8.1/C8.6 - the platform auto-assigns
// one at create time); a `kind: vpc` default must name a `vpc_subnet_id`
// owned by the account.
func (c *Client) UpdateMicrovmSettings(ctx context.Context, defaultNetwork map[string]any) (map[string]any, error) {
	// defaultNetwork is nil-able by design (clears the setting); the map
	// literal itself is never nil, so it always marshals to a JSON object
	// with an explicit "default_network" key (never an omitted field).
	body := map[string]any{"default_network": defaultNetwork}
	return c.doItem(ctx, "PUT", "/microvm/settings", body, "")
}
