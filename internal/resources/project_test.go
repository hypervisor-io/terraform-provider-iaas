package resources_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/iaas/terraform-provider-iaas/internal/acctest"
)

// ---------------------------------------------------------------------------
// TestAccProject_basic — LIVE acceptance test (manual staging gate).
//
// Auto-skips unless TF_ACC is set (resource.Test enforces this), so it never
// runs or blocks CI. Requires a reachable panel + IP-locked token via
// IAAS_API_ENDPOINT / IAAS_API_TOKEN (checked by acctest.PreCheck).
// ---------------------------------------------------------------------------
func TestAccProject_basic(t *testing.T) {
	const config = `
resource "iaas_project" "test" {
  name        = "tf acc test project"
  description = "Acceptance test project"
  color       = "#3B82F6"
}
`
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("iaas_project.test", "id"),
					resource.TestCheckResourceAttr("iaas_project.test", "name", "tf acc test project"),
					resource.TestCheckResourceAttr("iaas_project.test", "description", "Acceptance test project"),
					resource.TestCheckResourceAttr("iaas_project.test", "color", "#3B82F6"),
				),
			},
			{
				ResourceName:      "iaas_project.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// ---------------------------------------------------------------------------
// TestUnitProject_lifecycle — MOCK-backed lifecycle proof.
//
// Drives the full resource lifecycle against canned API responses, with no
// live panel. The Steps execute in this order:
//
//  1. Create + read-back — applies createCfg; checks id, name, description, color.
//  2. Import — imports the resource by UUID and verifies state matches prior step.
//  3. Update — applies updateCfg (renamed + new color + cleared description).
//
// Delete is implicit teardown after the final step, not an explicit Step.
//
// resource.UnitTest needs a terraform/opentofu binary on PATH or via
// TF_ACC_TERRAFORM_PATH; if none is found the test is skipped with a clear
// binary-not-found message (see ensureTFBinary).
// ---------------------------------------------------------------------------
func TestUnitProject_lifecycle(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)

	const (
		projectID = "22222222-2222-2222-2222-222222222222"
	)

	// currentState tracks the mutable server-side fields so READ always
	// reflects the latest state — exercising real drift-free read-back.
	currentName := "My Project"
	currentDesc := "Initial description"
	currentColor := "#3B82F6"

	// CREATE — POST /projects returns 200 + {success,message,project}.
	srv.Handle("POST", "/projects", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if n, ok := body["name"].(string); ok {
			currentName = n
		}
		if d, ok := body["description"].(string); ok {
			currentDesc = d
		} else {
			currentDesc = ""
		}
		if c, ok := body["color"].(string); ok {
			currentColor = c
		} else {
			currentColor = ""
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Project created successfully",
			"project": buildProjectObj(projectID, currentName, currentDesc, currentColor),
		})
	})

	// UPDATE — PATCH /project/{id} applies new fields.
	srv.Handle("PATCH", "/project/"+projectID, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if n, ok := body["name"].(string); ok {
			currentName = n
		}
		// description can be explicit null (clear) or a string
		if desc, exists := body["description"]; exists {
			if desc == nil {
				currentDesc = ""
			} else if s, ok := desc.(string); ok {
				currentDesc = s
			}
		}
		if c, ok := body["color"].(string); ok {
			currentColor = c
		} else if col, exists := body["color"]; exists && col == nil {
			currentColor = ""
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Project updated successfully",
			"project": buildProjectObj(projectID, currentName, currentDesc, currentColor),
		})
	})

	// READ — GET /project/{id} reflects the latest state.
	srv.Handle("GET", "/project/"+projectID, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"project": buildProjectObj(projectID, currentName, currentDesc, currentColor),
			// SHOW also returns embedded resource lists — resource ignores them.
			"instances":         map[string]any{"data": []any{}},
			"vpcs":              map[string]any{"data": []any{}},
			"load_balancers":    map[string]any{"data": []any{}},
			"s3_buckets":        map[string]any{"data": []any{}},
			"managed_databases": map[string]any{"data": []any{}},
		})
	})

	// DELETE — DELETE /project/{id} succeeds at 200.
	srv.Handle("DELETE", "/project/"+projectID, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Project deleted successfully",
		})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	createCfg := providerCfg + `
resource "iaas_project" "test" {
  name        = "My Project"
  description = "Initial description"
  color       = "#3B82F6"
}
`
	// Update: rename, change color, clear description.
	updateCfg := providerCfg + `
resource "iaas_project" "test" {
  name  = "Renamed Project"
  color = "#F59E0B"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			// Create + read-back.
			{
				Config: createCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_project.test", "id", projectID),
					resource.TestCheckResourceAttr("iaas_project.test", "name", "My Project"),
					resource.TestCheckResourceAttr("iaas_project.test", "description", "Initial description"),
					resource.TestCheckResourceAttr("iaas_project.test", "color", "#3B82F6"),
				),
			},
			// Import the existing resource and verify state matches.
			{
				ResourceName:      "iaas_project.test",
				ImportState:       true,
				ImportStateId:     projectID,
				ImportStateVerify: true,
			},
			// Update: rename, new color, description cleared.
			{
				Config: updateCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_project.test", "id", projectID),
					resource.TestCheckResourceAttr("iaas_project.test", "name", "Renamed Project"),
					resource.TestCheckResourceAttr("iaas_project.test", "color", "#F59E0B"),
					// description was removed from config → clears to null/unset.
					resource.TestCheckNoResourceAttr("iaas_project.test", "description"),
				),
			},
		},
	})

	// Assert the create request sent exactly the expected fields.
	creates := srv.Requests("POST", "/projects")
	if len(creates) == 0 {
		t.Fatal("expected at least one POST /projects")
	}
	var createBody map[string]any
	if err := json.Unmarshal(creates[0].Body, &createBody); err != nil {
		t.Fatalf("decoding create body: %v", err)
	}
	if createBody["name"] != "My Project" {
		t.Errorf("create body name = %v; want %q", createBody["name"], "My Project")
	}
	if createBody["description"] != "Initial description" {
		t.Errorf("create body description = %v; want %q", createBody["description"], "Initial description")
	}
	if createBody["color"] != "#3B82F6" {
		t.Errorf("create body color = %v; want %q", createBody["color"], "#3B82F6")
	}
}

// buildProjectObj builds a serialized project object matching the API shape.
// Empty description/color are returned as null (matching server behaviour).
func buildProjectObj(id, name, description, color string) map[string]any {
	obj := map[string]any{
		"id":   id,
		"name": name,
	}
	if description != "" {
		obj["description"] = description
	} else {
		obj["description"] = nil
	}
	if color != "" {
		obj["color"] = color
	} else {
		obj["color"] = nil
	}
	return obj
}
