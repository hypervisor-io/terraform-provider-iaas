package client

import (
	"context"
	"fmt"
	"net/url"
)

// MicroVM image endpoints (unified surface, MVU2-1; user_api, bearer token).
// An image is built once from a base/dockerfile/oci/git source and stamped
// into immutable versions; MicroVMs run a version.
//
//	INDEX   GET    /microvm/images            (?search=&kind=&status=) -> {success,images:{data:[...]}}
//	SHOW    GET    /microvm/image/{id}                             -> {success,image:{...},versions:[...],microvms_count}
//	CREATE  POST   /microvm/images            body per the U3 Create Image body
//	                                                                 -> {success,image:{...}} (201)
//	BUILD   POST   /microvm/image/{id}/build  body {hypervisor_group_id}
//	                                                                 -> {success,version:{...}} (201)
//	DELETE  DELETE /microvm/image/{id}                             -> {success,message}; 409 on image_in_use

// ListMicrovmImages returns the images visible to the account (platform base
// plus own), filtered by name substring (search), source_kind (kind) and
// build status. Empty filters are omitted from the query.
func (c *Client) ListMicrovmImages(ctx context.Context, search, kind, status string) ([]map[string]any, error) {
	q := url.Values{}
	if search != "" {
		q.Set("search", search)
	}
	if kind != "" {
		q.Set("kind", kind)
	}
	if status != "" {
		q.Set("status", status)
	}
	path := "/microvm/images"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	return c.listNamedPaginator(ctx, path, "images")
}

// GetMicrovmImage fetches the SHOW envelope for one image: the image object,
// its versions (newest first) and the count of MicroVMs running it. A missing
// id surfaces as a 404 *APIError.
func (c *Client) GetMicrovmImage(ctx context.Context, id string) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("GetMicrovmImage: empty id")
	}
	return c.doItem(ctx, "GET", "/microvm/image/"+url.PathEscape(id), nil, "")
}

// CreateMicrovmImage creates a custom image and dispatches its first build.
// body follows the U3 Create Image body: {name, source_kind, source,
// hypervisor_group_id, description?, auth?, base_image_id?, env?,
// lifecycle_hooks?, build_hooks?}. Credentials travel in "auth", never inside
// "source".
func (c *Client) CreateMicrovmImage(ctx context.Context, body map[string]any) (map[string]any, error) {
	return c.doItem(ctx, "POST", "/microvm/images", body, "image")
}

// BuildMicrovmImage stamps a new version of the image from the same source,
// built in the given hypervisor group.
func (c *Client) BuildMicrovmImage(ctx context.Context, id, hypervisorGroupID string) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("BuildMicrovmImage: empty id")
	}
	return c.doItem(ctx, "POST", "/microvm/image/"+url.PathEscape(id)+"/build",
		map[string]any{"hypervisor_group_id": hypervisorGroupID}, "version")
}

// DeleteMicrovmImage deletes an image and its versions. The API answers 409
// when a MicroVM still references the image (image_in_use).
func (c *Client) DeleteMicrovmImage(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("DeleteMicrovmImage: empty id")
	}
	return c.doVoid(ctx, "DELETE", "/microvm/image/"+url.PathEscape(id), nil)
}
