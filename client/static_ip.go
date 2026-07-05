package client

import (
	"context"
	"fmt"
	"net/url"
)

// Static IP endpoints (verified against the real controller + routes/user_api.php):
//
//	INDEX    GET    /static-ips          → Laravel paginator {data:[...]}; supports ?search,?status,?hypervisor_group_id
//	ALLOCATE POST   /static-ips/allocate body {ip_id,hypervisor_group_id}
//	                                     → 200 {success,message,static_ip:{id,status,ip:{ip,...},hypervisor_group:{...}}}
//	                                         or 200 {success:false,message:"Insufficient credits…"} (C3)
//	DELETE   DELETE /static-ip/{id}      (singular) → 200 {success,message}; 200 success:false on failure
//
// Notes:
//   - There is NO individual SHOW route (GET /static-ip/{id}) for static IPs.
//     GetStaticIP must therefore use ListStaticIPs + filter-by-id. A 404-shaped
//     *APIError is returned when the id is absent from the list.
//   - Create is SYNCHRONOUS: the allocate response carries the id directly - no task, no waiter.
//   - All routes are gated behind the billing.enabled middleware. When billing is disabled
//     the API returns HTTP 403 with {success:false,message:"This feature is unavailable because
//     billing is disabled."}; responseError maps 403 to a *APIError surfaced via diagFromErr.
//   - The DELETE route uses the singular form /static-ip/{id}, unlike the plural INDEX.
//   - An empty-id guard is applied on every path-id argument (consistency with other resources).

// ListStaticIPs returns all static IPs belonging to the authenticated user
// (paginator-aware). Optional filters may be supplied as query-string
// parameters in the path, but callers in this package always use the bare
// path and let higher layers filter if needed.
func (c *Client) ListStaticIPs(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/static-ips", nil)
}

// GetStaticIP fetches a single static IP by UUID. Because there is no
// individual SHOW route the implementation lists all IPs and scans for the
// matching id. A missing id is surfaced as a 404 *APIError (IsNotFound = true).
func (c *Client) GetStaticIP(ctx context.Context, id string) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("GetStaticIP: empty id")
	}
	items, err := c.ListStaticIPs(ctx)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if v, ok := item["id"].(string); ok && v == id {
			return item, nil
		}
	}
	// Simulate a 404 so IsNotFound works and Read can RemoveResource.
	return nil, &APIError{Status: 404, Message: "static IP not found"}
}

// AllocateStaticIP reserves a static IP. body must contain at least
// "ip_id" and "hypervisor_group_id" (both required by AllocateRequest).
// Additional optional keys are forwarded transparently. The response
// is unwrapped from the "static_ip" envelope.
func (c *Client) AllocateStaticIP(ctx context.Context, body map[string]any) (map[string]any, error) {
	return c.doItem(ctx, "POST", "/static-ips/allocate", body, "static_ip")
}

// DeleteStaticIP deallocates (releases) a reserved static IP. The DELETE
// route uses the singular form /static-ip/{id}. A failure is signalled
// with success:false at HTTP 200 (e.g. IP currently attached to an instance),
// so doVoid checks the flag.
func (c *Client) DeleteStaticIP(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("DeleteStaticIP: empty id")
	}
	return c.doVoid(ctx, "DELETE", "/static-ip/"+url.PathEscape(id), nil)
}
