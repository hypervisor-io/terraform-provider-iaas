package client

import (
	"context"
	"fmt"
	"net/url"
)

// Instance Backup Policy endpoints (verified against
// UserApi\InstanceBackupPolicyController + InstanceBackupPolicyService +
// InstancePolicyStoreRequest / InstancePolicyUpdateRequest /
// InstancePolicyAttachRequest + routes/user_api.php).
//
// An instance backup policy is a user-owned named schedule + retention
// configuration that can have individual instances attached to it (one
// instance can belong to at most one policy; attachment is managed via
// per-instance attach/detach endpoints, one instance per call).
//
// All routes share the singular/plural asymmetry pattern:
//
//	INDEX   GET    /backup-policies                          (PLURAL)
//	                → Laravel paginator {current_page,data:[...],total,...}
//	CREATE  POST   /backup-policies                          (PLURAL)
//	                body {name (required,max:255),
//	                      full_backup_frequency (required,in:daily|weekly),
//	                      full_backup_time (required,date_format:H:i),
//	                      full_backup_day (required_if:freq=weekly,0-6),
//	                      max_incremental_chain (required,0-30),
//	                      retention_count (required,1-365),
//	                      backup_device (required,in:primary|all)}
//	                → {success,message,policy:{id,name,...}}
//	SHOW    GET    /backup-policy/{id}                       (SINGULAR)
//	                → {policy:{id,name,full_backup_frequency,full_backup_time,
//	                           full_backup_day,max_incremental_chain,
//	                           retention_count,backup_device,status,
//	                           consecutive_failures,last_error,...,
//	                           instances:[{id,hostname,...}]},
//	                   available_instances:[...]}
//	UPDATE  PATCH  /backup-policy/{id}                       (SINGULAR)
//	                body same shape as CREATE (all fields required)
//	                → {success,message,policy:{...}}
//	DELETE  DELETE /backup-policy/{id}                       (SINGULAR)
//	                → {success,message}
//	ATTACH  POST   /backup-policy/{id}/attach                (SINGULAR)
//	                body {instance_id:"<uuid>"}  (one at a time)
//	                → {success,message}
//	DETACH  POST   /backup-policy/{id}/detach                (SINGULAR)
//	                body {instance_id:"<uuid>"}  (one at a time)
//	                → {success,message}
//
// Notes:
//   - All writes are SYNCHRONOUS (no task/waiter).
//   - The service converts the user's local time → UTC on store/update and
//     converts UTC → user's local time for display on SHOW. The provider
//     stores and sends UTC times directly (no timezone conversion).
//   - success:false at HTTP 200 = error (C3) — handled by doItem/doVoid.
//   - There is no billing gate (routes are NOT wrapped in billing.enabled).
//   - testConnection is NOT modelled (not IaC state).
//   - reset-failures is NOT modelled (operational, not IaC state).
//   - Instance IDs from SHOW come via the nested policy.instances array
//     (each element has an "id" field), NOT a top-level key like security_group.

// Database Backup Policy endpoints (verified against
// UserApi\DbBackupPolicyController + DbBackupPolicyService +
// DbPolicyStoreRequest / DbPolicyUpdateRequest / DbPolicyAttachRequest +
// routes/user_api.php).
//
// A database backup policy holds S3 credentials, schedule/PITR/retention
// config and can have individual ManagedDatabase records attached to it.
// Attachment is per-database (one at a time); attach dispatches a slave job.
//
//	INDEX   GET    /networking/db-backup-policies            (PLURAL)
//	                → Laravel paginator {current_page,data:[...],total,...}
//	CREATE  POST   /networking/db-backup-policies            (PLURAL)
//	                body {name (required,max:255),
//	                      s3_endpoint (required), s3_bucket (required),
//	                      s3_region (required), s3_access_key (required),
//	                      s3_secret_key (required), s3_path_prefix?,
//	                      full_backup_frequency (required,in:daily|weekly),
//	                      full_backup_time (required,date_format:H:i),
//	                      full_backup_day?,
//	                      incremental_frequency (required,in:none|1h|2h|4h|6h|12h),
//	                      pitr_enabled?, retention_full_count (required,1-365),
//	                      retention_incremental_days (required,1-365),
//	                      retention_pitr_hours (required,1-720),
//	                      encryption_enabled?}
//	                → {success,message,policy:{id,...}}
//	SHOW    GET    /networking/db-backup-policy/{id}         (SINGULAR)
//	                → {policy:{id,name,s3_endpoint,s3_bucket,s3_region,
//	                           s3_path_prefix,full_backup_frequency,
//	                           full_backup_time,full_backup_day,
//	                           incremental_frequency,pitr_enabled,
//	                           retention_full_count,retention_incremental_days,
//	                           retention_pitr_hours,encryption_enabled,status,
//	                           managed_databases:[{id,...}],...},
//	                   available_databases:[...]}
//	UPDATE  PATCH  /networking/db-backup-policy/{id}         (SINGULAR)
//	                body same fields as CREATE, but all are "sometimes|required"
//	                (partial update allowed; empty s3_access_key/s3_secret_key
//	                 in the body are stripped by the service, preserving existing)
//	                → {success,message,policy:{...}}
//	DELETE  DELETE /networking/db-backup-policy/{id}         (SINGULAR)
//	                → {success,message}
//	ATTACH  POST   /networking/db-backup-policy/{id}/attach  (SINGULAR)
//	                body {managed_database_id:"<uuid>"}  (one at a time)
//	                → {success,message}
//	DETACH  POST   /networking/db-backup-policy/{id}/detach  (SINGULAR)
//	                body {managed_database_id:"<uuid>"}  (one at a time)
//	                → {success,message}
//
// Notes:
//   - s3_access_key, s3_secret_key, encryption_key are $hidden on the model,
//     so SHOW never returns them. They are write-only (sensitive) inputs;
//     the resource must preserve prior values (do not blank on read).
//   - testConnection is NOT modelled (operational helper, not IaC state).
//   - reset-failures is NOT modelled (operational, not IaC state).
//   - No billing gate.

