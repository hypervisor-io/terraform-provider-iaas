package client

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/url"
)

// Kubernetes Cluster (CORE) endpoints, verified against the real UserApi
// Kubernetes\ClustersController + ClusterService + routes/user_api.php.
//
// This file covers ONLY the cluster ITSELF (create/show/list/update/destroy +
// async state convergence). The cluster's CHILDREN — node pools, default-pool
// workers (scale/labels/autoscaling/delete-node), upgrades (cp/workers/ccm/
// retry), per-cluster security-group rules, SSL certs, kubeconfig + autoscaler
// manifest, and tasks/logs/node-stats — are SEPARATE tasks (id32/33/34) and are
// NOT here. The child routes (for the next agents) are:
//
//	POOLS         GET    /kubernetes/cluster/{id}/pools
//	              POST   /kubernetes/cluster/{id}/pools
//	              PATCH  /kubernetes/cluster/{id}/pool/{poolId}
//	              POST   /kubernetes/cluster/{id}/pool/{poolId}/reassign
//	              POST   /kubernetes/cluster/{id}/pool/{poolId}/cancel-pending
//	              DELETE /kubernetes/cluster/{id}/pool/{poolId}
//	WORKERS       POST   /kubernetes/cluster/{id}/workers/scale       body {target_count}
//	              PATCH  /kubernetes/cluster/{id}/workers/labels
//	              POST   /kubernetes/cluster/{id}/workers/autoscaling
//	              DELETE /kubernetes/cluster/{id}/worker/{nodeName}
//	UPGRADES      POST   /kubernetes/cluster/{id}/upgrade/cp
//	              POST   /kubernetes/cluster/{id}/upgrade/workers
//	              POST   /kubernetes/cluster/{id}/upgrade/ccm
//	              POST   /kubernetes/cluster/{id}/upgrade/retry
//	SECURITY GRP  GET    /kubernetes/cluster/{id}/security-group/{scope}   scope=lb|cp|worker
//	              POST   /kubernetes/cluster/{id}/security-group/{scope}
//	              DELETE /kubernetes/cluster/{id}/security-group/{scope}/rule/{ruleId}
//	SSL CERTS     GET    /kubernetes/cluster/{id}/ssl-certificates
//	              POST   /kubernetes/cluster/{id}/ssl-certificates
//	              DELETE /kubernetes/cluster/{id}/ssl-certificate/{certId}
//	KUBECONFIG    GET    /kubernetes/cluster/{id}/kubeconfig               ← id34 DATA SOURCE
//	              POST   /kubernetes/cluster/{id}/kubeconfig/acknowledge
//	AUTOSCALER    GET    /kubernetes/cluster/{id}/autoscaler-manifest      ← id34 DATA SOURCE
//	TASKS         GET    /kubernetes/cluster/{id}/tasks
//	              GET    /kubernetes/cluster/{id}/task/{taskId}/logs
//	              GET    /kubernetes/cluster/{id}/node-stats
//	RETRY/ACK     POST   /kubernetes/cluster/{id}/retry                    (recover error state)
//	              POST   /kubernetes/cluster/{id}/acknowledge-error
//	CATALOG       GET    /kubernetes/search/{regions|vpcs|subnets|versions|
//	              plans|cp-plans|lb-plans}                                 ← id34 DATA SOURCES
//
// CORE CLUSTER ROUTES (this file):
//
//	CREATE  POST   /kubernetes/clusters                 body {name, slug (req!), hypervisor_group_id,
//	                                                     vpc_id, cp_vpc_subnet_id, worker_vpc_subnet_id,
//	                                                     kubernetes_version_id, control_node_count (1|3),
//	                                                     endpoint_mode (private|public_and_private),
//	                                                     cp_instance_plan_id, cp_lb_plan_id,
//	                                                     worker_instance_plan_id, + optionals} → 200
//	                                                     {success,cluster:{id,state:"created"},task_id}
//	                                                     [k8s.throttle:k8s_create, idempotency.user]
//	SHOW    GET    /kubernetes/cluster/{id}             → {success,cluster:{...,state,endpoint_url,
//	                                                     pools:[],tasks:[],...}}; 404 when absent/not owned
//	LIST    GET    /kubernetes/clusters                 → {success,clusters:{data:[...]}}
//	UPDATE  PATCH  /kubernetes/cluster/{id}             body {name?, description?, project_id?}
//	                                                     → {success,cluster:{...}} [idempotency.user]
//	DELETE  DELETE /kubernetes/cluster/{id}             → {success,task_id}; success:false on failure
//	                                                     [k8s.throttle:k8s_destroy, idempotency.user]
//
// Async behaviour (controller + state-machine verified):
//   - Create is ASYNC and multi-stage: ClusterService::create records the
//     cluster row with state="created", auto-creates the default node pool, and
//     dispatches the CreateCluster job onto the "kubernetes" queue. The create
//     response carries the cluster (with id) AND a tracking task_id. The
//     authoritative async signal is the cluster's OWN "state" field, polled via
//     the SHOW endpoint (KubernetesClusterStateMachine drives it):
//         created → starting → running   (READY)
//                            → error      (FAIL — recoverable via /retry)
//     Other states (stopped, alert, destroying, destroyed) occur later in the
//     cluster's life. Ready="running"; fail="error". GetKubernetesCluster IS the
//     poll. (We poll the cluster state rather than the task because the state
//     machine is the canonical lifecycle signal the UI itself polls; the task_id
//     is informational here and consumed by the deferred tasks/logs resources.)
//   - DELETE marks the cluster "destroying", dispatches the DeleteCluster job
//     (removes worker VMs, the CP load balancer, security groups, reserved IPs)
//     and soft-deletes the row, so a subsequent SHOW 404s. It returns a task_id.
//     An already-destroyed cluster is rejected with 422 success:false, so doVoid
//     checks the flag.
//
// Idempotency (controller-verified): the create/update/delete routes carry the
// `idempotency.user` middleware, which reads the "Idempotency-Key" REQUEST
// header. When present it caches the first 2xx response for 24h and replays it
// for any later request reusing the same key+user — so a retried create is
// deduplicated server-side (it will NOT spin up a second cluster). The header is
// OPTIONAL (the server falls back to a random per-request UUID when absent,
// which gives NO dedup), so to make retries safe the resource generates a stable
// key and passes it here. CreateKubernetesCluster generates a UUID fallback when
// the caller passes an empty key.
//
// Billing/feature gating (controller-verified): the cluster create is NOT
// wrapped in billing.enabled middleware — gating is IN-SERVICE and arrives as
// HTTP 200/403/422 success:false: "Cloud Service billing is disabled" (403),
// "This region does not have VPC and Load Balancer features enabled" / "Control
// plane must be deployed in a private subnet" / "Selected VPC has no NAT
// Gateway" / HA-requires-3-CP / cluster+LB quotas (422). doItem/doVoid surface
// all of these (C3). The k8s.throttle middleware returns HTTP 429 on rate-limit,
// which the shared transport already retries with backoff.
//
// An empty-id guard is applied on every path-id argument (consistency).

