package client

import (
	"context"
	"fmt"
	"net/url"
)

// Managed Database endpoints, verified against the real UserApi
// ManagedDatabaseController / ManagedDatabaseService + routes/user_api.php.
//
// This file covers the managed database ITSELF (create/show/list/destroy +
// async deploy convergence) AND its in-place / action mutations (resize,
// restart, reset-password, version upgrade, retry, acknowledge-error,
// resync-replicas) AND read-replica create (the replica is its own
// ManagedDatabase row, so it reuses GetManagedDatabase/DeleteManagedDatabase).
//
// ROUTES (controller-verified — all wrapped in the billing.enabled middleware):
//
//	CREATE  POST   /databases                          body {name (req), engine (req:
//	                                                     mysql|mariadb|postgresql),
//	                                                     engine_version (req), db_plan_id (req),
//	                                                     vpc_id (req), vpc_subnet_id (req),
//	                                                     hypervisor_group_id?}
//	                                                     → 200 {success,message,
//	                                                     managed_database:{id,status:"deploying",...}}
//	SHOW    GET    /database/{id}                       → {success,managed_database:{...,
//	                                                     public_ip:{ip},plan,vpc,...}}; 404 absent/non-owned
//	LIST    GET    /databases                           → {success,managed_databases:{data:[...]}}
//	DELETE  DELETE /database/{id}                       → {success,message}; success:false on failure
//	RESIZE  PATCH  /database/{id}/resize                body {db_plan_id (req)} → {success,message,
//	                                                     managed_database}
//	RESTART POST   /database/{id}/restart               → {success,message}
//	RESETPW POST   /database/{id}/reset-password        → {success,message,password:"<cleartext>"}
//	UPGRADE POST   /database/{id}/upgrade               body {target_version (req)} → {success,message};
//	                                                     T9 (subuser.permission:databases.manage) — see
//	                                                     UpgradeManagedDatabase for the async-timing caveat.
//	RETRY   POST   /database/{id}/retry                 → {success,message,task_id}; T9 — see
//	                                                     RetryManagedDatabase (rebuilds destructively;
//	                                                     implemented + tested, deliberately unwired).
//	ACKERR  POST   /database/{id}/acknowledge-error     → {success:true}; T9 — see
//	                                                     AcknowledgeManagedDatabaseError (non-destructive;
//	                                                     implemented + tested, deliberately unwired).
//	RESYNC  POST   /database/{id}/resync-replicas       → {success,message[,errors]}; T9 — see
//	                                                     ResyncManagedDatabaseReplicas.
//	REPLICA POST   /database/{id}/replica               body {name?, db_plan_id (req),
//	                                                     vpc_subnet_id (req)} → {success,message,
//	                                                     replica:{id,status:"deploying",...}}
//
// Async behaviour (controller-verified):
//   - CREATE is ASYNC and backed by a REAL instance: the managed_databases row is
//     recorded synchronously with status="deploying" and a backing Instance +
//     slave deploy task are created. There is NO task_id in the create response —
//     ManagedDatabaseService::deploy returns {success,message,managed_database:{id,
//     status:"deploying",...}} (the controller's Scribe {success,message}-only
//     annotation is stale, like VPC/LB). The async signal is the DB's own "status"
//     field, polled via the SHOW endpoint. Lifecycle: deploying → active (the
//     slave/cloud-init callback flips it once the engine is initialised) | error
//     (deploy failed). Ready="active"; fail="error". GetManagedDatabase IS the poll.
//   - REPLICA create is identically ASYNC: the replica is its OWN managed_databases
//     row with status="deploying" → poll GetManagedDatabase(replicaID) until active.
//   - RESIZE / RESTART / RESET-PASSWORD / RESYNC-REPLICAS are SYNC from the API's
//     view: the controller updates the row and dispatches a fire-and-forget slave
//     task, returning immediately. No waiter is needed for them.
//   - UPGRADE is a HYBRID: validation + the engine_version column write are
//     SYNCHRONOUS (the row already shows the target version once the call
//     returns success), but the actual engine upgrade on the box runs
//     asynchronously afterwards with no independently observable completion
//     signal through this API (see UpgradeManagedDatabase). The resource still
//     polls once via the shared waiter for consistency/fast-failure detection.
//   - DELETE flips status→"destroying", bills the final hours, destroys the backing
//     instance (releasing its public IP), and soft-deletes the row, so a subsequent
//     SHOW 404s. A failure (e.g. instance destroy threw, or a primary with replicas)
//     surfaces as success:false, so doVoid checks the flag.
//
// Connection details / secrets (controller + model verified):
//   - admin_password / superadmin_password / exporter_password / replication_password
//     are `encrypted` + in the model's $hidden, so they are NEVER returned by SHOW.
//     The ONLY place a cleartext password is returned is the reset-password action
//     response ({success,message,password}). So the resource cannot read the
//     password from create/SHOW — it exposes username (admin_user), port, and host
//     (the nested public_ip{ip}) as computed, and the password only as a Sensitive
//     value surfaced from a reset-password action (write-only-ish).
//
// Billing/feature gating (controller-verified):
//   - The managed-database routes ARE wrapped in billing.enabled → HTTP 403
//     {success:false,message:"This feature is unavailable because billing is
//     disabled."} when billing is off (admins bypass). responseError maps 403.
//   - Feature/quota gating beyond billing arrives as HTTP 200 success:false:
//     plan disabled, engine unsupported by plan, invalid version, managed_databases
//     quota, hg->db_enabled false, no managed_database image, subnet has no free
//     IP, NAT gateway required for a private subnet, no public IPv4 available.
//     doItem/doVoid surface all of these (C3).
//
// An empty-id guard is applied on every path-id argument (consistency).

