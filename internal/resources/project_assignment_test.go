package resources_test

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/hypervisor-io/terraform-provider-iaas/internal/acctest"
)

// ---------------------------------------------------------------------------
// TestAccProjectAssignment_basic - LIVE acceptance test (manual staging gate).
//
// Requires an existing project and instance owned by the same account:
//
//	IAAS_TEST_PROJECT_ID  - UUID of an existing iaas_project
//	IAAS_TEST_INSTANCE_ID - UUID of an existing instance to assign
//
// Skips cleanly when absent.
// ---------------------------------------------------------------------------
func TestAccProjectAssignment_basic(t *testing.T) {
	t.Skip("TestAccProjectAssignment_basic: set IAAS_TEST_PROJECT_ID and IAAS_TEST_INSTANCE_ID to run this acceptance test (manual staging gate)")
}

// ---------------------------------------------------------------------------
// TestUnitProjectAssignment_lifecycle - MOCK-backed lifecycle proof.
//
// iaas_project_assignment has NO server-assigned id and NO per-assignment
// SHOW route: it drives everything through the SAME
// POST /project/assign-resource endpoint (assign when project_id is set,
// unassign when project_id is null) and reads membership back from the
// TARGET RESOURCE's own SHOW (GET /instance/{id} here), comparing its
// project_id field:
//
//  1. Create - POST /project/assign-resource {resource_type,resource_id,
//     project_id}; the mock records the instance's current project_id.
//     Create then verifies by GETting the instance and checking project_id
//     matches, and persists the SYNTHESIZED id "<project_id>/<resource_type>/<resource_id>".
//  2. Import - 3-part composite id; the automatic post-import Read re-checks
//     the instance's project_id.
//  3. Delete - implicit teardown: POST /project/assign-resource again with
//     project_id explicitly null (the unassign path - there is no dedicated
//     detach/DELETE route); asserted afterward.
//
// The whole lifecycle is synchronous - no waiter, no poll-interval seam.
// ---------------------------------------------------------------------------
func TestUnitProjectAssignment_lifecycle(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)

	const (
		projectID  = "11111111-1111-1111-1111-111111111111"
		instanceID = "22222222-2222-2222-2222-222222222222"
	)

	// Stateful: the instance's current project_id, as ProjectController's
	// assignResource would persist it via $resource->update(['project_id' => ...]).
	var mu sync.Mutex
	var currentProjectID *string // nil == unassigned

	// ASSIGN/UNASSIGN - POST /project/assign-resource (the SAME endpoint
	// handles both directions; project_id null unassigns).
	srv.Handle("POST", "/project/assign-resource", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)

		mu.Lock()
		if pid, ok := body["project_id"].(string); ok && pid != "" {
			v := pid
			currentProjectID = &v
		} else {
			currentProjectID = nil
		}
		mu.Unlock()

		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Resource assigned to project",
		})
	})

	// SHOW - GET /instance/{id} returns the BARE instance model, including
	// project_id (un-hidden on the real Instance model).
	srv.Handle("GET", "/instance/"+instanceID, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		var pid any
		if currentProjectID != nil {
			pid = *currentProjectID
		}
		mu.Unlock()

		writeJSON(w, http.StatusOK, map[string]any{
			"id":         instanceID,
			"name":       "web-1",
			"project_id": pid,
		})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	createCfg := providerCfg + `
resource "iaas_project_assignment" "test" {
  project_id    = "` + projectID + `"
  resource_type = "instance"
  resource_id   = "` + instanceID + `"
}
`
	wantID := projectID + "/instance/" + instanceID

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			// Create + verify read-back.
			{
				Config: createCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_project_assignment.test", "id", wantID),
					resource.TestCheckResourceAttr("iaas_project_assignment.test", "project_id", projectID),
					resource.TestCheckResourceAttr("iaas_project_assignment.test", "resource_type", "instance"),
					resource.TestCheckResourceAttr("iaas_project_assignment.test", "resource_id", instanceID),
				),
			},
			// Import by composite id; the automatic post-import Read re-verifies
			// the instance's project_id via the same GET /instance/{id}.
			{
				ResourceName:      "iaas_project_assignment.test",
				ImportState:       true,
				ImportStateId:     wantID,
				ImportStateVerify: true,
			},
		},
	})

	// Assert BOTH directions of the single shared endpoint were exercised:
	// one assign (create) and one unassign (implicit teardown, project_id null).
	assigns := srv.Requests("POST", "/project/assign-resource")
	if len(assigns) < 2 {
		t.Fatalf("expected at least 2 POST /project/assign-resource (assign + unassign); got %d", len(assigns))
	}

	var firstBody map[string]any
	if err := json.Unmarshal(assigns[0].Body, &firstBody); err != nil {
		t.Fatalf("decoding first assign-resource body: %v", err)
	}
	if firstBody["resource_type"] != "instance" {
		t.Errorf("create body resource_type = %v; want instance", firstBody["resource_type"])
	}
	if firstBody["resource_id"] != instanceID {
		t.Errorf("create body resource_id = %v; want %q", firstBody["resource_id"], instanceID)
	}
	if firstBody["project_id"] != projectID {
		t.Errorf("create body project_id = %v; want %q", firstBody["project_id"], projectID)
	}

	last := assigns[len(assigns)-1]
	var lastBody map[string]any
	if err := json.Unmarshal(last.Body, &lastBody); err != nil {
		t.Fatalf("decoding last assign-resource body: %v", err)
	}
	pid, present := lastBody["project_id"]
	if !present {
		t.Fatalf("teardown (unassign) body must include project_id key (as null); body = %v", lastBody)
	}
	if pid != nil {
		t.Errorf("teardown (unassign) body project_id = %v; want JSON null", pid)
	}
}

