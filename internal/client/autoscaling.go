package client

import (
	"context"
	"fmt"
	"net/url"
)

// Autoscaling endpoints (verified against the real UserApi\AutoscalingController
// + AutoscalingService + StoreGroupRequest/StorePolicyRequest + the
// AutoscalingGroup/AutoscalingPolicy models + routes/user_api.php).
//
// There are TWO resources here: the GROUP (a fleet of identical instances kept
// between min/max) and the POLICY (a metric→scale rule, a CHILD of the group).
//
// GROUP routes (note the path asymmetry — the collection/create path is PLURAL
// but every item op is SINGULAR):
//
//	LIST    GET    /scaling-groups                  (PLURAL)
//	                → {success,scaling_groups:{current_page,data:[...],total,...}}
//	CREATE  POST   /scaling-groups                  (PLURAL)
//	                body {name (req), hypervisor_group_id (req,uuid),
//	                      plan_id (req,uuid), image_id (req,uuid),
//	                      vpc_id?/vpc_subnet_id? (paired),
//	                      load_balancer_id?/lb_backend_id? (paired),
//	                      min_instances? (default 1), max_instances? (default 5),
//	                      cloud_init?, ssh_keys? ([]uuid), security_group_ids? ([]uuid)}
//	                → {success,message,group:{id,...,status,current_count}}
//	SHOW    GET    /scaling-group/{id}              (SINGULAR)
//	                → {success,scaling_group:{...,policies:[...]},instances,activities,...}
//	UPDATE  PATCH  /scaling-group/{id}              (SINGULAR)
//	                body {name?, plan_id?, image_id?, min_instances?, max_instances?,
//	                      cloud_init?, security_group_ids?, load_balancer_id?, lb_backend_id?}
//	                → {success,message,group:{...}}
//	PAUSE   POST   /scaling-group/{id}/pause        → {success,message,group:{status:"paused"}}
//	RESUME  POST   /scaling-group/{id}/resume       → {success,message,group:{status:"active"}}
//	DELETE  DELETE /scaling-group/{id}              → {success,message} — ASYNC: marks
//	                paused immediately, dispatches DestroyGroup job to tear down
//	                member instances + the group row in the background.
//
// POLICY routes (a CHILD of the group; the group id is in the path; there is NO
// individual policy SHOW — policies are EMBEDDED in the group SHOW under
// scaling_group.policies[], so GetAutoscalingPolicy reads-by-scan):
//
//	CREATE  POST   /scaling-group/{groupId}/policy             → {success,message,policy:{id,...}}
//	UPDATE  PATCH  /scaling-group/{groupId}/policy/{policyId}  → {success,message,policy:{...}};
//	                403 success:false when the policy belongs to a different group
//	DELETE  DELETE /scaling-group/{groupId}/policy/{policyId}  → {success,message};
//	                403 success:false when the policy belongs to a different group
//
// Notes:
//   - Group CREATE is SYNCHRONOUS metadata-wise: the row is returned immediately
//     with its id and status. The initial min_instances ARE deployed inside the
//     request (scaleUpInstances runs inline), but the response does NOT carry a
//     task id or per-instance state to wait on; the only convergence signal is
//     current_count rising toward min_instances on subsequent SHOWs. The group is
//     usable as soon as the row exists.
//   - DELETE is ASYNC (background DestroyGroup job): the resource polls SHOW
//     until 404 to converge, like the instance resource.
//   - Billing/capacity gating is NOT a 403: an autoscaling-disabled hypervisor
//     group or a disabled plan returns 200 success:false (→ an error via C3).
//   - All write failures use success:false at HTTP 200 (C3) except the policy
//     wrong-group guard which is 403 success:false (still surfaced as an error).
//   - Each id is url.PathEscape'd into the path; empty-id guards mirror the other
//     child clients.

// CreateAutoscalingGroup creates a scaling group from the supplied body (name,
// hypervisor_group_id, plan_id, image_id required; the rest optional). The create
// is synchronous; the returned "group" object carries id + status. The collection
// path is PLURAL (/scaling-groups).
func (c *Client) CreateAutoscalingGroup(ctx context.Context, body map[string]any) (map[string]any, error) {
	return c.doItem(ctx, "POST", "/scaling-groups", body, "group")
}

// GetAutoscalingGroup fetches a single scaling group by id. The SHOW route is
// SINGULAR (/scaling-group/{id}) and wraps the object under "scaling_group" (note
// the different key vs the create "group" envelope). A 404 is recognised by
// IsNotFound.
func (c *Client) GetAutoscalingGroup(ctx context.Context, id string) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("GetAutoscalingGroup: empty id")
	}
	path := "/scaling-group/" + url.PathEscape(id)
	return c.doItem(ctx, "GET", path, nil, "scaling_group")
}

// UpdateAutoscalingGroup patches the mutable fields of a group (name, plan_id,
// image_id, min/max_instances, cloud_init, security_group_ids, lb ids). The
// response wraps the fresh group under "group".
func (c *Client) UpdateAutoscalingGroup(ctx context.Context, id string, fields map[string]any) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("UpdateAutoscalingGroup: empty id")
	}
	path := "/scaling-group/" + url.PathEscape(id)
	return c.doItem(ctx, "PATCH", path, fields, "group")
}

