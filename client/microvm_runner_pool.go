package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// CI runner pool endpoints (MV3-23; user_api, bearer token). A runner pool is
// a CI-provider configuration row (a GitHub App installation or a GitLab
// runner registration) that maps CI jobs onto MicroVM capacity in a
// hypervisor group under a MicroVM plan.
//
//	INDEX   GET    /microvm/runners/pools                 -> BARE Laravel paginator
//	                                                        {data:[...],current_page,last_page,...}
//	                                                        (server-side paginate(); no named key,
//	                                                        no success envelope)
//	CREATE  POST   /microvm/runners/pools/gitlab   body {hypervisor_group_id,plan_id,gitlab_url,
//	                                       gitlab_token?,warm_count?}
//	                                                      -> BARE pool object (no envelope)
//	UPDATE  PUT    /microvm/runners/pools/{provider}/{id}
//	                                 body {hypervisor_group_id,plan_id,max_concurrent,
//	                                       + warm_count (gitlab) | enabled (github)}
//	                                                      -> BARE pool object (no envelope)
//	DELETE  DELETE /microvm/runners/pools/{id}            -> {success:true}
//
// READ-PATH GAP (recorded for the planner, MV3-25): the merged MV3-23 routes
// have NO pool-show endpoint - GET /microvm/runners/pools/{id} does not exist
// (index / store github+gitlab / update github+gitlab / destroy only).
// GetRunnerPool therefore reads through the LIST endpoint and filters by id
// client-side; the clean fix is a Master-side GET /microvm/runners/pools/{id}
// route. GitHub pools additionally have no Terraform create path: the panel's
// Install GitHub App flow links them (POST /microvm/runners/pools/github only
// configures an already-linked draft and 409/422s otherwise), so the provider
// resource treats provider_type = "github" as import-only.
//
// SENSITIVE / WRITE-ONLY: gitlab_token is $hidden on the Master CiRunnerPool
// model - no response ever echoes it. It is accepted by the gitlab store body
// only (verified against GitLab, then stored encrypted), exactly like the
// certificate PEM bodies in certificate.go.

// ListRunnerPools returns every CI runner pool on the account. The index
// route returns a BARE Laravel paginator (a top-level {data:[...]} object,
// not a paginator nested under a named key), so this uses doList - NOT
// namedPaginatorList, which expects the named-key shape and returns only the
// first page. doList reads current_page/last_page and pages through with
// ?page=N, so pools beyond the server's first paginate() page are included.
func (c *Client) ListRunnerPools(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/microvm/runners/pools", nil)
}

// GetRunnerPool fetches a single pool by UUID. READ-PATH GAP: there is no
// pool-show route (see the file header), so this pages through
// ListRunnerPools and filters by id client-side. A missing id surfaces as a
// 404 *APIError so client.IsNotFound(err) is true and Read can
// RemoveResource, matching the SHOW-endpoint resources' contract.
func (c *Client) GetRunnerPool(ctx context.Context, id string) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("GetRunnerPool: empty id")
	}
	pools, err := c.ListRunnerPools(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range pools {
		if p["id"] == id {
			return p, nil
		}
	}
	return nil, &APIError{
		Status:  http.StatusNotFound,
		Message: fmt.Sprintf("runner pool %s not found in the GET /microvm/runners/pools listing", id),
	}
}

// CreateGitlabRunnerPool creates a GitLab runner pool. body must contain
// "hypervisor_group_id", "plan_id" and "gitlab_url"; "gitlab_token" is
// verified against GitLab and stored write-only, "warm_count" is optional.
// The response is the BARE pool object (no envelope), returned as-is.
// There is deliberately no CreateGithubRunnerPool: GitHub pools are linked
// by the panel's Install GitHub App flow and adopted via terraform import.
func (c *Client) CreateGitlabRunnerPool(ctx context.Context, body map[string]any) (map[string]any, error) {
	return c.doItem(ctx, "POST", "/microvm/runners/pools/gitlab", body, "")
}

// UpdateRunnerPool updates a pool through its provider-specific route:
// PUT /microvm/runners/pools/{provider}/{id}. providerType must be "github"
// or "gitlab" - the Master 404s a pool id on the wrong provider's route, and
// any other value is rejected client-side before a request is attempted. The
// response is the BARE updated pool object.
func (c *Client) UpdateRunnerPool(ctx context.Context, providerType, id string, body map[string]any) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("UpdateRunnerPool: empty id")
	}
	if providerType != "github" && providerType != "gitlab" {
		return nil, fmt.Errorf("UpdateRunnerPool: provider_type must be github or gitlab, got %q", providerType)
	}
	path := "/microvm/runners/pools/" + url.PathEscape(providerType) + "/" + url.PathEscape(id)
	return c.doItem(ctx, "PUT", path, body, "")
}

// DeleteRunnerPool destroys a pool (either provider). The success envelope's
// flag is checked by doVoid; a refusal (e.g. the pool still has an active
// job) surfaces as an error either way - success:false at HTTP 200 or a
// non-2xx *APIError.
func (c *Client) DeleteRunnerPool(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("DeleteRunnerPool: empty id")
	}
	return c.doVoid(ctx, "DELETE", "/microvm/runners/pools/"+url.PathEscape(id), nil)
}
