package resources_test

import (
	"encoding/json"
	"net/http"
	"regexp"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/iaas/terraform-provider-iaas/internal/acctest"
)

// ---------------------------------------------------------------------------
// TestAccDBParameterGroup_basic - LIVE acceptance test (manual staging gate).
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
// It SIMULATES the server's appendParameterSuffixes() behaviour: for the known
// suffix-bearing keys it appends the suffix on every write, exactly as the real
// Master controller does (Master app/Http/Controllers/UserApi/DbParameterGroupController.php).
//
// Suffix map (sourced from Master config/managed_database_parameters.php):
//
//	mysql/mariadb:
//	  innodb_buffer_pool_size  → "M"
//	  innodb_log_file_size     → "M"
//	  innodb_redo_log_capacity → "G"
//	  max_allowed_packet       → "M"
//	  tmp_table_size           → "M"
//	  max_heap_table_size      → "M"
//	postgresql:
//	  shared_buffers           → "MB"
//	  effective_cache_size     → "MB"
//	  work_mem                 → "MB"
//	  maintenance_work_mem     → "MB"
//	  wal_buffers              → "MB"
//	  max_wal_size             → "MB"
//
// The mock supports:
//   - GET  /db/parameter-groups         → list
//   - POST /db/parameter-groups         → create
//   - PATCH /db/parameter-group/{id}    → update
//   - DELETE /db/parameter-group/{id}   → delete
//
// ---------------------------------------------------------------------------

// mockSuffixes mirrors the server-side appendParameterSuffixes() logic.
// Key: engine → paramKey → suffix string to append.
var mockSuffixes = map[string]map[string]string{
	"mysql": {
		"innodb_buffer_pool_size":  "M",
		"innodb_log_file_size":     "M",
		"innodb_redo_log_capacity": "G",
		"max_allowed_packet":       "M",
		"tmp_table_size":           "M",
		"max_heap_table_size":      "M",
	},
	"mariadb": {
		"innodb_buffer_pool_size":  "M",
		"innodb_log_file_size":     "M",
		"innodb_redo_log_capacity": "G",
		"max_allowed_packet":       "M",
		"tmp_table_size":           "M",
		"max_heap_table_size":      "M",
	},
	"postgresql": {
		"shared_buffers":       "MB",
		"effective_cache_size": "MB",
		"work_mem":             "MB",
		"maintenance_work_mem": "MB",
		"wal_buffers":          "MB",
		"max_wal_size":         "MB",
	},
}

// applyServerSuffixes simulates the server's non-idempotent suffix append:
// for each key that has a known suffix for the engine, it appends the suffix
// to the value string (unconditionally, as the real server does).
func applyServerSuffixes(engine string, params map[string]string) map[string]string {
	suffixes, ok := mockSuffixes[engine]
	if !ok {
		return params
	}
	out := make(map[string]string, len(params))
	for k, v := range params {
		if sfx, hasSfx := suffixes[k]; hasSfx {
			out[k] = v + sfx
		} else {
			out[k] = v
		}
	}
	return out
}

type dbParameterGroupMockServer struct {
	mu   sync.Mutex
	id   string
	name string

	// engine is immutable once created.
	engine string

	// parameters stores what the server returns - i.e. with suffixes applied,
	// mirroring the real server's stored form.
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
// TestUnitDBParameterGroup_lifecycle - MOCK-backed lifecycle proof.
//
// Uses only SUFFIX-FREE parameters (max_connections, wait_timeout) so values
// round-trip cleanly through the honest mock (which simulates server-side suffix
// appending for the known suffix-bearing keys). This proves the happy path:
// suffix-free params are not transformed and survive plan→apply→read unchanged.
//
// Steps:
//  1. Create with max_connections=200 → id, name, engine, parameters assert.
//  2. Import by id → rehydrates from list.
//  3. Update: rename + new suffix-free parameters (wait_timeout added) →
//     assert new state. Assert PATCH body sent with full replacement map.
//  4. Delete is implicit teardown.
//
// ---------------------------------------------------------------------------
func TestUnitDBParameterGroup_lifecycle(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)

	const pgID = "44444444-4444-4444-4444-444444444444"

	store := &dbParameterGroupMockServer{
		id:         pgID,
		parameters: map[string]string{},
	}

	// LIST - GET /db/parameter-groups
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

	// CREATE - POST /db/parameter-groups
	// Simulates the server's appendParameterSuffixes() by applying mock suffixes
	// to the incoming parameter values before storing them.
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
			raw := make(map[string]string, len(p))
			for k, v := range p {
				if sv, ok := v.(string); ok {
					raw[k] = sv
				}
			}
			// Apply the same non-idempotent suffix transform the real server does.
			store.parameters = applyServerSuffixes(store.engine, raw)
		}
		store.deleted = false
		writeJSON(w, http.StatusOK, map[string]any{
			"success":         true,
			"parameter_group": store.groupObject(),
		})
	})

	// UPDATE - PATCH /db/parameter-group/{id}
	// Also applies server-side suffix transforms on the incoming parameters.
	srv.Handle("PATCH", "/db/parameter-group/"+pgID, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		store.mu.Lock()
		defer store.mu.Unlock()
		if n, ok := body["name"].(string); ok {
			store.name = n
		}
		if p, ok := body["parameters"].(map[string]any); ok {
			raw := make(map[string]string, len(p))
			for k, v := range p {
				if sv, ok := v.(string); ok {
					raw[k] = sv
				}
			}
			// Apply the same non-idempotent suffix transform the real server does.
			store.parameters = applyServerSuffixes(store.engine, raw)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success":         true,
			"parameter_group": store.groupObject(),
		})
	})

	// DELETE - DELETE /db/parameter-group/{id}
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

	// Create config: suffix-free parameters only - these round-trip cleanly.
	createCfg := providerCfg + `
resource "iaas_db_parameter_group" "test" {
  name   = "my-mysql-params"
  engine = "mysql"
  parameters = {
    max_connections = "200"
  }
}
`

	// Update config: rename + add wait_timeout (also suffix-free).
	updateCfg := providerCfg + `
resource "iaas_db_parameter_group" "test" {
  name   = "my-mysql-params-v2"
  engine = "mysql"
  parameters = {
    max_connections = "500"
    wait_timeout    = "3600"
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
			// 2. Import by id (rehydrates from list - no SHOW endpoint).
			{
				ResourceName:      "iaas_db_parameter_group.test",
				ImportState:       true,
				ImportStateId:     pgID,
				ImportStateVerify: true,
			},
			// 3. Update: rename + full parameter replacement (still suffix-free).
			{
				Config: updateCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_db_parameter_group.test", "name", "my-mysql-params-v2"),
					resource.TestCheckResourceAttr("iaas_db_parameter_group.test", "engine", "mysql"),
					resource.TestCheckResourceAttr("iaas_db_parameter_group.test", "parameters.max_connections", "500"),
					resource.TestCheckResourceAttr("iaas_db_parameter_group.test", "parameters.wait_timeout", "3600"),
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
	if params["max_connections"] != "500" {
		t.Errorf("patch params[max_connections] = %v; want 500", params["max_connections"])
	}
	if params["wait_timeout"] != "3600" {
		t.Errorf("patch params[wait_timeout] = %v; want 3600", params["wait_timeout"])
	}
}

// ---------------------------------------------------------------------------
// TestUnitDBParameterGroup_rejectSuffixKey - NEGATIVE validator test.
//
// Confirms that specifying a suffix-bearing parameter key (e.g.
// innodb_buffer_pool_size for mysql) is rejected at plan time with a clear
// error naming the offending key and explaining the root cause.
//
// This test does NOT need a mock server - the validator fires before any API
// call is made.
// ---------------------------------------------------------------------------
func TestUnitDBParameterGroup_rejectSuffixKey(t *testing.T) {
	ensureTFBinary(t)

	// A minimal mock server is required so the provider can configure itself
	// (the provider Configure step needs a reachable endpoint), but no API calls
	// will ever reach it because the validator errors out at plan time.
	srv := acctest.NewMockServer(t)
	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				// innodb_buffer_pool_size is a suffix-bearing key for mysql.
				Config: providerCfg + `
