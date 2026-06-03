package client

import (
	"context"
	"fmt"
	"net/url"
)

// S3 Object Storage endpoints — buckets + standalone access keys + their
// bucket↔key attachments. Verified against the real UserApi\S3BucketController,
// UserApi\S3AccessKeyController, S3BucketService, S3AccessKeyService, the
// Store/Update FormRequests, the S3Bucket / UserS3AccessKey models, and
// routes/user_api.php.
//
// ─── BUCKETS ────────────────────────────────────────────────────────────────
//
//	INDEX   GET    /object-storage/buckets                 (PLURAL) → Laravel
//	                                                         paginator {data:[...]}
//	CREATE  POST   /object-storage/buckets                 (PLURAL) body
//	                                                         {name (req), s3_plan_id (req),
//	                                                         s3_server_id (req)}
//	                                                         → 200 {success:true,message}
//	                                                         (NO id, NO body → C4 readback)
//	SHOW    GET    /object-storage/bucket/{id}             (SINGULAR) → 200
//	                                                         {bucket:{id,name,...},endpoint,
//	                                                         access_key,secret_key}
//	                                                         (envelope keys are TOP-LEVEL;
//	                                                         the bucket's OWN access/secret key
//	                                                         are returned here)
//	ACL     PATCH  /object-storage/bucket/{id}/acl/{action} (action ∈ public|private|
//	                                                         upload|download) → {success,message}
//	DELETE  DELETE /object-storage/bucket/{id}             (SINGULAR) → {success,message}
//	                                                         (dispatches an async delete JOB,
//	                                                         but the API call itself is sync)
//	KEYS    GET    /object-storage/bucket/{id}/keys        → paginator of attached key
//	                                                         objects, each carrying
//	                                                         pivot.permission
//
// ─── ACCESS KEYS (standalone) ───────────────────────────────────────────────
//
//	INDEX   GET    /object-storage/access-keys             (PLURAL) → paginator
//	                                                         {data:[{id,name,access_key,
//	                                                         active,...}]} — secret_key is
//	                                                         $hidden, NEVER listed
//	CREATE  POST   /object-storage/access-keys             (PLURAL) body {name (req)}
//	                                                         → 200 {success:true,message,
//	                                                         data:{access_key,secret_key}}
//	                                                         ★ the SECRET is shown ONCE here
//	                                                         and NEVER again; NO id in the
//	                                                         response → C4 readback by access_key
//	UPDATE  PATCH  /object-storage/access-key/{id}         (SINGULAR) body
//	                                                         {name?, active?} → {success,message}
//	                                                         (NO body → Read after; toggling
//	                                                         active dispatches a suspend/resume job)
//
//	★ There is NO access-key SHOW route and NO access-key DELETE route in the
//	  user API (only index/store/update). GetS3AccessKey therefore lists +
//	  scans-by-id (C4), and the resource's Delete is a state-only removal with a
//	  warning (the platform offers no user-API delete; cleanup is via the panel).
//
// ─── BUCKET ↔ KEY ATTACHMENTS ───────────────────────────────────────────────
//
//	ATTACH  POST   /object-storage/bucket/{bid}/attach/{kid}  body {permission
//	                                                         (req: read|write|readwrite)}
//	                                                         → {success,message}
//	UPDATE  PATCH  /object-storage/bucket/{bid}/update/{kid}  body {permission (req)}
//	                                                         → {success,message}  (in-place
//	                                                         permission change — NOT a
//	                                                         delete+add)
//	DETACH  POST   /object-storage/bucket/{bid}/detach/{kid}                  → {success,message}
//	                                                         (POST, not DELETE)
//
// Notes:
//   - The S3 routes are NOT wrapped in billing.enabled — they are reachable with
//     billing disabled (matches LB/natgw; differs from managed_database/static_ip).
//     A billing record (CsS3Bucket) is created internally but does not 403 the route.
//   - All writes are SYNCHRONOUS at HTTP 200 (no task/state, no waiter). The bucket
//     DELETE dispatches an async DeleteS3Bucket job, but the API call returns
//     immediately and a subsequent SHOW 404s once the row is removed.
//   - Failures surface as 200 success:false (quota exceeded, plan/server disabled,
//     key not attached, bucket suspended) OR HTTP 422 (validation, quota guard) —
//     doItem/doVoid surface both (C3 + responseError).
//   - Every id is url.PathEscape'd into the path; empty-id guards on every path arg.

// ─── Buckets ────────────────────────────────────────────────────────────────

// ListS3Buckets returns all S3 buckets owned by the authenticated account
// (paginator-aware). The collection path is PLURAL.
func (c *Client) ListS3Buckets(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/object-storage/buckets", nil)
}

