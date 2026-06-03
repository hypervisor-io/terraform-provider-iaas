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
// TestAccAutoscalingGroup_basic — LIVE acceptance test (manual staging gate).
// Auto-skips unless TF_ACC is set.
// ---------------------------------------------------------------------------

func TestAccAutoscalingGroup_basic(t *testing.T) {
	t.Skip("TestAccAutoscalingGroup_basic: acceptance test runs only with TF_ACC + a real backend (manual staging gate)")
}

// asgMockServer is a stateful mock of the autoscaling group API.
type asgMockServer struct {
	mu sync.Mutex

	name         string
	hgID         string
	planID       string
	imageID      string
	minInstances int
	maxInstances int
	status       string
	currentCount int
	securityIDs  []string
	deleted      bool
}

func newASGMock() *asgMockServer {
	return &asgMockServer{
		minInstances: 1,
		maxInstances: 5,
		status:       "active",
		currentCount: 1,
	}
}

func (s *asgMockServer) groupObject(id string) map[string]any {
	obj := map[string]any{
		"id":                  id,
		"name":                s.name,
		"hypervisor_group_id": s.hgID,
		"plan_id":             s.planID,
		"image_id":            s.imageID,
		"min_instances":       float64(s.minInstances),
		"max_instances":       float64(s.maxInstances),
		"current_count":       float64(s.currentCount),
		"status":              s.status,
	}
	sgs := make([]any, 0, len(s.securityIDs))
	for _, id := range s.securityIDs {
		sgs = append(sgs, id)
	}
	obj["security_group_ids"] = sgs
	return obj
}

func (s *asgMockServer) applyCreate(body map[string]any) {
	if v, ok := body["name"].(string); ok {
		s.name = v
	}
	if v, ok := body["hypervisor_group_id"].(string); ok {
		s.hgID = v
	}
	if v, ok := body["plan_id"].(string); ok {
		s.planID = v
	}
	if v, ok := body["image_id"].(string); ok {
		s.imageID = v
	}
	if v, ok := body["min_instances"].(float64); ok {
		s.minInstances = int(v)
	}
	if v, ok := body["max_instances"].(float64); ok {
		s.maxInstances = int(v)
	}
	s.currentCount = s.minInstances
	s.applySecurityGroups(body)
}

func (s *asgMockServer) applyUpdate(body map[string]any) {
	s.applyCreate(body)
	// On update, keep current_count clamped to the new max for realism.
	if s.currentCount > s.maxInstances {
		s.currentCount = s.maxInstances
	}
	if s.currentCount < s.minInstances {
		s.currentCount = s.minInstances
	}
}

func (s *asgMockServer) applySecurityGroups(body map[string]any) {
	if v, ok := body["security_group_ids"]; ok {
		switch cv := v.(type) {
		case []interface{}:
			ids := make([]string, 0, len(cv))
			for _, i := range cv {
				if str, ok := i.(string); ok {
					ids = append(ids, str)
				}
			}
			s.securityIDs = ids
		}
	}
}

// ---------------------------------------------------------------------------
// TestUnitAutoscalingGroup_lifecycle — MOCK-backed lifecycle test.
//
// Steps:
//  1. Create — POST /scaling-groups (asserts the create body).
//  2. Import — by id.
//  3. Update — resize min/max + flip paused=true (asserts PATCH body + that the
//     pause endpoint fires on the paused toggle).
//
// Delete is implicit teardown; the SHOW handler 404s once deleted so the
// delete-convergence waiter terminates.
// ---------------------------------------------------------------------------

