package client

import "context"

// GetWebhookEventKinds returns the published catalog of every event kind a
// webhook subscription's event_kinds may name (or "*" for all):
// [{kind, resource, description}]. Backs the iaas_webhook_event_kinds data
// source (NUI-V-R19-WH1). The response is {"success":true,"data":[...]}, a
// plain top-level "data" array with no pagination -- doList handles it
// directly (no named-paginator wrapping like the microvm image/catalog
// endpoints need).
func (c *Client) GetWebhookEventKinds(ctx context.Context) ([]map[string]any, error) {
	return c.doList(ctx, "GET", "/webhook-event-kinds", nil)
}
