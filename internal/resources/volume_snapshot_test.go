package resources_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/iaas/terraform-provider-iaas/internal/acctest"
)

// ---------------------------------------------------------------------------
// TestAccVolumeSnapshot_basic — LIVE acceptance test (manual staging gate).
//
// Auto-skips unless TF_ACC is set. Requires a reachable panel, billing enabled,
// and a real, already-available volume id supplied via IAAS_TEST_VOLUME_ID; the
// test skips cleanly when it is absent.
// ---------------------------------------------------------------------------
func TestAccVolumeSnapshot_basic(t *testing.T) {
	volumeID := os.Getenv("IAAS_TEST_VOLUME_ID")
	if volumeID == "" {
		t.Skip("TestAccVolumeSnapshot_basic: set IAAS_TEST_VOLUME_ID to run this acceptance test")
	}

	config := fmt.Sprintf(`
resource "iaas_volume_snapshot" "test" {
  volume_id = %q
  name      = "tf-acc-snap"
}
`, volumeID)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("iaas_volume_snapshot.test", "id"),
					resource.TestCheckResourceAttr("iaas_volume_snapshot.test", "status", "available"),
				),
			},
			{
				ResourceName: "iaas_volume_snapshot.test",
				ImportState:  true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					rs := s.RootModule().Resources["iaas_volume_snapshot.test"]
					return rs.Primary.Attributes["volume_id"] + "/" + rs.Primary.ID, nil
				},
				ImportStateVerify: true,
			},
		},
	})
}

// ---------------------------------------------------------------------------
// TestUnitVolumeSnapshot_lifecycle — MOCK-backed lifecycle proof.
//
// Drives the full async CHILD lifecycle against canned responses:
//
//  1. Create — POST /storage/volume/{id}/snapshot returns {queue:{...}}; the
//     parent volume SHOW immediately embeds the new snapshot with
//     status="available", so id-resolution-by-name and the readiness wait both
//     converge on the FIRST poll (no sleep). Asserts the snapshot create body.
//  2. Import — composite id "<volume_id>/<snapshot_id>", verifies state matches.
//  3. Delete — DELETE removes the snapshot from the embedded array, so the next
//     GetVolumeSnapshot 404s and the delete waiter converges.
//
// No update step: every input is RequiresReplace. The IAAS_INSTANCE_POLL_INTERVAL
// seam is set tiny so the waiters cannot hang.
// ---------------------------------------------------------------------------
func TestUnitVolumeSnapshot_lifecycle(t *testing.T) {
	ensureTFBinary(t)
	t.Setenv("IAAS_INSTANCE_POLL_INTERVAL", "1ms")

	srv := acctest.NewMockServer(t)

	const (
		volumeID   = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		snapshotID = "ffffffff-ffff-ffff-ffff-ffffffffffff"
		snapName   = "nightly-1"
	)

	var mu sync.Mutex
	snapshotExists := false // flipped true on create, false on delete

	embeddedSnapshots := func() []any {
		mu.Lock()
		defer mu.Unlock()
		if !snapshotExists {
			return []any{}
		}
		return []any{
			map[string]any{
				"id":     snapshotID,
				"name":   snapName,
				"status": "available",
				"size":   1073741824,
			},
		}
	}

	// Parent volume SHOW — embeds snapshots[] (the snapshot read/poll source).
	srv.Handle("GET", "/storage/volume/"+volumeID, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"volume": map[string]any{
				"id":        volumeID,
				"status":    "available",
				"snapshots": embeddedSnapshots(),
			},
		})
	})

	// CREATE snapshot — returns the QUEUE; flips the snapshot into existence so
	// the next volume SHOW embeds it (available on the first poll).
	srv.Handle("POST", "/storage/volume/"+volumeID+"/snapshot", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		snapshotExists = true
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Snapshot queued.",
			"queue":   map[string]any{"id": "queue-1", "operation": "snapshot", "source_id": snapshotID, "status": "pending"},
		})
	})

	// DELETE snapshot — removes it from the embedded array so the next read 404s.
	srv.Handle("DELETE", "/storage/volume/"+volumeID+"/snapshot/"+snapshotID, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		snapshotExists = false
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Delete queued.",
			"queue":   map[string]any{"id": "queue-2", "status": "pending"},
		})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	createCfg := providerCfg + `
resource "iaas_volume_snapshot" "test" {
  volume_id = "` + volumeID + `"
  name      = "` + snapName + `"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: createCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_volume_snapshot.test", "id", snapshotID),
					resource.TestCheckResourceAttr("iaas_volume_snapshot.test", "volume_id", volumeID),
					resource.TestCheckResourceAttr("iaas_volume_snapshot.test", "name", snapName),
					resource.TestCheckResourceAttr("iaas_volume_snapshot.test", "status", "available"),
					resource.TestCheckResourceAttr("iaas_volume_snapshot.test", "size", "1073741824"),
				),
			},
			{
				ResourceName:            "iaas_volume_snapshot.test",
				ImportState:             true,
				ImportStateId:           volumeID + "/" + snapshotID,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"timeouts"},
			},
		},
	})

	// Assert the snapshot CREATE body carried the name and no server-only fields.
	creates := srv.Requests("POST", "/storage/volume/"+volumeID+"/snapshot")
	if len(creates) == 0 {
		t.Fatal("expected at least one POST .../snapshot")
	}
	var createBody map[string]any
	if err := json.Unmarshal(creates[0].Body, &createBody); err != nil {
		t.Fatalf("decoding snapshot create body: %v", err)
	}
	if createBody["name"] != snapName {
		t.Errorf("snapshot create body name = %v; want %q", createBody["name"], snapName)
	}
	for _, stray := range []string{"id", "status", "size", "volume_id"} {
		if _, present := createBody[stray]; present {
			t.Errorf("snapshot create body must NOT include %q; got %v", stray, createBody)
		}
	}
}
