package client

import (
	"context"
	"fmt"
	"net/url"
)

// Volume + Volume Snapshot endpoints (verified against the real UserApi
// VolumeController / VolumeSnapshotController + routes/user_api.php).
//
// Note the singular/plural path asymmetry the controller exposes: the
// collection/create path is PLURAL (/storage/volumes) but every item path is
// SINGULAR (/storage/volume/{id}/...).
//
//	CREATE   POST   /storage/volumes                      body {name (req), volume_plan_id (req),
//	                                                       hypervisor_group_id (req), project_id?}
//	                                                       → 200 {success,volume:{id,status:"pending",
//	                                                       deployed:0,size,...}}; 422 {success:false,message}
//	SHOW     GET    /storage/volume/{id}                  → {volume:{...,snapshots:[...],backups:[...]}};
//	                                                       404 when absent / wrong owner
//	ATTACH   POST   /storage/volume/{id}/attach           body {instance_id (req)} → {success,volume}
//	DETACH   POST   /storage/volume/{id}/detach           (no body)               → {success,volume}
//	RESIZE   PATCH  /storage/volume/{id}/resize           body {volume_plan_id (req)} → {success,volume,is_downgrade}
//	DELETE   DELETE /storage/volume/{id}                  → {success,message}
//	         (all four: 422 {success:false,reason,message} on a definite refusal;
//	         the operation gate answers 409 reason volume_busy, and 429 reason
//	         volume_cooldown with a Retry-After header in whole seconds; older
//	         masters answered both as 422 reason volume_operation_rejected)
//
//	SNAPSHOT-CREATE  POST   /storage/volume/{id}/snapshot            body {name?} → {success,message,queue:{...}}
//	SNAPSHOT-DELETE  DELETE /storage/volume/{id}/snapshot/{snapId}   → {success,message,queue:{...}}
//
// Async behaviour (controller-verified):
//   - Volume CREATE is ASYNC: the row is recorded synchronously with status="pending"
//     and deployed=0, the slave creates the backing volume, then reports back flipping
//     status→"available" (deployed=1) or "failed". There is NO task_id in the create
//     response (the controller returns {success,volume}); the async signal is the
//     volume's own "status" field, polled via the SHOW endpoint. GetVolume IS the poll.
//   - ATTACH/DETACH/RESIZE/DELETE are CONFIRMED ASYNCHRONOUS operations (Master
//     3.3.5): a 200 means the operation was recorded and sent to the node; the
//     volume's "active_operation" field names it while pending, and the
//     attachment, plan and size (or the volume itself, for delete) change only
//     when the node confirms. A node failure clears active_operation and keeps
//     the previous state. A definite refusal is a 422 with a typed "reason"
//     (e.g. volume_operation_refused, volume_attached) and changes nothing.
//     The methods below only SEND the request, in ONE attempt: a 429/5xx or a
//     transport error is returned without the client's usual replay, because
//     the first attempt may already have been recorded (and even completed or
//     failed on the node) and a replay would issue a second operation. Package
//     volumeop resolves such an unknown outcome by reading the volume back and
//     is what callers should use. Attach/detach on a
//     stopped instance is applied immediately (no node command), which the
//     same wait observes on its first read.
//   - SNAPSHOT CREATE/DELETE are ASYNC: they enqueue a backup-queue job and return the
//     QUEUE object, not the snapshot. The snapshot row starts status="pending" and is
//     embedded in the volume SHOW under snapshots[]; there is no individual snapshot
//     SHOW route. Snapshot convergence is observed by polling the parent volume SHOW
//     and scanning snapshots[] for the matching id reaching status="available".
//
// Other notes:
//   - RESIZE is PLAN-BASED: the request takes a target volume_plan_id (a larger plan),
//     NOT a free-form size_gb. The new plan implies the new size.
//   - All routes are gated behind the billing.enabled middleware. When billing is
//     disabled the API returns HTTP 403 {success:false,message:"This feature is
//     unavailable because billing is disabled."}; responseError maps 403 → *APIError.
//   - The store FormRequest enforces name|required; volume_plan_id|exists; the service
//     additionally enforces the per-account max_volumes quota (422 on breach).
//   - An empty-id guard is applied on every path-id argument (consistency).