// ---------------------------------------------------------------------------
// Instance Backup Policy
// ---------------------------------------------------------------------------

// ListInstanceBackupPolicies returns all instance backup policies visible to
// the authenticated user (paginator-aware).
func (c *Client) ListInstanceBackupPolicies(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/backup-policies", nil)
}

// CreateInstanceBackupPolicy creates a new instance backup policy from the
// supplied body. The collection path is PLURAL (/backup-policies). The create
// response carries the new policy under the "policy" key.
func (c *Client) CreateInstanceBackupPolicy(ctx context.Context, body map[string]any) (map[string]any, error) {
	return c.doItem(ctx, "POST", "/backup-policies", body, "policy")
}

// GetInstanceBackupPolicy fetches a single instance backup policy by id. The
// SHOW route is SINGULAR (/backup-policy/{id}). A 404 is an *APIError
// recognised by IsNotFound. The returned object is the nested "policy" object
// (which contains the embedded "instances" array).
func (c *Client) GetInstanceBackupPolicy(ctx context.Context, id string) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("GetInstanceBackupPolicy: empty id")
	}
	path := "/backup-policy/" + url.PathEscape(id)
	return c.doItem(ctx, "GET", path, nil, "policy")
}

// UpdateInstanceBackupPolicy patches the mutable fields of an instance backup
// policy. The UPDATE route is SINGULAR. All fields are required by the
// controller (same rules as create). The response carries the updated policy
// under the "policy" key.
func (c *Client) UpdateInstanceBackupPolicy(ctx context.Context, id string, body map[string]any) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("UpdateInstanceBackupPolicy: empty id")
	}
	path := "/backup-policy/" + url.PathEscape(id)
	return c.doItem(ctx, "PATCH", path, body, "policy")
}

// DeleteInstanceBackupPolicy deletes an instance backup policy by id. The
// service detaches all instances before deletion. A failure is signalled with
// success:false at HTTP 200, so doVoid checks it.
func (c *Client) DeleteInstanceBackupPolicy(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("DeleteInstanceBackupPolicy: empty id")
	}
	path := "/backup-policy/" + url.PathEscape(id)
	return c.doVoid(ctx, "DELETE", path, nil)
}

// AttachInstanceToBackupPolicy attaches a single instance to a backup policy.
// The route is POST /backup-policy/{id}/attach with body {instance_id:"..."}.
// One instance per call (API validates a single instance_id, not an array).
func (c *Client) AttachInstanceToBackupPolicy(ctx context.Context, policyID, instanceID string) error {
	if policyID == "" {
		return fmt.Errorf("AttachInstanceToBackupPolicy: empty policyID")
	}
	if instanceID == "" {
		return fmt.Errorf("AttachInstanceToBackupPolicy: empty instanceID")
	}
	path := "/backup-policy/" + url.PathEscape(policyID) + "/attach"
	body := map[string]any{"instance_id": instanceID}
	return c.doVoid(ctx, "POST", path, body)
}

