package client

import (
	"context"
	"fmt"
	"net/url"
)

// Notification Channel endpoints (verified against
// UserApi\NotificationChannelController + StoreRequest + UpdateRequest +
// NotificationChannel model + routes/user_api.php).
//
// A notification channel is a user-owned delivery target (Slack, Discord,
// Telegram, or generic webhook) that alert rules dispatch through.
//
// Route summary (singular/plural asymmetry):
//
//	LIST    GET    /notification-channels              (PLURAL)
//	                → {success,channels:{current_page,data:[...],total,...}}
//	                  (paginator wrapped under "channels" key)
//	CREATE  POST   /notification-channels              (PLURAL)
//	                body {name (required,max:255),
//	                      type (required,in:slack|discord|telegram|webhook),
//	                      enabled (sometimes|boolean),
//	                      config.webhook_url (slack/discord: required|url),
//	                      config.bot_token   (telegram: required),
//	                      config.chat_id     (telegram: required),
//	                      config.url         (webhook: required|url),
//	                      config.method      (webhook: nullable|in:POST|PUT),
//	                      config.headers     (webhook: nullable|array),
//	                      config.secret      (webhook: nullable|string),
//	                      config.connect_timeout (webhook: nullable|int 1-30),
//	                      config.timeout     (webhook: nullable|int 1-60),
//	                      config.verify_ssl  (webhook: nullable|bool)}
//	                → {success,channel:{id,name,type,enabled,...}}
//	SHOW    GET    /notification-channel/{id}          (SINGULAR)
//	                → {success,channel:{id,name,type,config,enabled,...}}
//	                  config is returned (model has no $hidden; encrypted:array
//	                  is stored encrypted in DB but decrypted for responses)
//	UPDATE  PATCH  /notification-channel/{id}          (SINGULAR)
//	                body same shape as CREATE
//	                → {success,channel:{id,name,type,config,enabled,...}}
//	DELETE  DELETE /notification-channel/{id}          (SINGULAR)
//	                → {success}
//
// Notes:
//   - All writes are SYNCHRONOUS (no async task/waiter).
//   - config is returned by SHOW/UPDATE (encrypted at rest, decrypted on read).
//     It is NOT write-only; model it as a regular (Sensitive if desired) MapAttribute.
//   - type is updatable (update request accepts any valid type with matching config).
//   - test (POST /notification-channel/{id}/test) is NOT modelled — operational action.
//   - Routes are gated by subuser permissions (monitoring.view for LIST/SHOW,
//     monitoring.manage for CREATE/UPDATE/DELETE). The IP-locked Bearer token
//     controls access; a 403 means the token lacks the monitoring.manage scope
//     (e.g. read-only token or subuser restriction).
//   - No billing gate.
//   - success:false at HTTP 200 = error (C3) — handled by doItem/doVoid.

// ListNotificationChannels returns all notification channels belonging to the
// authenticated account. The API wraps the Laravel paginator under the
// "channels" key ({success,channels:{current_page,data:[...],total,...}}),
// so we use doItem to unwrap "channels" and then extract the "data" array.
func (c *Client) ListNotificationChannels(ctx context.Context) ([]map[string]any, error) {
	// The LIST response shape is:
	//   {"success":true,"channels":{"current_page":1,"data":[...],"total":N,...}}
	// doList only handles top-level "data" or bare arrays; it cannot reach a
	// "data" nested inside a named key. We use doItem to get the paginator
	// object, then extract its "data" slice manually.
	paginatorObj, err := c.doItem(ctx, "GET", "/notification-channels", nil, "channels")
	if err != nil {
		return nil, err
	}

	dataRaw, ok := paginatorObj["data"]
	if !ok {
		// Empty or unexpected shape — return empty slice rather than error.
		return []map[string]any{}, nil
	}
	dataSlice, ok := dataRaw.([]any)
	if !ok {
		return nil, fmt.Errorf("ListNotificationChannels: 'channels.data' is not an array (got %T)", dataRaw)
	}
	items := make([]map[string]any, 0, len(dataSlice))
	for i, v := range dataSlice {
		obj, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("ListNotificationChannels: channels.data[%d] is not an object (got %T)", i, v)
		}
		items = append(items, obj)
	}
	return items, nil
}

// CreateNotificationChannel creates a new notification channel. The body must
// contain name, type, and the per-type config sub-map. The create response
// carries the new channel under the "channel" key and includes the id.
func (c *Client) CreateNotificationChannel(ctx context.Context, body map[string]any) (map[string]any, error) {
	return c.doItem(ctx, "POST", "/notification-channels", body, "channel")
}

// GetNotificationChannel fetches a single notification channel by id. The SHOW
// route is SINGULAR (/notification-channel/{id}). A 404 is an *APIError
// recognised by IsNotFound. config is returned decrypted by the server.
func (c *Client) GetNotificationChannel(ctx context.Context, id string) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("GetNotificationChannel: empty id")
	}
	p := "/notification-channel/" + url.PathEscape(id)
	return c.doItem(ctx, "GET", p, nil, "channel")
}

// UpdateNotificationChannel patches a notification channel. The UPDATE route
// is SINGULAR. All fields (name, type, config, enabled) may be updated in
// place. The response carries the updated channel under the "channel" key.
func (c *Client) UpdateNotificationChannel(ctx context.Context, id string, body map[string]any) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("UpdateNotificationChannel: empty id")
	}
	p := "/notification-channel/" + url.PathEscape(id)
	return c.doItem(ctx, "PATCH", p, body, "channel")
}

// DeleteNotificationChannel deletes a notification channel by id. The service
// detaches the channel from any alert rules before deletion. success:false at
// HTTP 200 is treated as an error via doVoid.
func (c *Client) DeleteNotificationChannel(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("DeleteNotificationChannel: empty id")
	}
	p := "/notification-channel/" + url.PathEscape(id)
	return c.doVoid(ctx, "DELETE", p, nil)
}