// CreateVolume creates a block storage volume from the supplied prebuilt body
// (name + volume_plan_id + hypervisor_group_id required; project_id optional).
// Create is ASYNC: the returned object carries the id but status="pending"; the
// caller must poll GetVolume until status="available". The "volume" envelope is
// unwrapped.
func (c *Client) CreateVolume(ctx context.Context, body map[string]any) (map[string]any, error) {
	return c.doItem(ctx, "POST", "/storage/volumes", body, "volume")
}

// GetVolume fetches a single volume by UUID. The SHOW route is SINGULAR. A 404
// (absent or owned by a different account) is returned as an *APIError that
// IsNotFound recognises. The returned object includes the embedded snapshots[]
// and backups[] arrays from the SHOW payload, so snapshot convergence can be
// observed without an individual snapshot SHOW route.
func (c *Client) GetVolume(ctx context.Context, id string) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("GetVolume: empty id")
	}
	return c.doItem(ctx, "GET", "/storage/volume/"+url.PathEscape(id), nil, "volume")
}

// ListVolumes returns all volumes belonging to the authenticated account
// (paginator-aware via doList).
func (c *Client) ListVolumes(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/storage/volumes", nil)
}

// AttachVolume sends an attach request. body must contain "instance_id". The
// returned volume is the accepted state (active_operation "attach" while the
// node works); use volumeop.AttachVolume to wait for the confirmed attachment.
// A precondition failure (volume not available, different hypervisor group, no
// free device letter) or a node refusal surfaces as HTTP 422 with a reason.
func (c *Client) AttachVolume(ctx context.Context, id string, body map[string]any) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("AttachVolume: empty id")
	}
	return c.doItemOnce(ctx, "POST", "/storage/volume/"+url.PathEscape(id)+"/attach", body, "volume")
}

// DetachVolume sends a detach request (no body). Use volumeop.DetachVolume to
// wait for the confirmed detachment.
func (c *Client) DetachVolume(ctx context.Context, id string) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("DetachVolume: empty id")
	}
	return c.doItemOnce(ctx, "POST", "/storage/volume/"+url.PathEscape(id)+"/detach", nil, "volume")
}

// ResizeVolume sends a resize to a new (larger) plan. body must contain
// "volume_plan_id" (resize is plan-based, not a free-form size). The PATCH
// returns the top-level envelope {success, is_downgrade, volume:{...}}; the
// size and plan in it are still the OLD ones (they switch only on the node's
// confirmation), so use volumeop.ResizeVolume to wait. Shrinking and a
// cross-class/type or invalid-plan resize are refused with HTTP 422.
func (c *Client) ResizeVolume(ctx context.Context, id string, body map[string]any) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("ResizeVolume: empty id")
	}
	return c.doItemOnce(ctx, "PATCH", "/storage/volume/"+url.PathEscape(id)+"/resize", body, "")
}

// DeleteVolume sends a delete. The volume (and its billing) stays until the
// node confirms; the SHOW then 404s. An attached volume is refused with HTTP
// 422 reason volume_attached (detach first). Use volumeop.DeleteVolume to wait.
func (c *Client) DeleteVolume(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("DeleteVolume: empty id")
	}
	return c.doVoidOnce(ctx, "DELETE", "/storage/volume/"+url.PathEscape(id), nil)
}

// CreateVolumeSnapshot enqueues a point-in-time snapshot of the volume. body may
// carry an optional "name". The controller returns the backup-QUEUE object (not
// the snapshot itself) under the "queue" envelope; the snapshot row is created
// status="pending" and embedded in the parent volume SHOW under snapshots[]. The
// caller resolves the snapshot id by polling GetVolume and scanning snapshots[]
// for the newest entry reaching status="available".
//
// The create is sent in ONE attempt: a second snapshot would be a second
// operation, so a 429/5xx or transport error is returned without replay. The
// gate's transient refusals (409 volume_busy, 429 volume_cooldown with
// Retry-After, 429 account_inflight_limit) are definite and are waited out by
// volumeop.Admit, which callers should wrap this call in.
func (c *Client) CreateVolumeSnapshot(ctx context.Context, volumeID string, body map[string]any) (map[string]any, error) {
	if volumeID == "" {
		return nil, fmt.Errorf("CreateVolumeSnapshot: empty volumeID")
	}
	return c.doItemOnce(ctx, "POST", "/storage/volume/"+url.PathEscape(volumeID)+"/snapshot", body, "queue")
}

