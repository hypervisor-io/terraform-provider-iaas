package client

import (
	"context"
	"fmt"
	"net/url"
)

// Load Balancer BACKEND endpoints (verified against the real UserApi
// LoadBalancerController / LoadBalancerService + routes/user_api.php).
//
// A backend is a child of a load balancer. The routes are nested under the
// parent LB id:
//
//	CREATE  POST   /load-balancer/{lbId}/backends           body {name (req), algorithm?, mode?, ...}
//	                                                          → 200 {success,message,backend:{id,...},sync}
//	UPDATE  PATCH  /load-balancer/{lbId}/backend/{backendId} body {name?, algorithm?, mode?, ...}
//	                                                          → 200 {success,message,backend:{...},sync}
//	DELETE  DELETE /load-balancer/{lbId}/backend/{backendId} → 200 {success,message,sync}
//
// IMPORTANT field-name deviation vs the controller's Scribe #[BodyParam]
// annotation: the annotation calls the algorithm field "balance", but the real
// service (LoadBalancerService::createBackend / updateBackend) reads it from the
// "algorithm" column (enum roundrobin|leastconn|source). The wire field is
// therefore "algorithm", NOT "balance". The "mode" enum is http|tcp.
//
// Child writes are SYNCHRONOUS: storeBackend/updateBackend/destroyBackend each
// call syncConfig() INTERNALLY (pushing the HAProxy config to the backing
// instance) and return the fresh backend immediately. There is NO status field
// to wait on, so there is no waiter. A failure surfaces as 200 success:false,
// which doItem/doVoid map to an error (C3).
//
// There is NO individual backend SHOW route — backends are EMBEDDED in the LB
// SHOW under load_balancer.backends[] (each with its targets[]). GetLBBackend
// therefore reads-by-scan: it calls GetLoadBalancer and scans the embedded
// backends[] for the matching id, returning a 404-shaped *APIError (IsNotFound)
// when absent.
//
// An empty-id guard is applied on every path-id argument (consistency).

// CreateLBBackend creates a backend pool under the given load balancer from the
// supplied prebuilt body (name required; algorithm/mode optional). The "backend"
// envelope is unwrapped, returning the created backend WITH its id.
func (c *Client) CreateLBBackend(ctx context.Context, lbID string, body map[string]any) (map[string]any, error) {
	if lbID == "" {
		return nil, fmt.Errorf("CreateLBBackend: empty lbID")
	}
	return c.doItem(ctx, "POST", "/load-balancer/"+url.PathEscape(lbID)+"/backends", body, "backend")
}

// UpdateLBBackend patches an existing backend. The "backend" envelope is
// unwrapped, returning the fresh backend.
func (c *Client) UpdateLBBackend(ctx context.Context, lbID, backendID string, body map[string]any) (map[string]any, error) {
	if lbID == "" {
		return nil, fmt.Errorf("UpdateLBBackend: empty lbID")
	}
	if backendID == "" {
		return nil, fmt.Errorf("UpdateLBBackend: empty backendID")
	}
	path := "/load-balancer/" + url.PathEscape(lbID) + "/backend/" + url.PathEscape(backendID)
	return c.doItem(ctx, "PATCH", path, body, "backend")
}

// DeleteLBBackend deletes a backend (and all its targets). doVoid checks the
// success flag (a failure surfaces as 200 success:false).
func (c *Client) DeleteLBBackend(ctx context.Context, lbID, backendID string) error {
	if lbID == "" {
		return fmt.Errorf("DeleteLBBackend: empty lbID")
	}
	if backendID == "" {
		return fmt.Errorf("DeleteLBBackend: empty backendID")
	}
	path := "/load-balancer/" + url.PathEscape(lbID) + "/backend/" + url.PathEscape(backendID)
	return c.doVoid(ctx, "DELETE", path, nil)
}

// GetLBBackend resolves a single backend by scanning the parent load balancer's
// embedded backends[] array (there is NO individual backend SHOW route — the LB
// SHOW embeds the backends). It returns the matching backend object or a
// 404-shaped *APIError (IsNotFound = true) when the id is absent. This is the
// read-by-scan source for the resource's Read.
func (c *Client) GetLBBackend(ctx context.Context, lbID, backendID string) (map[string]any, error) {
	if lbID == "" {
		return nil, fmt.Errorf("GetLBBackend: empty lbID")
	}
	if backendID == "" {
		return nil, fmt.Errorf("GetLBBackend: empty backendID")
	}
	lb, err := c.GetLoadBalancer(ctx, lbID)
	if err != nil {
		// A 404 on the parent LB propagates (the backend is gone too).
		return nil, err
	}
	for _, b := range lbChildren(lb, "backends") {
		if id, ok := b["id"].(string); ok && id == backendID {
			return b, nil
		}
	}
	return nil, &APIError{Status: 404, Message: "load balancer backend not found"}
}
