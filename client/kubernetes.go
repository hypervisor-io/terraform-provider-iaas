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
// This file covers the cluster ITSELF (create/show/list/update/destroy + async
// state convergence) PLUS the in-place version-upgrade lifecycle (T7/id-G8:
// upgrade/{cp,workers,ccm,retry}) - folded into the same
// iaas_kubernetes_cluster resource's Update rather than a separate resource,
// since it mutates the SAME row's version columns. The cluster's remaining
// CHILDREN - node pools, default-pool workers (scale/labels/autoscaling/
// delete-node), per-cluster security-group rules, SSL certs, kubeconfig +
// autoscaler manifest, and tasks/logs/node-stats - are SEPARATE tasks (id32/34)
// and are NOT here. The child routes (for the next agents) are:
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
//	UPG CP  POST   /kubernetes/cluster/{id}/upgrade/cp      body {target_version_id (uuid, req),
//	                                                         drain_grace_period (int 0-3600, req)}
//	                                                         → {task_id,target_version_id,
//	                                                         current_version_id,planned_waves} (BARE,
//	                                                         no success/cluster wrapper). ASYNC - a
//	                                                         detached `kubernetes:cp-rolling-upgrade`
//	                                                         command surge-replaces CP nodes one at a
//	                                                         time. [k8s.throttle:k8s_cp_upgrade,
//	                                                         idempotency.user]
//	UPG WK  POST   /kubernetes/cluster/{id}/upgrade/workers  body {target_version_id (uuid, req),
//	                                                         max_surge (int >=1, req),
//	                                                         drain_grace_period (int 0-3600, req)}
//	                                                         → same bare envelope shape as upgrade/cp.
//	                                                         ASYNC - `kubernetes:workers-rolling-upgrade`
//	                                                         surge-replaces up to max_surge workers per
//	                                                         wave. Rejected 422 "target_exceeds_cp_version"
//	                                                         if target > the CP's CURRENT version (upstream
//	                                                         kubelet-must-not-outrun-apiserver policy) - CP
//	                                                         must be upgraded first for a multi-minor jump.
//	                                                         [k8s.throttle:k8s_upgrade, idempotency.user]
//	UPG CCM POST   /kubernetes/cluster/{id}/upgrade/ccm      no body → {success,message} SYNCHRONOUS (no
//	                                                         task_id) - redeploys cloud-controller-manager
//	                                                         using whatever image the CURRENT worker-
//	                                                         baseline kubernetes_version's
//	                                                         bundled_components.ccm_image resolves to. No
//	                                                         separate CCM version exists to target. 409 if
//	                                                         cluster.state != "running".
//	                                                         [k8s.throttle:k8s_upgrade, idempotency.user]
//	UPG RETRY POST /kubernetes/cluster/{id}/upgrade/retry    no body → {success,task_id,cleanup_errors}
//	                                                         or 422 success:false. NOT a "resume the failed
//	                                                         CP/worker upgrade" - see
//	                                                         RetryK8sClusterUpgrade's doc comment; gated on
//	                                                         cluster.state=="error" and rebuilds the WHOLE
//	                                                         cluster from scratch on the same row.
//	                                                         [k8s.throttle:k8s_upgrade, idempotency.user]
//
// Version tracking (T7/id-G8, controller+model verified): the cluster row
// tracks TWO independent version columns, both initialised to the SAME value at
// create (ClusterService::create sets cp_kubernetes_version_id ==
// kubernetes_version_id):
//   - kubernetes_version_id     - the WORKER baseline (also what CreateKubernetesCluster
//     accepts and what upgrade/workers finalizes into on success).
//   - cp_kubernetes_version_id  - the control-plane's current version (only
//     upgrade/cp's rolling-upgrade command finalize step mutates this).
//
// Both relations (kubernetes_version, cp_kubernetes_version) are eager-loaded on
// SHOW, so GetKubernetesCluster's result carries both
// {kubernetes_version_id,kubernetes_version:{semantic_version,...}} and
// {cp_kubernetes_version_id,cp_kubernetes_version:{semantic_version,...}}. There
// are also two EPHEMERAL *_target_kubernetes_version_id columns (non-null only
// while a CP or worker rolling upgrade is in flight; cleared to null on both
// success and failure) - not surfaced as separate client methods here since the
// resource waiter polls the task, not these columns, for convergence.
//
// Convergence signal for upgrade/cp and upgrade/workers: cluster.state is NEVER
// touched by either rolling-upgrade command (state stays "running" throughout,
// confirmed against both Console\Commands\Kubernetes\{Cp,Workers}RollingUpgrade
// - neither ever calls KubernetesClusterStateMachine::transition nor writes
// `state`). The authoritative per-task signal is instead the
// KubernetesClusterTask row named by the response's task_id: `status` transitions
// pending → running → completed|failed. There is NO per-task GET route for
// clusters (unlike instances' GetInstanceTask) - SHOW eager-loads the cluster's
// last 20 tasks under "tasks", so the resource-level waiter polls
// GetKubernetesCluster and scans that embedded array for the matching task id's
// "status" (see waitForClusterUpgradeTask in internal/resources), reusing the
// SAME waiter.WaitFor primitive as create/delete convergence.
//
// Async behaviour (controller + state-machine verified):
//   - Create is ASYNC and multi-stage: ClusterService::create records the
//     cluster row with state="created", auto-creates the default node pool, and
//     dispatches the CreateCluster job onto the "kubernetes" queue. The create
//     response carries the cluster (with id) AND a tracking task_id. The
//     authoritative async signal is the cluster's OWN "state" field, polled via
//     the SHOW endpoint (KubernetesClusterStateMachine drives it):
//         created → starting → running   (READY)
//                            → error      (FAIL - recoverable via /retry)
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
// for any later request reusing the same key+user - so a retried create is
// deduplicated server-side (it will NOT spin up a second cluster). The header is
// OPTIONAL (the server falls back to a random per-request UUID when absent,
// which gives NO dedup), so to make retries safe the resource generates a stable
// key and passes it here. CreateKubernetesCluster generates a UUID fallback when
// the caller passes an empty key.
//
// Billing/feature gating (controller-verified): the cluster create is NOT
// wrapped in billing.enabled middleware - gating is IN-SERVICE and arrives as
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
// dropped - state polling is the convergence signal).
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
// ({success,clusters:{data:[...]}}) - NOT a top-level "data" array - so the
// shared doList paginator decoder cannot be used directly. Instead doItem
// unwraps the named key (surfacing C3 success:false), then the inner paginator's
// "data" array is flattened to []map[string]any.
//
// CAVEAT: this fetches only page 1 (the named-key paginator can't use the
// auto-paginating doList) - a future list data source must add page iteration.
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
// topology is intentionally immutable - version/plan/CIDR changes go through the
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

