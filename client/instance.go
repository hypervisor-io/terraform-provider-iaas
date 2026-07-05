package client

import (
	"context"
	"fmt"
	"net/url"
)

// Instance endpoints (verified against the real controller, routes/user_api.php).
//
// The instance is the GOLDEN ASYNC resource and create is TWO-PHASE:
//
//	PHASE 1  CREATE  POST   /cloud-service/instances
//	                 body {location_id,plan_id,vpc_id?,vpc_subnet_id?,hostname?,static_ip_ids?}
//	                 → 200 {success,message,instance:{...full model incl id...}}
//	                 SYNCHRONOUS - records the row only (NO OS, NO task). Capture
//	                 instance.id from the response.
//
//	PHASE 2  DEPLOY  POST   /instance/{id}/deploy
//	                 body {image_id(REQUIRED),ssh_keys?,hostname?,timezone?,cloudcfg?}
//	                 → 200 {success,message,task_id:"<uuid>"}  (task_id is TOP-LEVEL,
//	                 no nested object). ASYNCHRONOUS - deploys the OS via a task.
//	                 NOTE: the ssh keys field is "ssh_keys" (array of ids), NOT
//	                 "ssh_key_id"; "password" is ignored (server force-generates).
//
//	TASK    POLL     GET    /instance/{id}/task/{taskId}
//	                 → {logs:[...],task:{...,status,progress}}; the create waiter
//	                 converges on task.status == "completed" (fail: "failed").
//
//	SHOW    GET    /instance/{id}              → BARE instance model (no envelope,
//	                 no success wrapper); 404 when absent/not-owned. Appended:
//	                 primary_public_ip{ip}, primary_private_ip, primary_vpc_ip,
//	                 task_running. vnc_password is CLEARTEXT here (Sensitive!).
//
//	UPDATE  PATCH  /instance/{id}              body {display_name?,hostname?,notes?,boot?}
//	                 → 200 {success,message,instance:{...}}. METADATA only, SYNC,
//	                 no task; plan/location/image/network are NOT mutable here.
//
//	DELETE  DELETE /cloud-service/instances/{id} → 200 {success}; the slave
//	                 finalizes asynchronously and the row soft-deletes later, so the
//	                 resource converges by polling SHOW until 404. A failure (e.g.
//	                 protection_enabled) is signalled with success:false at 200.
//
// Every write path returns HTTP 200 even on failure and branches on the "success"
// flag, so doItem/doVoid (which surface success:false as an error) are reused.

// ListCSInstances returns every instance visible to the token. The INDEX route
// GET /instances returns a raw Laravel paginator ({data:[...], current_page,
// last_page, ...}), so doList auto-paginates and accumulates all pages. There
// is no envelope key and no success flag on this read path.
//
// This read has no matching Terraform resource/data-source (the provider works
// per-id), so it exists purely for the MCP server's user.instance.list tool;
// it is additive and referenced by nothing else in the provider.
func (c *Client) ListCSInstances(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/instances", nil)
}

// CreateCSInstance performs PHASE 1: it records the instance row from the
// supplied body and returns the full instance object (with its id) under key
// "instance". This call is synchronous and does NOT deploy an OS - call
// DeployInstance next with the returned id.
func (c *Client) CreateCSInstance(ctx context.Context, body map[string]any) (map[string]any, error) {
	return c.doItem(ctx, "POST", "/cloud-service/instances", body, "instance")
}

// DeployInstance performs PHASE 2: it deploys the OS onto an already-created
// instance and returns the bare envelope carrying the top-level task_id. The
// caller reads obj["task_id"] and polls GetInstanceTask until the task completes.
//
// The envelope has no nested object, so key "" returns the top-level map.
func (c *Client) DeployInstance(ctx context.Context, id string, body map[string]any) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("DeployInstance: empty id")
	}
	return c.doItem(ctx, "POST", "/instance/"+url.PathEscape(id)+"/deploy", body, "")
}

// GetInstance fetches a single instance by id. SHOW returns the BARE instance
// model (no envelope), so key "" returns the top-level map unchanged. A 404 is
// returned as an *APIError recognised by IsNotFound (used by Read drift-handling
// and by the DELETE convergence waiter).
func (c *Client) GetInstance(ctx context.Context, id string) (map[string]any, error) {
	return c.doItem(ctx, "GET", "/instance/"+url.PathEscape(id), nil, "")
}

// GetInstanceTask fetches the deploy task for an instance and returns the
// unwrapped "task" object (carrying the authoritative status the create waiter
// converges on: "completed" = ready, "failed" = terminal).
func (c *Client) GetInstanceTask(ctx context.Context, instanceID, taskID string) (map[string]any, error) {
	path := "/instance/" + url.PathEscape(instanceID) + "/task/" + url.PathEscape(taskID)
	return c.doItem(ctx, "GET", path, nil, "task")
}

// UpdateInstance patches instance metadata (display_name, hostname, notes, boot).
// The PATCH is synchronous and the response wraps the refreshed object under key
// "instance". plan/location/image/network are immutable here.
func (c *Client) UpdateInstance(ctx context.Context, id string, fields map[string]any) (map[string]any, error) {
	return c.doItem(ctx, "PATCH", "/instance/"+url.PathEscape(id), fields, "instance")
}

// DeleteCSInstance enqueues deletion of an instance. The DELETE is asynchronous
// (the slave finalizes and the row soft-deletes later), so the resource converges
// by polling GetInstance until IsNotFound. A failure (e.g. protection_enabled) is
// signalled with success:false at HTTP 200, which doVoid surfaces as an error.
func (c *Client) DeleteCSInstance(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("DeleteCSInstance: empty id")
	}
	return c.doVoid(ctx, "DELETE", "/cloud-service/instances/"+url.PathEscape(id), nil)
}
