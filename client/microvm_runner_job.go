package client

import (
	"context"
	"fmt"
	"net/url"
)

// CI runner job history endpoint (MV3-24): GET /microvm/runners/jobs, a bare
// Laravel paginator (no named key, no success envelope) filterable by
// ?pool_id= - read through doList, which walks current_page/last_page via
// ?page=N. This is the MCP-parity method for user.microvm.runner.job.list;
// the OpenTofu provider has no runner-job resource (jobs are read-only
// operational data, not declarative state).
func (c *Client) ListRunnerJobs(ctx context.Context, poolID string) ([]map[string]any, error) {
	path := "/microvm/runners/jobs"
	if poolID != "" {
		path += "?pool_id=" + url.QueryEscape(poolID)
	}

	return c.doList(ctx, "GET", path, nil)
}

func (c *Client) GetRunnerJob(ctx context.Context, id string) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("GetRunnerJob: empty id")
	}

	// The show endpoint answers the bare job model (no envelope key).
	return c.doItem(ctx, "GET", "/microvm/runners/jobs/"+url.PathEscape(id), nil, "")
}