// DeleteVolumeSnapshot enqueues deletion of a snapshot. The DELETE route is
// nested under the parent volume: /storage/volume/{volumeId}/snapshot/{snapId}.
// The controller returns {success,message,queue}; the snapshot row flips to
// status="deleting" and is removed when the slave reports back. doVoid checks the
// success flag.
func (c *Client) DeleteVolumeSnapshot(ctx context.Context, volumeID, snapshotID string) error {
	if volumeID == "" {
		return fmt.Errorf("DeleteVolumeSnapshot: empty volumeID")
	}
	if snapshotID == "" {
		return fmt.Errorf("DeleteVolumeSnapshot: empty snapshotID")
	}
	path := "/storage/volume/" + url.PathEscape(volumeID) + "/snapshot/" + url.PathEscape(snapshotID)
	return c.doVoid(ctx, "DELETE", path, nil)
}

// GetVolumeSnapshot resolves a single snapshot by scanning the parent volume's
// embedded snapshots[] array (there is NO individual snapshot SHOW route - the
// SHOW endpoint embeds the snapshots). It returns the matching snapshot object
// or a 404-shaped *APIError (IsNotFound = true) when the id is absent (e.g. the
// snapshot was deleted, or its delete-queue finished). This doubles as the
// async poll source for snapshot create (scan for status="available") and the
// 404 signal for snapshot delete.
func (c *Client) GetVolumeSnapshot(ctx context.Context, volumeID, snapshotID string) (map[string]any, error) {
	if volumeID == "" {
		return nil, fmt.Errorf("GetVolumeSnapshot: empty volumeID")
	}
	if snapshotID == "" {
		return nil, fmt.Errorf("GetVolumeSnapshot: empty snapshotID")
	}
	vol, err := c.GetVolume(ctx, volumeID)
	if err != nil {
		// A 404 on the parent volume propagates (the snapshot is gone too).
		return nil, err
	}
	for _, s := range snapshotsOf(vol) {
		if id, ok := s["id"].(string); ok && id == snapshotID {
			return s, nil
		}
	}
	return nil, &APIError{Status: 404, Message: "volume snapshot not found"}
}

// FindVolumeSnapshotByName resolves a snapshot by NAME from the parent volume's
// embedded snapshots[] array. The snapshot CREATE endpoint returns the backup
// QUEUE (not the snapshot id), so the resource resolves the freshly-created
// snapshot by the unique name it supplied. Returns an error when more than one
// snapshot shares the given name (ambiguous - callers MUST use unique names per
// volume). Returns a 404-shaped *APIError when no snapshot with that name exists.
func (c *Client) FindVolumeSnapshotByName(ctx context.Context, volumeID, name string) (map[string]any, error) {
	if volumeID == "" {
		return nil, fmt.Errorf("FindVolumeSnapshotByName: empty volumeID")
	}
	if name == "" {
		return nil, fmt.Errorf("FindVolumeSnapshotByName: empty name")
	}
	vol, err := c.GetVolume(ctx, volumeID)
	if err != nil {
		return nil, err
	}
	var matches []map[string]any
	for _, s := range snapshotsOf(vol) {
		if n, ok := s["name"].(string); ok && n == name {
			matches = append(matches, s)
		}
	}
	switch len(matches) {
	case 0:
		return nil, &APIError{Status: 404, Message: "volume snapshot not found by name"}
	case 1:
		return matches[0], nil
	default:
		return nil, fmt.Errorf("multiple snapshots named %q on volume %s; use unique names", name, volumeID)
	}
}

// snapshotsOf extracts the embedded snapshots[] array from a volume SHOW object,
// coercing each element to a map. A missing/empty/malformed array yields nil.
func snapshotsOf(vol map[string]any) []map[string]any {
	raw, ok := vol["snapshots"].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, e := range raw {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}
