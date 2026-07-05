package client

import (
	"context"
	"fmt"
	"net/url"
)

// VPC subnet endpoints (verified against the real controller, routes/user_api.php).
//
// vpc_subnet is a CHILD resource of vpc: the parent VPC id is part of every URL
// path. Note the path asymmetry the controller exposes - the collection/create
// path is PLURAL but the item paths are SINGULAR:
//
//	CREATE  POST   /vpc/{vpcId}/subnets       (PLURAL)  body {cidr (required IPv4),
//	                                           name?, type? public|private}
//	                                           → 200 {success,message,subnet:{...}}
//	SHOW    GET    /vpc/{vpcId}/subnet/{id}    (SINGULAR) → {success,subnet:{...,ips:[...]}};
//	                                           404 when absent / wrong vpc
//	UPDATE  PATCH  /vpc/{vpcId}/subnet/{id}    (SINGULAR) body {name} - only name is
//	                                           mutable → 200 {success,message,subnet}
//	DELETE  DELETE /vpc/{vpcId}/subnet/{id}    (SINGULAR) → 200 {success,message};
//	                                           200 success:false on failure (IP in use)
//
// Notes:
//   - Create is SYNCHRONOUS and HTTP 200 (not 201): the subnet ROW is returned
//     immediately with its id and the server-DERIVED gateway/netmask. The gateway
//     and netmask are computed server-side from the cidr, so they are NEVER sent
//     in the request body. IP generation (which populates used/free) is async on
//     a queue - there is NO status field to wait on, so there is no waiter.
//   - Failure is signalled with success:false at HTTP 200 (overlap, out-of-range,
//     IP-in-use on delete). doItem/doVoid surface this as an error.
//   - CreateVPCSubnet takes a prebuilt body so the resource controls which
//     optional fields are sent (name/type are omitted from the map when unset
//     rather than sent as null).
//   - Both vpcID and id are url.PathEscape'd into the path.

// CreateVPCSubnet creates a subnet under the given VPC from the supplied body
// (cidr required; name/type optional). The create is synchronous: the returned
// object carries id plus the server-derived gateway/netmask. The collection
// path is PLURAL (/subnets).
func (c *Client) CreateVPCSubnet(ctx context.Context, vpcID string, body map[string]any) (map[string]any, error) {
	if vpcID == "" {
		return nil, fmt.Errorf("CreateVPCSubnet: empty vpcID")
	}
	path := "/vpc/" + url.PathEscape(vpcID) + "/subnets"
	return c.doItem(ctx, "POST", path, body, "subnet")
}

// GetVPCSubnet fetches a single subnet by id under the given VPC. The SHOW route
// is SINGULAR (/subnet/{id}). A 404 (absent or belonging to a different VPC) is
// returned as an *APIError recognised by IsNotFound.
func (c *Client) GetVPCSubnet(ctx context.Context, vpcID, id string) (map[string]any, error) {
	path := "/vpc/" + url.PathEscape(vpcID) + "/subnet/" + url.PathEscape(id)
	return c.doItem(ctx, "GET", path, nil, "subnet")
}

// UpdateVPCSubnet patches the mutable fields of a subnet (only name is mutable;
// cidr/type/gateway are immutable). The UPDATE route is SINGULAR. The response
// is a fresh full subnet object the caller can rehydrate state from.
func (c *Client) UpdateVPCSubnet(ctx context.Context, vpcID, id string, fields map[string]any) (map[string]any, error) {
	path := "/vpc/" + url.PathEscape(vpcID) + "/subnet/" + url.PathEscape(id)
	return c.doItem(ctx, "PATCH", path, fields, "subnet")
}

// DeleteVPCSubnet deletes a subnet by id under the given VPC. The DELETE route
// is SINGULAR. A failure is signalled with success:false at HTTP 200 (e.g. an IP
// in the subnet is still in use), so doVoid checks the flag.
func (c *Client) DeleteVPCSubnet(ctx context.Context, vpcID, id string) error {
	if vpcID == "" {
		return fmt.Errorf("DeleteVPCSubnet: empty vpcID")
	}
	if id == "" {
		return fmt.Errorf("DeleteVPCSubnet: empty id")
	}
	path := "/vpc/" + url.PathEscape(vpcID) + "/subnet/" + url.PathEscape(id)
	return c.doVoid(ctx, "DELETE", path, nil)
}
