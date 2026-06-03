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
// TestAccDBParameterGroup_basic — LIVE acceptance test (manual staging gate).
//
// Auto-skips unless TF_ACC is set (resource.Test enforces this), so it never
// runs in CI. Requires a reachable panel + IP-locked token + billing enabled.
// ---------------------------------------------------------------------------
func TestAccDBParameterGroup_basic(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: `
resource "iaas_db_parameter_group" "test" {
  name   = "tf-acc-mysql-params"
  engine = "mysql"
  parameters = {
    max_connections = "200"
  }
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("iaas_db_parameter_group.test", "id"),
					resource.TestCheckResourceAttr("iaas_db_parameter_group.test", "name", "tf-acc-mysql-params"),
					resource.TestCheckResourceAttr("iaas_db_parameter_group.test", "engine", "mysql"),
					resource.TestCheckResourceAttr("iaas_db_parameter_group.test", "parameters.max_connections", "200"),
				),
			},
			{
				ResourceName:      "iaas_db_parameter_group.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// ---------------------------------------------------------------------------
// dbParameterGroupMockServer is a stateful mock of the parameter-group API.
//
// State is a single group keyed by id. The mock supports:
//   - GET  /db/parameter-groups → list (returns the stored group)
//   - POST /db/parameter-groups → create (stores name/engine/parameters)
//   - PATCH /db/parameter-group/{id} → update (patches name/parameters)
//   - DELETE /db/parameter-group/{id} → delete (removes group)
//
// ---------------------------------------------------------------------------
type dbParameterGroupMockServer struct {
	mu   sync.Mutex
	id   string
	name string

	// engine is immutable once created.
	engine string

	// parameters mirrors what the API returns (server may have applied suffix
	// transforms; in the mock we store as-given for simplicity).
	parameters map[string]string

	deleted bool
}

func (s *dbParameterGroupMockServer) groupObject() map[string]any {
	params := make(map[string]any, len(s.parameters))
	for k, v := range s.parameters {
		params[k] = v
	}
	return map[string]any{
		"id":         s.id,
		"name":       s.name,
		"engine":     s.engine,
		"parameters": params,
	}
}

// ---------------------------------------------------------------------------
// TestUnitDBParameterGroup_lifecycle — MOCK-backed lifecycle proof.
//
// Steps:
//  1. Create with name + engine + parameters → assert id, name, engine,
//     parameters.max_connections. Assert the POST body carried name, engine,
//     parameters.
//  2. Import by id → rehydrates from list (no SHOW endpoint).
//  3. Update: rename + replace parameters map → assert the state reflects the
//     new name and new parameters; assert PATCH body was sent with the new values.
//  4. Delete is implicit teardown.
//
// This proves the Map-based parameter model: create sets the full map, update
// sends the full replacement map, read rebuilds from the list-and-match GET.
// ---------------------------------------------------------------------------
func TestUnitDBParameterGroup_lifecycle(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)

	const pgID = "44444444-4444-4444-4444-444444444444"

	store := &dbParameterGroupMockServer{
		id:         pgID,
		parameters: map[string]string{},
	}

	// LIST — GET /db/parameter-groups returns the stored group (or an empty list
	// if deleted).
	srv.Handle("GET", "/db/parameter-groups", func(w http.ResponseWriter, r *http.Request) {
		store.mu.Lock()
		defer store.mu.Unlock()
		var groups []any
		if !store.deleted && store.name != "" {
			groups = []any{store.groupObject()}
		} else {
			groups = []any{}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success":          true,
			"parameter_groups": groups,
		})
	})

	// CREATE — POST /db/parameter-groups stores name/engine/parameters, returns the
	// new group object under "parameter_group".
	srv.Handle("POST", "/db/parameter-groups", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		store.mu.Lock()
		defer store.mu.Unlock()
		if n, ok := body["name"].(string); ok {
			store.name = n
		}
		if e, ok := body["engine"].(string); ok {
			store.engine = e
		}
		if p, ok := body["parameters"].(map[string]any); ok {
			store.parameters = make(map[string]string, len(p))
			for k, v := range p {
				if sv, ok := v.(string); ok {
					store.parameters[k] = sv
				}
			}
		}
		store.deleted = false
		writeJSON(w, http.StatusOK, map[string]any{
			"success":         true,
			"parameter_group": store.groupObject(),
		})
	})

	// UPDATE — PATCH /db/parameter-group/{id} patches name and/or parameters.
	srv.Handle("PATCH", "/db/parameter-group/"+pgID, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		store.mu.Lock()
		defer store.mu.Unlock()
		if n, ok := body["name"].(string); ok {
			store.name = n
		}
		if p, ok := body["parameters"].(map[string]any); ok {
			store.parameters = make(map[string]string, len(p))
			for k, v := range p {
				if sv, ok := v.(string); ok {
					store.parameters[k] = sv
				}
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success":         true,
			"parameter_group": store.groupObject(),
		})
	})

	// DELETE — DELETE /db/parameter-group/{id} marks deleted.
	srv.Handle("DELETE", "/db/parameter-group/"+pgID, func(w http.ResponseWriter, r *http.Request) {
		store.mu.Lock()
		defer store.mu.Unlock()
		store.deleted = true
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Parameter group deleted.",
		})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	// Create config: MySQL parameter group with one parameter.
	createCfg := providerCfg + `
resource "iaas_db_parameter_group" "test" {
  name   = "my-mysql-params"
  engine = "mysql"
  parameters = {
    max_connections = "200"
  }
}
`

	// Update config: renamed + different parameters (full replacement map).
	updateCfg := providerCfg + `
resource "iaas_db_parameter_group" "test" {
  name   = "my-mysql-params-v2"
  engine = "mysql"
  parameters = {
    innodb_buffer_pool_size = "512M"
    max_connections         = "500"
  }
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			// 1. Create + read-back.
			{
				Config: createCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_db_parameter_group.test", "id", pgID),
					resource.TestCheckResourceAttr("iaas_db_parameter_group.test", "name", "my-mysql-params"),
					resource.TestCheckResourceAttr("iaas_db_parameter_group.test", "engine", "mysql"),
					resource.TestCheckResourceAttr("iaas_db_parameter_group.test", "parameters.max_connections", "200"),
					resource.TestCheckResourceAttr("iaas_db_parameter_group.test", "parameters.%", "1"),
				),
			},
			// 2. Import by id (rehydrates from list — no SHOW endpoint).
			{
				ResourceName:      "iaas_db_parameter_group.test",
				ImportState:       true,
				ImportStateId:     pgID,
				ImportStateVerify: true,
			},
			// 3. Update: rename + full parameter replacement.
			{
				Config: updateCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_db_parameter_group.test", "name", "my-mysql-params-v2"),
					resource.TestCheckResourceAttr("iaas_db_parameter_group.test", "engine", "mysql"),
					resource.TestCheckResourceAttr("iaas_db_parameter_group.test", "parameters.innodb_buffer_pool_size", "512M"),
					resource.TestCheckResourceAttr("iaas_db_parameter_group.test", "parameters.max_connections", "500"),
					resource.TestCheckResourceAttr("iaas_db_parameter_group.test", "parameters.%", "2"),
				),
			},
		},
	})

	// Assert the create POST carried name, engine, and parameters.
	creates := srv.Requests("POST", "/db/parameter-groups")
	if len(creates) != 1 {
		t.Fatalf("expected 1 POST /db/parameter-groups; got %d", len(creates))
	}
	var createBody map[string]any
	if err := json.Unmarshal(creates[0].Body, &createBody); err != nil {
		t.Fatalf("decoding create body: %v", err)
	}
	for _, k := range []string{"name", "engine", "parameters"} {
		if _, ok := createBody[k]; !ok {
			t.Errorf("create body missing key %q; got %v", k, createBody)
		}
	}
	if createBody["engine"] != "mysql" {
		t.Errorf("create body engine = %v; want mysql", createBody["engine"])
	}

	// Assert the update PATCH sent a full parameters replacement map (both keys).
	patches := srv.Requests("PATCH", "/db/parameter-group/"+pgID)
	if len(patches) != 1 {
		t.Fatalf("expected 1 PATCH /db/parameter-group/%s; got %d", pgID, len(patches))
	}
	var patchBody map[string]any
	if err := json.Unmarshal(patches[0].Body, &patchBody); err != nil {
		t.Fatalf("decoding patch body: %v", err)
	}
	params, ok := patchBody["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("patch body parameters not a map: %T", patchBody["parameters"])
	}
	if len(params) != 2 {
		t.Errorf("patch parameters map has %d keys; want 2", len(params))
	}
	if params["innodb_buffer_pool_size"] != "512M" {
		t.Errorf("patch params[innodb_buffer_pool_size] = %v; want 512M", params["innodb_buffer_pool_size"])
	}
}
