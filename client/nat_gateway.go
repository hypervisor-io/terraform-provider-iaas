package client

import (
	"context"
	"fmt"
	"net/url"
)

// VPC NAT Gateway endpoints (verified against the real UserApi
// VpcNatGatewayController + routes/user_api.php). A NAT gateway is a CHILD of a
// VPC: the parent VPC id is part of every request path. Each VPC can have AT
// MOST ONE NAT gateway. The gateway provides outbound internet access for the
// VPC's PRIVATE subnets; a public IP is auto-assigned by the controller at
// create time (the customer does NOT supply one). Private subnets are attached
// to / detached from the gateway individually via per-subnet endpoints, and the
// attached subnets are embedded in the gateway SHOW.
//
// Routes (all NESTED under /vpc/{vpcId}, the parent in the path):
//
//	INDEX   GET    /vpc/{vpcId}/nat-gateway              → {success,vpc,gateway|null}
//	                                                      (gateway is a SINGLE object
//	                                                       or null - one per VPC)
//	CREATE  POST   /vpc/{vpcId}/nat-gateway              body {name?, nat_enabled?,
//	                                                      subnet_ids?:[uuid]}
//	                                                      → {success,message,gateway:{id,
//	                                                      status:"pending",...}}
//	SHOW    GET    /vpc/{vpcId}/nat-gateway/{id}         → {success,gateway:{...,
//	                                                      subnets:[...],public_ip:{ip}}}
//	UPDATE  PATCH  /vpc/{vpcId}/nat-gateway/{id}         body {name?, nat_enabled?}
//	                                                      → {success,message,gateway}
//	DELETE  DELETE /vpc/{vpcId}/nat-gateway/{id}         → {success,message}
//	ENABLE  POST   /vpc/{vpcId}/nat-gateway/{id}/enable  → {success,message,gateway}
//	DISABLE POST   /vpc/{vpcId}/nat-gateway/{id}/disable → {success,message,gateway}
//	ATTACH  POST   /vpc/{vpcId}/nat-gateway/{id}/subnet  body {subnet_id (req)}
//	                                                      → {success,message,gateway}
//	DETACH  DELETE /vpc/{vpcId}/nat-gateway/{id}/subnet/{subnetId}
//	                                                      → {success,message,gateway}
//
// Async behaviour (controller-verified):
//   - CREATE is ASYNC: the row is recorded synchronously with status="pending"
//     (and a public IP is allocated + subnets attached in the same DB
//     transaction). The slave then provisions the gateway and reports back via
//     the slave VPC status-report endpoint, which flips status→"active" once the
//     infrastructure is healthy. There is NO task_id in the create response (the
//     controller returns {success,message,gateway}); the async signal is the
//     gateway's own "status" field, polled via the SHOW endpoint. GetNatGateway
//     IS the poll. The controller never sets a "failed" terminal status for a
//     NAT gateway (only pending → active, and deleting on teardown), so the
//     waiter's ready value is "active" and the fail set is defensive only.
//   - UPDATE / ENABLE / DISABLE / ATTACH / DETACH are SYNCHRONOUS from the API's
//     perspective: the controller mutates the row in place (name / nat_enabled,
//     or the attached-subnet pivot) and returns the fresh gateway immediately;
//     the slave-side NAT reconfiguration is a slave-pulled sync, not waited on.
//   - DELETE dispatches a slave teardown task, flips status→"deleting", releases
//     the public IP, detaches all subnets, and SOFT-DELETES the row immediately,
//     so a subsequent SHOW 404s right away - no delete waiter is required.
//
// Feature gating & errors (controller-verified):
//   - These routes are NOT wrapped in the billing.enabled middleware (unlike
//     volume / static_ip), so there is NO HTTP 403 billing gate. Instead the
//     controller gates the feature in-line and returns HTTP 200 success:false
//     when: the location's hypervisor group does not have natgw_enabled, the
//     per-account max_nat_gateways quota is reached, the VPC already has a NAT
//     gateway, the VPC has no private subnets, or no public IP is available.
//     doItem/doVoid surface all of these (C3: 200+success:false → error).
//   - Empty-id guards are applied on every path-id argument (consistency).
//   - Every id is url.PathEscape'd into the path.