// CreateKubernetesCluster deploys a managed Kubernetes cluster from the supplied
// prebuilt body. Create is ASYNC: the returned object carries the id but
// state="created"; the caller must poll GetKubernetesCluster until
// state="running". The "cluster" envelope is unwrapped (the top-level task_id is
// dropped — state polling is the convergence signal).
//
// idemKey is sent as the "Idempotency-Key" request header so a retried create is
// deduplicated by the idempotency.user middleware. When idemKey is "", a random
// UUID is generated so the create is never sent without a key.
func (c *Client) CreateKubernetesCluster(ctx context.Context, body map[string]any, idemKey string) (map[string]any, error) {
	if idemKey == "" {
		idemKey = newUUIDv4()
	}
	return c.doItemWithHeaders(ctx, "POST", "/kubernetes/clusters", body, "cluster", map[string]string{
		"Idempotency-Key": idemKey,
	})
}

// GetKubernetesCluster fetches a single cluster by UUID. The SHOW route is
// SINGULAR. A 404 (absent or owned by a different account) is returned as an
// *APIError that IsNotFound recognises. The returned object includes the cluster
// "state", endpoint fields, version refs, worker_count, and the embedded
// pools[]/tasks[] arrays from the SHOW payload. This doubles as the async poll
// source for create (scan "state" for "running") and the 404 signal for delete
// convergence.
func (c *Client) GetKubernetesCluster(ctx context.Context, id string) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("GetKubernetesCluster: empty id")
	}
	return c.doItem(ctx, "GET", "/kubernetes/cluster/"+url.PathEscape(id), nil, "cluster")
}

