package client

import "context"

// GetPlatformVersion returns the master's own platform version info:
// {version, api_envelope, min_agent_version} (min_agent_version may be nil —
// nothing in the Master enforces an agent-version floor yet). Backs the
// iaas_platform_version data source (NUI-V-R20-VER1, round-20 customer
// report item 1: the master's own version was not readable through the
// API). The response is {"success":true,"data":{...}}, a plain top-level
// "data" object -- doItem unwraps it directly.
func (c *Client) GetPlatformVersion(ctx context.Context) (map[string]any, error) {
	return c.doItem(ctx, "GET", "/version", nil, "data")
}