// CreateManagedDatabase deploys a managed database from the supplied prebuilt
// body (name + engine + engine_version + db_plan_id + vpc_id + vpc_subnet_id
// required). Create is ASYNC: the returned object carries the id but
// status="deploying"; the caller must poll GetManagedDatabase until
// status="active". The "managed_database" envelope is unwrapped.
func (c *Client) CreateManagedDatabase(ctx context.Context, body map[string]any) (map[string]any, error) {
	return c.doItem(ctx, "POST", "/databases", body, "managed_database")
}

// GetManagedDatabase fetches a single managed database by UUID. The SHOW route is
// SINGULAR. A 404 (absent or owned by a different account) is returned as an
// *APIError that IsNotFound recognises. The returned object includes the nested
// public_ip object and the embedded plan/vpc from the SHOW payload. This doubles
// as the async poll source for create (scan "status" for "active") and the 404
// signal for delete convergence. It is also reused by the replica resource (a
// replica is its own managed_databases row).
func (c *Client) GetManagedDatabase(ctx context.Context, id string) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("GetManagedDatabase: empty id")
	}
	return c.doItem(ctx, "GET", "/database/"+url.PathEscape(id), nil, "managed_database")
}

// ListManagedDatabases returns all PRIMARY managed databases belonging to the
// authenticated account (the controller hides replicas from the index, nesting
// them under each primary's "replicas" relation). The index wraps the Laravel
// paginator under the named "managed_databases" key
// ({success,managed_databases:{data:[...]}}) — NOT a top-level "data" array — so
// the shared doList paginator decoder cannot be used directly. Instead doItem
// unwraps the named key (surfacing C3 success:false), then the inner paginator's
// "data" array is flattened to []map[string]any.
//
// CAVEAT: this fetches only page 1 (the named-key paginator can't use the
// auto-paginating doList) — a future list data source must add page iteration.
func (c *Client) ListManagedDatabases(ctx context.Context) ([]map[string]any, error) {
	paginator, err := c.doItem(ctx, "GET", "/databases", nil, "managed_databases")
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

// DeleteManagedDatabase deletes (soft-deletes) the managed database and destroys
// its backing instance. The row is flipped to status="destroying" and removed, so
// a subsequent SHOW 404s. A failure (e.g. the backing instance destroy threw, or
// a primary that still has replicas) is signalled with success:false, so doVoid
// checks the flag. Reused by the replica resource.
func (c *Client) DeleteManagedDatabase(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("DeleteManagedDatabase: empty id")
	}
	return c.doVoid(ctx, "DELETE", "/database/"+url.PathEscape(id), nil)
}

