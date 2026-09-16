package client

import (
	"context"
	"fmt"
	"net/url"
)

// Serverless App endpoints (user_api, bearer token) - see
// docs/superpowers/specs/2026-09-04-microvm-platform-design.md section 3.5
// and the routes registered in Task MV2-25.
//
//	INDEX    GET    /microvm/apps                          -> {success,apps:{data:[...]}}
//	SHOW     GET    /microvm/app/{id}                       -> {success,app:{...},revisions:[...]}
//	CREATE   POST   /microvm/apps                           -> {success,app:{...}}
//	DEPLOY   POST   /microvm/app/{id}/deploy   body {revision_id}
//	ROLLBACK POST   /microvm/app/{id}/rollback body {revision_id}
//	STOP     POST   /microvm/app/{id}/stop
//	START    POST   /microvm/app/{id}/start
//	SET ENV  PUT    /microvm/app/{id}/env      body {env: {K: V, ...}}
//	DELETE   DELETE /microvm/app/{id}

// ListApps returns the account's serverless apps. The merged MV2-25 index
// nests a Laravel paginator under "apps" ({success,apps:{data:[...]}}), so
// the unwrap goes through namedPaginatorList, not namedList (which expects a
// bare collection and would silently return an empty slice).
func (c *Client) ListApps(ctx context.Context) ([]map[string]any, error) {
	return c.namedPaginatorList(ctx, "GET", "/microvm/apps", "apps")
}

// GetApp fetches a single app by UUID, including its currentRevision and
// domains embed. A missing id surfaces as a 404 *APIError (IsNotFound = true),
// so Read can RemoveResource.
func (c *Client) GetApp(ctx context.Context, id string) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("GetApp: empty id")
	}
	return c.doItem(ctx, "GET", "/microvm/app/"+url.PathEscape(id), nil, "app")
}

// CreateApp creates a serverless app. body must contain
// "hypervisor_group_id", "slug", "source_kind" and "source"
// ({image} for oci, {repo,branch?} for git); "port", "min_instances",
// "idle_timeout_seconds" and "health_path" are optional and omitted keys fall
// back to the API defaults. The server creates the first revision and
// dispatches its build as part of the same call.
func (c *Client) CreateApp(ctx context.Context, body map[string]any) (map[string]any, error) {
	return c.doItem(ctx, "POST", "/microvm/apps", body, "app")
}

// DeployAppRevision deploys an existing revision of the app (build-then-deploy
// flow: the revision id comes from the build the API produced).
func (c *Client) DeployAppRevision(ctx context.Context, id, revisionID string) error {
	return c.doVoid(ctx, "POST", "/microvm/app/"+url.PathEscape(id)+"/deploy", map[string]any{"revision_id": revisionID})
}

// RollbackAppRevision redeploys a previous revision of the app.
func (c *Client) RollbackAppRevision(ctx context.Context, id, revisionID string) error {
	return c.doVoid(ctx, "POST", "/microvm/app/"+url.PathEscape(id)+"/rollback", map[string]any{"revision_id": revisionID})
}

// StopApp stops the app's running instances (scale to zero on demand).
func (c *Client) StopApp(ctx context.Context, id string) error {
	return c.doVoid(ctx, "POST", "/microvm/app/"+url.PathEscape(id)+"/stop", nil)
}

// StartApp wakes the app after a stop.
func (c *Client) StartApp(ctx context.Context, id string) error {
	return c.doVoid(ctx, "POST", "/microvm/app/"+url.PathEscape(id)+"/start", nil)
}

// SetAppEnv replaces the app's environment variables. A failure is signalled
// with success:false at HTTP 200, so doVoid checks the flag.
func (c *Client) SetAppEnv(ctx context.Context, id string, env map[string]string) error {
	return c.doVoid(ctx, "PUT", "/microvm/app/"+url.PathEscape(id)+"/env", map[string]any{"env": env})
}

// DeleteApp permanently destroys the app. A failure is signalled with
// success:false at HTTP 200, so doVoid checks the flag.
func (c *Client) DeleteApp(ctx context.Context, id string) error {
	return c.doVoid(ctx, "DELETE", "/microvm/app/"+url.PathEscape(id), nil)
}
