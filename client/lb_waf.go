package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

// Load Balancer WAF policy endpoint (verified against Master's
// App\Http\Controllers\UserApi\LbWafController + routes/user_api.php, S6 Task
// 1). Policy is a SINGLETON child of a load balancer:
//
//	SHOW    GET    /load-balancer/{lbId}/waf/policy  -> 200 {success, policy:{...}|null}
//	UPSERT  PUT    /load-balancer/{lbId}/waf/policy  -> 200 {success, policy:{...}}
//	DISABLE DELETE /load-balancer/{lbId}/waf/policy  -> 200 {success, message}
//
// The policy object's wire field names are the customer-facing names the S5
// panel and S6 API both use - NOT the raw `lb_waf_policies` column names:
//
//	{id, enabled, mode, fail_mode, crs_enabled, sensitivity,
//	 response_inspection, full_audit, exclusions}
//
// ("sensitivity" is the raw `paranoia_level` column; "exclusions" is the raw
// `crs_exclusions` column - product-identity lock, no "paranoia" anywhere in
// a payload. There is no retention_days/s3_archive field here: those are
// global `system.lb_waf.*` settings, not per-policy.)
//
// All WAF writes are SYNCHRONOUS (the service regenerates config + reloads
// the appliance internally, or is a safe no-op when the load balancer isn't
// active yet).

// GetLBWafPolicy returns the singleton WAF policy of a load balancer. A
// response with policy:null (no policy configured) is surfaced as a
// 404-shaped *APIError so the resource Read can RemoveResource cleanly.
//
// This does NOT reuse doItem: decodeItem's generic object-unwrap treats a
// JSON `null` under the "policy" key as a decode error (json.Unmarshal turns
// `null` into an untyped nil, which fails the `map[string]any` type
// assertion in decodeItem, producing "expected object under \"policy\"; got
// <nil>" instead of a clean not-found). That would make client.IsNotFound()
// return false for the ordinary "no policy configured" case and break
// Read/refresh for exactly the state importers rely on. Decoding it here by
// hand keeps doItem's contract (always a real object) intact for every other
// caller.
func (c *Client) GetLBWafPolicy(ctx context.Context, lbID string) (map[string]any, error) {
	if lbID == "" {
		return nil, fmt.Errorf("GetLBWafPolicy: empty lbID")
	}
	path := "/load-balancer/" + url.PathEscape(lbID) + "/waf/policy"

	resp, raw, err := c.do(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	if err := responseError(resp, raw); err != nil {
		return nil, err
	}

	var top map[string]any
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	if err := checkSuccessFlag(top); err != nil {
		return nil, err
	}

	policy, ok := top["policy"]
	if !ok || policy == nil {
		return nil, &APIError{Status: 404, Message: "load balancer WAF policy not configured"}
	}
	obj, ok := policy.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected object under \"policy\"; got %T", policy)
	}
	return obj, nil
}

// PutLBWafPolicy upserts the singleton WAF policy. The response always
// carries a real object (never null), so the shared doItem path is safe here.
func (c *Client) PutLBWafPolicy(ctx context.Context, lbID string, body map[string]any) (map[string]any, error) {
	if lbID == "" {
		return nil, fmt.Errorf("PutLBWafPolicy: empty lbID")
	}
	path := "/load-balancer/" + url.PathEscape(lbID) + "/waf/policy"
	return c.doItem(ctx, "PUT", path, body, "policy")
}

// DeleteLBWafPolicy disables the WAF (removes the policy row and re-delivers
// config without the WAF chain).
func (c *Client) DeleteLBWafPolicy(ctx context.Context, lbID string) error {
	if lbID == "" {
		return fmt.Errorf("DeleteLBWafPolicy: empty lbID")
	}
	path := "/load-balancer/" + url.PathEscape(lbID) + "/waf/policy"
	return c.doVoid(ctx, "DELETE", path, nil)
}
