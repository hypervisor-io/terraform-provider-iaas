package client

import (
	"context"
	"fmt"
	"net/url"
)

// Load Balancer TARGET endpoints (verified against the real UserApi
// LoadBalancerController / LoadBalancerService + routes/user_api.php).
//
// A target is a child of a BACKEND (which is itself a child of a load balancer),
// so its routes carry BOTH the lb id and the backend id in the path:
//
//	CREATE  POST   /load-balancer/{lbId}/backend/{backendId}/targets
//	                  body {target_instance_id?, target_ip (req), target_port (req), weight?, enabled?}
//	                  → 200 {success,message,target:{id,...},sync}; 422 success:false on a (ip,port) dup
//	UPDATE  PATCH  /load-balancer/{lbId}/backend/{backendId}/target/{targetId}
//	                  body {target_ip?, target_port?, weight?, enabled?, status?}
//	                  → 200 {success,message,target:{...},sync}
//	DELETE  DELETE /load-balancer/{lbId}/backend/{backendId}/target/{targetId} → 200 {success,message,sync}
//
// IMPORTANT field-name deviation vs the controller's #[BodyParam] annotation: the
// annotation documents "instance_id" and "port", but the real service
// (LoadBalancerService::createTarget / updateTarget) reads the DB columns
// "target_instance_id", "target_ip", "target_port". A target is keyed by
// (lb_backend_id, target_ip, target_port) - the unique index - so target_ip and
// target_port are the real required inputs; target_instance_id is nullable (it
// links the target to an instance for tracking but is NOT used to derive the ip).
//
// Child writes are SYNCHRONOUS (syncConfig runs internally). A duplicate
// (target_ip, target_port) returns HTTP 422 success:false → an error (C3).
//
// There is NO individual target SHOW route - targets are EMBEDDED in the LB SHOW
// under load_balancer.backends[].targets[]. GetLBTarget reads-by-scan: it calls
// GetLoadBalancer, finds the backend, then scans that backend's targets[].

// CreateLBTarget adds a target to the given backend. target_ip + target_port are
// required; target_instance_id/weight/enabled optional. The "target" envelope is
// unwrapped, returning the created target WITH its id.
func (c *Client) CreateLBTarget(ctx context.Context, lbID, backendID string, body map[string]any) (map[string]any, error) {
	if lbID == "" {
		return nil, fmt.Errorf("CreateLBTarget: empty lbID")
	}
	if backendID == "" {
		return nil, fmt.Errorf("CreateLBTarget: empty backendID")
	}
	path := "/load-balancer/" + url.PathEscape(lbID) + "/backend/" + url.PathEscape(backendID) + "/targets"
	return c.doItem(ctx, "POST", path, body, "target")
}

// UpdateLBTarget patches an existing target. The "target" envelope is unwrapped.
func (c *Client) UpdateLBTarget(ctx context.Context, lbID, backendID, targetID string, body map[string]any) (map[string]any, error) {
	if lbID == "" {
		return nil, fmt.Errorf("UpdateLBTarget: empty lbID")
	}
	if backendID == "" {
		return nil, fmt.Errorf("UpdateLBTarget: empty backendID")
	}
	if targetID == "" {
		return nil, fmt.Errorf("UpdateLBTarget: empty targetID")
	}
	path := "/load-balancer/" + url.PathEscape(lbID) + "/backend/" + url.PathEscape(backendID) + "/target/" + url.PathEscape(targetID)
	return c.doItem(ctx, "PATCH", path, body, "target")
}

// DeleteLBTarget removes a target from its backend. doVoid checks the success flag.
func (c *Client) DeleteLBTarget(ctx context.Context, lbID, backendID, targetID string) error {
	if lbID == "" {
		return fmt.Errorf("DeleteLBTarget: empty lbID")
	}
	if backendID == "" {
		return fmt.Errorf("DeleteLBTarget: empty backendID")
	}
	if targetID == "" {
		return fmt.Errorf("DeleteLBTarget: empty targetID")
	}
	path := "/load-balancer/" + url.PathEscape(lbID) + "/backend/" + url.PathEscape(backendID) + "/target/" + url.PathEscape(targetID)
	return c.doVoid(ctx, "DELETE", path, nil)
}

// GetLBTarget resolves a single target by scanning the parent load balancer's
// embedded backends[].targets[] (there is NO individual target SHOW route). It
// returns the matching target object or a 404-shaped *APIError (IsNotFound) when
// the backend or the target id is absent.
func (c *Client) GetLBTarget(ctx context.Context, lbID, backendID, targetID string) (map[string]any, error) {
	if lbID == "" {
		return nil, fmt.Errorf("GetLBTarget: empty lbID")
	}
	if backendID == "" {
		return nil, fmt.Errorf("GetLBTarget: empty backendID")
	}
	if targetID == "" {
		return nil, fmt.Errorf("GetLBTarget: empty targetID")
	}
	lb, err := c.GetLoadBalancer(ctx, lbID)
	if err != nil {
		return nil, err
	}
	for _, b := range lbChildren(lb, "backends") {
		bid, _ := b["id"].(string)
		if bid != backendID {
			continue
		}
		for _, tgt := range lbNestedChild(b, "targets") {
			if id, ok := tgt["id"].(string); ok && id == targetID {
				return tgt, nil
			}
		}
	}
	return nil, &APIError{Status: 404, Message: "load balancer target not found"}
}