// UpgradeK8sClusterControlPlane starts a rolling control-plane upgrade,
// surge-replacing CP nodes one at a time onto the version named by
// body["target_version_id"] (required uuid), honouring
// body["drain_grace_period"] (required int, 0-3600 seconds) before each old CP
// is drained. ASYNC: the response is a BARE envelope
// {task_id,target_version_id,current_version_id,planned_waves} (no
// "cluster"/"success" wrapper) - the caller polls the cluster's own embedded
// "tasks" array for task_id to reach status "completed" (fail: "failed"); see
// the package doc comment and internal/resources' waitForClusterUpgradeTask.
// Errors: 422 {"code":"target_not_active"} (or another
// InvalidUpgradeTargetException code - forward-only / same-major / ≤1-minor
// jump / target must be state=="active"), 409 {"code":"op_locked"} when another
// cluster operation is in flight. The route carries idempotency.user; idemKey
// is sent as the Idempotency-Key header (a UUID is generated when empty).
func (c *Client) UpgradeK8sClusterControlPlane(ctx context.Context, clusterID string, body map[string]any, idemKey string) (map[string]any, error) {
	if clusterID == "" {
		return nil, fmt.Errorf("UpgradeK8sClusterControlPlane: empty cluster id")
	}
	if idemKey == "" {
		idemKey = newUUIDv4()
	}
	return c.doItemWithHeaders(ctx, "POST",
		"/kubernetes/cluster/"+url.PathEscape(clusterID)+"/upgrade/cp",
		body, "", map[string]string{"Idempotency-Key": idemKey})
}