// ResizeManagedDatabase changes the plan of a managed database in place via PATCH
// /database/{id}/resize {db_plan_id}. The controller updates db_plan_id on the row
// (and the backing instance) and dispatches a fire-and-forget configure task, so
// this is SYNC from the API's view (no status flip / waiter). The new plan storage
// must be >= the current plan's, and the engine must be supported by the new plan,
// else the controller returns 200 success:false (surfaced by doItem). The
// "managed_database" envelope is unwrapped.
func (c *Client) ResizeManagedDatabase(ctx context.Context, id string, body map[string]any) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("ResizeManagedDatabase: empty id")
	}
	return c.doItem(ctx, "PATCH", "/database/"+url.PathEscape(id)+"/resize", body, "managed_database")
}

// RestartManagedDatabase restarts the database engine via POST
// /database/{id}/restart. It is a stateless action (dispatches a slave task,
// returns {success,message}); a failure surfaces as success:false (doVoid).
func (c *Client) RestartManagedDatabase(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("RestartManagedDatabase: empty id")
	}
	return c.doVoid(ctx, "POST", "/database/"+url.PathEscape(id)+"/restart", nil)
}

// ResetManagedDatabasePassword resets the admin password via POST
// /database/{id}/reset-password. This is the ONLY endpoint that returns a
// cleartext password: {success,message,password:"<new>"}. The top-level "password"
// field is read with key="" (no wrapper) and returned to the caller, which surfaces
// it as a Sensitive value. A failure surfaces as success:false (doItem).
func (c *Client) ResetManagedDatabasePassword(ctx context.Context, id string) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("ResetManagedDatabasePassword: empty id")
	}
	return c.doItem(ctx, "POST", "/database/"+url.PathEscape(id)+"/reset-password", nil, "")
}

// UpgradeManagedDatabase performs an in-place engine version upgrade via POST
// /database/{id}/upgrade, body {target_version (req, string)}. Controller-verified
// (ManagedDatabaseService::upgradeVersion): the target must be a version offered
// for the database's engine AND version_compare() strictly HIGHER than the
// current engine_version (no downgrade, no re-apply of the same version); a
// backing instance must exist, and a pre-upgrade backup is taken first (its
// failure aborts the upgrade before anything is sent to the hypervisor).
//
// CRITICAL timing note (documented concern, not a bug in this provider): the
// row's engine_version column is updated to the target SYNCHRONOUSLY, the
// moment the hypervisor ACCEPTS the upgrade command — not once the engine
// upgrade actually finishes running on the box. There is no task_id in the
// response and the UserApi SHOW does not eager-load the tasks[] relation (unlike
// iaas_kubernetes_cluster's upgrade, which polls an embedded task), so real
// completion on the box is NOT independently observable through this API: a
// slave-side failure only surfaces later, asynchronously, as last_error /
// error_acknowledged=false on a subsequent SHOW (see last_error/error_acknowledged
// on managedDatabaseModel). A synchronous validation failure (unsupported/
// not-higher version, missing instance, backup failure, hypervisor rejects the
// command) surfaces immediately as success:false (doVoid/C3).
func (c *Client) UpgradeManagedDatabase(ctx context.Context, id, targetVersion string) error {
	if id == "" {
		return fmt.Errorf("UpgradeManagedDatabase: empty id")
	}
	return c.doVoid(ctx, "POST", "/database/"+url.PathEscape(id)+"/upgrade", map[string]any{
		"target_version": targetVersion,
	})
}