// ListKubernetesClusters returns all clusters belonging to the authenticated
// account. The index wraps the Laravel paginator under the named "clusters" key
// ({success,clusters:{data:[...]}}) — NOT a top-level "data" array — so the
// shared doList paginator decoder cannot be used directly. Instead doItem
// unwraps the named key (surfacing C3 success:false), then the inner paginator's
// "data" array is flattened to []map[string]any.
//
// CAVEAT: this fetches only page 1 (the named-key paginator can't use the
// auto-paginating doList) — a future list data source must add page iteration.
func (c *Client) ListKubernetesClusters(ctx context.Context) ([]map[string]any, error) {
	paginator, err := c.doItem(ctx, "GET", "/kubernetes/clusters", nil, "clusters")
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

// UpdateKubernetesCluster patches the user-facing metadata of a cluster. Only
// name/description/project_id are mutable through this endpoint (resource
// topology is intentionally immutable — version/plan/CIDR changes go through the
// dedicated lifecycle endpoints). The route is a PATCH to the SINGULAR path and
// carries the idempotency.user middleware. The "cluster" envelope is unwrapped.
func (c *Client) UpdateKubernetesCluster(ctx context.Context, id string, body map[string]any, idemKey string) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("UpdateKubernetesCluster: empty id")
	}
	if idemKey == "" {
		idemKey = newUUIDv4()
	}
	return c.doItemWithHeaders(ctx, "PATCH", "/kubernetes/cluster/"+url.PathEscape(id), body, "cluster", map[string]string{
		"Idempotency-Key": idemKey,
	})
}

// DeleteKubernetesCluster deletes (soft-deletes) the cluster and dispatches the
// teardown job. The row is flipped to state="destroying" and removed, so a
// subsequent SHOW 404s. The route is a DELETE to the SINGULAR path and carries
// the idempotency.user middleware. An already-destroyed cluster is rejected with
// 422 success:false, so doVoid checks the flag.
func (c *Client) DeleteKubernetesCluster(ctx context.Context, id, idemKey string) error {
	if id == "" {
		return fmt.Errorf("DeleteKubernetesCluster: empty id")
	}
	if idemKey == "" {
		idemKey = newUUIDv4()
	}
	resp, raw, err := c.doWithHeaders(ctx, "DELETE", "/kubernetes/cluster/"+url.PathEscape(id), nil, map[string]string{
		"Idempotency-Key": idemKey,
	})
	if err != nil {
		return err
	}
	if err := responseError(resp, raw); err != nil {
		return err
	}
	// Reuse the success-flag logic (C3): decodeItem with an empty key returns
	// the bare envelope and short-circuits on success:false. Discard the object.
	_, err = decodeItem(raw, "")
	return err
}

// newUUIDv4 returns a random RFC-4122 v4 UUID string. Used as the Idempotency-Key
// fallback when the caller supplies an empty key, so a create/update/delete is
// never sent without one. Generated from crypto/rand to avoid adding a UUID
// dependency to the client package.
func newUUIDv4() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// rand.Read on a healthy system does not fail; on the vanishingly rare
		// error we still return a syntactically valid (all-zero variant/version)
		// UUID so the header is non-empty.
		b = [16]byte{}
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
