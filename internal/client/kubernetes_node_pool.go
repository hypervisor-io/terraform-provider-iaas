package client

import (
	"context"
	"fmt"
	"net/url"
)

// Kubernetes node-pool (CHILD of a cluster) endpoints, verified against the real
// UserApi Kubernetes\PoolsController + NodePoolService + routes/user_api.php
// (Phase 5 Wave 2). The cluster id is part of every path. There is NO per-pool
// SHOW endpoint - reads scan the cluster's pool LIST (C4 read-by-scan).
//
//	CREATE  POST   /kubernetes/cluster/{clusterID}/pools
//	                body {name (req), instance_plan_id (req), min_size?, max_size?,
//	                      target_count?, weight?, labels?(obj), taints?(arr of
//	                      {key,value?,effect}), autoscaling_enabled?, is_default?,
//	                      + drain/rate-limit/CAS tunables} → 201 {pool:{id,...}}
//	                [k8s.throttle:k8s_pool_create, idempotency.user]
//	LIST    GET    /kubernetes/cluster/{clusterID}/pools → {pools:[...]} (BARE
//	                array under "pools", NOT a Laravel paginator; default first)
//	UPDATE  PATCH  /kubernetes/cluster/{clusterID}/pool/{poolID}
//	                body {name?, instance_plan_id?, min_size?, max_size?,
//	                      target_count?, weight?, labels?, taints?,
//	                      autoscaling_enabled?, + tunables} → {pool:{...}}
//	                [k8s.throttle:k8s_pool_update, idempotency.user]
//	DELETE  DELETE /kubernetes/cluster/{clusterID}/pool/{poolID}?force=<bool>
//	                → {task_id, force}; 409 op_locked/no_eligible_victims; 422
//	                success:false (default_pool_protected / must_reassign_first)
//	                [k8s.throttle:k8s_pool_delete, idempotency.user]
//
// ACTIONS NOT MODELLED (out of the IaC lifecycle, recorded for completeness):
//	POST /cluster/{id}/pool/{poolId}/reassign       - promote pool to default
//	POST /cluster/{id}/pool/{poolId}/cancel-pending  - clear a vm_ref deletion mark
// reassign mutates the (server-managed, non-IaC) is_default flag; cancel-pending
// is a transient recovery operation on an individual node. Both are operational,
// not declarative state, so neither is exposed by the resource.
//
// SYNC vs ASYNC: createPool inserts the pool row in a DB transaction and returns
// it synchronously (201 with id). Worker provisioning is dispatched
// fire-and-forget (reconcilePoolSize) and there is NO per-pool status/state
// column to poll - so, like vpc_subnet (async IP generation, no status field),
// this resource is SYNC with no waiter. The live worker count surfaces later via
// the LIST endpoint's vm_refs_count (a server-mutable computed field).
//
// IDEMPOTENCY: create/update/delete carry idempotency.user; the resource passes a
// stable key on create so a lost-response retry is deduplicated. Empty key → the
// client generates a UUID so the header is never empty (matches the cluster).
//
// An empty-id guard is applied on every path-id argument (consistency).

// CreateKubernetesNodePool creates a new node pool under the given cluster. The
// cluster id is in the path; the prebuilt body carries name + instance_plan_id
// (required) plus any optional sizing/labels/taints/autoscaling fields. The
// response carries the created pool (with id) under the "pool" envelope. idemKey
// is sent as the Idempotency-Key header (a UUID is generated when empty).
func (c *Client) CreateKubernetesNodePool(ctx context.Context, clusterID string, body map[string]any, idemKey string) (map[string]any, error) {
	if clusterID == "" {
		return nil, fmt.Errorf("CreateKubernetesNodePool: empty cluster id")
	}
	if idemKey == "" {
		idemKey = newUUIDv4()
	}
	return c.doItemWithHeaders(ctx, "POST",
		"/kubernetes/cluster/"+url.PathEscape(clusterID)+"/pools",
		body, "pool", map[string]string{"Idempotency-Key": idemKey})
}