// DetachInstanceFromBackupPolicy detaches a single instance from a backup
// policy. The route is POST /backup-policy/{id}/detach with body
// {instance_id:"..."}. One instance per call.
func (c *Client) DetachInstanceFromBackupPolicy(ctx context.Context, policyID, instanceID string) error {
	if policyID == "" {
		return fmt.Errorf("DetachInstanceFromBackupPolicy: empty policyID")
	}
	if instanceID == "" {
		return fmt.Errorf("DetachInstanceFromBackupPolicy: empty instanceID")
	}
	path := "/backup-policy/" + url.PathEscape(policyID) + "/detach"
	body := map[string]any{"instance_id": instanceID}
	return c.doVoid(ctx, "POST", path, body)
}

// ---------------------------------------------------------------------------
// Database Backup Policy
// ---------------------------------------------------------------------------

// ListDBBackupPolicies returns all database backup policies visible to the
// authenticated user (paginator-aware).
func (c *Client) ListDBBackupPolicies(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/networking/db-backup-policies", nil)
}

// CreateDBBackupPolicy creates a new database backup policy from the supplied
// body. The collection path is PLURAL (/networking/db-backup-policies). The
// create response carries the new policy under the "policy" key.
func (c *Client) CreateDBBackupPolicy(ctx context.Context, body map[string]any) (map[string]any, error) {
	return c.doItem(ctx, "POST", "/networking/db-backup-policies", body, "policy")
}

// GetDBBackupPolicy fetches a single database backup policy by id. The SHOW
// route is SINGULAR (/networking/db-backup-policy/{id}). A 404 is an *APIError
// recognised by IsNotFound. The returned object is the nested "policy" object
// (which contains the embedded "managed_databases" array). Note that
// s3_access_key, s3_secret_key, and encryption_key are hidden by the model
// and will never appear in this response.
func (c *Client) GetDBBackupPolicy(ctx context.Context, id string) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("GetDBBackupPolicy: empty id")
	}
	path := "/networking/db-backup-policy/" + url.PathEscape(id)
	return c.doItem(ctx, "GET", path, nil, "policy")
}

// UpdateDBBackupPolicy patches the mutable fields of a database backup policy.
// The UPDATE route is SINGULAR. The response carries the updated policy under
// the "policy" key. Empty s3_access_key / s3_secret_key values in the body
// are stripped by the service, so callers should omit those keys when not
// changing credentials.
func (c *Client) UpdateDBBackupPolicy(ctx context.Context, id string, body map[string]any) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("UpdateDBBackupPolicy: empty id")
	}
	path := "/networking/db-backup-policy/" + url.PathEscape(id)
	return c.doItem(ctx, "PATCH", path, body, "policy")
}

// DeleteDBBackupPolicy deletes a database backup policy by id. The service
// detaches all databases before deletion. A failure is signalled with
// success:false at HTTP 200, so doVoid checks it.
func (c *Client) DeleteDBBackupPolicy(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("DeleteDBBackupPolicy: empty id")
	}
	path := "/networking/db-backup-policy/" + url.PathEscape(id)
	return c.doVoid(ctx, "DELETE", path, nil)
}

// AttachDatabaseToBackupPolicy attaches a single managed database to a backup
// policy. The route is POST /networking/db-backup-policy/{id}/attach with body
// {managed_database_id:"..."}. One database per call. The service dispatches a
// slave job to configure the backup agent after updating the DB record.
func (c *Client) AttachDatabaseToBackupPolicy(ctx context.Context, policyID, databaseID string) error {
	if policyID == "" {
		return fmt.Errorf("AttachDatabaseToBackupPolicy: empty policyID")
	}
	if databaseID == "" {
		return fmt.Errorf("AttachDatabaseToBackupPolicy: empty databaseID")
	}
	path := "/networking/db-backup-policy/" + url.PathEscape(policyID) + "/attach"
	body := map[string]any{"managed_database_id": databaseID}
	return c.doVoid(ctx, "POST", path, body)
}

// DetachDatabaseFromBackupPolicy detaches a single managed database from a
// backup policy. The route is POST /networking/db-backup-policy/{id}/detach
// with body {managed_database_id:"..."}. One database per call.
func (c *Client) DetachDatabaseFromBackupPolicy(ctx context.Context, policyID, databaseID string) error {
	if policyID == "" {
		return fmt.Errorf("DetachDatabaseFromBackupPolicy: empty policyID")
	}
	if databaseID == "" {
		return fmt.Errorf("DetachDatabaseFromBackupPolicy: empty databaseID")
	}
	path := "/networking/db-backup-policy/" + url.PathEscape(policyID) + "/detach"
	body := map[string]any{"managed_database_id": databaseID}
	return c.doVoid(ctx, "POST", path, body)
}