// CreateS3Bucket creates a bucket from the supplied prebuilt body
// (name + s3_plan_id + s3_server_id, all required). The create is SYNCHRONOUS
// but the response is only {success:true,message} — it carries NEITHER an id NOR
// a body — so the caller MUST list-and-match by the unique bucket name to
// discover the id (C4 readback). The collection path is PLURAL.
//
// On failure the controller returns HTTP 422 {success:false,message} (quota,
// plan/server disabled) or a validation 422 — both surfaced by doItem.
func (c *Client) CreateS3Bucket(ctx context.Context, body map[string]any) (map[string]any, error) {
	return c.doItem(ctx, "POST", "/object-storage/buckets", body, "")
}

// GetS3Bucket fetches a single bucket by id, returning the ENTIRE SHOW envelope
// (key=""). The bucket object is nested under "bucket"; the bucket's own
// access_key / secret_key and the computed endpoint are TOP-LEVEL envelope keys.
// The SHOW route is SINGULAR. A 404 (absent / owned by another account) is an
// *APIError recognised by IsNotFound.
func (c *Client) GetS3Bucket(ctx context.Context, id string) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("GetS3Bucket: empty id")
	}
	return c.doItem(ctx, "GET", "/object-storage/bucket/"+url.PathEscape(id), nil, "")
}

// GetS3BucketByName lists all buckets and returns the one whose name matches.
// Used as the C4 create-without-id readback (the create response has no id, but
// the bucket name is unique — StoreBucketRequest enforces unique:s3_buckets,name).
// A missing name is surfaced as a 404 *APIError (IsNotFound = true).
func (c *Client) GetS3BucketByName(ctx context.Context, name string) (map[string]any, error) {
	if name == "" {
		return nil, fmt.Errorf("GetS3BucketByName: empty name")
	}
	items, err := c.ListS3Buckets(ctx)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if v, ok := item["name"].(string); ok && v == name {
			return item, nil
		}
	}
	return nil, &APIError{Status: 404, Message: "S3 bucket not found"}
}

// SetS3BucketACL changes a bucket's default access control via
// PATCH /object-storage/bucket/{id}/acl/{action}. action must be one of
// public|private|upload|download (the value is taken from the path, not the
// body). A failure (e.g. the bucket is suspended → 422, or the admin call threw
// → 200 success:false) is surfaced by doVoid.
func (c *Client) SetS3BucketACL(ctx context.Context, id, action string) error {
	if id == "" {
		return fmt.Errorf("SetS3BucketACL: empty id")
	}
	if action == "" {
		return fmt.Errorf("SetS3BucketACL: empty action")
	}
	path := "/object-storage/bucket/" + url.PathEscape(id) + "/acl/" + url.PathEscape(action)
	return c.doVoid(ctx, "PATCH", path, nil)
}

// DeleteS3Bucket deletes a bucket by id (the service bills the final hours then
// dispatches an async DeleteS3Bucket job that removes the bucket, its policies
// and users on the S3 server before soft-deleting the row). The API call is
// synchronous {success,message}; a subsequent SHOW 404s. The DELETE route is
// SINGULAR. A failure is signalled with success:false at HTTP 200, so doVoid
// checks the flag.
func (c *Client) DeleteS3Bucket(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("DeleteS3Bucket: empty id")
	}
	return c.doVoid(ctx, "DELETE", "/object-storage/bucket/"+url.PathEscape(id), nil)
}

// ListS3BucketKeys returns the access keys attached to a bucket
// (GET /object-storage/bucket/{id}/keys), paginator-aware. Each returned key
// object carries the pivot under "pivot" with the attachment's "permission", so
// the resource can rebuild the attached-key set with its per-key permission.
func (c *Client) ListS3BucketKeys(ctx context.Context, bucketID string) ([]map[string]any, error) {
	if bucketID == "" {
		return nil, fmt.Errorf("ListS3BucketKeys: empty bucket id")
	}
	return c.doList(ctx, "GET", "/object-storage/bucket/"+url.PathEscape(bucketID)+"/keys", nil)
}

// AttachS3BucketKey grants an access key access to a bucket with the given
// permission (read|write|readwrite) via
// POST /object-storage/bucket/{bid}/attach/{kid} {permission}. A failure
// (bucket suspended → 422, invalid permission / admin error → 200 success:false)
// is surfaced by doVoid.
func (c *Client) AttachS3BucketKey(ctx context.Context, bucketID, keyID, permission string) error {
	if bucketID == "" {
		return fmt.Errorf("AttachS3BucketKey: empty bucket id")
	}
	if keyID == "" {
		return fmt.Errorf("AttachS3BucketKey: empty key id")
	}
	path := "/object-storage/bucket/" + url.PathEscape(bucketID) + "/attach/" + url.PathEscape(keyID)
	return c.doVoid(ctx, "POST", path, map[string]any{"permission": permission})
}

