package client

import (
	"context"
	"fmt"
	"net/url"
)

// Security Group endpoints (verified against the real UserApi\SecurityGroupController
// + SecurityGroupService + routes/user_api.php). A security group is a named,
// user-owned (or global) collection of firewall rules; instances are attached to
// it many-to-many. The rules are managed as child rows under the parent group via
// per-rule add/remove endpoints, and instances are attached/detached via bulk
// instance-id endpoints. Both rules and the attached-instance set are embedded in
// the SHOW response so Read can hydrate them.
//
// Parent (security_group) CRUD - note the plural-vs-singular path asymmetry that
// mirrors ip_set:
//
//	INDEX   GET    /security-groups        (PLURAL)  → Laravel paginator {data:[...]}
//	                                        each row carries rules_count, instances_count
//	CREATE  POST   /security-groups        (PLURAL)  body {name (required),
//	                                        description?}
//	                                        → 200 {success,message,security_group:{id,name}}
//	SHOW    GET    /security-group/{id}     (SINGULAR) → 200 {success,
//	                                        security_group:{...,rules:[{id,...},...]},
//	                                        attached_instances:[{id,name,...}],
//	                                        all_security_groups,all_ip_sets,user_instances};
//	                                        rules are EMBEDDED on security_group;
//	                                        attached instances are a TOP-LEVEL envelope key
//	UPDATE  PATCH  /security-group/{id}     (SINGULAR) body {name (required),
//	                                        description?} → 200 {success,message}
//	                                        (NOTE: the UPDATE response carries NO
//	                                         security_group body → Read back after)
//	DELETE  DELETE /security-group/{id}     (SINGULAR) → 200 {success,message};
//	                                        200 success:false on a global SG (C3)
//
// Rule operations (children of a security_group):
//
//	ADD     POST   /security-group/{id}/rules   body {direction (ingress|egress),
//	                                        protocol (tcp|udp|icmp|icmpv6|all),
//	                                        ip_version (ipv4|ipv6), port_range_min?,
//	                                        port_range_max?, cidr?, remote_group_id?,
//	                                        ip_set_id?, description?}
//	                                        → 200 {success,message,rule:{id,...}}
//	                                        (cidr / remote_group_id / ip_set_id are
//	                                         mutually exclusive; ports required for
//	                                         tcp/udp). Single rule per call.
//	REMOVE  DELETE /security-group/{id}/rule/{ruleId}   (SINGULAR "rule")
//	                                        → 200 {success,message}
//
// Instance attachment (many-to-many; bulk by instance id):
//
//	ATTACH  POST   /security-group/{id}/attach-instances   body {instance_ids:[...]}
//	                                        → 200 {success,message}
//	DETACH  POST   /security-group/{id}/detach-instances   body {instance_ids:[...]}
//	                                        → 200 {success,message}
//	                                        (NOTE: detach is POST, not DELETE)
//
// Notes:
//   - All writes are SYNCHRONOUS at HTTP 200 (no task/state, no waiter). The
//     slave-side firewall convergence (SecurityGroupService::getAssignments /
//     getInstanceSgData) is a SLAVE-pulled sync, NOT a user-API "apply" call, so
//     no sync/apply request is needed after rule or attachment changes.
//   - Failure is signalled with success:false at HTTP 200 (duplicate rule,
//     invalid cidr, global SG on modify/delete, max-groups exceeded). doItem/
//     doVoid surface this (C3).
//   - Empty-id guards are applied on every path-id argument (consistency).
//   - Every id is url.PathEscape'd into the path.

// ListSecurityGroups returns all security groups visible to the authenticated
// user (own + global), paginator-aware.
func (c *Client) ListSecurityGroups(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/security-groups", nil)
}

// CreateSecurityGroup creates a security group from the supplied body (name
// required; description optional). The create is synchronous: the returned object
// carries the id. The collection path is PLURAL (/security-groups).
func (c *Client) CreateSecurityGroup(ctx context.Context, body map[string]any) (map[string]any, error) {
	return c.doItem(ctx, "POST", "/security-groups", body, "security_group")
}

// GetSecurityGroup fetches a single security group by id, unwrapping the
// "security_group" envelope. The returned object carries the EMBEDDED "rules"
// array (so Read hydrates the rule set). The SHOW route is SINGULAR
// (/security-group/{id}). A 404 (absent / belonging to another user) is an
// *APIError recognised by IsNotFound.
//
// NOTE: the attached-instance list lives at the TOP LEVEL of the SHOW envelope
// (key "attached_instances"), not inside the security_group object, so use
// GetSecurityGroupEnvelope when you need the attached instances too.
func (c *Client) GetSecurityGroup(ctx context.Context, id string) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("GetSecurityGroup: empty id")
	}
	path := "/security-group/" + url.PathEscape(id)
	return c.doItem(ctx, "GET", path, nil, "security_group")
}

