package client

import (
	"context"
	"fmt"
	"net/url"
)

// DB Parameter Group endpoints, verified against the real UserApi
// DbParameterGroupController + routes/user_api.php.
//
// A parameter group is a named, engine-scoped collection of key=value database
// configuration parameters that can be applied to a managed database via the
// separate PATCH /database/{id}/parameter-group endpoint (not modelled here;
// applying a parameter group is a db-resource action).
//
// ROUTES (all wrapped in billing.enabled middleware; note singular/plural asymmetry):
//
//	LIST    GET    /db/parameter-groups         (PLURAL)
//	                                              → 200 {success,parameter_groups:[...]}
//	                                              (bare array under named key, NOT a paginator)
//	CREATE  POST   /db/parameter-groups         (PLURAL)
//	                                              body {name (req), engine (req:
//	                                                mysql|mariadb|postgresql),
//	                                                parameters (req: map[string]any)}
//	                                              → 200 {success,parameter_group:{id,...}}
//	UPDATE  PATCH  /db/parameter-group/{id}     (SINGULAR) requires databases.manage
//	                                              body {name?, parameters?}
//	                                              → 200 {success,parameter_group:{id,...}}
//	DELETE  DELETE /db/parameter-group/{id}     (SINGULAR) requires databases.delete
//	                                              → 200 {success,message}
//
// DEVIATION: There is NO SHOW endpoint in user_api.php. The resource must use
// LIST and scan for the matching id to implement Read. GetDBParameterGroup
// implements this list-and-match pattern (C4).
//
// Parameters storage: parameters is a map[string]any in the request, stored as
// JSON with the cast 'array'. The controller appends unit suffixes from the
// catalog (e.g. 512 → "512M") before storing. Values in the API response may be
// strings with those suffixes. The Terraform resource models parameters as
// MapAttribute(String) - users provide string values as stored/returned by the
// API. This keeps the schema simple (a flat map, no nested set or per-param
// endpoints).
//
// Billing/feature gating: all routes are guarded by billing.enabled (HTTP 403
// when billing is off). responseError maps this to an error.
//
// All writes are SYNCHRONOUS (no task/state machine). Empty-id guards are applied
// on every path-id argument.

// ListDBParameterGroups returns all parameter groups belonging to the
// authenticated account. The response envelope is
// {success, parameter_groups:[...]} where "parameter_groups" is a bare JSON
// array (NOT a Laravel paginator). We fetch the top-level map with key="" and
// extract the array ourselves.
func (c *Client) ListDBParameterGroups(ctx context.Context) ([]map[string]any, error) {
	top, err := c.doItem(ctx, "GET", "/db/parameter-groups", nil, "")
	if err != nil {
		return nil, err
	}
	raw, _ := top["parameter_groups"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, v := range raw {
		if obj, ok := v.(map[string]any); ok {
			out = append(out, obj)
		}
	}
	return out, nil
}

// GetDBParameterGroup finds a single parameter group by id by listing all groups
// and scanning for the matching id. This is necessary because there is no SHOW
// endpoint in user_api.php (DEVIATION from a standard CRUD resource).
//
// Returns an *APIError with Status 404 (recognised by IsNotFound) when no group
// with the given id is found (the group was deleted out of band).
func (c *Client) GetDBParameterGroup(ctx context.Context, id string) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("GetDBParameterGroup: empty id")
	}
	groups, err := c.ListDBParameterGroups(ctx)
	if err != nil {
		return nil, err
	}
	for _, g := range groups {
		if gid, _ := g["id"].(string); gid == id {
			return g, nil
		}
	}
	return nil, &APIError{Status: 404, Message: "parameter group not found"}
}

// CreateDBParameterGroup creates a new parameter group from the supplied body
// (name + engine + parameters required). Returns the created object under the
// "parameter_group" envelope, which carries the server-assigned id.
func (c *Client) CreateDBParameterGroup(ctx context.Context, body map[string]any) (map[string]any, error) {
	return c.doItem(ctx, "POST", "/db/parameter-groups", body, "parameter_group")
}

// UpdateDBParameterGroup patches a parameter group (name and/or parameters).
// The PATCH route is SINGULAR (/db/parameter-group/{id}). The response carries
// the updated "parameter_group" object under the "parameter_group" envelope.
func (c *Client) UpdateDBParameterGroup(ctx context.Context, id string, body map[string]any) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("UpdateDBParameterGroup: empty id")
	}
	return c.doItem(ctx, "PATCH", "/db/parameter-group/"+url.PathEscape(id), body, "parameter_group")
}

// DeleteDBParameterGroup deletes a parameter group by id. Any managed databases
// referencing it are automatically detached (parameter_group_id set to null
// server-side). The DELETE route is SINGULAR.
func (c *Client) DeleteDBParameterGroup(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("DeleteDBParameterGroup: empty id")
	}
	return c.doVoid(ctx, "DELETE", "/db/parameter-group/"+url.PathEscape(id), nil)
}
