package client

import (
	"context"
	"fmt"
	"net/url"
)

// Alert Rule endpoints (verified against UserApi\AlertRuleController +
// StoreRequest + UpdateRequest + AlertRule model + routes/user_api.php).
//
// An alert rule watches a metric on a resource (or all resources of a type)
// and dispatches through attached notification channels when the threshold is
// breached for the configured duration.
//
// Route summary (singular/plural asymmetry):
//
//	LIST    GET    /alert-rules                (PLURAL)
//	                → {success,alert_rules:{current_page,data:[...],total,...}}
//	                  (paginator wrapped under "alert_rules" key; each item
//	                   includes an embedded "channels" array)
//	CREATE  POST   /alert-rules                (PLURAL)
//	                body {name (required,max:255),
//	                      resource_type (required,in:instance|managed_database|
//	                                     load_balancer|vpn_gateway),
//	                      resource_id   (nullable|uuid — omit to match ALL
//	                                     resources of the given type),
//	                      metric        (required|string, e.g. cpu_pct),
//	                      operator      (required,in:gt|lt|gte|lte|eq),
//	                      threshold     (required|numeric),
//	                      duration      (nullable|integer|min:0, seconds),
//	                      reminder_interval (nullable|integer|min:0, seconds),
//	                      channel_ids   (nullable|array of UUIDs)}
//	                → {success,alert_rule:{id,name,...,channels:[...]}}
//	SHOW    GET    /alert-rule/{id}            (SINGULAR)
//	                → {success,alert_rule:{id,name,...,channels:[...]}}
//	UPDATE  PATCH  /alert-rule/{id}            (SINGULAR)
//	                body same as CREATE plus enabled (sometimes|boolean)
//	                → {success,alert_rule:{id,name,...,channels:[...]}}
//	DELETE  DELETE /alert-rule/{id}            (SINGULAR)
//	                → {success}
//
// Notes:
//   - All writes are SYNCHRONOUS (no async task/waiter).
//   - channel_ids is a nullable array. On UPDATE, passing channel_ids replaces
//     the attached set via channels().sync(); omitting it leaves channels
//     unchanged. The resource models this as a SetAttribute and always sends
//     the full desired set, letting the controller handle the diff server-side.
//   - resource_id is optional; when omitted the rule fires for every resource of
//     resource_type owned by the account.
//   - status (ok/firing) and fired_at/resolved_at/last_notified_at are
//     server-mutable computed fields — they are NOT sent in Create/Update bodies.
//   - acknowledge (POST /alert-rule/{id}/acknowledge) is an operational action,
//     not IaC state; it is NOT modelled.
//   - Routes are gated by subuser permissions (monitoring.view for LIST/SHOW/HISTORY,
//     monitoring.manage for CREATE/UPDATE/DELETE/acknowledge).
//   - success:false at HTTP 200 = error (C3) — handled by doItem/doVoid.

// ListAlertRules returns all alert rules belonging to the authenticated account.
// Each item includes an embedded "channels" array of attached notification channels.
// The response wraps the Laravel paginator under the "alert_rules" key; we use
// doItem to unwrap that key and then extract the nested "data" array.
func (c *Client) ListAlertRules(ctx context.Context) ([]map[string]any, error) {
	// Shape: {"success":true,"alert_rules":{"current_page":1,"data":[...],"total":N,...}}
	paginatorObj, err := c.doItem(ctx, "GET", "/alert-rules", nil, "alert_rules")
	if err != nil {
		return nil, err
	}

	dataRaw, ok := paginatorObj["data"]
	if !ok {
		return []map[string]any{}, nil
	}
	dataSlice, ok := dataRaw.([]any)
	if !ok {
		return nil, fmt.Errorf("ListAlertRules: 'alert_rules.data' is not an array (got %T)", dataRaw)
	}
	items := make([]map[string]any, 0, len(dataSlice))
	for i, v := range dataSlice {
		obj, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("ListAlertRules: alert_rules.data[%d] is not an object (got %T)", i, v)
		}
		items = append(items, obj)
	}
	return items, nil
}

// CreateAlertRule creates a new alert rule. The body must contain at minimum
// name, resource_type, metric, operator, and threshold. Optional fields:
// resource_id, duration, reminder_interval, channel_ids. The response carries
// the new rule (with channels) under the "alert_rule" key and includes the id.
func (c *Client) CreateAlertRule(ctx context.Context, body map[string]any) (map[string]any, error) {
	return c.doItem(ctx, "POST", "/alert-rules", body, "alert_rule")
}

// GetAlertRule fetches a single alert rule by id. The SHOW route is SINGULAR
// (/alert-rule/{id}). The response includes the embedded "channels" array.
// A 404 is an *APIError recognised by IsNotFound.
func (c *Client) GetAlertRule(ctx context.Context, id string) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("GetAlertRule: empty id")
	}
	p := "/alert-rule/" + url.PathEscape(id)
	return c.doItem(ctx, "GET", p, nil, "alert_rule")
}

// UpdateAlertRule patches an alert rule. The UPDATE route is SINGULAR. All
// fields (name, resource_type, resource_id, metric, operator, threshold,
// duration, reminder_interval, enabled, channel_ids) may be updated in place.
// When channel_ids is present, the server syncs the attached channels to
// exactly that set (replaces). The response carries the updated rule under
// the "alert_rule" key.
func (c *Client) UpdateAlertRule(ctx context.Context, id string, body map[string]any) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("UpdateAlertRule: empty id")
	}
	p := "/alert-rule/" + url.PathEscape(id)
	return c.doItem(ctx, "PATCH", p, body, "alert_rule")
}

// DeleteAlertRule deletes an alert rule. The controller detaches all channels
// before deletion. success:false at HTTP 200 is treated as an error via doVoid.
func (c *Client) DeleteAlertRule(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("DeleteAlertRule: empty id")
	}
	p := "/alert-rule/" + url.PathEscape(id)
	return c.doVoid(ctx, "DELETE", p, nil)
}
