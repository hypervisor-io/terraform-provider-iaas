package client

import (
	"context"
	"fmt"
	"net/url"
)

// Load Balancer ROUTING RULE endpoints (verified against the real UserApi
// LoadBalancerController / LoadBalancerService + routes/user_api.php).
//
// A routing rule is a child of a FRONTEND (L7 path/host/header routing), so its
// routes carry BOTH the lb id and the frontend id in the path:
//
//	CREATE  POST   /load-balancer/{lbId}/frontend/{frontendId}/rules
//	                  body {lb_backend_id (req), match_type?, match_value (req), match_host?,
//	                  match_header_name?, priority?, enabled?}
//	                  → 200 {success,message,rule:{id,...},sync}
//	UPDATE  PATCH  /load-balancer/{lbId}/frontend/{frontendId}/rule/{ruleId}
//	                  body {lb_backend_id?, match_type?, match_value?, match_host?,
//	                  match_header_name?, priority?, enabled?}
//	                  → 200 {success,message,rule:{...},sync}
//	DELETE  DELETE /load-balancer/{lbId}/frontend/{frontendId}/rule/{ruleId} → 200 {success,message,sync}
//
// IMPORTANT field-name deviation vs the controller's #[BodyParam] annotation: the
// annotation documents "condition_type", "condition_value" and "backend_id", but
// the real service (LoadBalancerService::storeRoutingRule / updateRoutingRule)
// reads the columns "match_type" (enum path_prefix|path_exact|host|header|any),
// "match_value", "match_host", "match_header_name", and the backend FK
// "lb_backend_id".
//
// Child writes are SYNCHRONOUS (syncConfig runs internally). There is NO
// individual rule SHOW route — rules are EMBEDDED in the LB SHOW under
// load_balancer.frontends[].routing_rules[]. GetLBRoutingRule reads-by-scan.

// CreateLBRoutingRule creates a routing rule under the given frontend.
func (c *Client) CreateLBRoutingRule(ctx context.Context, lbID, frontendID string, body map[string]any) (map[string]any, error) {
	if lbID == "" {
		return nil, fmt.Errorf("CreateLBRoutingRule: empty lbID")
	}
	if frontendID == "" {
		return nil, fmt.Errorf("CreateLBRoutingRule: empty frontendID")
	}
	path := "/load-balancer/" + url.PathEscape(lbID) + "/frontend/" + url.PathEscape(frontendID) + "/rules"
	return c.doItem(ctx, "POST", path, body, "rule")
}

// UpdateLBRoutingRule patches an existing routing rule. The "rule" envelope is unwrapped.
func (c *Client) UpdateLBRoutingRule(ctx context.Context, lbID, frontendID, ruleID string, body map[string]any) (map[string]any, error) {
	if lbID == "" {
		return nil, fmt.Errorf("UpdateLBRoutingRule: empty lbID")
	}
	if frontendID == "" {
		return nil, fmt.Errorf("UpdateLBRoutingRule: empty frontendID")
	}
	if ruleID == "" {
		return nil, fmt.Errorf("UpdateLBRoutingRule: empty ruleID")
	}
	path := "/load-balancer/" + url.PathEscape(lbID) + "/frontend/" + url.PathEscape(frontendID) + "/rule/" + url.PathEscape(ruleID)
	return c.doItem(ctx, "PATCH", path, body, "rule")
}

// DeleteLBRoutingRule removes a routing rule from its frontend. doVoid checks the flag.
func (c *Client) DeleteLBRoutingRule(ctx context.Context, lbID, frontendID, ruleID string) error {
	if lbID == "" {
		return fmt.Errorf("DeleteLBRoutingRule: empty lbID")
	}
	if frontendID == "" {
		return fmt.Errorf("DeleteLBRoutingRule: empty frontendID")
	}
	if ruleID == "" {
		return fmt.Errorf("DeleteLBRoutingRule: empty ruleID")
	}
	path := "/load-balancer/" + url.PathEscape(lbID) + "/frontend/" + url.PathEscape(frontendID) + "/rule/" + url.PathEscape(ruleID)
	return c.doVoid(ctx, "DELETE", path, nil)
}

// GetLBRoutingRule resolves a single routing rule by scanning the parent load
// balancer's embedded frontends[].routing_rules[]. Returns a 404-shaped *APIError
// (IsNotFound) when the frontend or the rule id is absent.
func (c *Client) GetLBRoutingRule(ctx context.Context, lbID, frontendID, ruleID string) (map[string]any, error) {
	if lbID == "" {
		return nil, fmt.Errorf("GetLBRoutingRule: empty lbID")
	}
	if frontendID == "" {
		return nil, fmt.Errorf("GetLBRoutingRule: empty frontendID")
	}
	if ruleID == "" {
		return nil, fmt.Errorf("GetLBRoutingRule: empty ruleID")
	}
	lb, err := c.GetLoadBalancer(ctx, lbID)
	if err != nil {
		return nil, err
	}
	for _, f := range lbChildren(lb, "frontends") {
		fid, _ := f["id"].(string)
		if fid != frontendID {
			continue
		}
		for _, rule := range lbNestedChild(f, "routing_rules") {
			if id, ok := rule["id"].(string); ok && id == ruleID {
				return rule, nil
			}
		}
	}
	return nil, &APIError{Status: 404, Message: "load balancer routing rule not found"}
}
