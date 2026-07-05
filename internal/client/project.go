package client

import (
	"context"
	"fmt"
	"net/url"
)

// Project endpoints (verified against the real controller):
//
//	INDEX   GET    /projects          → Laravel paginator {current_page,data:[...],per_page,total}
//	CREATE  POST   /projects          body {name,description?,color?} → 200 {success,message,project:{...}}
//	SHOW    GET    /project/{id}      (singular) → {success,project:{...},instances:{...},...}; 404 absent
//	UPDATE  PATCH  /project/{id}      (singular) body {name,description?,color?} → 200 {success,message,project:{...}}
//	DELETE  DELETE /project/{id}      (singular) → 200 {success,message}
//
// Notes:
//   - INDEX is plural (/projects); SHOW/UPDATE/DELETE are singular (/project/{id}).
//   - CREATE returns {success,message,project} - key "project" unwrapped via doItem.
//   - SHOW returns {success,project,...} (plus embedded resources); we unwrap key "project".
//   - UPDATE returns {success,message,project} - key "project" unwrapped via doItem.
//   - DELETE returns {success,message} - no object; use doVoid.
//   - All operations are synchronous (no async task/waiter needed).
//   - Validation: name required max:64 alphanumeric+space+dot+dash+underscore;
//     description optional max:255; color optional hex #RRGGBB.

// ListProjects returns all projects visible to the token (paginator-aware).
func (c *Client) ListProjects(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/projects", nil)
}

// CreateProject creates a new project. body must contain "name" (required) and
// may optionally contain "description" and/or "color". Returns the created
// project object (unwrapped from the "project" envelope key).
func (c *Client) CreateProject(ctx context.Context, body map[string]any) (map[string]any, error) {
	return c.doItem(ctx, "POST", "/projects", body, "project")
}

// GetProject fetches a single project by id. The SHOW route is singular
// (/project/{id}). The response also contains embedded paginated resources
// (instances, vpcs, etc.); the "project" key is unwrapped and returned.
// A 404 is returned as an *APIError recognised by IsNotFound.
func (c *Client) GetProject(ctx context.Context, id string) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("GetProject: empty id")
	}
	return c.doItem(ctx, "GET", "/project/"+url.PathEscape(id), nil, "project")
}

// UpdateProject patches a project. body must contain "name" (required by the
// controller) and may optionally contain "description" and/or "color".
// The UPDATE route is singular (/project/{id}). Returns the fresh project object.
func (c *Client) UpdateProject(ctx context.Context, id string, body map[string]any) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("UpdateProject: empty id")
	}
	return c.doItem(ctx, "PATCH", "/project/"+url.PathEscape(id), body, "project")
}

// DeleteProject deletes a project by id. The DELETE route is singular
// (/project/{id}). A failure is signalled with success:false at HTTP 200, so
// doVoid checks the flag.
func (c *Client) DeleteProject(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("DeleteProject: empty id")
	}
	return c.doVoid(ctx, "DELETE", "/project/"+url.PathEscape(id), nil)
}
