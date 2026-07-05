package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// User image (capture-from-instance) endpoints, verified against
// UserApi\ImageController + routes/user_api.php:
//
//	INDEX   GET    /images         → Laravel paginator {data:[{id,name,status,…}]}
//	                                  (myImages: purpose="user" images owned by the
//	                                  caller; each item is eager-loaded with
//	                                  source_instance/image_storage/hypervisor_group)
//	CREATE  POST   /images         body {instance_id,name,cloudinit?,type?}
//	                                  → 200 {success:true,message,image:{...}}
//	                                    or 422 {success:false,message} on failure
//	DELETE  DELETE /image/{id}     (singular; route param user_imageId) →
//	                                  200 {success:true,message}; an unknown id
//	                                  404s from the route-model-binding closure
//	                                  itself ({"error":"Image not found"}) before
//	                                  the controller runs.
//
// There is NO SHOW route. GetImage therefore lists and matches by id,
// synthesising a 404 *APIError when the id is absent — the same pattern as
// GetUserScript.
//
// Image capture is genuinely ASYNCHRONOUS: CreateImage's response is the Image
// row created synchronously with status "creating"; ImageService dispatches a
// fire-and-forget command to the source instance's hypervisor, and the actual
// disk-capture work is finalized later out-of-band via a slave→master callback
// route this client never calls. The two terminal states are "available"
// (ready) and "error" (fail) — callers must wrap CreateImage with
// internal/waiter, polling GetImage's "status" field until one of those is
// reached; "creating" means keep polling.
//
// Delete is SYNCHRONOUS from the API's perspective: deleteUserImage sets
// status="deleting" and hard-deletes the row inline before the response
// returns, so no delete-side waiter is needed.
//
// There is also no update route registered for user images (only the
// images.view / images.manage / images.delete permissions exist), so the
// resource layer treats every input (name, instance_id, cloudinit, type) as
// RequiresReplace.

// ListImages returns the caller's user-purpose images (paginator-aware).
func (c *Client) ListImages(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/images", nil)
}

// CreateImage snapshots an instance into a new user image. fields must carry
// instance_id and name; cloudinit and type are optional overrides. Returns the
// created Image object (status will be "creating"; poll GetImage to converge).
func (c *Client) CreateImage(ctx context.Context, fields map[string]any) (map[string]any, error) {
	return c.doItem(ctx, "POST", "/images", fields, "image")
}

// GetImage returns a single image by id. Because the API has no SHOW route, it
// lists and matches; an absent id yields a 404 *APIError recognised by
// IsNotFound.
func (c *Client) GetImage(ctx context.Context, id string) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("GetImage: empty id")
	}
	items, err := c.ListImages(ctx)
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		if s, ok := it["id"].(string); ok && s == id {
			return it, nil
		}
	}
	return nil, &APIError{Status: http.StatusNotFound, Message: "image not found"}
}

// DeleteImage deletes a user image by id (singular route). Deletion is
// synchronous — the row is gone by the time this returns successfully.
func (c *Client) DeleteImage(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("DeleteImage: empty id")
	}
	return c.doVoid(ctx, "DELETE", "/image/"+url.PathEscape(id), nil)
}