// UpgradeK8sClusterWorkers starts a rolling worker (default-baseline) upgrade:
// surge-replaces up to body["max_surge"] workers at a time onto
// body["target_version_id"], honouring body["drain_grace_period"] before each
// old worker is cordoned/drained. ASYNC, same bare {task_id,...} envelope and
// idempotency.user handling as UpgradeK8sClusterControlPlane. The Master
// enforces "kubelet must not run ahead of the apiserver": a target exceeding
// the CP's CURRENT version is rejected 422 {"code":"target_exceeds_cp_version"}
// - callers wanting a multi-minor jump must upgrade the control plane first.
func (c *Client) UpgradeK8sClusterWorkers(ctx context.Context, clusterID string, body map[string]any, idemKey string) (map[string]any, error) {
	if clusterID == "" {
		return nil, fmt.Errorf("UpgradeK8sClusterWorkers: empty cluster id")
	}
	if idemKey == "" {
		idemKey = newUUIDv4()
	}
	return c.doItemWithHeaders(ctx, "POST",
		"/kubernetes/cluster/"+url.PathEscape(clusterID)+"/upgrade/workers",
		body, "", map[string]string{"Idempotency-Key": idemKey})
}

// UpgradeK8sClusterCCM redeploys the cloud-controller-manager. UNLIKE the CP/
// worker stages this is SYNCHRONOUS - no request body, no task_id; the response
// is {"success":true,"message":"CCM redeployed"} and the call blocks
// server-side until the kubectl apply + rollout restart finish (or fails 409 if
// cluster.state != "running", 500 on a kubectl/apply error). There is no
// separate CCM version to target: the image is resolved server-side from the
// cluster's CURRENT worker-baseline kubernetes_version's
// bundled_components.ccm_image, so this is a plain "resync the CCM to whatever
// version is now in effect" action - callers wanting the CCM image to track a
// version bump should invoke this AFTER the worker upgrade stage completes.
func (c *Client) UpgradeK8sClusterCCM(ctx context.Context, clusterID, idemKey string) error {
	if clusterID == "" {
		return fmt.Errorf("UpgradeK8sClusterCCM: empty cluster id")
	}
	if idemKey == "" {
		idemKey = newUUIDv4()
	}
	_, err := c.doItemWithHeaders(ctx, "POST",
		"/kubernetes/cluster/"+url.PathEscape(clusterID)+"/upgrade/ccm",
		nil, "", map[string]string{"Idempotency-Key": idemKey})
	return err
}

// RetryK8sClusterUpgrade is NOT a "resume the stuck CP/worker rolling upgrade"
// operation despite the route name - verified against the Master's
// Kubernetes\ClusterService::retry(), which UpgradeController::retryUpgrade
// unconditionally delegates to: it is gated on cluster.state=="error" (422
// otherwise) and, when it runs, tears down every partial artifact (worker + CP
// VMs, the CP load balancer, all three security groups, reserved CP IPs),
// clears BOTH cp_target_kubernetes_version_id and
// worker_target_kubernetes_version_id, transitions state error→starting, and
// re-dispatches the FULL cluster-create job on the same row (same CA, same
// row id - effectively a from-scratch rebuild). A failed CP/worker rolling
// upgrade leaves cluster.state=="running" (rolling upgrades never touch
// `state` - only the per-task KubernetesClusterTask.status fails), so calling
// this after a failed upgrade task would itself fail 422 ("Retry is only
// available for clusters in 'error' state") rather than resume anything, and
// calling it whenever state genuinely IS "error" destroys/rebuilds the whole
// cluster - a disproportionate response to a narrowly-failed version bump.
// Implemented for completeness/future use (e.g. a dedicated cluster-recovery
// action); the iaas_kubernetes_cluster resource's Update does NOT call this
// automatically on an upgrade-task failure - see that Update method's doc
// comment for the reasoning.
func (c *Client) RetryK8sClusterUpgrade(ctx context.Context, clusterID, idemKey string) (map[string]any, error) {
	if clusterID == "" {
		return nil, fmt.Errorf("RetryK8sClusterUpgrade: empty cluster id")
	}
	if idemKey == "" {
		idemKey = newUUIDv4()
	}
	return c.doItemWithHeaders(ctx, "POST",
		"/kubernetes/cluster/"+url.PathEscape(clusterID)+"/upgrade/retry",
		nil, "", map[string]string{"Idempotency-Key": idemKey})
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
