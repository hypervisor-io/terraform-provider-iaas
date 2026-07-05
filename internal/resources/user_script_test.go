package resources_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/iaas/terraform-provider-iaas/internal/acctest"
)

// userScriptObject builds a serialized user_script object matching the API shape.
func userScriptObject(id, name, scriptType, desc, shebang, content string) map[string]any {
	return map[string]any{
		"id":          id,
		"name":        name,
		"type":        scriptType,
		"description": desc,
		"shebang":     shebang,
		"content":     content,
	}
}

// TestUnitUserScript_lifecycle drives create → import → update against a mock.
func TestUnitUserScript_lifecycle(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)

	const (
		scriptID = "22222222-2222-2222-2222-222222222222"
		content  = "apt-get update && apt-get upgrade -y"
	)
	currentName := "bootstrap"

	// CREATE - POST /user-scripts.
	srv.Handle("POST", "/user-scripts", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if n, ok := body["name"].(string); ok {
			currentName = n
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Script created successfully.",
			"script":  userScriptObject(scriptID, currentName, "bash", "server bootstrap", "#!/bin/bash", content),
		})
	})

	// UPDATE - PATCH /user-script/{id} (singular).
	srv.Handle("PATCH", "/user-script/"+scriptID, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if n, ok := body["name"].(string); ok {
			currentName = n
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Script updated successfully.",
			"script":  userScriptObject(scriptID, currentName, "bash", "server bootstrap", "#!/bin/bash", content),
		})
	})

	// READ - no SHOW route; the resource lists and matches by id.
	srv.Handle("GET", "/user-scripts", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"current_page": 1,
			"per_page":     10,
			"total":        1,
			"data": []any{
				userScriptObject(scriptID, currentName, "bash", "server bootstrap", "#!/bin/bash", content),
			},
		})
	})

	// DELETE - DELETE /user-script/{id} (singular).
	srv.Handle("DELETE", "/user-script/"+scriptID, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Script deleted successfully."})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())
	createCfg := providerCfg + `
resource "iaas_user_script" "test" {
  name        = "bootstrap"
  type        = "bash"
  description = "server bootstrap"
  shebang     = "#!/bin/bash"
  content     = "apt-get update && apt-get upgrade -y"
}
`
	updateCfg := providerCfg + `
resource "iaas_user_script" "test" {
  name        = "bootstrap-v2"
  type        = "bash"
  description = "server bootstrap"
  shebang     = "#!/bin/bash"
  content     = "apt-get update && apt-get upgrade -y"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: createCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_user_script.test", "id", scriptID),
					resource.TestCheckResourceAttr("iaas_user_script.test", "name", "bootstrap"),
					resource.TestCheckResourceAttr("iaas_user_script.test", "type", "bash"),
				),
			},
			{
				ResourceName:      "iaas_user_script.test",
				ImportState:       true,
				ImportStateId:     scriptID,
				ImportStateVerify: true,
			},
			{
				Config: updateCfg,
				Check:  resource.TestCheckResourceAttr("iaas_user_script.test", "name", "bootstrap-v2"),
			},
		},
	})
}
