package resources_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/iaas/terraform-provider-iaas/internal/acctest"
)

// imageObject builds a serialized image object matching the shape returned by
// both POST /images (under the "image" key) and GET /images (paginator
// "data" items) — both carry the same base columns per ImageService/Image
// model (id, name, instance_id, cloudinit, type, status, size).
func imageObject(id, name, instanceID string, cloudinit int, imgType, status string, size any) map[string]any {
	return map[string]any{
		"id":          id,
		"name":        name,
		"instance_id": instanceID,
		"cloudinit":   cloudinit,
		"type":        imgType,
		"status":      status,
		"size":        size,
		"purpose":     "user",
	}
}

// TestUnitImage_lifecycle drives create (async capture convergence) → import →
// delete against a mock. There is no update endpoint for images (every input
// is RequiresReplace), so — unlike the ssh_key/user_script lifecycle tests —
// there is no update step; delete is implicit teardown after the import step,
// matching the ssh_key golden pattern.
func TestUnitImage_lifecycle(t *testing.T) {
	ensureTFBinary(t)

	// TEST-ONLY poll-interval seam shared with the instance/volume_snapshot
	// waiters (see instance.go's pollInterval): make convergence instant so the
	// waiter never sleeps and the test cannot hang.
	t.Setenv("IAAS_INSTANCE_POLL_INTERVAL", "1ms")

	srv := acctest.NewMockServer(t)

	const (
		imageID    = "66666666-6666-6666-6666-666666666666"
		instanceID = "77777777-7777-7777-7777-777777777777"
		imageName  = "web-prod-2024-05"
	)

	// CREATE — POST /images. Image capture is async on the real API (status
	// starts "creating"), but the mock reports "available" from the very first
	// GET /images poll so the waiter converges on its first check (no sleep),
	// exactly like TestUnitInstance_lifecycle's task poll.
	srv.Handle("POST", "/images", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Image creation started.",
			"image":   imageObject(imageID, imageName, instanceID, 1, "linux", "creating", nil),
		})
	})

	// READ — no SHOW route; the resource lists and matches by id. Reports
	// "available" immediately so the create-time waiter converges on the first
	// poll.
	srv.Handle("GET", "/images", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"current_page": 1,
			"per_page":     25,
			"total":        1,
			"data": []any{
				imageObject(imageID, imageName, instanceID, 1, "linux", "available", float64(21474836480)),
			},
		})
	})

	// DELETE — DELETE /image/{id} (singular). Deletion is synchronous.
	srv.Handle("DELETE", "/image/"+imageID, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Image deleted successfully."})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())
	createCfg := providerCfg + `
resource "iaas_image" "test" {
  instance_id = "` + instanceID + `"
  name        = "` + imageName + `"
  cloudinit   = true
  type        = "linux"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			// Create + async capture convergence + read-back.
			{
				Config: createCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_image.test", "id", imageID),
					resource.TestCheckResourceAttr("iaas_image.test", "name", imageName),
					resource.TestCheckResourceAttr("iaas_image.test", "instance_id", instanceID),
					resource.TestCheckResourceAttr("iaas_image.test", "cloudinit", "true"),
					resource.TestCheckResourceAttr("iaas_image.test", "type", "linux"),
					resource.TestCheckResourceAttr("iaas_image.test", "status", "available"),
					resource.TestCheckResourceAttr("iaas_image.test", "size", "21474836480"),
				),
			},
			// Import — verify state matches. timeouts is a provider-side-only
			// value with no API equivalent, so it's ignored like instance.go's
			// import step.
			{
				ResourceName:            "iaas_image.test",
				ImportState:             true,
				ImportStateId:           imageID,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"timeouts"},
			},
		},
	})

	// Assert the create request sent instance_id/name/cloudinit/type.
	creates := srv.Requests("POST", "/images")
	if len(creates) == 0 {
		t.Fatal("expected at least one POST /images")
	}
	var createBody map[string]any
	if err := json.Unmarshal(creates[0].Body, &createBody); err != nil {
		t.Fatalf("decoding create body: %v", err)
	}
	if createBody["instance_id"] != instanceID {
		t.Errorf("create body instance_id = %v; want %q", createBody["instance_id"], instanceID)
	}
	if createBody["name"] != imageName {
		t.Errorf("create body name = %v; want %q", createBody["name"], imageName)
	}
	if createBody["cloudinit"] != true {
		t.Errorf("create body cloudinit = %v; want true", createBody["cloudinit"])
	}
	if createBody["type"] != "linux" {
		t.Errorf("create body type = %v; want %q", createBody["type"], "linux")
	}
}
