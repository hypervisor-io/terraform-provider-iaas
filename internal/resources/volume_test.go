package resources_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/iaas/terraform-provider-iaas/internal/acctest"
)

// ---------------------------------------------------------------------------
// TestAccVolume_basic — LIVE acceptance test (manual staging gate).
//
// Auto-skips unless TF_ACC is set (resource.Test enforces this). Requires a
// reachable panel + IP-locked token (IAAS_API_ENDPOINT / IAAS_API_TOKEN), with
// billing enabled, plus real UUIDs supplied via the env vars below; the test
// skips cleanly when either var is absent so a bare TF_ACC=1 run does not fail.
//
//	IAAS_TEST_VOLUME_PLAN_ID — UUID of an enabled volume plan
//	IAAS_TEST_HG_ID          — UUID of a volume-enabled hypervisor group
//
// ---------------------------------------------------------------------------
func TestAccVolume_basic(t *testing.T) {
	planID := os.Getenv("IAAS_TEST_VOLUME_PLAN_ID")
	hgID := os.Getenv("IAAS_TEST_HG_ID")
	if planID == "" || hgID == "" {
		t.Skip("TestAccVolume_basic: set IAAS_TEST_VOLUME_PLAN_ID and IAAS_TEST_HG_ID to run this acceptance test")
	}

	config := fmt.Sprintf(`
resource "iaas_volume" "test" {
  name                = "tf-acc-vol"
  volume_plan_id      = %q
  hypervisor_group_id = %q
}
`, planID, hgID)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("iaas_volume.test", "id"),
					resource.TestCheckResourceAttr("iaas_volume.test", "status", "available"),
					resource.TestCheckResourceAttrSet("iaas_volume.test", "size"),
				),
			},
			{
				ResourceName:      "iaas_volume.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// ---------------------------------------------------------------------------
// TestUnitVolume_lifecycle — MOCK-backed lifecycle proof.
//
// Drives the full async resource lifecycle against canned API responses with no
// live panel:
//
//  1. Create — POST /storage/volumes returns {volume:{id,status:"pending"}};
//     the SHOW then immediately returns status="available" (ready on the FIRST
//     poll → the waiter converges instantly, no sleep). Asserts the create body.
//  2. Import — by UUID, verifies state matches.
//  3. Update — resize to a larger plan (PATCH /resize) AND attach to an instance
//     (POST /attach). Asserts BOTH the resize and attach bodies fired.
//  4. Delete — implicit teardown; DELETE soft-deletes and the next SHOW 404s.
//
// The IAAS_INSTANCE_POLL_INTERVAL seam (shared with instance.go's pollInterval)
// is set tiny so the waiter cannot hang; combined with available-on-first-poll
// the test must NOT sleep. resource.UnitTest needs a terraform/opentofu binary
// (see ensureTFBinary); absent one, the test is skipped with a clear message.
// ---------------------------------------------------------------------------
func TestUnitVolume_lifecycle(t *testing.T) {
	ensureTFBinary(t)

	// TEST-ONLY poll-interval seam (shared with instance.go): instant convergence.
	t.Setenv("IAAS_INSTANCE_POLL_INTERVAL", "1ms")

	srv := acctest.NewMockServer(t)

	const (
		volumeID   = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		planID     = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
		planID2    = "cccccccc-cccc-cccc-cccc-cccccccccccc"
		groupID    = "dddddddd-dddd-dddd-dddd-dddddddddddd"
		instanceID = "eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee"
		volName    = "data-vol"
	)

	// Stateful server-side fields mutated by attach/resize.
	var mu sync.Mutex
	currentStatus := "available"
	currentSize := 50
	currentPlan := planID
	currentInstance := "" // empty = detached
	currentDev := ""
	deleted := false

	showObject := func() map[string]any {
		mu.Lock()
		defer mu.Unlock()
		obj := map[string]any{
			"id":                  volumeID,
			"name":                volName,
			"volume_plan_id":      currentPlan,
			"hypervisor_group_id": groupID,
			"status":              currentStatus,
			"deployed":            1,
			"size":                currentSize,
			"path":                "ceph-pool/" + volumeID,
		}
		if currentInstance != "" {
			obj["instance_id"] = currentInstance
			obj["dev"] = currentDev
		} else {
			obj["instance_id"] = nil
			obj["dev"] = nil
		}
		return obj
	}

	// CREATE — record the row; first SHOW already reports "available" so the
	// waiter converges on the first poll.
	srv.Handle("POST", "/storage/volumes", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Volume creation initiated.",
			"volume": map[string]any{
				"id":             volumeID,
				"name":           volName,
				"status":         "pending",
				"deployed":       0,
				"size":           currentSize,
				"volume_plan_id": planID,
			},
		})
	})

	// SHOW — 404 once delete has been enqueued.
	srv.Handle("GET", "/storage/volume/"+volumeID, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gone := deleted
		mu.Unlock()
		if gone {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "Volume not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "volume": showObject()})
	})

	// RESIZE — PATCH /resize; mutates plan + size.
	srv.Handle("PATCH", "/storage/volume/"+volumeID+"/resize", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		if p, ok := body["volume_plan_id"].(string); ok {
			currentPlan = p
		}
		currentSize = 100
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "is_downgrade": false, "volume": showObject()})
	})

	// ATTACH — POST /attach; flips status→attached, sets instance + dev.
	srv.Handle("POST", "/storage/volume/"+volumeID+"/attach", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		if iid, ok := body["instance_id"].(string); ok {
			currentInstance = iid
		}
		currentStatus = "attached"
		currentDev = "xvda"
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "volume": showObject()})
	})

	// DETACH — POST /detach; flips status→available, clears instance + dev.
	srv.Handle("POST", "/storage/volume/"+volumeID+"/detach", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		currentInstance = ""
		currentDev = ""
		currentStatus = "available"
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "volume": showObject()})
	})

	// DELETE — soft-delete; the next SHOW 404s.
	srv.Handle("DELETE", "/storage/volume/"+volumeID, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		deleted = true
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Volume deletion initiated."})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	createCfg := providerCfg + `
resource "iaas_volume" "test" {
  name                = "` + volName + `"
  volume_plan_id      = "` + planID + `"
  hypervisor_group_id = "` + groupID + `"
}
`
	// Update: resize to planID2 AND attach to an instance.
	updateCfg := providerCfg + `
resource "iaas_volume" "test" {
  name                = "` + volName + `"
  volume_plan_id      = "` + planID2 + `"
  hypervisor_group_id = "` + groupID + `"
  instance_id         = "` + instanceID + `"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			// Create + read-back (async wait converges immediately).
			{
				Config: createCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_volume.test", "id", volumeID),
					resource.TestCheckResourceAttr("iaas_volume.test", "name", volName),
					resource.TestCheckResourceAttr("iaas_volume.test", "volume_plan_id", planID),
					resource.TestCheckResourceAttr("iaas_volume.test", "hypervisor_group_id", groupID),
					resource.TestCheckResourceAttr("iaas_volume.test", "status", "available"),
					resource.TestCheckResourceAttr("iaas_volume.test", "size", "50"),
					resource.TestCheckResourceAttr("iaas_volume.test", "deployed", "true"),
				),
			},
			// Import the existing resource and verify state matches.
			{
				ResourceName:      "iaas_volume.test",
				ImportState:       true,
				ImportStateId:     volumeID,
				ImportStateVerify: true,
				// timeouts is config-only (not returned by SHOW); ignore on import.
				ImportStateVerifyIgnore: []string{"timeouts"},
			},
			// Update: resize (plan change) + attach (instance_id set).
			{
				Config: updateCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_volume.test", "volume_plan_id", planID2),
					resource.TestCheckResourceAttr("iaas_volume.test", "size", "100"),
					resource.TestCheckResourceAttr("iaas_volume.test", "instance_id", instanceID),
					resource.TestCheckResourceAttr("iaas_volume.test", "status", "attached"),
					resource.TestCheckResourceAttr("iaas_volume.test", "dev", "xvda"),
				),
			},
		},
	})

	// Assert the CREATE body carried the required fields and NOT server-only ones.
	creates := srv.Requests("POST", "/storage/volumes")
	if len(creates) == 0 {
		t.Fatal("expected at least one POST /storage/volumes")
	}
	var createBody map[string]any
	if err := json.Unmarshal(creates[0].Body, &createBody); err != nil {
		t.Fatalf("decoding create body: %v", err)
	}
	if createBody["name"] != volName {
		t.Errorf("create body name = %v; want %q", createBody["name"], volName)
	}
	if createBody["volume_plan_id"] != planID {
		t.Errorf("create body volume_plan_id = %v; want %q", createBody["volume_plan_id"], planID)
	}
	if createBody["hypervisor_group_id"] != groupID {
		t.Errorf("create body hypervisor_group_id = %v; want %q", createBody["hypervisor_group_id"], groupID)
	}
	for _, stray := range []string{"id", "status", "size", "dev", "deployed", "path"} {
		if _, present := createBody[stray]; present {
			t.Errorf("create body must NOT include %q; got %v", stray, createBody)
		}
	}

	// Assert the RESIZE fired with the new plan.
	resizes := srv.Requests("PATCH", "/storage/volume/"+volumeID+"/resize")
	if len(resizes) != 1 {
		t.Fatalf("expected exactly 1 PATCH /resize, got %d", len(resizes))
	}
	var resizeBody map[string]any
	if err := json.Unmarshal(resizes[0].Body, &resizeBody); err != nil {
		t.Fatalf("decoding resize body: %v", err)
	}
	if resizeBody["volume_plan_id"] != planID2 {
		t.Errorf("resize body volume_plan_id = %v; want %q", resizeBody["volume_plan_id"], planID2)
	}

	// Assert the ATTACH fired with the instance id.
	attaches := srv.Requests("POST", "/storage/volume/"+volumeID+"/attach")
	if len(attaches) != 1 {
		t.Fatalf("expected exactly 1 POST /attach, got %d", len(attaches))
	}
	var attachBody map[string]any
	if err := json.Unmarshal(attaches[0].Body, &attachBody); err != nil {
		t.Fatalf("decoding attach body: %v", err)
	}
	if attachBody["instance_id"] != instanceID {
		t.Errorf("attach body instance_id = %v; want %q", attachBody["instance_id"], instanceID)
	}
}
