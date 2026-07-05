package client

import (
	"context"
	"fmt"
	"net/url"
)

// Instance VPC attachment endpoints (verified against the real
// UserApi\InstanceVpcController + InstanceService + routes/user_api.php,
// Gap G3). An instance can be attached to AT MOST ONE VPC + subnet at a time —
// the `instances` table carries a single nullable vpc_id/vpc_subnet_id pair,
// and InstanceService::createVpcInterface throws "VPC interface already
// exists for this instance" if a type=vpc instance_interface row is already
// present. The attachment itself has NO dedicated id; it is identified
// entirely by instance_id (see instance_vpc_attachment.go).
//
//	ENABLE  POST   /instance/{instanceId}/vpc/enable
//	                body {vpc_id (required uuid), vpc_subnet_id (required uuid)}
//	                → 200 {success,message}. NO nested object is returned, and
//	                NO task_id is ever surfaced even when a Task is enqueued
//	                (only for a currently-running instance assigned to a
//	                hypervisor — the hot-attach to the live libvirt domain is
//	                fire-and-forget from this API's perspective). Auto-assigns
//	                the LOWEST free ip in the subnet and marks it primary —
//	                there is no way to choose or omit the initial ip. All DB
//	                writes (instances.vpc_id/vpc_subnet_id, the new
//	                vpc_subnet_ips row, the type=vpc instance_interface row)
//	                are committed before the response returns, so a subsequent
//	                GET .../vpc/ips immediately reflects them — this is what
//	                makes the DB-level attachment state safe to treat as
//	                synchronous (no waiter is used by the resource).
//	DISABLE POST   /instance/{instanceId}/vpc/disable   (no request body)
//	                → 200 {success,message}. Releases every attached
//	                vpc_subnet_ips row (instance_id=null, status=free,
//	                is_primary=false), deletes the type=vpc interface row, and
//	                nulls instances.vpc_id/vpc_subnet_id — synchronously, same
//	                async hot-detach caveat as enable.
//	ADD IP  POST   /instance/{instanceId}/vpc/ip/add
//	                body {ip_id (uuid of an existing FREE vpc_subnet_ips row in
//	                the instance's currently attached subnet) | random (bool)}
//	                → 200 {success,message,vpc_ip:{id,vpc_subnet_id,
//	                instance_id,ip,mac,is_primary,status,...,
//	                subnet:{...,vpc:{...}}}}.
//	                IMPORTANT: there is no free-form "ip" (dotted-quad) field —
//	                a caller can only reference a pre-existing FREE pool row by
//	                its uuid (obtained from ListInstanceAvailableVpcIPs), or ask
//	                for `random`. A newly added ip is never automatically
//	                primary.
//	SET PRIMARY  POST /instance/{instanceId}/vpc/ip/{vpcIpId}/primary (no body)
//	                → 200 {success,message}. Clears is_primary on every other
//	                attached ip first, then sets this one — pure metadata, no
//	                task/slave command.
//	REMOVE IP    DELETE /instance/{instanceId}/vpc/ip/{vpcIpId}
//	                → 200 {success,message}. GUARD: fails with success:false
//	                ("Cannot remove the last VPC IP. Disable VPC instead.")
//	                when it is the instance's only attached ip. The resource
//	                never targets its bookkeeping "auto_assigned_ip" for
//	                removal (see instance_vpc_attachment.go), so in practice
//	                this guard is only ever hit by a misconfigured
//	                additional_ips entry that names the auto-assigned address.
//	LIST IPS     GET  /instance/{instanceId}/vpc/ips
//	                → Laravel paginator {data:[...ip rows...]}. THIS is the
//	                read-back of current attachment state: an empty result
//	                (total 0) means no VPC is attached (the resource's Read
//	                treats this as drift and removes the resource from state).
//	                Each row carries vpc_subnet_id directly; the parent vpc's
//	                id is nested at row.subnet.vpc_id. doList auto-paginates
//	                the (fixed page size 10) envelope.
//	AVAILABLE IPS GET /instance/{instanceId}/vpc/available-ips
//	                → bare array [{id,ip}, ...] (only free ips in the
//	                instance's CURRENTLY attached subnet; capped at 50,
//	                lowest-first) OR {success:false,message} when no VPC is
//	                attached yet. Used to resolve a desired dotted-quad address
//	                (from additional_ips) to the vpc_subnet_ips row id that
//	                ip/add requires.
//
// Every mutating endpoint returns HTTP 200 even on business-logic failure,
// signalling it only via success:false — doItem/doVoid (which already check
// that flag) are reused throughout; none of these need doItemWithHeaders
// (no idempotency.user middleware is applied to this controller's routes).

