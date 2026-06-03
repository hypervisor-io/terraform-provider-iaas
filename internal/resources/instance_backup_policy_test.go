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
// TestAccInstanceBackupPolicy_basic — LIVE acceptance test (manual staging gate).
// Auto-skips unless TF_ACC is set.
// ---------------------------------------------------------------------------
func TestAccInstanceBackupPolicy_basic(t *testing.T) {
	const config = `
resource "iaas_instance_backup_policy" "test" {
  name                  = "tf-acc-daily-backup"
  full_backup_frequency = "daily"
  full_backup_time      = "02:00"
  max_incremental_chain = 3
  retention_count       = 7
  backup_device         = "primary"
}
`
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("iaas_instance_backup_policy.test", "id"),
					resource.TestCheckResourceAttr("iaas_instance_backup_policy.test", "name", "tf-acc-daily-backup"),
					resource.TestCheckResourceAttr("iaas_instance_backup_policy.test", "full_backup_frequency", "daily"),
					resource.TestCheckResourceAttr("iaas_instance_backup_policy.test", "retention_count", "7"),
				),
			},
			{
				ResourceName:      "iaas_instance_backup_policy.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// ---------------------------------------------------------------------------
// ibpMockServer — stateful mock of the instance backup policy API.
// ---------------------------------------------------------------------------
type ibpMockServer struct {
	mu sync.Mutex

	name                string
	fullBackupFrequency string
	fullBackupTime      string
	fullBackupDay       *int
	maxIncrementalChain int
	retentionCount      int
	backupDevice        string

	// attached: instance id → present.
	attached map[string]struct{}

	attachCalls int
	detachCalls int
}

func (s *ibpMockServer) policyObject(id string) map[string]any {
	insts := make([]any, 0, len(s.attached))
	for instID := range s.attached {
		insts = append(insts, map[string]any{"id": instID, "hostname": instID, "display_name": instID, "status": "deployed"})
	}

	obj := map[string]any{
		"id":                    id,
		"name":                  s.name,
		"full_backup_frequency": s.fullBackupFrequency,
		"full_backup_time":      s.fullBackupTime,
		"max_incremental_chain": s.maxIncrementalChain,
		"retention_count":       s.retentionCount,
		"backup_device":         s.backupDevice,
		"status":                "active",
		"consecutive_failures":  0,
		"last_error":            nil,
		"instances":             insts,
	}
	if s.fullBackupDay != nil {
		obj["full_backup_day"] = *s.fullBackupDay
	} else {
		obj["full_backup_day"] = nil
	}
	return obj
}

// ---------------------------------------------------------------------------
// TestUnitInstanceBackupPolicy_lifecycle — MOCK-backed lifecycle test.
//
// Steps:
//  1. Create with daily schedule + ONE attached instance → assert id/name,
//     instance_ids.# = 1; assert ONE attach call.
//  2. Import by id → instance_ids rehydrated from SHOW.
//  3. Update: rename, DETACH original instance, ATTACH a new one → assert
//     schedule fields updated, one MORE attach + one detach.
//
// Delete is implicit teardown.
// ---------------------------------------------------------------------------
func TestUnitInstanceBackupPolicy_lifecycle(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)

	const policyID = "55555555-5555-5555-5555-555555555555"

	store := &ibpMockServer{
		attached: map[string]struct{}{},
	}

	// CREATE — POST /backup-policies
	srv.Handle("POST", "/backup-policies", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		store.mu.Lock()
		defer store.mu.Unlock()
		if n, ok := body["name"].(string); ok {
			store.name = n
		}
		if f, ok := body["full_backup_frequency"].(string); ok {
			store.fullBackupFrequency = f
		}
		if t, ok := body["full_backup_time"].(string); ok {
			store.fullBackupTime = t
		}
		if v, ok := body["max_incremental_chain"].(float64); ok {
			store.maxIncrementalChain = int(v)
		}
		if v, ok := body["retention_count"].(float64); ok {
			store.retentionCount = int(v)
		}
		if d, ok := body["backup_device"].(string); ok {
			store.backupDevice = d
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Backup policy created successfully.",
			"policy":  store.policyObject(policyID),
		})
	})

	// SHOW — GET /backup-policy/{id}
	srv.Handle("GET", "/backup-policy/"+policyID, func(w http.ResponseWriter, r *http.Request) {
		store.mu.Lock()
		defer store.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"policy":              store.policyObject(policyID),
			"available_instances": []any{},
		})
	})

	// UPDATE — PATCH /backup-policy/{id}
	srv.Handle("PATCH", "/backup-policy/"+policyID, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		store.mu.Lock()
		defer store.mu.Unlock()
		if n, ok := body["name"].(string); ok {
			store.name = n
		}
		if f, ok := body["full_backup_frequency"].(string); ok {
			store.fullBackupFrequency = f
		}
		if t, ok := body["full_backup_time"].(string); ok {
			store.fullBackupTime = t
		}
		if v, ok := body["max_incremental_chain"].(float64); ok {
			store.maxIncrementalChain = int(v)
		}
		if v, ok := body["retention_count"].(float64); ok {
			store.retentionCount = int(v)
		}
		if d, ok := body["backup_device"].(string); ok {
			store.backupDevice = d
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Backup policy updated successfully.",
			"policy":  store.policyObject(policyID),
		})
	})

	// ATTACH — POST /backup-policy/{id}/attach
	srv.Handle("POST", "/backup-policy/"+policyID+"/attach", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		store.mu.Lock()
		defer store.mu.Unlock()
		store.attachCalls++
		if id, ok := body["instance_id"].(string); ok && id != "" {
			store.attached[id] = struct{}{}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Instance attached to policy.",
		})
	})

	// DETACH — POST /backup-policy/{id}/detach
	srv.Handle("POST", "/backup-policy/"+policyID+"/detach", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		store.mu.Lock()
		defer store.mu.Unlock()
		store.detachCalls++
		if id, ok := body["instance_id"].(string); ok && id != "" {
			delete(store.attached, id)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Instance detached from policy.",
		})
	})

	// DELETE — DELETE /backup-policy/{id}
	srv.Handle("DELETE", "/backup-policy/"+policyID, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Backup policy deleted successfully.",
		})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	// Create: daily schedule, 3 incrementals, 7-count retention, attach inst-a.
	createCfg := providerCfg + `
resource "iaas_instance_backup_policy" "test" {
  name                  = "daily-backup"
  full_backup_frequency = "daily"
  full_backup_time      = "02:00"
  max_incremental_chain = 3
  retention_count       = 7
  backup_device         = "primary"
  instance_ids          = ["inst-a"]
}
`

	// Update: rename, increase chain/retention, swap instances.
	updateCfg := providerCfg + `
resource "iaas_instance_backup_policy" "test" {
  name                  = "daily-backup-v2"
  full_backup_frequency = "daily"
  full_backup_time      = "03:00"
  max_incremental_chain = 5
  retention_count       = 14
  backup_device         = "all"
  instance_ids          = ["inst-b"]
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			// 1. Create with one attached instance.
			{
				Config: createCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_instance_backup_policy.test", "id", policyID),
					resource.TestCheckResourceAttr("iaas_instance_backup_policy.test", "name", "daily-backup"),
					resource.TestCheckResourceAttr("iaas_instance_backup_policy.test", "full_backup_frequency", "daily"),
					resource.TestCheckResourceAttr("iaas_instance_backup_policy.test", "full_backup_time", "02:00"),
					resource.TestCheckResourceAttr("iaas_instance_backup_policy.test", "max_incremental_chain", "3"),
					resource.TestCheckResourceAttr("iaas_instance_backup_policy.test", "retention_count", "7"),
					resource.TestCheckResourceAttr("iaas_instance_backup_policy.test", "backup_device", "primary"),
					resource.TestCheckResourceAttr("iaas_instance_backup_policy.test", "status", "active"),
					resource.TestCheckResourceAttr("iaas_instance_backup_policy.test", "instance_ids.#", "1"),
					resource.TestCheckTypeSetElemAttr("iaas_instance_backup_policy.test", "instance_ids.*", "inst-a"),
				),
			},
			// 2. Import by id; instance_ids rehydrated from SHOW.
			{
				ResourceName:      "iaas_instance_backup_policy.test",
				ImportState:       true,
				ImportStateId:     policyID,
				ImportStateVerify: true,
			},
			// 3. Update: rename, different schedule, swap instances.
			{
				Config: updateCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_instance_backup_policy.test", "name", "daily-backup-v2"),
					resource.TestCheckResourceAttr("iaas_instance_backup_policy.test", "full_backup_time", "03:00"),
					resource.TestCheckResourceAttr("iaas_instance_backup_policy.test", "max_incremental_chain", "5"),
					resource.TestCheckResourceAttr("iaas_instance_backup_policy.test", "retention_count", "14"),
					resource.TestCheckResourceAttr("iaas_instance_backup_policy.test", "backup_device", "all"),
					resource.TestCheckResourceAttr("iaas_instance_backup_policy.test", "instance_ids.#", "1"),
					resource.TestCheckTypeSetElemAttr("iaas_instance_backup_policy.test", "instance_ids.*", "inst-b"),
				),
			},
		},
	})

	// Assert call counts: create=1 attach(inst-a), update=1 attach(inst-b)+1 detach(inst-a).
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.attachCalls != 2 {
		t.Errorf("attachCalls = %d; want 2 (1 on create + 1 on update)", store.attachCalls)
	}
	if store.detachCalls != 1 {
		t.Errorf("detachCalls = %d; want 1 (1 on update)", store.detachCalls)
	}

	// Assert the create attach call carried inst-a.
	attaches := srv.Requests("POST", "/backup-policy/"+policyID+"/attach")
	if len(attaches) < 1 {
		t.Fatalf("expected at least 1 attach call; got %d", len(attaches))
	}
	sawInstA := false
	for _, req := range attaches {
		var b map[string]any
		if err := json.Unmarshal(req.Body, &b); err != nil {
			t.Fatalf("decoding attach body: %v", err)
		}
		if b["instance_id"] == "inst-a" {
			sawInstA = true
		}
	}
	if !sawInstA {
		t.Error("expected an attach call to carry instance_id=inst-a")
	}

	// Assert the create POST body carried required fields.
	creates := srv.Requests("POST", "/backup-policies")
	if len(creates) < 1 {
		t.Fatalf("expected at least 1 POST /backup-policies; got %d", len(creates))
	}
	var createBody map[string]any
	if err := json.Unmarshal(creates[0].Body, &createBody); err != nil {
		t.Fatalf("decoding create body: %v", err)
	}
	for _, field := range []string{"name", "full_backup_frequency", "full_backup_time", "max_incremental_chain", "retention_count", "backup_device"} {
		if createBody[field] == nil {
			t.Errorf("create body missing required field: %s", field)
		}
	}
	// instance_id must NOT appear in the policy create body.
	if _, ok := createBody["instance_id"]; ok {
		t.Error("create body should not carry instance_id (attach is a separate call)")
	}
}
