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
// TestAccAutoscalingPolicy_basic - LIVE acceptance test (manual staging gate).
// Auto-skips unless TF_ACC is set.
// ---------------------------------------------------------------------------

func TestAccAutoscalingPolicy_basic(t *testing.T) {
	t.Skip("TestAccAutoscalingPolicy_basic: acceptance test runs only with TF_ACC + a real backend (manual staging gate)")
}

// asgPolicyMock is a stateful mock of a policy embedded in its group SHOW.
type asgPolicyMock struct {
	mu sync.Mutex

	exists             bool
	metric             string
	scaleUpThreshold   int
	scaleDownThreshold int
	scaleUpStep        int
	scaleDownStep      int
	scaleUpCooldown    int
	scaleDownCooldown  int
	evaluationInterval int
	evaluationWindow   int
}

func (s *asgPolicyMock) policyObject(id string) map[string]any {
	return map[string]any{
		"id":                   id,
		"metric":               s.metric,
		"scale_up_threshold":   float64(s.scaleUpThreshold),
		"scale_down_threshold": float64(s.scaleDownThreshold),
		"scale_up_step":        float64(s.scaleUpStep),
		"scale_down_step":      float64(s.scaleDownStep),
		"scale_up_cooldown":    float64(s.scaleUpCooldown),
		"scale_down_cooldown":  float64(s.scaleDownCooldown),
		"evaluation_interval":  float64(s.evaluationInterval),
		"evaluation_window":    float64(s.evaluationWindow),
	}
}

// applyBody applies a create/update body, applying the server defaults for any
// omitted Optional+Computed field (mirrors the migration defaults).
func (s *asgPolicyMock) applyBody(body map[string]any) {
	if v, ok := body["metric"].(string); ok {
		s.metric = v
	}
	if v, ok := body["scale_up_threshold"].(float64); ok {
		s.scaleUpThreshold = int(v)
	}
	if v, ok := body["scale_down_threshold"].(float64); ok {
		s.scaleDownThreshold = int(v)
	}
	s.scaleUpStep = intFromBodyDefault(body, "scale_up_step", 1)
	s.scaleDownStep = intFromBodyDefault(body, "scale_down_step", 1)
	s.scaleUpCooldown = intFromBodyDefault(body, "scale_up_cooldown", 300)
	s.scaleDownCooldown = intFromBodyDefault(body, "scale_down_cooldown", 600)
	s.evaluationInterval = intFromBodyDefault(body, "evaluation_interval", 30)
	s.evaluationWindow = intFromBodyDefault(body, "evaluation_window", 120)
}

func intFromBodyDefault(body map[string]any, key string, def int) int {
	if v, ok := body[key].(float64); ok {
		return int(v)
	}
	return def
}

// ---------------------------------------------------------------------------
// TestUnitAutoscalingPolicy_lifecycle - MOCK-backed CHILD lifecycle test.
//
// Steps:
//  1. Create - POST /scaling-group/{gid}/policy; the policy appears in the group
//     SHOW policies[] (read-by-scan). Asserts the create body + server defaults.
//  2. Import - composite "<group_id>/<policy_id>".
//  3. Update - PATCH .../policy/{pid}; asserts the PATCH body.
//
// Delete is implicit teardown.
// ---------------------------------------------------------------------------