// EnableInstanceVpc attaches instanceID to vpcID/vpcSubnetID. Synchronous at
// the DB level (see the ENABLE note above); the response carries no nested
// object, so key "" returns the bare {success,message} envelope.
func (c *Client) EnableInstanceVpc(ctx context.Context, instanceID, vpcID, vpcSubnetID string) (map[string]any, error) {
	if instanceID == "" {
		return nil, fmt.Errorf("EnableInstanceVpc: empty instance id")
	}
	body := map[string]any{
		"vpc_id":        vpcID,
		"vpc_subnet_id": vpcSubnetID,
	}
	path := "/instance/" + url.PathEscape(instanceID) + "/vpc/enable"
	return c.doItem(ctx, "POST", path, body, "")
}

// DisableInstanceVpc detaches any VPC from instanceID: releases every
// attached ip, deletes the vpc interface, and clears the instance's
// vpc_id/vpc_subnet_id. No request body.
func (c *Client) DisableInstanceVpc(ctx context.Context, instanceID string) error {
	if instanceID == "" {
		return fmt.Errorf("DisableInstanceVpc: empty instance id")
	}
	path := "/instance/" + url.PathEscape(instanceID) + "/vpc/disable"
	return c.doVoid(ctx, "POST", path, nil)
}

// ListInstanceVpcIPs returns every vpc_subnet_ips row currently attached to
// instanceID — the read-back of attachment state. An empty slice means no
// VPC is attached. doList auto-paginates the Laravel paginator envelope
// (fixed page size 10 server-side).
func (c *Client) ListInstanceVpcIPs(ctx context.Context, instanceID string) ([]map[string]any, error) {
	if instanceID == "" {
		return nil, fmt.Errorf("ListInstanceVpcIPs: empty instance id")
	}
	path := "/instance/" + url.PathEscape(instanceID) + "/vpc/ips"
	return c.doList(ctx, "GET", path, nil)
}

// ListInstanceAvailableVpcIPs returns the free ip pool rows ({id,ip}) in the
// instance's currently attached subnet. doList handles both response shapes
// transparently: a bare JSON array on success, or a {success:false,message}
// object (surfaced as an error) when no VPC is attached yet.
func (c *Client) ListInstanceAvailableVpcIPs(ctx context.Context, instanceID string) ([]map[string]any, error) {
	if instanceID == "" {
		return nil, fmt.Errorf("ListInstanceAvailableVpcIPs: empty instance id")
	}
	path := "/instance/" + url.PathEscape(instanceID) + "/vpc/available-ips"
	return c.doList(ctx, "GET", path, nil)
}

// AddInstanceVpcIP attaches one additional ip to instanceID's currently
// attached subnet. body must carry either {"ip_id": "<uuid>"} (a specific
// free pool row, resolved via ListInstanceAvailableVpcIPs) or
// {"random": true}. The created ip row is returned under key "vpc_ip".
func (c *Client) AddInstanceVpcIP(ctx context.Context, instanceID string, body map[string]any) (map[string]any, error) {
	if instanceID == "" {
		return nil, fmt.Errorf("AddInstanceVpcIP: empty instance id")
	}
	path := "/instance/" + url.PathEscape(instanceID) + "/vpc/ip/add"
	return c.doItem(ctx, "POST", path, body, "vpc_ip")
}

// SetPrimaryInstanceVpcIP marks vpcIPID as the instance's primary vpc ip,
// clearing the flag on every other attached ip first. No request body.
func (c *Client) SetPrimaryInstanceVpcIP(ctx context.Context, instanceID, vpcIPID string) error {
	if instanceID == "" {
		return fmt.Errorf("SetPrimaryInstanceVpcIP: empty instance id")
	}
	if vpcIPID == "" {
		return fmt.Errorf("SetPrimaryInstanceVpcIP: empty vpc ip id")
	}
	path := "/instance/" + url.PathEscape(instanceID) + "/vpc/ip/" + url.PathEscape(vpcIPID) + "/primary"
	return c.doVoid(ctx, "POST", path, nil)
}

// RemoveInstanceVpcIP detaches a single ip from instanceID. The API refuses
// (success:false) to remove an instance's LAST attached ip; callers in that
// situation should call DisableInstanceVpc instead.
func (c *Client) RemoveInstanceVpcIP(ctx context.Context, instanceID, vpcIPID string) error {
	if instanceID == "" {
		return fmt.Errorf("RemoveInstanceVpcIP: empty instance id")
	}
	if vpcIPID == "" {
		return fmt.Errorf("RemoveInstanceVpcIP: empty vpc ip id")
	}
	path := "/instance/" + url.PathEscape(instanceID) + "/vpc/ip/" + url.PathEscape(vpcIPID)
	return c.doVoid(ctx, "DELETE", path, nil)
}