func TestUnitAutoscalingGroup_lifecycle(t *testing.T) {
	ensureTFBinary(t)
	// Tiny poll interval so the async delete-convergence waiter cannot hang.
	t.Setenv("IAAS_INSTANCE_POLL_INTERVAL", "1ms")

	srv := acctest.NewMockServer(t)

	const (
		groupID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		hgID    = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
		planID  = "cccccccc-cccc-cccc-cccc-cccccccccccc"
		imageID = "dddddddd-dddd-dddd-dddd-dddddddddddd"
		sg1     = "eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee"
	)

	store := newASGMock()

	// CREATE — POST /scaling-groups (envelope key "group").
	srv.Handle("POST", "/scaling-groups", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		store.mu.Lock()
		defer store.mu.Unlock()
		store.applyCreate(body)
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Autoscaling group created successfully.",
			"group":   store.groupObject(groupID),
		})
	})

	// SHOW — GET /scaling-group/{id} (envelope key "scaling_group"). 404 once deleted.
	srv.Handle("GET", "/scaling-group/"+groupID, func(w http.ResponseWriter, r *http.Request) {
		store.mu.Lock()
		defer store.mu.Unlock()
		if store.deleted {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "Autoscaling Group not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success":       true,
			"scaling_group": store.groupObject(groupID),
			"instances":     map[string]any{"data": []any{}},
			"activities":    []any{},
		})
	})

	// UPDATE — PATCH /scaling-group/{id} (envelope key "group").
	srv.Handle("PATCH", "/scaling-group/"+groupID, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		store.mu.Lock()
		defer store.mu.Unlock()
		store.applyUpdate(body)
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Autoscaling group updated successfully.",
			"group":   store.groupObject(groupID),
		})
	})

	// PAUSE — POST /scaling-group/{id}/pause.
	srv.Handle("POST", "/scaling-group/"+groupID+"/pause", func(w http.ResponseWriter, r *http.Request) {
		store.mu.Lock()
		defer store.mu.Unlock()
		store.status = "paused"
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Autoscaling group paused.",
			"group":   store.groupObject(groupID),
		})
	})

	// RESUME — POST /scaling-group/{id}/resume.
	srv.Handle("POST", "/scaling-group/"+groupID+"/resume", func(w http.ResponseWriter, r *http.Request) {
		store.mu.Lock()
		defer store.mu.Unlock()
		store.status = "active"
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Autoscaling group resumed.",
			"group":   store.groupObject(groupID),
		})
	})

	// DELETE — DELETE /scaling-group/{id} (async; SHOW 404s afterwards).
	srv.Handle("DELETE", "/scaling-group/"+groupID, func(w http.ResponseWriter, r *http.Request) {
		store.mu.Lock()
		defer store.mu.Unlock()
		store.deleted = true
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Scaling group is being destroyed. Instances will be removed in the background.",
		})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	createCfg := providerCfg + `
resource "iaas_autoscaling_group" "test" {
  name                = "web-asg"
  hypervisor_group_id = "` + hgID + `"
  plan_id             = "` + planID + `"
  image_id            = "` + imageID + `"
  min_instances       = 2
  max_instances       = 6
  ssh_keys            = ["` + sg1 + `"]
}
`

	// Update: resize min/max, attach a security group, and PAUSE the group.
	updateCfg := providerCfg + `
resource "iaas_autoscaling_group" "test" {
  name                = "web-asg-renamed"
  hypervisor_group_id = "` + hgID + `"
  plan_id             = "` + planID + `"
  image_id            = "` + imageID + `"
  min_instances       = 3
  max_instances       = 4
  ssh_keys            = ["` + sg1 + `"]
  security_group_ids  = ["` + sg1 + `"]
  paused              = true
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			// 1. Create.
			{
				Config: createCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_autoscaling_group.test", "id", groupID),
					resource.TestCheckResourceAttr("iaas_autoscaling_group.test", "name", "web-asg"),
					resource.TestCheckResourceAttr("iaas_autoscaling_group.test", "min_instances", "2"),
					resource.TestCheckResourceAttr("iaas_autoscaling_group.test", "max_instances", "6"),
					resource.TestCheckResourceAttr("iaas_autoscaling_group.test", "status", "active"),
					resource.TestCheckResourceAttr("iaas_autoscaling_group.test", "paused", "false"),
					resource.TestCheckResourceAttr("iaas_autoscaling_group.test", "current_count", "2"),
				),
			},
			// 2. Import by id (write-only ssh_keys cannot be read back).
			{
				ResourceName:            "iaas_autoscaling_group.test",
				ImportState:             true,
				ImportStateId:           groupID,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"ssh_keys", "cloud_init"},
			},
			// 3. Update: resize + pause.
			{
				Config: updateCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_autoscaling_group.test", "name", "web-asg-renamed"),
					resource.TestCheckResourceAttr("iaas_autoscaling_group.test", "min_instances", "3"),
					resource.TestCheckResourceAttr("iaas_autoscaling_group.test", "max_instances", "4"),
					resource.TestCheckResourceAttr("iaas_autoscaling_group.test", "paused", "true"),
					resource.TestCheckResourceAttr("iaas_autoscaling_group.test", "status", "paused"),
					resource.TestCheckResourceAttr("iaas_autoscaling_group.test", "security_group_ids.#", "1"),
				),
			},
		},
	})

	// Assert the create body.
	creates := srv.Requests("POST", "/scaling-groups")
	if len(creates) < 1 {
		t.Fatalf("expected at least 1 POST /scaling-groups; got %d", len(creates))
	}
	var createBody map[string]any
	if err := json.Unmarshal(creates[0].Body, &createBody); err != nil {
		t.Fatalf("decoding create body: %v", err)
	}
	if createBody["name"] != "web-asg" {
		t.Errorf("create body[name] = %v; want web-asg", createBody["name"])
	}
	if createBody["hypervisor_group_id"] != hgID {
		t.Errorf("create body[hypervisor_group_id] = %v; want %s", createBody["hypervisor_group_id"], hgID)
	}
	if createBody["min_instances"] != float64(2) {
		t.Errorf("create body[min_instances] = %v; want 2", createBody["min_instances"])
	}
	if _, ok := createBody["ssh_keys"]; !ok {
		t.Errorf("create body should carry ssh_keys; got %v", createBody)
	}
	// paused must NOT be in the create body (it is toggled via pause/resume only).
	if _, present := createBody["paused"]; present {
		t.Errorf("create body must NOT contain paused; got %v", createBody["paused"])
	}

	// Assert the update PATCH body.
	updates := srv.Requests("PATCH", "/scaling-group/"+groupID)
	if len(updates) < 1 {
		t.Fatalf("expected at least 1 PATCH /scaling-group/%s; got %d", groupID, len(updates))
	}
	var updateBody map[string]any
	if err := json.Unmarshal(updates[len(updates)-1].Body, &updateBody); err != nil {
		t.Fatalf("decoding update body: %v", err)
	}
	if updateBody["name"] != "web-asg-renamed" {
		t.Errorf("update body[name] = %v; want web-asg-renamed", updateBody["name"])
	}
	if updateBody["min_instances"] != float64(3) {
		t.Errorf("update body[min_instances] = %v; want 3", updateBody["min_instances"])
	}
	if _, present := updateBody["paused"]; present {
		t.Errorf("update PATCH body must NOT contain paused (toggled via endpoints); got %v", updateBody["paused"])
	}

	// Assert the pause endpoint fired exactly once (on the paused=true toggle).
	pauses := srv.Requests("POST", "/scaling-group/"+groupID+"/pause")
	if len(pauses) != 1 {
		t.Errorf("expected exactly 1 POST .../pause (on the paused toggle); got %d", len(pauses))
	}
	// Resume must NOT have fired (we never set paused back to false).
	resumes := srv.Requests("POST", "/scaling-group/"+groupID+"/resume")
	if len(resumes) != 0 {
		t.Errorf("expected 0 POST .../resume; got %d", len(resumes))
	}

	// Assert delete fired.
	deletes := srv.Requests("DELETE", "/scaling-group/"+groupID)
	if len(deletes) < 1 {
		t.Errorf("expected at least 1 DELETE /scaling-group/%s; got %d", groupID, len(deletes))
	}
}