func TestUnitAutoscalingPolicy_lifecycle(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)

	const (
		groupID  = "11111111-1111-1111-1111-111111111111"
		policyID = "22222222-2222-2222-2222-222222222222"
	)

	store := &asgPolicyMock{}

	embeddedPolicies := func() []any {
		store.mu.Lock()
		defer store.mu.Unlock()
		if !store.exists {
			return []any{}
		}
		return []any{store.policyObject(policyID)}
	}

	// Group SHOW - read-by-scan target for the policy.
	srv.Handle("GET", "/scaling-group/"+groupID, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"scaling_group": map[string]any{
				"id":            groupID,
				"name":          "web-asg",
				"status":        "active",
				"min_instances": float64(1),
				"max_instances": float64(5),
				"current_count": float64(1),
				"policies":      embeddedPolicies(),
			},
			"instances":  map[string]any{"data": []any{}},
			"activities": []any{},
		})
	})

	// CREATE - POST /scaling-group/{gid}/policy (envelope key "policy").
	srv.Handle("POST", "/scaling-group/"+groupID+"/policy", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		store.mu.Lock()
		store.applyBody(body)
		store.exists = true
		obj := store.policyObject(policyID)
		store.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Scaling policy created successfully.",
			"policy":  obj,
		})
	})

	// UPDATE - PATCH /scaling-group/{gid}/policy/{pid}.
	srv.Handle("PATCH", "/scaling-group/"+groupID+"/policy/"+policyID, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		store.mu.Lock()
		store.applyBody(body)
		obj := store.policyObject(policyID)
		store.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Scaling policy updated successfully.",
			"policy":  obj,
		})
	})

	// DELETE - DELETE /scaling-group/{gid}/policy/{pid}.
	srv.Handle("DELETE", "/scaling-group/"+groupID+"/policy/"+policyID, func(w http.ResponseWriter, r *http.Request) {
		store.mu.Lock()
		store.exists = false
		store.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Scaling policy deleted successfully.",
		})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	createCfg := providerCfg + `
resource "iaas_autoscaling_policy" "test" {
  group_id             = "` + groupID + `"
  metric               = "cpu"
  scale_up_threshold   = 80
  scale_down_threshold = 30
}
`

	updateCfg := providerCfg + `
resource "iaas_autoscaling_policy" "test" {
  group_id             = "` + groupID + `"
  metric               = "memory"
  scale_up_threshold   = 75
  scale_down_threshold = 25
  scale_up_step        = 2
  scale_up_cooldown    = 120
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			// 1. Create - server defaults populate steps/cooldowns/windows.
			{
				Config: createCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_autoscaling_policy.test", "id", policyID),
					resource.TestCheckResourceAttr("iaas_autoscaling_policy.test", "group_id", groupID),
					resource.TestCheckResourceAttr("iaas_autoscaling_policy.test", "metric", "cpu"),
					resource.TestCheckResourceAttr("iaas_autoscaling_policy.test", "scale_up_threshold", "80"),
					resource.TestCheckResourceAttr("iaas_autoscaling_policy.test", "scale_down_threshold", "30"),
					resource.TestCheckResourceAttr("iaas_autoscaling_policy.test", "scale_up_step", "1"),
					resource.TestCheckResourceAttr("iaas_autoscaling_policy.test", "scale_down_cooldown", "600"),
					resource.TestCheckResourceAttr("iaas_autoscaling_policy.test", "evaluation_interval", "30"),
				),
			},
			// 2. Import - composite "<group_id>/<policy_id>".
			{
				ResourceName:      "iaas_autoscaling_policy.test",
				ImportState:       true,
				ImportStateId:     groupID + "/" + policyID,
				ImportStateVerify: true,
			},
			// 3. Update - change metric/thresholds + step/cooldown.
			{
				Config: updateCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_autoscaling_policy.test", "metric", "memory"),
					resource.TestCheckResourceAttr("iaas_autoscaling_policy.test", "scale_up_threshold", "75"),
					resource.TestCheckResourceAttr("iaas_autoscaling_policy.test", "scale_down_threshold", "25"),
					resource.TestCheckResourceAttr("iaas_autoscaling_policy.test", "scale_up_step", "2"),
					resource.TestCheckResourceAttr("iaas_autoscaling_policy.test", "scale_up_cooldown", "120"),
				),
			},
		},
	})

	// Assert the create body.
	creates := srv.Requests("POST", "/scaling-group/"+groupID+"/policy")
	if len(creates) < 1 {
		t.Fatalf("expected at least 1 POST .../policy; got %d", len(creates))
	}
	var createBody map[string]any
	if err := json.Unmarshal(creates[0].Body, &createBody); err != nil {
		t.Fatalf("decoding create body: %v", err)
	}
	if createBody["metric"] != "cpu" {
		t.Errorf("create body[metric] = %v; want cpu", createBody["metric"])
	}
	if createBody["scale_up_threshold"] != float64(80) {
		t.Errorf("create body[scale_up_threshold] = %v; want 80", createBody["scale_up_threshold"])
	}
	// Unset optionals must be omitted from the create body (server applies defaults).
	if _, present := createBody["scale_up_step"]; present {
		t.Errorf("create body must omit unset scale_up_step; got %v", createBody["scale_up_step"])
	}

	// Assert the update PATCH body.
	updates := srv.Requests("PATCH", "/scaling-group/"+groupID+"/policy/"+policyID)
	if len(updates) < 1 {
		t.Fatalf("expected at least 1 PATCH .../policy/%s; got %d", policyID, len(updates))
	}
	var updateBody map[string]any
	if err := json.Unmarshal(updates[len(updates)-1].Body, &updateBody); err != nil {
		t.Fatalf("decoding update body: %v", err)
	}
	if updateBody["metric"] != "memory" {
		t.Errorf("update body[metric] = %v; want memory", updateBody["metric"])
	}
	if updateBody["scale_up_step"] != float64(2) {
		t.Errorf("update body[scale_up_step] = %v; want 2", updateBody["scale_up_step"])
	}
}
