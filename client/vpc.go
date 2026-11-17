package client

import (
	"context"
	"fmt"
	"net/url"
)

// VPC endpoints (verified against the real controller, routes/user_api.php):
//
//	INDEX   GET    /vpcs        → raw Laravel paginator {data:[...]} (subnets eager-loaded)
//	CREATE  POST   /vpcs        body {name,cidr,hypervisor_group_id,description?}
//	                            → 200 {success,message,vpc:{...,id,vni_number}}
//	SHOW    GET    /vpc/{id}    (singular) → {success,vpc:{...,subnets:[...]}}; 404 when absent
//	DELETE  DELETE /vpc/{id}    (singular) → 200 {success}; 409 reason vpc_delete_pending
//	                            or vpc_delete_incomplete while the delete is still
//	                            finishing (repeat the DELETE); 200 success:false for
//	                            other refusals (e.g. a subnet in use)
//
// Notes:
//   - Create is SYNCHRONOUS and HTTP 200 (not 201). VpcService::store returns the
//     refreshed object - with its id and the appended vni_number - under key
//     "vpc". The id is read directly from the create response: there is NO task,
//     no waiter, and NO list-and-match-by-name read-back. (An earlier plan called
//     this the "create-without-ID readback" demo; the real controller does return
//     the id, so that read-back is intentionally NOT implemented.)
//   - Failure is signalled with success:false at HTTP 200 (invalid CIDR, location
//     not VPC-enabled, quota exceeded). doItem/doVoid surface this as an error.
//   - There is NO UPDATE route for VPC. VpcService::update exists but is unwired,
//     so every configurable attribute is immutable over this API - hence no
//     UpdateVPC method here.
//   - CreateVPC takes a prebuilt body so the resource controls which optional
//     fields are sent (description is omitted from the map when unset rather than
//     sent as null).

// Typed 409 reasons of the VPC lifecycle.
//
//   - ReasonVpcDeletePending: a DELETE of the VPC left it in status "deleting"
//     because a NAT gateway is still being removed on its node; the body lists
//     pending_nat_gateways. Repeating the same DELETE finishes it.
//   - ReasonVpcDeleteIncomplete: a step after the gateways failed; the VPC is in
//     status "deleting" and repeating the DELETE finishes it.
//   - ReasonVpcDeleting: a create that targets a VPC in status "deleting" is
//     refused. It is permanent for that VPC.
const (
	ReasonVpcDeletePending    = "vpc_delete_pending"
	ReasonVpcDeleteIncomplete = "vpc_delete_incomplete"
	ReasonVpcDeleting         = "vpc_deleting"
)

// ListVPCs returns all VPCs visible to the token (paginator-aware).
func (c *Client) ListVPCs(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/vpcs", nil)
}

// CreateVPC creates a VPC from the supplied body (name, cidr,
// hypervisor_group_id, and optionally description). The create is synchronous:
// the returned object already carries id and the appended vni_number.
func (c *Client) CreateVPC(ctx context.Context, body map[string]any) (map[string]any, error) {
	return c.doItem(ctx, "POST", "/vpcs", body, "vpc")
}

// GetVPC fetches a single VPC by id. The SHOW route is singular. A 404 is
// returned as an *APIError recognised by IsNotFound.
func (c *Client) GetVPC(ctx context.Context, id string) (map[string]any, error) {
	return c.doItem(ctx, "GET", "/vpc/"+url.PathEscape(id), nil, "vpc")
}

// DeleteVPC deletes a VPC by id. The DELETE route is singular. A failure is
// signalled with success:false at HTTP 200 (e.g. a subnet still has used IPs),
// so doVoid checks the flag.
func (c *Client) DeleteVPC(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("DeleteVPC: empty id")
	}
	return c.doVoid(ctx, "DELETE", "/vpc/"+url.PathEscape(id), nil)
}