// GetSecurityGroupEnvelope fetches a single security group by id and returns the
// ENTIRE SHOW envelope (key=""), so the caller can read both the nested
// "security_group" object (with embedded rules) AND the top-level
// "attached_instances" array. The resource Read uses this to rebuild both the
// rules set and the instance_ids set in one round-trip.
func (c *Client) GetSecurityGroupEnvelope(ctx context.Context, id string) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("GetSecurityGroupEnvelope: empty id")
	}
	path := "/security-group/" + url.PathEscape(id)
	// key="" → return the bare envelope (security_group + attached_instances live
	// side by side at the top level).
	return c.doItem(ctx, "GET", path, nil, "")
}

// UpdateSecurityGroup patches the mutable scalar fields of a security group (name
// required; description optional/nullable). The UPDATE route is SINGULAR. The
// PATCH response carries NO security_group body (only {success,message}), so the
// resource must call GetSecurityGroupEnvelope afterwards to refresh state - this
// method returns the bare envelope map for completeness but callers should not
// rely on it carrying the id.
func (c *Client) UpdateSecurityGroup(ctx context.Context, id string, fields map[string]any) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("UpdateSecurityGroup: empty id")
	}
	path := "/security-group/" + url.PathEscape(id)
	// key="" → return the bare envelope (no security_group wrapper on UPDATE).
	return c.doItem(ctx, "PATCH", path, fields, "")
}

// DeleteSecurityGroup deletes a security group by id (the SecurityGroupService
// detaches it from all instances and cascades its rules server-side). The DELETE
// route is SINGULAR. A failure is signalled with success:false at HTTP 200 (e.g.
// the group is global), so doVoid checks it.
func (c *Client) DeleteSecurityGroup(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("DeleteSecurityGroup: empty id")
	}
	path := "/security-group/" + url.PathEscape(id)
	return c.doVoid(ctx, "DELETE", path, nil)
}

// AddSecurityGroupRule adds a single firewall rule to a security group, sending
// the prebuilt rule body (direction, protocol, ip_version required; ports, cidr,
// remote_group_id, ip_set_id, description optional). The response is unwrapped
// from the "rule" envelope and carries the new rule's id (needed so the resource
// can later delete this exact rule). There is no bulk-add or rule-update
// endpoint, so add-and-remove is the only way to mutate the rule set.
func (c *Client) AddSecurityGroupRule(ctx context.Context, sgID string, body map[string]any) (map[string]any, error) {
	if sgID == "" {
		return nil, fmt.Errorf("AddSecurityGroupRule: empty sgID")
	}
	path := "/security-group/" + url.PathEscape(sgID) + "/rules"
	return c.doItem(ctx, "POST", path, body, "rule")
}

// DeleteSecurityGroupRule removes a single rule from a security group by rule id.
// The route is DELETE /security-group/{sgID}/rule/{ruleID} (singular "rule"). A
// failure is signalled with success:false at HTTP 200, so doVoid checks the flag.
func (c *Client) DeleteSecurityGroupRule(ctx context.Context, sgID, ruleID string) error {
	if sgID == "" {
		return fmt.Errorf("DeleteSecurityGroupRule: empty sgID")
	}
	if ruleID == "" {
		return fmt.Errorf("DeleteSecurityGroupRule: empty ruleID")
	}
	path := "/security-group/" + url.PathEscape(sgID) + "/rule/" + url.PathEscape(ruleID)
	return c.doVoid(ctx, "DELETE", path, nil)
}

// AttachSecurityGroupInstances attaches the security group to one or more
// instances. The route is POST /security-group/{id}/attach-instances with body
// {instance_ids:[...]}. instanceIDs must be non-empty (the controller validates
// required|array). A failure (e.g. max-groups exceeded, instance not owned) is
// signalled with success:false at HTTP 200, so doVoid checks it.
func (c *Client) AttachSecurityGroupInstances(ctx context.Context, sgID string, instanceIDs []string) error {
	if sgID == "" {
		return fmt.Errorf("AttachSecurityGroupInstances: empty sgID")
	}
	path := "/security-group/" + url.PathEscape(sgID) + "/attach-instances"
	body := map[string]any{"instance_ids": instanceIDs}
	return c.doVoid(ctx, "POST", path, body)
}

// DetachSecurityGroupInstances detaches the security group from one or more
// instances. The route is POST /security-group/{id}/detach-instances (POST, NOT
// DELETE) with body {instance_ids:[...]}. instanceIDs must be non-empty. A
// failure is signalled with success:false at HTTP 200, so doVoid checks it.
func (c *Client) DetachSecurityGroupInstances(ctx context.Context, sgID string, instanceIDs []string) error {
	if sgID == "" {
		return fmt.Errorf("DetachSecurityGroupInstances: empty sgID")
	}
	path := "/security-group/" + url.PathEscape(sgID) + "/detach-instances"
	body := map[string]any{"instance_ids": instanceIDs}
	return c.doVoid(ctx, "POST", path, body)
}