// RetryManagedDatabase retries a failed deployment via POST /database/{id}/retry.
// Controller-verified (ManagedDatabaseService::retryDeploy): despite the name,
// this is NOT a "resume the stuck step" operation — it rebuilds the backing
// instance from scratch (the same cloud-init rebuild code path as an instance
// reinstall: deployed=0, status=0, fresh cloudcfg, a new deploy task with
// rebuild=true), flips the database status back to "deploying" (and, for a
// replica, replication_status back to "syncing"). It refuses (success:false,
// no rebuild) if the database entered "deploying" less than 10 minutes ago
// (still legitimately deploying, not stuck).
//
// Implemented + unit-tested for T9 but DELIBERATELY LEFT UNWIRED from any
// automatic invocation (mirrors T7's kubernetes_cluster upgrade/retry
// precedent — see wave-bc-t7-report.md): auto-invoking this from a waiter fail
// path would silently destroy and rebuild a database that may still be
// perfectly healthy (e.g. a transient slave-side blip, or an unrelated
// last_error), which is far more damaging than surfacing a clear Diagnostics
// error and letting the operator decide.
func (c *Client) RetryManagedDatabase(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("RetryManagedDatabase: empty id")
	}
	return c.doVoid(ctx, "POST", "/database/"+url.PathEscape(id)+"/retry", nil)
}

// AcknowledgeManagedDatabaseError clears the database's alert state via POST
// /database/{id}/acknowledge-error — controller-verified to unconditionally set
// last_error=null, error_acknowledged=true and return {success:true}; it never
// fails once the id resolves. Unlike RetryManagedDatabase this is NOT
// destructive (it touches no infrastructure, only the notification flags), but
// it is likewise implemented + unit-tested for T9 and left UNWIRED from
// automatic invocation: last_error/error_acknowledged are surfaced as computed
// attributes (see managed_database.go) precisely so an operator can see that
// something failed, and silently auto-acknowledging the moment this resource
// observes an error would erase that signal instead of just tidying up a
// resolved condition. It is available for a future explicit acknowledge_error
// trigger attribute (the reset_password/resync_replicas pattern) if wanted.
func (c *Client) AcknowledgeManagedDatabaseError(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("AcknowledgeManagedDatabaseError: empty id")
	}
	return c.doVoid(ctx, "POST", "/database/"+url.PathEscape(id)+"/acknowledge-error", nil)
}

// ResyncManagedDatabaseReplicas resyncs every eligible replica of a PRIMARY
// database from the current primary snapshot, via POST
// /database/{id}/resync-replicas (no body). Controller-verified
// (ManagedDatabaseService::resyncReplicas): the target must be a primary,
// "active", and have at least one replica that is not currently "deploying" or
// already "syncing" — the primary's replication user is (re)configured if no
// replica is currently active/syncing, then every eligible replica is sent a
// resync command. SYNC from the API's view (mirrors resize/restart/
// reset-password): it dispatches the per-replica slave tasks and returns once
// every dispatch has been attempted, so no waiter is needed. A wholesale
// rejection (not a primary, not active, no eligible replicas) or a partial
// per-replica dispatch failure both surface as success:false (doVoid/C3); the
// controller's message differs ("No replicas available to resync." vs "Some
// replicas failed to resync.") but both are surfaced verbatim.
func (c *Client) ResyncManagedDatabaseReplicas(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("ResyncManagedDatabaseReplicas: empty id")
	}
	return c.doVoid(ctx, "POST", "/database/"+url.PathEscape(id)+"/resync-replicas", nil)
}

// CreateDatabaseReplica creates a read replica of the primary managed database
// {primaryID} via POST /database/{primaryID}/replica {name?, db_plan_id (req),
// vpc_subnet_id (req)}. The replica is its OWN managed_databases row, returned
// under the "replica" envelope with status="deploying". Create is ASYNC: poll
// GetManagedDatabase(replicaID) until status="active". The primary must be active
// + a primary + in a VPC, the replica plan storage must be >= the primary plan's,
// and the per-primary replica count limit applies — all enforced as 200
// success:false (surfaced by doItem).
func (c *Client) CreateDatabaseReplica(ctx context.Context, primaryID string, body map[string]any) (map[string]any, error) {
	if primaryID == "" {
		return nil, fmt.Errorf("CreateDatabaseReplica: empty primary id")
	}
	return c.doItem(ctx, "POST", "/database/"+url.PathEscape(primaryID)+"/replica", body, "replica")
}
