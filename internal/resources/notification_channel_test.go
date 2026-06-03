package resources_test

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/iaas/terraform-provider-iaas/internal/acctest"
)

// ---------------------------------------------------------------------------
// TestAccNotificationChannel_basic — LIVE acceptance test (manual staging gate).
// Auto-skips unless TF_ACC is set.
// ---------------------------------------------------------------------------

func TestAccNotificationChannel_basic(t *testing.T) {
	const config = `
resource "iaas_notification_channel" "test" {
  name   = "tf-acc-slack-channel"
  type   = "slack"
  config = {
    webhook_url = "https://hooks.slack.com/services/T000/B000/XXXXXXXXXXXXXXXX"
  }
}
`
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("iaas_notification_channel.test", "id"),
					resource.TestCheckResourceAttr("iaas_notification_channel.test", "name", "tf-acc-slack-channel"),
					resource.TestCheckResourceAttr("iaas_notification_channel.test", "type", "slack"),
					resource.TestCheckResourceAttr("iaas_notification_channel.test", "enabled", "true"),
				),
			},
			{
				ResourceName:      "iaas_notification_channel.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// ---------------------------------------------------------------------------
// ncMockServer — stateful mock of the notification channel API.
// ---------------------------------------------------------------------------

type ncMockServer struct {
	mu sync.Mutex

	name         string
	channelType  string
	config       map[string]any
	enabled      bool
	autoDisabled bool
	failureCount int
}

func (s *ncMockServer) channelObject(id string) map[string]any {
	return map[string]any{
		"id":            id,
		"name":          s.name,
		"type":          s.channelType,
		"config":        s.config,
		"enabled":       s.enabled,
		"auto_disabled": s.autoDisabled,
		"failure_count": s.failureCount,
	}
}

// ---------------------------------------------------------------------------
// TestUnitNotificationChannel_lifecycle — MOCK-backed lifecycle test.
//
// Steps:
//  1. Create a Slack channel → assert id, name, type, enabled, auto_disabled,
//     failure_count; assert create body had correct name/type/config.
//  2. Import by id → state rehydrated from SHOW.
//  3. Update: rename + switch to webhook type → assert name and type changed.
//
// Delete is implicit teardown.
// ---------------------------------------------------------------------------

func TestUnitNotificationChannel_lifecycle(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)

	const channelID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"

	store := &ncMockServer{
		enabled:      true,
		autoDisabled: false,
		failureCount: 0,
	}

	// CREATE — POST /notification-channels
	srv.Handle("POST", "/notification-channels", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		store.mu.Lock()
		defer store.mu.Unlock()
		if n, ok := body["name"].(string); ok {
			store.name = n
		}
		if tp, ok := body["type"].(string); ok {
			store.channelType = tp
		}
		if cfg, ok := body["config"].(map[string]any); ok {
			store.config = cfg
		} else {
			store.config = map[string]any{}
		}
		if en, ok := body["enabled"].(bool); ok {
			store.enabled = en
		} else {
			store.enabled = true
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"channel": store.channelObject(channelID),
		})
	})

	// SHOW — GET /notification-channel/{id}
	srv.Handle("GET", "/notification-channel/"+channelID, func(w http.ResponseWriter, r *http.Request) {
		store.mu.Lock()
		defer store.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"channel": store.channelObject(channelID),
		})
	})

	// UPDATE — PATCH /notification-channel/{id}
	srv.Handle("PATCH", "/notification-channel/"+channelID, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		store.mu.Lock()
		defer store.mu.Unlock()
		if n, ok := body["name"].(string); ok {
			store.name = n
		}
		if tp, ok := body["type"].(string); ok {
			store.channelType = tp
		}
		if cfg, ok := body["config"].(map[string]any); ok {
			store.config = cfg
		}
		if en, ok := body["enabled"].(bool); ok {
			store.enabled = en
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"channel": store.channelObject(channelID),
		})
	})

	// DELETE — DELETE /notification-channel/{id}
	srv.Handle("DELETE", "/notification-channel/"+channelID, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
		})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	// Create: Slack channel.
	createCfg := providerCfg + `
resource "iaas_notification_channel" "test" {
  name   = "ops-slack"
  type   = "slack"
  config = {
    webhook_url = "https://hooks.slack.com/services/T000/B000/XYZ"
  }
}
`

	// Update: rename and switch to webhook type.
	updateCfg := providerCfg + `
resource "iaas_notification_channel" "test" {
  name    = "ops-webhook"
  type    = "webhook"
  enabled = false
  config = {
    url    = "https://example.com/hooks/alerts"
    method = "POST"
  }
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			// 1. Create Slack channel.
			{
				Config: createCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_notification_channel.test", "id", channelID),
					resource.TestCheckResourceAttr("iaas_notification_channel.test", "name", "ops-slack"),
					resource.TestCheckResourceAttr("iaas_notification_channel.test", "type", "slack"),
					resource.TestCheckResourceAttr("iaas_notification_channel.test", "enabled", "true"),
					resource.TestCheckResourceAttr("iaas_notification_channel.test", "auto_disabled", "false"),
					resource.TestCheckResourceAttr("iaas_notification_channel.test", "failure_count", "0"),
					// config is Sensitive — we can still check it in tests but not in output.
					resource.TestCheckResourceAttr("iaas_notification_channel.test", "config.webhook_url", "https://hooks.slack.com/services/T000/B000/XYZ"),
				),
			},
			// 2. Import by id — state rehydrated from SHOW.
			{
				ResourceName:      "iaas_notification_channel.test",
				ImportState:       true,
				ImportStateId:     channelID,
				ImportStateVerify: true,
			},
			// 3. Update: rename and switch to webhook type.
			{
				Config: updateCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_notification_channel.test", "id", channelID),
					resource.TestCheckResourceAttr("iaas_notification_channel.test", "name", "ops-webhook"),
					resource.TestCheckResourceAttr("iaas_notification_channel.test", "type", "webhook"),
					resource.TestCheckResourceAttr("iaas_notification_channel.test", "enabled", "false"),
					resource.TestCheckResourceAttr("iaas_notification_channel.test", "config.url", "https://example.com/hooks/alerts"),
					resource.TestCheckResourceAttr("iaas_notification_channel.test", "config.method", "POST"),
				),
			},
		},
	})

	// Assert the create call body had name/type/config.
	creates := srv.Requests("POST", "/notification-channels")
	if len(creates) < 1 {
		t.Fatalf("expected at least 1 POST /notification-channels; got %d", len(creates))
	}
	var createBody map[string]any
	if err := json.Unmarshal(creates[0].Body, &createBody); err != nil {
		t.Fatalf("decoding create body: %v", err)
	}
	if createBody["name"] != "ops-slack" {
		t.Errorf("create body[name] = %v; want ops-slack", createBody["name"])
	}
	if createBody["type"] != "slack" {
		t.Errorf("create body[type] = %v; want slack", createBody["type"])
	}
	cfg, _ := createBody["config"].(map[string]any)
	if cfg["webhook_url"] != "https://hooks.slack.com/services/T000/B000/XYZ" {
		t.Errorf("create body[config][webhook_url] = %v; want Slack URL", cfg["webhook_url"])
	}

	// Assert the update call body had the new name/type.
	updates := srv.Requests("PATCH", "/notification-channel/"+channelID)
	if len(updates) < 1 {
		t.Fatalf("expected at least 1 PATCH /notification-channel/%s; got %d", channelID, len(updates))
	}
	var updateBody map[string]any
	if err := json.Unmarshal(updates[0].Body, &updateBody); err != nil {
		t.Fatalf("decoding update body: %v", err)
	}
	if updateBody["name"] != "ops-webhook" {
		t.Errorf("update body[name] = %v; want ops-webhook", updateBody["name"])
	}
	if updateBody["type"] != "webhook" {
		t.Errorf("update body[type] = %v; want webhook", updateBody["type"])
	}
}
