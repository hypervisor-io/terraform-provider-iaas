package client

import (
	"context"
	"fmt"
	"net/url"
)

// Load Balancer FRONTEND endpoints (verified against the real UserApi
// LoadBalancerController / LoadBalancerService + routes/user_api.php).
//
// A frontend is a child of a load balancer (a listener: port + protocol):
//
//	CREATE  POST   /load-balancer/{lbId}/frontends            body {name (req), mode?, port (req),
//	                  protocol?, ssl_certificate_id?, default_backend_id?, enabled?}
//	                  → 200 {success,message,frontend:{id,...},sync}; 200 success:false on a
//	                  (port,protocol) conflict
//	UPDATE  PATCH  /load-balancer/{lbId}/frontend/{frontendId} body {name?, mode?, port?, protocol?,
//	                  ssl_certificate_id?, default_backend_id?, enabled?}
//	                  → 200 {success,message,frontend:{...},sync}; success:false on a port conflict
//	DELETE  DELETE /load-balancer/{lbId}/frontend/{frontendId} → 200 {success,message,sync}
//
// IMPORTANT field-name deviation vs the controller's #[BodyParam] annotation: the
// annotation documents "bind_port" and an "http|tcp" mode, but the real service
// (LoadBalancerService::createFrontend / updateFrontend) reads the columns
// "port" (int) and "protocol" (enum http|https|tcp|udp), plus a separate "mode"
// enum (http|tcp). The listener identity is (port, protocol), which is the
// table's unique key.
//
// Child writes are SYNCHRONOUS (syncConfig runs internally). A duplicate
// (port, protocol) returns HTTP 200 success:false → an error (C3).
//
// There is NO individual frontend SHOW route - frontends are EMBEDDED in the LB
// SHOW under load_balancer.frontends[] (each with its routing_rules[]).
// GetLBFrontend reads-by-scan.

// CreateLBFrontend creates a frontend listener under the given load balancer.
func (c *Client) CreateLBFrontend(ctx context.Context, lbID string, body map[string]any) (map[string]any, error) {
	if lbID == "" {
		return nil, fmt.Errorf("CreateLBFrontend: empty lbID")
	}
	return c.doItem(ctx, "POST", "/load-balancer/"+url.PathEscape(lbID)+"/frontends", body, "frontend")
}

// UpdateLBFrontend patches an existing frontend. The "frontend" envelope is unwrapped.
func (c *Client) UpdateLBFrontend(ctx context.Context, lbID, frontendID string, body map[string]any) (map[string]any, error) {
	if lbID == "" {
		return nil, fmt.Errorf("UpdateLBFrontend: empty lbID")
	}
	if frontendID == "" {
		return nil, fmt.Errorf("UpdateLBFrontend: empty frontendID")
	}
	path := "/load-balancer/" + url.PathEscape(lbID) + "/frontend/" + url.PathEscape(frontendID)
	return c.doItem(ctx, "PATCH", path, body, "frontend")
}

// DeleteLBFrontend deletes a frontend (and its routing rules). doVoid checks the flag.
func (c *Client) DeleteLBFrontend(ctx context.Context, lbID, frontendID string) error {
	if lbID == "" {
		return fmt.Errorf("DeleteLBFrontend: empty lbID")
	}
	if frontendID == "" {
		return fmt.Errorf("DeleteLBFrontend: empty frontendID")
	}
	path := "/load-balancer/" + url.PathEscape(lbID) + "/frontend/" + url.PathEscape(frontendID)
	return c.doVoid(ctx, "DELETE", path, nil)
}

// GetLBFrontend resolves a single frontend by scanning the parent load balancer's
// embedded frontends[] (there is NO individual frontend SHOW route). Returns a
// 404-shaped *APIError (IsNotFound) when absent.
func (c *Client) GetLBFrontend(ctx context.Context, lbID, frontendID string) (map[string]any, error) {
	if lbID == "" {
		return nil, fmt.Errorf("GetLBFrontend: empty lbID")
	}
	if frontendID == "" {
		return nil, fmt.Errorf("GetLBFrontend: empty frontendID")
	}
	lb, err := c.GetLoadBalancer(ctx, lbID)
	if err != nil {
		return nil, err
	}
	for _, f := range lbChildren(lb, "frontends") {
		if id, ok := f["id"].(string); ok && id == frontendID {
			return f, nil
		}
	}
	return nil, &APIError{Status: 404, Message: "load balancer frontend not found"}
}
