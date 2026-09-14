package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// MicroVM connector endpoints (unified surface, MVU2-3; user_api, bearer
// token). A connector is an integration (github_app | gitlab_runner) that
// launches MicroVMs automatically for incoming CI jobs.
//
//	INDEX   GET    /microvm/connectors          -> {success,connectors:{data:[...]},
//	                                               plans:[...],hypervisor_groups:[...],
//	                                               images:[...],git_sources:[...],
//	                                               github_app_configured}
//	JOBS    GET    /microvm/connectors/jobs     -> {success,jobs:{data:[...]}}
//	GITHUB  POST   /microvm/connectors/github   body {git_source_id,name,labels?,
//	                                               hypervisor_group_id?,plan_id?,image_id?,
//	                                               max_concurrent?,warm_count?}
//	                                                                 -> {success,connector:{...}} (201)
//	GITLAB  POST   /microvm/connectors/gitlab   body {name,gitlab_url,gitlab_token,
//	                                               gitlab_tag_list?,gitlab_run_untagged?,
//	                                               hypervisor_group_id?,plan_id?,image_id?,
//	                                               max_concurrent?,warm_count?}
//	                                                                 -> {success,connector:{...}} (201)
//	UPDATE  PUT    /microvm/connector/{id}      body {name?,labels?,
//	                                               hypervisor_group_id?,plan_id?,image_id?,
//	                                               max_concurrent?,warm_count?,enabled?}
//	                                                                 -> {success,connector:{...}};
//	                                                                   422 needs_plan when enabling
//	                                                                   without plan + location
//	DELETE  DELETE /microvm/connector/{id}                           -> {success}
//
// The connector's "config" (tokens, installation ids) is encrypted at rest
// and never serialized by any endpoint.

// ListMicrovmConnectors returns the bare INDEX envelope: the connectors
// paginator plus the create-context lists (plans, hypervisor_groups, images,
// git_sources) and github_app_configured.
func (c *Client) ListMicrovmConnectors(ctx context.Context) (map[string]any, error) {
	return c.doItem(ctx, "GET", "/microvm/connectors", nil, "")
}

// GetMicrovmConnector scans the connector INDEX for one connector by id. A
// missing id surfaces as a 404 *APIError.
func (c *Client) GetMicrovmConnector(ctx context.Context, id string) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("GetMicrovmConnector: empty id")
	}
	env, err := c.ListMicrovmConnectors(ctx)
	if err != nil {
		return nil, err
	}
	pag, _ := env["connectors"].(map[string]any)
	for _, connector := range objSlice(pag["data"]) {
		if connector["id"] == id {
			return connector, nil
		}
	}
	return nil, &APIError{Status: http.StatusNotFound, Message: fmt.Sprintf("microvm connector %s not found", id)}
}

// ListMicrovmConnectorJobs returns the account's connector jobs, newest first.
func (c *Client) ListMicrovmConnectorJobs(ctx context.Context) ([]map[string]any, error) {
	return c.listNamedPaginator(ctx, "/microvm/connectors/jobs", "jobs")
}

// CreateGithubMicrovmConnector creates a github_app connector from an
// existing github_app Git Source that has an installation_id.
func (c *Client) CreateGithubMicrovmConnector(ctx context.Context, body map[string]any) (map[string]any, error) {
	return c.doItem(ctx, "POST", "/microvm/connectors/github", body, "connector")
}

// CreateGitlabMicrovmConnector registers a GitLab runner and creates its
// gitlab_runner connector. The token (glrt-...) travels in the body and is
// stored encrypted; it is never returned.
func (c *Client) CreateGitlabMicrovmConnector(ctx context.Context, body map[string]any) (map[string]any, error) {
	return c.doItem(ctx, "POST", "/microvm/connectors/gitlab", body, "connector")
}

// UpdateMicrovmConnector updates a connector's settings. Enabling without a
// plan and location surfaces as a 422 *APIError with the needs_plan error on
// the "enabled" field.
func (c *Client) UpdateMicrovmConnector(ctx context.Context, id string, body map[string]any) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("UpdateMicrovmConnector: empty id")
	}
	return c.doItem(ctx, "PUT", "/microvm/connector/"+url.PathEscape(id), body, "connector")
}

// DeleteMicrovmConnector deletes a connector. Its queued jobs stop dispatching.
func (c *Client) DeleteMicrovmConnector(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("DeleteMicrovmConnector: empty id")
	}
	return c.doVoid(ctx, "DELETE", "/microvm/connector/"+url.PathEscape(id), nil)
}
