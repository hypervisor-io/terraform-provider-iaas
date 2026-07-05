package resources_test

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/hypervisor-io/terraform-provider-iaas/internal/acctest"
)

// ---------------------------------------------------------------------------
// TestAccInstance_basic - LIVE acceptance test (manual staging gate).
//
// Auto-skips unless TF_ACC is set (resource.Test enforces this), so it never
// runs or blocks CI. Requires a reachable panel + IP-locked token via
// IAAS_API_ENDPOINT / IAAS_API_TOKEN (checked by acctest.PreCheck), plus real
// location_id / plan_id / image_id values. It exercises the real two-phase
// create + deploy-task convergence and an import.
//
// The write-only deploy fields (ssh_keys/timezone/cloudcfg) cannot be read back
// from SHOW, so they are listed in ImportStateVerifyIgnore.
// ---------------------------------------------------------------------------
func TestAccInstance_basic(t *testing.T) {
	const config = `
resource "iaas_instance" "test" {
  location_id = "REPLACE-WITH-A-LOCATION-UUID"
  plan_id     = "REPLACE-WITH-A-PLAN-UUID"
  image_id    = "REPLACE-WITH-AN-IMAGE-UUID"
}
`
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("iaas_instance.test", "id"),
					resource.TestCheckResourceAttrSet("iaas_instance.test", "cpu_cores"),
					resource.TestCheckResourceAttrSet("iaas_instance.test", "primary_public_ip"),
				),
			},
			{
				ResourceName:            "iaas_instance.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"ssh_keys", "timezone", "cloudcfg", "timeouts"},
			},
		},
	})
}

