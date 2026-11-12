package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// Sandbox disk snapshots (user_api, bearer token).
//
//	CREATE  POST   /microvm/vm/{id}/snapshots  body {name, kind?, consistency?, description?}
//	                                           -> 202 {success,snapshot:{...}} (status "creating")
//	INDEX   GET    /microvm/snapshots (?sandbox_id=&status=&kind=&per_page=)
//	                                           -> {success,snapshots:{data:[...],last_page,...}}
//	SHOW    GET    /microvm/snapshot/{id}      -> {success,snapshot:{...}}
//	DELETE  DELETE /microvm/snapshot/{id}      -> 202 {success,snapshot:{id,name,status:"deleting"}}
//
// A snapshot is taken ASYNCHRONOUSLY: the create answer is 202 with status
// creating, and the snapshot turns ready (or failed) within a minute or so, so a
// caller that needs the snapshot must poll GetMicrovmSnapshot (the
// iaas_microvm_snapshot resource does). The object: id, name, description,
// kind, status (creating|ready|failed|deleting|unavailable), source_sandbox_id
// (null once the sandbox is deleted), source_state, consistency
// (quiesced|crash|clean), size_bytes, used_bytes, disk_gib, vcpu, mem_mib,
// locality, location_id, image_id, capture_method (reflink|paused_copy|
// offline_copy), paused_ms, billable_gib, price_gib_hour, failure_reason,
// created_at, ready_at. The create call first asks the location, quickly, whether it would refuse: a refusal it can
// name is answered AT ONCE as a typed 4xx (snapshot_requires_pause with pause_estimate_ms and pause_limit_ms in the
// body when known, quiesce_unavailable, node_insufficient_disk ...) with nothing created, so the same call, even with
// the same Idempotency-Key, can succeed after the sandbox is paused. The 202 may carry pause_estimate_ms and
// pause_limit_ms beside "snapshot" (not returned by CreateMicrovmSnapshot). When the check cannot answer, the same reasons
// arrive later as a snapshot with status failed, failure_reason and failure_detail. Refusals are status codes with a "reason": 404
// sandbox_not_found / snapshot_not_found, 402 insufficient_credit, 409
// sandbox_state_invalid, snapshot_in_progress, snapshot_limit,
// quiesce_unavailable, node_insufficient_disk, node_snapshots_unsupported,
// snapshot_requires_pause (with pause_estimate_ms and pause_limit_ms when the
// location reports them), snapshot_pause_exceeded, snapshot_in_use; 422
// name_taken, snapshot_kind_unsupported; 429 rate_limited.

func microvmSnapshot(id string) string { return "/microvm/snapshot/" + url.PathEscape(id) }

// CreateMicrovmSnapshot asks for a disk snapshot of the sandbox and returns the
// snapshot object as accepted (status "creating"). idemKey is sent as the
// Idempotency-Key header (a fresh key when empty), so a retried request after a
// lost answer does not take a second snapshot.
func (c *Client) CreateMicrovmSnapshot(ctx context.Context, sandboxID string, body map[string]any, idemKey string) (map[string]any, error) {
	if sandboxID == "" {
		return nil, fmt.Errorf("CreateMicrovmSnapshot: empty sandbox id")
	}
	return c.idemItem(ctx, http.MethodPost, microvm(sandboxID)+"/snapshots", body, "snapshot", idemKey)
}

// GetMicrovmSnapshot returns one snapshot. A missing, foreign or legacy id is a
// 404 *APIError (client.IsNotFound).
func (c *Client) GetMicrovmSnapshot(ctx context.Context, id string) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("GetMicrovmSnapshot: empty id")
	}
	return c.doItem(ctx, http.MethodGet, microvmSnapshot(id), nil, "snapshot")
}

// ListMicrovmSnapshots returns the account's snapshots across all pages,
// newest first. Empty filters are omitted from the query.
func (c *Client) ListMicrovmSnapshots(ctx context.Context, sandboxID, status, kind string) ([]map[string]any, error) {
	q := url.Values{}
	if sandboxID != "" {
		q.Set("sandbox_id", sandboxID)
	}
	if status != "" {
		q.Set("status", status)
	}
	if kind != "" {
		q.Set("kind", kind)
	}
	path := "/microvm/snapshots"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	return c.listNamedPaginator(ctx, path, "snapshots")
}

// DeleteMicrovmSnapshot deletes a snapshot. The answer is 202 (deleting): the
// snapshot disappears once its location confirmed. A 404 is returned as an
// error the caller may treat as already gone (client.IsNotFound).
func (c *Client) DeleteMicrovmSnapshot(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("DeleteMicrovmSnapshot: empty id")
	}
	_, err := c.doItem(ctx, http.MethodDelete, microvmSnapshot(id), nil, "snapshot")
	return err
}
