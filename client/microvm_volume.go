package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// Persistent data volumes for sandboxes (user_api, bearer token).
//
//	CREATE  POST   /storage/volumes                    body {for:"sandbox", name, size_gib, location_id,
//	                                                    delete_on_sandbox_delete?} -> 201 {success,volume}
//	SHOW    GET    /storage/volume/{id}                -> {volume:{...}}
//	DELETE  DELETE /storage/volume/{id}                -> 202 {success,volume:{status:"deleting"}}
//	LIST    GET    /microvm/vm/{id}/volumes            -> {success,volumes:[...]}
//	ATTACH  POST   /microvm/vm/{id}/volumes            body {volume_id, mount_path?, read_only?, noexec?,
//	                                                    delete_on_sandbox_delete?} (sandbox STOPPED) -> {success,volume}
//	DETACH  DELETE /microvm/vm/{id}/volume/{volume_id} (sandbox STOPPED) -> {success,volume}
//
// A sandbox volume object: id, name, for ("sandbox"), size_gib, status (pending|available|attaching|attached|
// detaching|deleting|error), location_id, sandbox_id and sandbox_name (null while free), mount_path, slot, read_only,
// noexec, delete_on_sandbox_delete, node_pinned, failure_reason, created_at. Refusals are status codes with a "reason":
// 404 volume_not_found / sandbox_not_found; 409 volume_in_use, sandbox_state_invalid, volume_node_mismatch,
// volume_slots_exhausted, mount_path_conflict, volume_busy, volume_not_attached, volume_node_unavailable,
// node_volumes_unsupported, no_sandbox_capacity, volume_limit; 422 mount_path_invalid, volume_size_invalid.

func microvmVolumes(sandboxID string) string { return microvm(sandboxID) + "/volumes" }

// CreateSandboxVolume creates a volume for sandboxes. body must carry for="sandbox". It is sent once (a volume is a
// billed object, a replay would make a second one); the answer is the volume, status "available".
func (c *Client) CreateSandboxVolume(ctx context.Context, body map[string]any) (map[string]any, error) {
	if body == nil {
		return nil, fmt.Errorf("CreateSandboxVolume: empty body")
	}
	return c.doItemOnce(ctx, http.MethodPost, "/storage/volumes", body, "volume")
}

// DeleteSandboxVolume deletes a sandbox volume. The answer is 202 (deleting); a held volume is 409 volume_in_use.
// A 404 may be treated as already gone (IsNotFound).
func (c *Client) DeleteSandboxVolume(ctx context.Context, id string) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("DeleteSandboxVolume: empty id")
	}
	return c.doItemOnce(ctx, http.MethodDelete, "/storage/volume/"+url.PathEscape(id), nil, "volume")
}

// ListMicrovmVolumes returns the volumes a sandbox holds, in slot order.
func (c *Client) ListMicrovmVolumes(ctx context.Context, sandboxID string) ([]map[string]any, error) {
	if sandboxID == "" {
		return nil, fmt.Errorf("ListMicrovmVolumes: empty sandbox id")
	}
	envelope, err := c.doItem(ctx, http.MethodGet, microvmVolumes(sandboxID), nil, "")
	if err != nil {
		return nil, err
	}
	return objSlice(envelope["volumes"]), nil
}

// AttachMicrovmVolume attaches a volume to a STOPPED sandbox. idemKey is sent as the Idempotency-Key header (a fresh
// key when empty).
func (c *Client) AttachMicrovmVolume(ctx context.Context, sandboxID string, body map[string]any, idemKey string) (map[string]any, error) {
	if sandboxID == "" {
		return nil, fmt.Errorf("AttachMicrovmVolume: empty sandbox id")
	}
	return c.idemItem(ctx, http.MethodPost, microvmVolumes(sandboxID), body, "volume", idemKey)
}

// DetachMicrovmVolume detaches a volume from a STOPPED sandbox. Repeating it after it succeeded succeeds again.
func (c *Client) DetachMicrovmVolume(ctx context.Context, sandboxID, volumeID string) (map[string]any, error) {
	if sandboxID == "" || volumeID == "" {
		return nil, fmt.Errorf("DetachMicrovmVolume: empty id")
	}
	return c.doItem(ctx, http.MethodDelete, microvm(sandboxID)+"/volume/"+url.PathEscape(volumeID), nil, "volume")
}

// ListSandboxVolumes returns the account's sandbox volumes (GET /storage/volumes?for=sandbox, all pages). status, when
// not empty, is a client-side filter on the volume status.
func (c *Client) ListSandboxVolumes(ctx context.Context, status string) ([]map[string]any, error) {
	rows, err := c.doList(ctx, http.MethodGet, "/storage/volumes?for=sandbox", nil)
	if err != nil {
		return nil, err
	}
	if status == "" {
		return rows, nil
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		if s, _ := row["status"].(string); s == status {
			out = append(out, row)
		}
	}
	return out, nil
}