// natGatewayBasePath builds the collection path "/vpc/{vpcId}/nat-gateway" with
// the parent VPC id escaped into it.
func natGatewayBasePath(vpcID string) string {
	return "/vpc/" + url.PathEscape(vpcID) + "/nat-gateway"
}

// CreateNatGateway creates the VPC's NAT gateway from the supplied prebuilt body
// (all fields optional: name, nat_enabled, subnet_ids). Create is ASYNC: the
// returned gateway carries the id but status="pending"; the caller must poll
// GetNatGateway until status="active". The "gateway" envelope is unwrapped. A
// feature-gate failure (natgw not available, quota reached, already exists, no
// private subnets, no public IP) surfaces as success:false at HTTP 200 → error.
func (c *Client) CreateNatGateway(ctx context.Context, vpcID string, body map[string]any) (map[string]any, error) {
	if vpcID == "" {
		return nil, fmt.Errorf("CreateNatGateway: empty vpcID")
	}
	return c.doItem(ctx, "POST", natGatewayBasePath(vpcID), body, "gateway")
}

// GetNatGateway fetches the NAT gateway by its id within the parent VPC,
// unwrapping the "gateway" envelope. The returned object carries the embedded
// "subnets" array (the attached private subnets, each with an id) and the
// "public_ip" object (with its "ip"). A 404 (absent, wrong VPC, or owned by a
// different account) is an *APIError recognised by IsNotFound. GetNatGateway is
// the async poll source for create (scan "status" for "active").
func (c *Client) GetNatGateway(ctx context.Context, vpcID, id string) (map[string]any, error) {
	if vpcID == "" {
		return nil, fmt.Errorf("GetNatGateway: empty vpcID")
	}
	if id == "" {
		return nil, fmt.Errorf("GetNatGateway: empty id")
	}
	return c.doItem(ctx, "GET", natGatewayBasePath(vpcID)+"/"+url.PathEscape(id), nil, "gateway")
}

// GetVpcNatGateway fetches the VPC's single NAT gateway via the INDEX endpoint,
// which returns {success,vpc,gateway|null}. It returns a 404-shaped *APIError
// (IsNotFound = true) when the VPC has no gateway (gateway:null). This is handy
// for callers that know only the VPC id (the INDEX is the only id-less lookup),
// but the per-id GetNatGateway is preferred once the gateway id is known.
func (c *Client) GetVpcNatGateway(ctx context.Context, vpcID string) (map[string]any, error) {
	if vpcID == "" {
		return nil, fmt.Errorf("GetVpcNatGateway: empty vpcID")
	}
	env, err := c.doItem(ctx, "GET", natGatewayBasePath(vpcID), nil, "")
	if err != nil {
		return nil, err
	}
	gw, ok := env["gateway"].(map[string]any)
	if !ok || gw == nil {
		return nil, &APIError{Status: 404, Message: "VPC has no NAT gateway"}
	}
	return gw, nil
}

// UpdateNatGateway patches the mutable scalar fields of the gateway (name and/or
// nat_enabled). The PATCH returns the fresh gateway under the "gateway" envelope.
func (c *Client) UpdateNatGateway(ctx context.Context, vpcID, id string, fields map[string]any) (map[string]any, error) {
	if vpcID == "" {
		return nil, fmt.Errorf("UpdateNatGateway: empty vpcID")
	}
	if id == "" {
		return nil, fmt.Errorf("UpdateNatGateway: empty id")
	}
	return c.doItem(ctx, "PATCH", natGatewayBasePath(vpcID)+"/"+url.PathEscape(id), fields, "gateway")
}

