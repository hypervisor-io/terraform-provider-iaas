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

// Load Balancer WAF custom-rule endpoints (S6 Task 2, verified against
// Master's App\Http\Controllers\UserApi\LbWafController + routes/user_api.php):
//
//	LIST   GET    /load-balancer/{lbId}/waf/rules              -> 200 {success, rules:[{...}]}
//	CREATE POST   /load-balancer/{lbId}/waf/rules              -> 200 {success, rule:{...}}
//	UPDATE PATCH  /load-balancer/{lbId}/waf/rule/{ruleId}       -> 200 {success, rule:{...}}
//	DELETE DELETE /load-balancer/{lbId}/waf/rule/{ruleId}       -> 200 {success, message}
//
// The rule object's wire field names are the real `lb_waf_rules` columns
// only: {id, rule_id, name, seclang, enabled, sort_order}. Custom rule ids
// must be 190000-199999 (rejected with a 422 otherwise). A rule may be
// authored either directly (rule_id + raw_seclang) or via a guided-builder
// convenience shape (target/operator/match_value/action/priority, rendered
// into `seclang` server-side) - see StoreWafRuleRequest's docblock in Master.
//
// ListLBWafRules's response is a bare Eloquent collection under "rules"
// ({"success":true,"rules":[...]}), NOT a Laravel paginator ({"data":[...]}) -
// the shared doList helper only understands top-level arrays or a "data"
// envelope (client/decode.go's decodeList), so it would fail on this shape
// with "no 'data' array". namedList is the existing helper for exactly this
// envelope (see ListLBSecurityGroupRules).

// ListLBWafRules lists the custom WAF rules of a load balancer.
func (c *Client) ListLBWafRules(ctx context.Context, lbID string) ([]map[string]any, error) {
	if lbID == "" {
		return nil, fmt.Errorf("ListLBWafRules: empty lbID")
	}
	path := "/load-balancer/" + url.PathEscape(lbID) + "/waf/rules"
	return c.namedList(ctx, "GET", path, "rules")
}

// CreateLBWafRule creates a custom WAF rule (rule_id must be 190000-199999).
func (c *Client) CreateLBWafRule(ctx context.Context, lbID string, body map[string]any) (map[string]any, error) {
	if lbID == "" {
		return nil, fmt.Errorf("CreateLBWafRule: empty lbID")
	}
	path := "/load-balancer/" + url.PathEscape(lbID) + "/waf/rules"
	return c.doItem(ctx, "POST", path, body, "rule")
}

// UpdateLBWafRule patches a custom WAF rule.
func (c *Client) UpdateLBWafRule(ctx context.Context, lbID, ruleID string, body map[string]any) (map[string]any, error) {
	if lbID == "" || ruleID == "" {
		return nil, fmt.Errorf("UpdateLBWafRule: empty lbID or ruleID")
	}
	path := "/load-balancer/" + url.PathEscape(lbID) + "/waf/rule/" + url.PathEscape(ruleID)
	return c.doItem(ctx, "PATCH", path, body, "rule")
}

// DeleteLBWafRule removes a custom WAF rule.
func (c *Client) DeleteLBWafRule(ctx context.Context, lbID, ruleID string) error {
	if lbID == "" || ruleID == "" {
		return fmt.Errorf("DeleteLBWafRule: empty lbID or ruleID")
	}
	path := "/load-balancer/" + url.PathEscape(lbID) + "/waf/rule/" + url.PathEscape(ruleID)
	return c.doVoid(ctx, "DELETE", path, nil)
}

// GetLBWafRule resolves a single rule by scanning the rules list (there is no
// individual SHOW route). Returns a 404-shaped *APIError when absent.
func (c *Client) GetLBWafRule(ctx context.Context, lbID, ruleID string) (map[string]any, error) {
	rules, err := c.ListLBWafRules(ctx, lbID)
	if err != nil {
		return nil, err
	}
	for _, r := range rules {
		if id, ok := r["id"].(string); ok && id == ruleID {
			return r, nil
		}
	}
	return nil, &APIError{Status: 404, Message: "load balancer WAF rule not found"}
}

// WAF event log endpoints (S6 Task 3, spec 17). Read-only log access, owner-
// scoped server-side by Master's LbWafLogService - OpenTofu does not model
// these as a resource (log/telemetry reads and an imperative export action
// have no declarative state), they exist here only so the MCP server can
// reuse this client:
//
//	QUERY  GET  /load-balancer/{lbId}/waf/events         -> 200 {success, events:{data:[...],current_page,total,...}}
//	EXPORT POST /load-balancer/{lbId}/waf/events/export   -> 200 {success, download_url}
//
// QueryLBWafEvents lists one page of the owner-scoped WAF event log.
// filters keys: from, to, client_ip, rule_id, action, page, per_page -
// forwarded as query params. The response nests a Laravel paginator under
// "events" (not a bare array or top-level "data") - namedPaginatorList is the
// existing helper for exactly this envelope shape (see ListVPNGateways).
func (c *Client) QueryLBWafEvents(ctx context.Context, lbID string, filters map[string]string) ([]map[string]any, error) {
	if lbID == "" {
		return nil, fmt.Errorf("QueryLBWafEvents: empty lbID")
	}
	q := url.Values{}
	for k, v := range filters {
		if v != "" {
			q.Set(k, v)
		}
	}
	path := "/load-balancer/" + url.PathEscape(lbID) + "/waf/events"
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}
	return c.namedPaginatorList(ctx, "GET", path, "events")
}

// ExportLBWafEvents archives the filtered events to object storage and
// returns a temporary download URL. The response envelope has no nested
// object key ({"success":true,"download_url":"..."}), so key is "" (bare
// top-level envelope).
func (c *Client) ExportLBWafEvents(ctx context.Context, lbID string, filters map[string]any) (string, error) {
	if lbID == "" {
		return "", fmt.Errorf("ExportLBWafEvents: empty lbID")
	}
	path := "/load-balancer/" + url.PathEscape(lbID) + "/waf/events/export"
	obj, err := c.doItem(ctx, "POST", path, filters, "")
	if err != nil {
		return "", err
	}
	if u, ok := obj["download_url"].(string); ok {
		return u, nil
	}
	return "", fmt.Errorf("ExportLBWafEvents: response missing download_url")
}