// ---------------------------------------------------------------------------
// TestUnitInstance_lifecycle - MOCK-backed lifecycle proof (GOLDEN ASYNC).
//
// Drives the full two-phase async lifecycle against a STATEFUL mock, with no
// live panel. The mock:
//
//   - POST /cloud-service/instances  → {success,instance:{id:"i1",…}}  (phase 1)
//   - POST /instance/i1/deploy       → {success,task_id:"t1"}          (phase 2)
//   - GET  /instance/i1/task/t1      → {task:{status:"completed",…}}   (ready on
//     the FIRST poll → the waiter converges immediately, no sleep)
//   - GET  /instance/i1              → full model; AFTER delete → HTTP 404
//   - PATCH /instance/i1             → renamed display_name (stateful)
//   - DELETE /cloud-service/instances/i1 → {success}; flips deleted so the next
//     SHOW 404s and the delete waiter converges.
//
// The IAAS_INSTANCE_POLL_INTERVAL seam is set tiny so even a multi-poll waiter
// would not sleep meaningfully; combined with completed-on-first-poll the test
// must NOT hang. resource.UnitTest needs a terraform/opentofu binary (see
// ensureTFBinary); absent one, the test is skipped with a clear message.
// ---------------------------------------------------------------------------
func TestUnitInstance_lifecycle(t *testing.T) {
	ensureTFBinary(t)

	// TEST-ONLY poll-interval seam: make convergence instant so the waiter never
	// sleeps and the test cannot hang.
	t.Setenv("IAAS_INSTANCE_POLL_INTERVAL", "1ms")

	srv := acctest.NewMockServer(t)

	const (
		instanceID = "11111111-1111-1111-1111-111111111111"
		taskID     = "t1"
		locationID = "22222222-2222-2222-2222-222222222222"
		planID     = "33333333-3333-3333-3333-333333333333"
		imageID    = "44444444-4444-4444-4444-444444444444"
		sshKeyID   = "55555555-5555-5555-5555-555555555555"
		vncPass    = "secret"
		publicIP   = "1.2.3.4"
		createName = "web-01"
		updateName = "renamed"
	)

	// Stateful server-side fields.
	currentDisplay := createName
	var deleted atomic.Bool

	// showObject builds the BARE instance model returned by GET /instance/{id}.
	showObject := func() map[string]any {
		return map[string]any{
			"id":                instanceID,
			"location_id":       locationID,
			"plan_id":           planID,
			"image_id":          imageID,
			"hostname":          createName,
			"display_name":      currentDisplay,
			"cpu_cores":         2,
			"ram":               2048,
			"status":            1,
			"deployed":          1,
			"vnc_password":      vncPass,
			"primary_public_ip": map[string]any{"ip": publicIP},
			"task_running":      false,
		}
	}

	// PHASE 1 - create the record (sync, returns id under "instance").
	srv.Handle("POST", "/cloud-service/instances", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success":  true,
			"message":  "Instance record created",
			"instance": map[string]any{"id": instanceID, "display_name": currentDisplay},
		})
	})

	// PHASE 2 - deploy the OS (async, returns top-level task_id).
	srv.Handle("POST", "/instance/"+instanceID+"/deploy", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Deploy queued",
			"task_id": taskID,
		})
	})

	// TASK poll - completed on the first poll so the waiter converges instantly.
	srv.Handle("GET", "/instance/"+instanceID+"/task/"+taskID, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"logs": []any{map[string]any{"message": "deploy complete"}},
			"task": map[string]any{"id": taskID, "status": "completed", "progress": 100},
		})
	})

	// SHOW - bare model; 404 once delete has been enqueued (delete waiter signal).
	srv.Handle("GET", "/instance/"+instanceID, func(w http.ResponseWriter, r *http.Request) {
		if deleted.Load() {
			writeJSON(w, http.StatusNotFound, map[string]any{"message": "Not found."})
			return
		}
		writeJSON(w, http.StatusOK, showObject())
	})

	// UPDATE - PATCH metadata (display_name); stateful so SHOW reflects it.
	srv.Handle("PATCH", "/instance/"+instanceID, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if n, ok := body["display_name"].(string); ok {
			currentDisplay = n
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success":  true,
			"message":  "Instance updated",
			"instance": map[string]any{"id": instanceID, "display_name": currentDisplay},
		})
	})

	// DELETE - async enqueue; flips deleted so the next SHOW 404s.
	srv.Handle("DELETE", "/cloud-service/instances/"+instanceID, func(w http.ResponseWriter, r *http.Request) {
		deleted.Store(true)
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Instance deletion queued"})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	createCfg := providerCfg + `
resource "iaas_instance" "test" {
  location_id = "` + locationID + `"
  plan_id     = "` + planID + `"
  image_id    = "` + imageID + `"
  ssh_keys    = ["` + sshKeyID + `"]
}
`
	updateCfg := providerCfg + `
resource "iaas_instance" "test" {
  location_id  = "` + locationID + `"
  plan_id      = "` + planID + `"
  image_id     = "` + imageID + `"
  ssh_keys     = ["` + sshKeyID + `"]
  display_name = "` + updateName + `"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			// Create + two-phase deploy + waiter convergence + read-back.
			{
				Config: createCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_instance.test", "id", instanceID),
					resource.TestCheckResourceAttr("iaas_instance.test", "location_id", locationID),
					resource.TestCheckResourceAttr("iaas_instance.test", "plan_id", planID),
					resource.TestCheckResourceAttr("iaas_instance.test", "image_id", imageID),
					resource.TestCheckResourceAttr("iaas_instance.test", "cpu_cores", "2"),
					resource.TestCheckResourceAttr("iaas_instance.test", "ram", "2048"),
					resource.TestCheckResourceAttr("iaas_instance.test", "deployed", "true"),
					resource.TestCheckResourceAttr("iaas_instance.test", "status", "1"),
					resource.TestCheckResourceAttr("iaas_instance.test", "primary_public_ip", publicIP),
					resource.TestCheckResourceAttr("iaas_instance.test", "vnc_password", vncPass),
					resource.TestCheckResourceAttr("iaas_instance.test", "ssh_keys.0", sshKeyID),
				),
			},
			// Import - write-only deploy fields can't be read back, so ignore them.
			{
				ResourceName:            "iaas_instance.test",
				ImportState:             true,
				ImportStateId:           instanceID,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"ssh_keys", "timezone", "cloudcfg", "timeouts"},
			},
			// Update - rename display_name (metadata PATCH, sync).
			{
				Config: updateCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_instance.test", "id", instanceID),
					resource.TestCheckResourceAttr("iaas_instance.test", "display_name", updateName),
				),
			},
		},
	})

	// Assert the phase-1 create body carried location_id + plan_id (and NOT the
	// deploy-only fields).
	creates := srv.Requests("POST", "/cloud-service/instances")
	if len(creates) == 0 {
		t.Fatal("expected at least one POST /cloud-service/instances")
	}
	var createBody map[string]any
	if err := json.Unmarshal(creates[0].Body, &createBody); err != nil {
		t.Fatalf("decoding create body: %v", err)
	}
	if createBody["location_id"] != locationID {
		t.Errorf("create body location_id = %v; want %q", createBody["location_id"], locationID)
	}
	if createBody["plan_id"] != planID {
		t.Errorf("create body plan_id = %v; want %q", createBody["plan_id"], planID)
	}
	for _, stray := range []string{"image_id", "ssh_keys", "ssh_key_id", "timezone", "cloudcfg"} {
		if _, present := createBody[stray]; present {
			t.Errorf("phase-1 create body must NOT include %q (deploy-only); got %v", stray, createBody)
		}
	}

	// Assert the phase-2 deploy body carried image_id and ssh_keys (the array
	// field - NOT ssh_key_id).
	deploys := srv.Requests("POST", "/instance/"+instanceID+"/deploy")
	if len(deploys) == 0 {
		t.Fatalf("expected at least one POST /instance/%s/deploy", instanceID)
	}
	var deployBody map[string]any
	if err := json.Unmarshal(deploys[0].Body, &deployBody); err != nil {
		t.Fatalf("decoding deploy body: %v", err)
	}
	if deployBody["image_id"] != imageID {
		t.Errorf("deploy body image_id = %v; want %q", deployBody["image_id"], imageID)
	}
	keys, ok := deployBody["ssh_keys"].([]any)
	if !ok || len(keys) != 1 || keys[0] != sshKeyID {
		t.Errorf("deploy body ssh_keys = %v; want [%q]", deployBody["ssh_keys"], sshKeyID)
	}
	if _, present := deployBody["ssh_key_id"]; present {
		t.Errorf("deploy body must use ssh_keys (array), NOT ssh_key_id; got %v", deployBody)
	}
}