// UpdateS3BucketKey changes an already-attached key's permission IN PLACE via
// PATCH /object-storage/bucket/{bid}/update/{kid} {permission}. This is a true
// in-place update (the pivot's permission column is updated and the S3 policy is
// rewritten) — NOT a delete+add. A failure is surfaced by doVoid.
func (c *Client) UpdateS3BucketKey(ctx context.Context, bucketID, keyID, permission string) error {
	if bucketID == "" {
		return fmt.Errorf("UpdateS3BucketKey: empty bucket id")
	}
	if keyID == "" {
		return fmt.Errorf("UpdateS3BucketKey: empty key id")
	}
	path := "/object-storage/bucket/" + url.PathEscape(bucketID) + "/update/" + url.PathEscape(keyID)
	return c.doVoid(ctx, "PATCH", path, map[string]any{"permission": permission})
}

// DetachS3BucketKey revokes an access key's access to a bucket via
// POST /object-storage/bucket/{bid}/detach/{kid} (POST, not DELETE). A failure
// is surfaced by doVoid.
func (c *Client) DetachS3BucketKey(ctx context.Context, bucketID, keyID string) error {
	if bucketID == "" {
		return fmt.Errorf("DetachS3BucketKey: empty bucket id")
	}
	if keyID == "" {
		return fmt.Errorf("DetachS3BucketKey: empty key id")
	}
	path := "/object-storage/bucket/" + url.PathEscape(bucketID) + "/detach/" + url.PathEscape(keyID)
	return c.doVoid(ctx, "POST", path, nil)
}

// ─── Access keys (standalone) ────────────────────────────────────────────────

// ListS3AccessKeys returns all standalone S3 access keys owned by the
// authenticated account (paginator-aware). The collection path is PLURAL. The
// secret_key is $hidden on the model, so it is NEVER present in this listing —
// only id, name, access_key, active.
func (c *Client) ListS3AccessKeys(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/object-storage/access-keys", nil)
}

// CreateS3AccessKey creates a standalone access key from the supplied body
// (name required). The response is {success:true,message,data:{access_key,
// secret_key}}: the data sub-object is unwrapped, exposing the PUBLIC access_key
// AND the SECRET secret_key. ★ This is the ONLY time the secret is ever
// returned (it is $hidden everywhere else). The response carries NO id, so the
// caller must list-and-match by the just-issued access_key to discover the
// record id (C4 readback). A failure surfaces as 200 success:false / 422.
func (c *Client) CreateS3AccessKey(ctx context.Context, body map[string]any) (map[string]any, error) {
	return c.doItem(ctx, "POST", "/object-storage/access-keys", body, "data")
}

// GetS3AccessKey fetches a single access key by id. There is NO SHOW route, so
// this lists all keys and scans for the matching id (C4). The returned object
// carries id, name, access_key, active — but NEVER secret_key ($hidden). A
// missing id is surfaced as a 404 *APIError (IsNotFound = true).
func (c *Client) GetS3AccessKey(ctx context.Context, id string) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("GetS3AccessKey: empty id")
	}
	items, err := c.ListS3AccessKeys(ctx)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if v, ok := item["id"].(string); ok && v == id {
			return item, nil
		}
	}
	return nil, &APIError{Status: 404, Message: "S3 access key not found"}
}

// GetS3AccessKeyByAccessKey lists all access keys and returns the one whose
// access_key (the public id, ak_…) matches. Used as the C4 create-without-id
// readback: the create response has no record id but does return the just-issued
// access_key, which is unique. A missing access_key is surfaced as a 404.
func (c *Client) GetS3AccessKeyByAccessKey(ctx context.Context, accessKey string) (map[string]any, error) {
	if accessKey == "" {
		return nil, fmt.Errorf("GetS3AccessKeyByAccessKey: empty access_key")
	}
	items, err := c.ListS3AccessKeys(ctx)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if v, ok := item["access_key"].(string); ok && v == accessKey {
			return item, nil
		}
	}
	return nil, &APIError{Status: 404, Message: "S3 access key not found"}
}

// UpdateS3AccessKey patches an access key's mutable fields (name and/or active)
// via PATCH /object-storage/access-key/{id}. The route is SINGULAR. The PATCH
// response carries NO key body (only {success,message}); toggling active
// dispatches an async suspend/resume job. The caller should re-Read afterwards.
// A failure (duplicate name → 422, etc.) is surfaced by doVoid.
func (c *Client) UpdateS3AccessKey(ctx context.Context, id string, fields map[string]any) error {
	if id == "" {
		return fmt.Errorf("UpdateS3AccessKey: empty id")
	}
	return c.doVoid(ctx, "PATCH", "/object-storage/access-key/"+url.PathEscape(id), fields)
}
