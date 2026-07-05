package resources_test

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"

	"github.com/hypervisor-io/terraform-provider-iaas/internal/acctest"
)

// ---------------------------------------------------------------------------
// TestAccAlertRule_basic - LIVE acceptance test (manual staging gate).
// Auto-skips unless TF_ACC is set.
// ---------------------------------------------------------------------------

func TestAccAlertRule_basic(t *testing.T) {
	const config = `
resource "iaas_notification_channel" "slack" {
  name   = "tf-acc-slack-for-alert"
  type   = "slack"
  config = {
    webhook_url = "https://hooks.slack.com/services/T000/B000/XXXXXXXXXXXXXXXX"
  }
}

resource "iaas_alert_rule" "test" {
  name          = "tf-acc-cpu-alert"
  resource_type = "instance"
  metric        = "cpu_pct"
  operator      = "gt"
  threshold     = 80
  duration      = 300
  channel_ids   = [iaas_notification_channel.slack.id]
}
`
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("iaas_alert_rule.test", "id"),
					resource.TestCheckResourceAttr("iaas_alert_rule.test", "name", "tf-acc-cpu-alert"),
					resource.TestCheckResourceAttr("iaas_alert_rule.test", "metric", "cpu_pct"),
					resource.TestCheckResourceAttr("iaas_alert_rule.test", "operator", "gt"),
					resource.TestCheckResourceAttr("iaas_alert_rule.test", "threshold", "80"),
					resource.TestCheckResourceAttr("iaas_alert_rule.test", "enabled", "true"),
					resource.TestCheckResourceAttr("iaas_alert_rule.test", "status", "ok"),
				),
			},
			{
				ResourceName:      "iaas_alert_rule.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// ---------------------------------------------------------------------------
// arMockServer - stateful mock of the alert rule API.
// ---------------------------------------------------------------------------

type arMockServer struct {
	mu sync.Mutex

	name             string
	resourceType     string
	resourceID       string
	metric           string
	operator         string
	threshold        float64
	duration         int
	reminderInterval int
	channelIDs       []string
	enabled          bool
	status           string
}

func newARMock() *arMockServer {
	return &arMockServer{
		enabled: true,
		status:  "ok",
	}
}

// ruleObject returns the alert rule object as the API would return it,
// with an inline channels array (each element has at least an "id" field).
func (s *arMockServer) ruleObject(id string) map[string]any {
	channels := make([]map[string]any, 0, len(s.channelIDs))
	for _, cid := range s.channelIDs {
		channels = append(channels, map[string]any{"id": cid, "name": "channel-" + cid})
	}
	obj := map[string]any{
		"id":                id,
		"name":              s.name,
		"resource_type":     s.resourceType,
		"metric":            s.metric,
		"operator":          s.operator,
		"threshold":         s.threshold,
		"duration":          float64(s.duration),
		"reminder_interval": float64(s.reminderInterval),
		"enabled":           s.enabled,
		"status":            s.status,
		"channels":          channels,
	}
	if s.resourceID != "" {
		obj["resource_id"] = s.resourceID
	} else {
		obj["resource_id"] = nil
	}
	return obj
}

func (s *arMockServer) applyBody(body map[string]any) {
	if v, ok := body["name"].(string); ok {
		s.name = v
	}
	if v, ok := body["resource_type"].(string); ok {
		s.resourceType = v
	}
	if v, ok := body["resource_id"].(string); ok {
		s.resourceID = v
	}
	if v, ok := body["metric"].(string); ok {
		s.metric = v
	}
	if v, ok := body["operator"].(string); ok {
		s.operator = v
	}
	if v, ok := body["threshold"].(float64); ok {
		s.threshold = v
	}
	if v, ok := body["duration"].(float64); ok {
		s.duration = int(v)
	}
	if v, ok := body["reminder_interval"].(float64); ok {
		s.reminderInterval = int(v)
	}
	if v, ok := body["enabled"].(bool); ok {
		s.enabled = v
	}
	if v, ok := body["channel_ids"]; ok {
		switch cv := v.(type) {
		case []interface{}:
			ids := make([]string, 0, len(cv))
			for _, i := range cv {
				if s, ok := i.(string); ok {
					ids = append(ids, s)
				}
			}
			s.channelIDs = ids
		case []string:
			s.channelIDs = cv
		}
	}
}

// ---------------------------------------------------------------------------
// TestUnitAlertRule_lifecycle - MOCK-backed lifecycle test.
//
// Steps:
//  1. Create a rule (no resource_id, one channel) - assert id, fields, channel.
//  2. Import by id - state rehydrated from SHOW.
//  3. Update: rename, change threshold, add second channel, set duration.
//
// Delete is implicit teardown.
// ---------------------------------------------------------------------------

func TestUnitAlertRule_lifecycle(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)

	const ruleID = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	const ch1ID = "cccccccc-cccc-cccc-cccc-cccccccccccc"
	const ch2ID = "dddddddd-dddd-dddd-dddd-dddddddddddd"

	store := newARMock()

	// CREATE - POST /alert-rules
	srv.Handle("POST", "/alert-rules", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		store.mu.Lock()
		defer store.mu.Unlock()
		store.applyBody(body)
		writeJSON(w, http.StatusOK, map[string]any{
			"success":    true,
			"alert_rule": store.ruleObject(ruleID),
		})
	})

	// SHOW - GET /alert-rule/{id}
	srv.Handle("GET", "/alert-rule/"+ruleID, func(w http.ResponseWriter, r *http.Request) {
		store.mu.Lock()
		defer store.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"success":    true,
			"alert_rule": store.ruleObject(ruleID),
		})
	})

	// UPDATE - PATCH /alert-rule/{id}
	srv.Handle("PATCH", "/alert-rule/"+ruleID, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		store.mu.Lock()
		defer store.mu.Unlock()
		store.applyBody(body)
		writeJSON(w, http.StatusOK, map[string]any{
			"success":    true,
			"alert_rule": store.ruleObject(ruleID),
		})
	})

	// DELETE - DELETE /alert-rule/{id}
	srv.Handle("DELETE", "/alert-rule/"+ruleID, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
		})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	// Create: single channel, no resource_id (global rule).
	createCfg := providerCfg + `
resource "iaas_alert_rule" "test" {
  name          = "High CPU"
  resource_type = "instance"
  metric        = "cpu_pct"
  operator      = "gt"
  threshold     = 80
  channel_ids   = ["` + ch1ID + `"]
}
`

	// Update: rename, raise threshold, add second channel, set duration.
	updateCfg := providerCfg + `
resource "iaas_alert_rule" "test" {
  name              = "High CPU (updated)"
  resource_type     = "instance"
  metric            = "cpu_pct"
  operator          = "gte"
  threshold         = 85
  duration          = 300
  reminder_interval = 3600
  channel_ids       = ["` + ch1ID + `", "` + ch2ID + `"]
  enabled           = false
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			// 1. Create - global rule with one channel.
			{
				Config: createCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_alert_rule.test", "id", ruleID),
					resource.TestCheckResourceAttr("iaas_alert_rule.test", "name", "High CPU"),
					resource.TestCheckResourceAttr("iaas_alert_rule.test", "resource_type", "instance"),
					resource.TestCheckResourceAttr("iaas_alert_rule.test", "metric", "cpu_pct"),
					resource.TestCheckResourceAttr("iaas_alert_rule.test", "operator", "gt"),
					resource.TestCheckResourceAttr("iaas_alert_rule.test", "threshold", "80"),
					resource.TestCheckResourceAttr("iaas_alert_rule.test", "enabled", "true"),
					resource.TestCheckResourceAttr("iaas_alert_rule.test", "status", "ok"),
					resource.TestCheckResourceAttr("iaas_alert_rule.test", "channel_ids.#", "1"),
					resource.TestCheckTypeSetElemAttr("iaas_alert_rule.test", "channel_ids.*", ch1ID),
				),
			},
			// 2. Import by id.
			{
				ResourceName:      "iaas_alert_rule.test",
				ImportState:       true,
				ImportStateId:     ruleID,
				ImportStateVerify: true,
			},
			// 3. Update: rename, adjust threshold, add second channel.
			{
				Config: updateCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_alert_rule.test", "id", ruleID),
					resource.TestCheckResourceAttr("iaas_alert_rule.test", "name", "High CPU (updated)"),
					resource.TestCheckResourceAttr("iaas_alert_rule.test", "operator", "gte"),
					resource.TestCheckResourceAttr("iaas_alert_rule.test", "threshold", "85"),
					resource.TestCheckResourceAttr("iaas_alert_rule.test", "duration", "300"),
					resource.TestCheckResourceAttr("iaas_alert_rule.test", "reminder_interval", "3600"),
					resource.TestCheckResourceAttr("iaas_alert_rule.test", "enabled", "false"),
					resource.TestCheckResourceAttr("iaas_alert_rule.test", "channel_ids.#", "2"),
					resource.TestCheckTypeSetElemAttr("iaas_alert_rule.test", "channel_ids.*", ch1ID),
					resource.TestCheckTypeSetElemAttr("iaas_alert_rule.test", "channel_ids.*", ch2ID),
				),
			},
		},
	})

	// Assert the create call body.
	creates := srv.Requests("POST", "/alert-rules")
	if len(creates) < 1 {
		t.Fatalf("expected at least 1 POST /alert-rules; got %d", len(creates))
	}
	var createBody map[string]any
	if err := json.Unmarshal(creates[0].Body, &createBody); err != nil {
		t.Fatalf("decoding create body: %v", err)
	}
	if createBody["name"] != "High CPU" {
		t.Errorf("create body[name] = %v; want High CPU", createBody["name"])
	}
	if createBody["metric"] != "cpu_pct" {
		t.Errorf("create body[metric] = %v; want cpu_pct", createBody["metric"])
	}
	if createBody["operator"] != "gt" {
		t.Errorf("create body[operator] = %v; want gt", createBody["operator"])
	}
	if createBody["threshold"] != float64(80) {
		t.Errorf("create body[threshold] = %v; want 80", createBody["threshold"])
	}
	channelIDsRaw, _ := createBody["channel_ids"].([]interface{})
	if len(channelIDsRaw) != 1 {
		t.Errorf("create body[channel_ids] len = %d; want 1", len(channelIDsRaw))
	} else if channelIDsRaw[0] != ch1ID {
		t.Errorf("create body[channel_ids][0] = %v; want %s", channelIDsRaw[0], ch1ID)
	}
	// resource_id should NOT be in the body (null/omitted → global rule).
	if _, present := createBody["resource_id"]; present {
		t.Errorf("create body should not contain resource_id for a global rule, got: %v", createBody["resource_id"])
	}

	// Assert the update call body.
	updates := srv.Requests("PATCH", "/alert-rule/"+ruleID)
	if len(updates) < 1 {
		t.Fatalf("expected at least 1 PATCH /alert-rule/%s; got %d", ruleID, len(updates))
	}
	var updateBody map[string]any
	if err := json.Unmarshal(updates[0].Body, &updateBody); err != nil {
		t.Fatalf("decoding update body: %v", err)
	}
	if updateBody["name"] != "High CPU (updated)" {
		t.Errorf("update body[name] = %v; want High CPU (updated)", updateBody["name"])
	}
	if updateBody["operator"] != "gte" {
		t.Errorf("update body[operator] = %v; want gte", updateBody["operator"])
	}
	if updateBody["threshold"] != float64(85) {
		t.Errorf("update body[threshold] = %v; want 85", updateBody["threshold"])
	}
	updatedChanIDs, _ := updateBody["channel_ids"].([]interface{})
	if len(updatedChanIDs) != 2 {
		t.Errorf("update body[channel_ids] len = %d; want 2", len(updatedChanIDs))
	}
	if updateBody["enabled"] != false {
		t.Errorf("update body[enabled] = %v; want false", updateBody["enabled"])
	}
}

// ---------------------------------------------------------------------------
// TestUnitAlertRule_noChannelsNoChurn - verifies that omitting channel_ids in
// the config does not produce a perpetual diff when the API returns an empty
// channels array. The post-apply refresh must result in an empty plan (null
// channel_ids in config stays null in state after read).
// ---------------------------------------------------------------------------

func TestUnitAlertRule_noChannelsNoChurn(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)

	const ruleID = "eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee"

	store := newARMock()

	// CREATE - POST /alert-rules (no channel_ids in body)
	srv.Handle("POST", "/alert-rules", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		store.mu.Lock()
		defer store.mu.Unlock()
		store.applyBody(body)
		writeJSON(w, http.StatusOK, map[string]any{
			"success":    true,
			"alert_rule": store.ruleObject(ruleID),
		})
	})

	// SHOW - GET /alert-rule/{id} - returns empty channels array
	srv.Handle("GET", "/alert-rule/"+ruleID, func(w http.ResponseWriter, r *http.Request) {
		store.mu.Lock()
		defer store.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"success":    true,
			"alert_rule": store.ruleObject(ruleID),
		})
	})

	// DELETE - DELETE /alert-rule/{id}
	srv.Handle("DELETE", "/alert-rule/"+ruleID, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
		})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	// Config omits channel_ids entirely.
	cfg := providerCfg + `
resource "iaas_alert_rule" "nochurn" {
  name          = "No-channel rule"
  resource_type = "instance"
  metric        = "cpu_pct"
  operator      = "gt"
  threshold     = 90
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				// Apply the config, then confirm no diff on the next plan.
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_alert_rule.nochurn", "id", ruleID),
					resource.TestCheckNoResourceAttr("iaas_alert_rule.nochurn", "channel_ids.#"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
		},
	})
}
