package client

import (
	"context"
	"fmt"
	"net/url"
)

// Load Balancer (CORE) endpoints, verified against the real UserApi
// LoadBalancerController / LoadBalancerService + routes/user_api.php.
//
// This file covers ONLY the load balancer ITSELF (create/show/list/destroy +
// async deploy convergence). The LB's CHILDREN — frontends, backends, targets,
// certificates, routing rules — are a SEPARATE task (id21) and are NOT here. The
// child routes (for the next agent) are:
//
//	FRONTENDS     POST   /load-balancer/{lbId}/frontends
//	              PATCH  /load-balancer/{lbId}/frontend/{frontendId}
//	              DELETE /load-balancer/{lbId}/frontend/{frontendId}
//	BACKENDS      POST   /load-balancer/{lbId}/backends
//	              PATCH  /load-balancer/{lbId}/backend/{backendId}
//	              DELETE /load-balancer/{lbId}/backend/{backendId}
//	TARGETS       POST   /load-balancer/{lbId}/backend/{backendId}/targets
//	              PATCH  /load-balancer/{lbId}/backend/{backendId}/target/{targetId}
//	              DELETE /load-balancer/{lbId}/backend/{backendId}/target/{targetId}
//	CERTIFICATES  POST   /load-balancer/{lbId}/certificates
//	              DELETE /load-balancer/{lbId}/certificate/{certificateId}
//	              (+ Let's Encrypt: POST .../le-certificate, POST .../certificate/{id}/retry)
//	ROUTING RULES POST   /load-balancer/{lbId}/frontend/{frontendId}/rules
//	              PATCH  /load-balancer/{lbId}/frontend/{frontendId}/rule/{ruleId}
//	              DELETE /load-balancer/{lbId}/frontend/{frontendId}/rule/{ruleId}
//	SYNC          POST   /load-balancer/{lbId}/sync   ← child mutations are pushed to the
//	              backing instance by the service automatically (syncConfig is called inside
//	              the store*/update*/destroy* service methods), so an explicit sync after a
//	              child change is NOT required for the API to apply it. The /sync endpoint is
//	              a manual "force a reload" affordance, not a mandatory apply step.
//
// CORE LB ROUTES (this file):
//
//	CREATE  POST   /load-balancers                      body {name (req), lb_plan_id (req),
//	                                                     vpc_id?, vpc_subnet_id? (required_with vpc_id),
//	                                                     hypervisor_group_id? (required_without vpc_id)}
//	                                                     → 200 {success,message,load_balancer:{id,
//	                                                     status:"deploying",...}}
//	SHOW    GET    /load-balancer/{id}                  → {success,load_balancer:{...,public_ip:{ip},
//	                                                     frontends:[],backends:[],certificates:[]}};
//	                                                     404 when absent / wrong owner
//	LIST    GET    /load-balancers                      → {success,load_balancers:{data:[...]}}
//	DELETE  DELETE /load-balancer/{id}                  → {success,message}; success:false on failure
//
// Async behaviour (controller-verified):
//   - Create is ASYNC and backed by a REAL instance: the LB row is recorded
//     synchronously with status="deploying" and a backing Instance + slave deploy
//     task are created. There is NO task_id in the create response — the create
//     returns {success,message,load_balancer:{id,status:"deploying"}} (the
//     controller's Scribe {success,message}-only annotation is stale, like VPC).
//     The async signal is the LB's own "status" field, polled via the SHOW
//     endpoint. The lifecycle is: deploying → configuring (the slave reports the
//     instance deployed) → active (HAProxy config applied) | error (config-apply
//     failed). Ready="active"; fail="error". GetLoadBalancer IS the poll.
//   - DELETE flips status→"deleting", bills the final hours, destroys the backing
//     instance (releasing its public IP), and soft-deletes the row, so a
//     subsequent SHOW 404s. A failure (e.g. instance destroy threw) surfaces as
//     success:false, so doVoid checks the flag.
//
// Billing/feature gating (controller-verified DEVIATION vs volume/static_ip):
//   - The CORE LB routes are NOT wrapped in the billing.enabled middleware, so
//     there is NO HTTP 403 billing gate. Feature gating is IN-CONTROLLER and
//     arrives as HTTP 200 success:false: "Load balancing is not enabled for this
//     hypervisor group" (hg->lb_enabled false), the per-account load_balancers
//     quota, "No public IPv4 addresses available", "Selected load balancer plan
//     is not enabled", "A NAT gateway is required for private subnet deployments",
//     etc. doItem/doVoid surface all of these (C3).
//
// There is NO update/PATCH route for the LB itself — only the children have
// PATCH endpoints. So every CORE create input (name, lb_plan_id, vpc_id,
// vpc_subnet_id, hypervisor_group_id) is immutable from the resource's view and
// there is no UpdateLoadBalancer client method.
//
// An empty-id guard is applied on every path-id argument (consistency).

// CreateLoadBalancer deploys a load balancer from the supplied prebuilt body
// (name + lb_plan_id required; supply vpc_id + vpc_subnet_id for VPC mode, or
// hypervisor_group_id for public mode). Create is ASYNC: the returned object
// carries the id but status="deploying"; the caller must poll GetLoadBalancer
// until status="active". The "load_balancer" envelope is unwrapped.
func (c *Client) CreateLoadBalancer(ctx context.Context, body map[string]any) (map[string]any, error) {
	return c.doItem(ctx, "POST", "/load-balancers", body, "load_balancer")
}

// GetLoadBalancer fetches a single load balancer by UUID. The SHOW route is
// SINGULAR. A 404 (absent or owned by a different account) is returned as an
// *APIError that IsNotFound recognises. The returned object includes the nested
// public_ip object and the embedded frontends[]/backends[]/certificates[] arrays
// from the SHOW payload. This doubles as the async poll source for create (scan
// "status" for "active") and the 404 signal for delete convergence.
func (c *Client) GetLoadBalancer(ctx context.Context, id string) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("GetLoadBalancer: empty id")
	}
	return c.doItem(ctx, "GET", "/load-balancer/"+url.PathEscape(id), nil, "load_balancer")
}

// ListLoadBalancers returns all load balancers belonging to the authenticated
// account. The index wraps the Laravel paginator under the named "load_balancers"
// key ({success,load_balancers:{data:[...]}}) — NOT a top-level "data" array — so
// the shared doList paginator decoder cannot be used directly. Instead doItem
// unwraps the named key (surfacing C3 success:false), then the inner paginator's
// "data" array is flattened to []map[string]any.
func (c *Client) ListLoadBalancers(ctx context.Context) ([]map[string]any, error) {
	paginator, err := c.doItem(ctx, "GET", "/load-balancers", nil, "load_balancers")
	if err != nil {
		return nil, err
	}
	dataRaw, ok := paginator["data"].([]any)
	if !ok {
		// No data array (empty/unexpected envelope) → empty list, not an error.
		return []map[string]any{}, nil
	}
	out := make([]map[string]any, 0, len(dataRaw))
	for _, v := range dataRaw {
		if obj, ok := v.(map[string]any); ok {
			out = append(out, obj)
		}
	}
	return out, nil
}

// DeleteLoadBalancer deletes (soft-deletes) the load balancer and destroys its
// backing instance. The row is flipped to status="deleting" and removed, so a
// subsequent SHOW 404s. A failure (e.g. the backing instance destroy threw) is
// signalled with success:false, so doVoid checks the flag.
func (c *Client) DeleteLoadBalancer(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("DeleteLoadBalancer: empty id")
	}
	return c.doVoid(ctx, "DELETE", "/load-balancer/"+url.PathEscape(id), nil)
}