// DeleteNatGateway deletes the NAT gateway: the controller dispatches a slave
// teardown task, releases the public IP, detaches all subnets, and soft-deletes
// the row immediately, so a subsequent SHOW 404s right away - no delete waiter
// is required. A failure is signalled with success:false at HTTP 200, so doVoid
// checks the flag.
func (c *Client) DeleteNatGateway(ctx context.Context, vpcID, id string) error {
	if vpcID == "" {
		return fmt.Errorf("DeleteNatGateway: empty vpcID")
	}
	if id == "" {
		return fmt.Errorf("DeleteNatGateway: empty id")
	}
	return c.doVoid(ctx, "DELETE", natGatewayBasePath(vpcID)+"/"+url.PathEscape(id), nil)
}

// EnableNatGateway enables NAT functionality on the gateway (sets nat_enabled=1).
// It is blocked (success:false at HTTP 200) when the gateway is
// bandwidth-suspended. Returns the fresh gateway under the "gateway" envelope.
func (c *Client) EnableNatGateway(ctx context.Context, vpcID, id string) (map[string]any, error) {
	if vpcID == "" {
		return nil, fmt.Errorf("EnableNatGateway: empty vpcID")
	}
	if id == "" {
		return nil, fmt.Errorf("EnableNatGateway: empty id")
	}
	return c.doItem(ctx, "POST", natGatewayBasePath(vpcID)+"/"+url.PathEscape(id)+"/enable", nil, "gateway")
}

// DisableNatGateway disables NAT functionality on the gateway (sets
// nat_enabled=0). Returns the fresh gateway under the "gateway" envelope.
func (c *Client) DisableNatGateway(ctx context.Context, vpcID, id string) (map[string]any, error) {
	if vpcID == "" {
		return nil, fmt.Errorf("DisableNatGateway: empty vpcID")
	}
	if id == "" {
		return nil, fmt.Errorf("DisableNatGateway: empty id")
	}
	return c.doItem(ctx, "POST", natGatewayBasePath(vpcID)+"/"+url.PathEscape(id)+"/disable", nil, "gateway")
}

// AttachNatGatewaySubnet attaches a single PRIVATE subnet to the gateway. The
// route is POST /vpc/{vpcId}/nat-gateway/{id}/subnet with body {subnet_id}. A
// failure (subnet not in the VPC, not private, or already attached) is signalled
// with success:false at HTTP 200 → error. Returns the fresh gateway.
func (c *Client) AttachNatGatewaySubnet(ctx context.Context, vpcID, id, subnetID string) (map[string]any, error) {
	if vpcID == "" {
		return nil, fmt.Errorf("AttachNatGatewaySubnet: empty vpcID")
	}
	if id == "" {
		return nil, fmt.Errorf("AttachNatGatewaySubnet: empty id")
	}
	if subnetID == "" {
		return nil, fmt.Errorf("AttachNatGatewaySubnet: empty subnetID")
	}
	body := map[string]any{"subnet_id": subnetID}
	return c.doItem(ctx, "POST", natGatewayBasePath(vpcID)+"/"+url.PathEscape(id)+"/subnet", body, "gateway")
}

// DetachNatGatewaySubnet detaches a single subnet from the gateway. The route is
// DELETE /vpc/{vpcId}/nat-gateway/{id}/subnet/{subnetId}. A failure (subnet not
// in the VPC) is signalled with success:false at HTTP 200 → error. Returns the
// fresh gateway.
func (c *Client) DetachNatGatewaySubnet(ctx context.Context, vpcID, id, subnetID string) (map[string]any, error) {
	if vpcID == "" {
		return nil, fmt.Errorf("DetachNatGatewaySubnet: empty vpcID")
	}
	if id == "" {
		return nil, fmt.Errorf("DetachNatGatewaySubnet: empty id")
	}
	if subnetID == "" {
		return nil, fmt.Errorf("DetachNatGatewaySubnet: empty subnetID")
	}
	path := natGatewayBasePath(vpcID) + "/" + url.PathEscape(id) + "/subnet/" + url.PathEscape(subnetID)
	return c.doItem(ctx, "DELETE", path, nil, "gateway")
}
