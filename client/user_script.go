package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// User script endpoints (verified against UserScriptController + routes/user_api.php):
//
//	INDEX   GET    /user-scripts       → Laravel paginator {data:[{id,name,type,…}]}
//	CREATE  POST   /user-scripts       body {name,type,content,description?,shebang?} → 200 {success,script}
//	UPDATE  PATCH  /user-script/{id}   (singular) body {any subset} → 200 {success,script}
//	DELETE  DELETE /user-script/{id}   (singular) → 200 {success}; success:false at 200 on failure
//
// There is NO SHOW route. GetUserScript therefore lists and matches by id,
// synthesising a 404 *APIError when the id is absent so IsNotFound-driven drift
// handling works exactly as it does for resources that do have a SHOW route.
// content is stored encrypted at rest and returned decrypted in the object.

// ListUserScripts returns all user scripts visible to the token (paginator-aware).
func (c *Client) ListUserScripts(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/user-scripts", nil)
}

// CreateUserScript creates a user script. fields must carry name, type and
// content; description and shebang are optional. Returns the created object.
func (c *Client) CreateUserScript(ctx context.Context, fields map[string]any) (map[string]any, error) {
	return c.doItem(ctx, "POST", "/user-scripts", fields, "script")
}

// GetUserScript returns a single user script by id. Because the API has no SHOW
// route, it lists and matches; an absent id yields a 404 *APIError recognised by
// IsNotFound.
func (c *Client) GetUserScript(ctx context.Context, id string) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("GetUserScript: empty id")
	}
	items, err := c.ListUserScripts(ctx)
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		if s, ok := it["id"].(string); ok && s == id {
			return it, nil
		}
	}
	return nil, &APIError{Status: http.StatusNotFound, Message: "user script not found"}
}

// UpdateUserScript patches a user script (singular route). fields may carry any
// subset of name/type/content/description/shebang. Returns the fresh object.
func (c *Client) UpdateUserScript(ctx context.Context, id string, fields map[string]any) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("UpdateUserScript: empty id")
	}
	return c.doItem(ctx, "PATCH", "/user-script/"+url.PathEscape(id), fields, "script")
}

// DeleteUserScript deletes a user script by id (singular route). A failure is
// signalled with success:false at HTTP 200, so doVoid checks the flag.
func (c *Client) DeleteUserScript(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("DeleteUserScript: empty id")
	}
	return c.doVoid(ctx, "DELETE", "/user-script/"+url.PathEscape(id), nil)
}