resource "iaas_db_parameter_group" "bad" {
  name   = "should-be-rejected"
  engine = "mysql"
  parameters = {
    innodb_buffer_pool_size = "512"
    max_connections         = "200"
  }
}
`,
				// The validator must fire with an error that names the offending key
				// and explains the non-idempotent suffix problem.
				ExpectError: regexp.MustCompile(`innodb_buffer_pool_size`),
			},
		},
	})
}

// ---------------------------------------------------------------------------
// TestUnitDBParameterGroup_rejectSuffixKeyPostgresql - NEGATIVE validator test
// for the postgresql engine.
//
// Confirms that shared_buffers (suffix 'MB') is rejected for postgresql.
// ---------------------------------------------------------------------------
func TestUnitDBParameterGroup_rejectSuffixKeyPostgresql(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)
	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: providerCfg + `
resource "iaas_db_parameter_group" "bad_pg" {
  name   = "pg-should-be-rejected"
  engine = "postgresql"
  parameters = {
    shared_buffers  = "256"
    max_connections = "100"
  }
}
`,
				ExpectError: regexp.MustCompile(`shared_buffers`),
			},
		},
	})
}

// ---------------------------------------------------------------------------
// TestUnitDBParameterGroup_suffixSimulation - unit test for the mock's
// applyServerSuffixes helper: verifies it correctly simulates the server-side
// suffix transform, i.e. the same non-idempotency the validator protects against.
// ---------------------------------------------------------------------------
func TestUnitDBParameterGroup_suffixSimulation(t *testing.T) {
	cases := []struct {
		engine string
		input  map[string]string
		want   map[string]string
	}{
		{
			engine: "mysql",
			input:  map[string]string{"innodb_buffer_pool_size": "512", "max_connections": "200"},
			want:   map[string]string{"innodb_buffer_pool_size": "512M", "max_connections": "200"},
		},
		{
			engine: "mysql",
			// Simulates second write with already-suffixed value → corruption.
			input: map[string]string{"innodb_buffer_pool_size": "512M"},
			want:  map[string]string{"innodb_buffer_pool_size": "512MM"},
		},
		{
			engine: "postgresql",
			input:  map[string]string{"shared_buffers": "128", "max_connections": "100"},
			want:   map[string]string{"shared_buffers": "128MB", "max_connections": "100"},
		},
		{
			engine: "mariadb",
			input:  map[string]string{"max_heap_table_size": "64", "wait_timeout": "3600"},
			want:   map[string]string{"max_heap_table_size": "64M", "wait_timeout": "3600"},
		},
		{
			// Unknown engine: no suffixes applied.
			engine: "unknown",
			input:  map[string]string{"some_key": "value"},
			want:   map[string]string{"some_key": "value"},
		},
	}

	for _, tc := range cases {
		got := applyServerSuffixes(tc.engine, tc.input)
		for k, wantV := range tc.want {
			if got[k] != wantV {
				t.Errorf("engine=%q key=%q: got %q, want %q", tc.engine, k, got[k], wantV)
			}
		}
		if len(got) != len(tc.want) {
			t.Errorf("engine=%q: result has %d keys, want %d", tc.engine, len(got), len(tc.want))
		}
	}
}