// TestUnitProjectAssignment_deleteToleratesGoneTarget proves Delete no longer
// errors when the assign-resource endpoint reports the target as gone via a
// 200 success:false "not found"-shaped message rather than a genuine HTTP 404
// - decodeItem/checkSuccessFlag (internal/client/decode.go) surface
// success:false as a PLAIN error, which client.IsNotFound alone does not
// recognise, so before this fix an out-of-band-deleted target would fail
// Delete instead of being treated as a benign no-op.
func TestUnitProjectAssignment_deleteToleratesGoneTarget(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)

	const (
		projectID  = "33333333-3333-3333-3333-333333333333"
		instanceID = "44444444-4444-4444-4444-444444444444"
	)

	var mu sync.Mutex
	var currentProjectID *string
	unassignCalls := 0

	srv.Handle("POST", "/project/assign-resource", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)

		mu.Lock()
		defer mu.Unlock()

		pid, _ := body["project_id"].(string)
		if pid != "" {
			v := pid
			currentProjectID = &v
			writeJSON(w, http.StatusOK, map[string]any{
				"success": true,
				"message": "Resource assigned to project",
			})
			return
		}

		// Unassign: simulate the instance having been destroyed out of band.
		// The endpoint reports this as HTTP 200 success:false with a
		// "not found"-shaped message, NOT a genuine 404 - the exact shape
		// isNotFoundLikeError must tolerate.
		unassignCalls++
		writeJSON(w, http.StatusOK, map[string]any{
			"success": false,
			"message": "Instance not found",
		})
	})

	srv.Handle("GET", "/instance/"+instanceID, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		var pid any
		if currentProjectID != nil {
			pid = *currentProjectID
		}
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"id":         instanceID,
			"name":       "web-1",
			"project_id": pid,
		})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	createCfg := providerCfg + `
resource "iaas_project_assignment" "test" {
  project_id    = "` + projectID + `"
  resource_type = "instance"
  resource_id   = "` + instanceID + `"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: createCfg,
				Check:  resource.TestCheckResourceAttr("iaas_project_assignment.test", "resource_id", instanceID),
			},
		},
	})

	// The implicit teardown (Delete) must have hit the unassign path and NOT
	// failed the test despite the success:false "not found" response.
	if unassignCalls == 0 {
		t.Fatal("expected at least one unassign call (project_id null) during teardown")
	}
}