// PauseAutoscalingGroup pauses the group (the evaluator stops scaling it). The
// response wraps the fresh group (status:"paused") under "group".
func (c *Client) PauseAutoscalingGroup(ctx context.Context, id string) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("PauseAutoscalingGroup: empty id")
	}
	path := "/scaling-group/" + url.PathEscape(id) + "/pause"
	return c.doItem(ctx, "POST", path, nil, "group")
}

// ResumeAutoscalingGroup resumes a paused group and re-enforces min_instances.
// The response wraps the fresh group (status:"active") under "group".
func (c *Client) ResumeAutoscalingGroup(ctx context.Context, id string) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("ResumeAutoscalingGroup: empty id")
	}
	path := "/scaling-group/" + url.PathEscape(id) + "/resume"
	return c.doItem(ctx, "POST", path, nil, "group")
}

// DeleteAutoscalingGroup enqueues destruction of the group. The controller marks
// it paused and dispatches a background job to tear down member instances and the
// group row, so callers should poll GetAutoscalingGroup until it 404s to converge.
// doVoid checks the success flag.
func (c *Client) DeleteAutoscalingGroup(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("DeleteAutoscalingGroup: empty id")
	}
	path := "/scaling-group/" + url.PathEscape(id)
	return c.doVoid(ctx, "DELETE", path, nil)
}

// CreateAutoscalingPolicy adds a scaling policy to the given group (metric +
// scale_up_threshold + scale_down_threshold required; steps/cooldowns/windows
// optional). The "policy" envelope is unwrapped, returning the created policy
// WITH its id.
func (c *Client) CreateAutoscalingPolicy(ctx context.Context, groupID string, body map[string]any) (map[string]any, error) {
	if groupID == "" {
		return nil, fmt.Errorf("CreateAutoscalingPolicy: empty groupID")
	}
	path := "/scaling-group/" + url.PathEscape(groupID) + "/policy"
	return c.doItem(ctx, "POST", path, body, "policy")
}

// UpdateAutoscalingPolicy patches an existing policy under its group. The "policy"
// envelope is unwrapped. A policy that belongs to a different group returns HTTP
// 403 success:false → an error (surfaced by doItem).
func (c *Client) UpdateAutoscalingPolicy(ctx context.Context, groupID, policyID string, body map[string]any) (map[string]any, error) {
	if groupID == "" {
		return nil, fmt.Errorf("UpdateAutoscalingPolicy: empty groupID")
	}
	if policyID == "" {
		return nil, fmt.Errorf("UpdateAutoscalingPolicy: empty policyID")
	}
	path := "/scaling-group/" + url.PathEscape(groupID) + "/policy/" + url.PathEscape(policyID)
	return c.doItem(ctx, "PATCH", path, body, "policy")
}

// DeleteAutoscalingPolicy removes a policy from its group. doVoid checks the
// success flag; a wrong-group policy returns 403 success:false → an error.
func (c *Client) DeleteAutoscalingPolicy(ctx context.Context, groupID, policyID string) error {
	if groupID == "" {
		return fmt.Errorf("DeleteAutoscalingPolicy: empty groupID")
	}
	if policyID == "" {
		return fmt.Errorf("DeleteAutoscalingPolicy: empty policyID")
	}
	path := "/scaling-group/" + url.PathEscape(groupID) + "/policy/" + url.PathEscape(policyID)
	return c.doVoid(ctx, "DELETE", path, nil)
}

// GetAutoscalingPolicy resolves a single policy by scanning the parent group's
// embedded policies[] (there is NO individual policy SHOW route). It returns the
// matching policy object or a 404-shaped *APIError (IsNotFound) when the group is
// gone or the policy id is absent — a 404 on the group itself propagates so the
// resource can RemoveResource.
func (c *Client) GetAutoscalingPolicy(ctx context.Context, groupID, policyID string) (map[string]any, error) {
	if groupID == "" {
		return nil, fmt.Errorf("GetAutoscalingPolicy: empty groupID")
	}
	if policyID == "" {
		return nil, fmt.Errorf("GetAutoscalingPolicy: empty policyID")
	}
	group, err := c.GetAutoscalingGroup(ctx, groupID)
	if err != nil {
		return nil, err
	}
	for _, p := range autoscalingChildren(group, "policies") {
		if id, ok := p["id"].(string); ok && id == policyID {
			return p, nil
		}
	}
	return nil, &APIError{Status: 404, Message: "autoscaling policy not found"}
}

// autoscalingChildren extracts a top-level embedded array (e.g. "policies") from a
// scaling_group SHOW object, coercing each element to a map. A missing/empty/
// malformed array yields nil. (Mirrors lbChildren for the LB children.)
func autoscalingChildren(group map[string]any, key string) []map[string]any {
	raw, ok := group[key].([]any)
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