// ListKubernetesNodePools returns every active pool on the cluster. The index
// returns {pools:[...]} - a BARE array under the named "pools" key (NOT a
// paginator), so doItem(key="") fetches the bare envelope (surfacing C3
// success:false should the API ever add one) and the "pools" array is flattened.
// A 404 on the parent cluster propagates as an *APIError (IsNotFound).
func (c *Client) ListKubernetesNodePools(ctx context.Context, clusterID string) ([]map[string]any, error) {
	if clusterID == "" {
		return nil, fmt.Errorf("ListKubernetesNodePools: empty cluster id")
	}
	top, err := c.doItem(ctx, "GET",
		"/kubernetes/cluster/"+url.PathEscape(clusterID)+"/pools", nil, "")
	if err != nil {
		return nil, err
	}
	raw, _ := top["pools"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, v := range raw {
		if obj, ok := v.(map[string]any); ok {
			out = append(out, obj)
		}
	}
	return out, nil
}

// GetKubernetesNodePool finds a single pool by id via read-by-scan over the
// cluster's pool LIST (the user-API surface has NO per-pool SHOW route, C4). A
// pool id absent from the list - or a 404 on the parent cluster - both surface
// as an *APIError with Status 404 (recognised by IsNotFound), so the resource's
// Read removes the row from state and Terraform plans a recreate.
func (c *Client) GetKubernetesNodePool(ctx context.Context, clusterID, poolID string) (map[string]any, error) {
	if clusterID == "" {
		return nil, fmt.Errorf("GetKubernetesNodePool: empty cluster id")
	}
	if poolID == "" {
		return nil, fmt.Errorf("GetKubernetesNodePool: empty pool id")
	}
	pools, err := c.ListKubernetesNodePools(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	for _, p := range pools {
		if pid, _ := p["id"].(string); pid == poolID {
			return p, nil
		}
	}
	return nil, &APIError{Status: 404, Message: "node pool not found"}
}

// UpdateKubernetesNodePool patches a pool. Mutable fields are name,
// instance_plan_id (only when the pool has no live workers), min_size/max_size,
// target_count (routed through the scaler so workers actually provision/drain),
// weight, labels, taints, autoscaling_enabled, and the drain/rate-limit
// tunables. The route is a PATCH to the SINGULAR pool path and carries
// idempotency.user. The "pool" envelope is unwrapped.
func (c *Client) UpdateKubernetesNodePool(ctx context.Context, clusterID, poolID string, body map[string]any, idemKey string) (map[string]any, error) {
	if clusterID == "" {
		return nil, fmt.Errorf("UpdateKubernetesNodePool: empty cluster id")
	}
	if poolID == "" {
		return nil, fmt.Errorf("UpdateKubernetesNodePool: empty pool id")
	}
	if idemKey == "" {
		idemKey = newUUIDv4()
	}
	return c.doItemWithHeaders(ctx, "PATCH",
		"/kubernetes/cluster/"+url.PathEscape(clusterID)+"/pool/"+url.PathEscape(poolID),
		body, "pool", map[string]string{"Idempotency-Key": idemKey})
}

// DeleteKubernetesNodePool soft-deletes a pool. force=true is sent as a query
// param so the destroy routes through the synchronous cordon/drain/delete path
// (cordonDrainDeletePool) and the pool's workers are torn down inline - this
// makes `terraform destroy` deterministic rather than leaving a cron-driven
// progressive deletion in flight. The route is a DELETE to the SINGULAR pool
// path and carries idempotency.user.
//
// Errors surface as: 409 op_locked / no_eligible_victims (another op in flight,
// or the drain config has no eligible victims), 422 success:false
// (default_pool_protected - last/default pool; reassign first), or the standard
// non-2xx mapping. A successful response is {task_id, force} (task_id may be null
// when the pool was empty and removed immediately) - we only need the success.
func (c *Client) DeleteKubernetesNodePool(ctx context.Context, clusterID, poolID, idemKey string) error {
	if clusterID == "" {
		return fmt.Errorf("DeleteKubernetesNodePool: empty cluster id")
	}
	if poolID == "" {
		return fmt.Errorf("DeleteKubernetesNodePool: empty pool id")
	}
	if idemKey == "" {
		idemKey = newUUIDv4()
	}
	path := "/kubernetes/cluster/" + url.PathEscape(clusterID) +
		"/pool/" + url.PathEscape(poolID) + "?force=true"

	resp, raw, err := c.doWithHeaders(ctx, "DELETE", path, nil, map[string]string{
		"Idempotency-Key": idemKey,
	})
	if err != nil {
		return err
	}
	if err := responseError(resp, raw); err != nil {
		return err
	}
	// Reuse the success-flag logic (C3): decodeItem with an empty key returns the
	// bare envelope and short-circuits on success:false. Discard the object.
	_, err = decodeItem(raw, "")
	return err
}
