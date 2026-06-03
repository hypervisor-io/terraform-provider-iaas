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
// TestAccDBBackupPolicy_basic — LIVE acceptance test (manual staging gate).
// Auto-skips unless TF_ACC is set.
// ---------------------------------------------------------------------------
func TestAccDBBackupPolicy_basic(t *testing.T) {
	const config = `
resource "iaas_db_backup_policy" "test" {
  name                       = "tf-acc-db-backup"
  s3_endpoint                = "s3.example.com"
  s3_bucket                  = "test-backups"
  s3_region                  = "us-east-1"
  s3_access_key              = "AKIAIOSFODNN7EXAMPLE"
  s3_secret_key              = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
  full_backup_frequency      = "daily"
  full_backup_time           = "01:00"
  incremental_frequency      = "6h"
  retention_full_count       = 7
  retention_incremental_days = 14
  retention_pitr_hours       = 72
}
`
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("iaas_db_backup_policy.test", "id"),
					resource.TestCheckResourceAttr("iaas_db_backup_policy.test", "name", "tf-acc-db-backup"),
					resource.TestCheckResourceAttr("iaas_db_backup_policy.test", "s3_bucket", "test-backups"),
				),
			},
			{
				ResourceName:            "iaas_db_backup_policy.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"s3_access_key", "s3_secret_key"},
			},
		},
	})
}

// ---------------------------------------------------------------------------
// dbpMockServer — stateful mock of the database backup policy API.
// ---------------------------------------------------------------------------
type dbpMockServer struct {
	mu sync.Mutex

	name                     string
	s3Endpoint               string
	s3Bucket                 string
	s3Region                 string
	s3PathPrefix             string
	fullBackupFrequency      string
	fullBackupTime           string
	fullBackupDay            *int
	incrementalFrequency     string
	pitrEnabled              bool
	retentionFullCount       int
	retentionIncrementalDays int
	retentionPitrHours       int
	encryptionEnabled        bool

	// attached: database id → present.
	attached map[string]struct{}

	attachCalls int
	detachCalls int
}

func (s *dbpMockServer) policyObject(id string) map[string]any {
	dbs := make([]any, 0, len(s.attached))
	for dbID := range s.attached {
		dbs = append(dbs, map[string]any{"id": dbID, "name": dbID, "engine": "mysql"})
	}

	obj := map[string]any{
		"id":                         id,
		"name":                       s.name,
		"s3_endpoint":                s.s3Endpoint,
		"s3_bucket":                  s.s3Bucket,
		"s3_region":                  s.s3Region,
		"s3_path_prefix":             s.s3PathPrefix,
		"full_backup_frequency":      s.fullBackupFrequency,
		"full_backup_time":           s.fullBackupTime,
		"incremental_frequency":      s.incrementalFrequency,
		"pitr_enabled":               s.pitrEnabled,
		"retention_full_count":       s.retentionFullCount,
		"retention_incremental_days": s.retentionIncrementalDays,
		"retention_pitr_hours":       s.retentionPitrHours,
		"encryption_enabled":         s.encryptionEnabled,
		"status":                     "active",
		"consecutive_failures":       0,
		"last_error":                 nil,
		"managed_databases":          dbs,
		// s3_access_key, s3_secret_key, encryption_key intentionally absent ($hidden).
	}
	if s.fullBackupDay != nil {
		obj["full_backup_day"] = *s.fullBackupDay
	} else {
		obj["full_backup_day"] = nil
	}
	return obj
}

// ---------------------------------------------------------------------------
// TestUnitDBBackupPolicy_lifecycle — MOCK-backed lifecycle test.
//
// Steps:
//  1. Create with daily schedule, 6h incrementals, attach db-a → assert id/name,
//     credentials preserved in state (not blank), database_ids.# = 1.
//  2. Import by id → database_ids rehydrated, credentials ignored on verify.
//  3. Update: rename, change retention, DETACH db-a, ATTACH db-b → assert the
//     right call counts.
//
// Delete is implicit teardown.
// ---------------------------------------------------------------------------
func TestUnitDBBackupPolicy_lifecycle(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)

	const policyID = "66666666-6666-6666-6666-666666666666"

	store := &dbpMockServer{
		attached: map[string]struct{}{},
	}

	// CREATE — POST /networking/db-backup-policies
	srv.Handle("POST", "/networking/db-backup-policies", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		store.mu.Lock()
		defer store.mu.Unlock()
		if n, ok := body["name"].(string); ok {
			store.name = n
		}
		if v, ok := body["s3_endpoint"].(string); ok {
			store.s3Endpoint = v
		}
		if v, ok := body["s3_bucket"].(string); ok {
			store.s3Bucket = v
		}
		if v, ok := body["s3_region"].(string); ok {
			store.s3Region = v
		}
		if v, ok := body["s3_path_prefix"].(string); ok {
			store.s3PathPrefix = v
		}
		if v, ok := body["full_backup_frequency"].(string); ok {
			store.fullBackupFrequency = v
		}
		if v, ok := body["full_backup_time"].(string); ok {
			store.fullBackupTime = v
		}
		if v, ok := body["incremental_frequency"].(string); ok {
			store.incrementalFrequency = v
		}
		if v, ok := body["retention_full_count"].(float64); ok {
			store.retentionFullCount = int(v)
		}
		if v, ok := body["retention_incremental_days"].(float64); ok {
			store.retentionIncrementalDays = int(v)
		}
		if v, ok := body["retention_pitr_hours"].(float64); ok {
			store.retentionPitrHours = int(v)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Backup policy created successfully.",
			"policy":  store.policyObject(policyID),
		})
	})

	// SHOW — GET /networking/db-backup-policy/{id}
	srv.Handle("GET", "/networking/db-backup-policy/"+policyID, func(w http.ResponseWriter, r *http.Request) {
		store.mu.Lock()
		defer store.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"policy":              store.policyObject(policyID),
			"available_databases": []any{},
		})
	})

	// UPDATE — PATCH /networking/db-backup-policy/{id}
	srv.Handle("PATCH", "/networking/db-backup-policy/"+policyID, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		store.mu.Lock()
		defer store.mu.Unlock()
		if n, ok := body["name"].(string); ok {
			store.name = n
		}
		if v, ok := body["retention_full_count"].(float64); ok {
			store.retentionFullCount = int(v)
		}
		if v, ok := body["retention_incremental_days"].(float64); ok {
			store.retentionIncrementalDays = int(v)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Backup policy updated successfully.",
			"policy":  store.policyObject(policyID),
		})
	})

	// ATTACH — POST /networking/db-backup-policy/{id}/attach
	srv.Handle("POST", "/networking/db-backup-policy/"+policyID+"/attach", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		store.mu.Lock()
		defer store.mu.Unlock()
		store.attachCalls++
		if id, ok := body["managed_database_id"].(string); ok && id != "" {
			store.attached[id] = struct{}{}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Database attached to backup policy. Configuration is being applied.",
		})
	})

	// DETACH — POST /networking/db-backup-policy/{id}/detach
	srv.Handle("POST", "/networking/db-backup-policy/"+policyID+"/detach", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		store.mu.Lock()
		defer store.mu.Unlock()
		store.detachCalls++
		if id, ok := body["managed_database_id"].(string); ok && id != "" {
			delete(store.attached, id)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Database detached from backup policy.",
		})
	})

	// DELETE — DELETE /networking/db-backup-policy/{id}
	srv.Handle("DELETE", "/networking/db-backup-policy/"+policyID, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Backup policy deleted successfully.",
		})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	const (
		accessKey = "AKIAIOSFODNN7EXAMPLE"
		secretKey = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	)

	// Create: daily backups, 6h incrementals, attach db-a.
	createCfg := providerCfg + `
resource "iaas_db_backup_policy" "test" {
  name                       = "prod-db-backup"
  s3_endpoint                = "s3.example.com"
  s3_bucket                  = "my-backups"
  s3_region                  = "us-east-1"
  s3_access_key              = "` + accessKey + `"
  s3_secret_key              = "` + secretKey + `"
  full_backup_frequency      = "daily"
  full_backup_time           = "01:00"
  incremental_frequency      = "6h"
  retention_full_count       = 7
  retention_incremental_days = 14
  retention_pitr_hours       = 72
  database_ids               = ["db-a"]
}
`

	// Update: rename, change retention, swap databases.
	updateCfg := providerCfg + `
resource "iaas_db_backup_policy" "test" {
  name                       = "prod-db-backup-v2"
  s3_endpoint                = "s3.example.com"
  s3_bucket                  = "my-backups"
  s3_region                  = "us-east-1"
  s3_access_key              = "` + accessKey + `"
  s3_secret_key              = "` + secretKey + `"
  full_backup_frequency      = "daily"
  full_backup_time           = "01:00"
  incremental_frequency      = "6h"
  retention_full_count       = 14
  retention_incremental_days = 30
  retention_pitr_hours       = 72
  database_ids               = ["db-b"]
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			// 1. Create with one attached database.
			{
				Config: createCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_db_backup_policy.test", "id", policyID),
					resource.TestCheckResourceAttr("iaas_db_backup_policy.test", "name", "prod-db-backup"),
					resource.TestCheckResourceAttr("iaas_db_backup_policy.test", "s3_bucket", "my-backups"),
					resource.TestCheckResourceAttr("iaas_db_backup_policy.test", "s3_access_key", accessKey),
					resource.TestCheckResourceAttr("iaas_db_backup_policy.test", "s3_secret_key", secretKey),
					resource.TestCheckResourceAttr("iaas_db_backup_policy.test", "full_backup_frequency", "daily"),
					resource.TestCheckResourceAttr("iaas_db_backup_policy.test", "retention_full_count", "7"),
					resource.TestCheckResourceAttr("iaas_db_backup_policy.test", "status", "active"),
					resource.TestCheckResourceAttr("iaas_db_backup_policy.test", "database_ids.#", "1"),
					resource.TestCheckTypeSetElemAttr("iaas_db_backup_policy.test", "database_ids.*", "db-a"),
				),
			},
			// 2. Import by id — credentials not recoverable from SHOW, must ignore.
			{
				ResourceName:            "iaas_db_backup_policy.test",
				ImportState:             true,
				ImportStateId:           policyID,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"s3_access_key", "s3_secret_key"},
			},
			// 3. Update: rename, new retention, swap databases.
			{
				Config: updateCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_db_backup_policy.test", "name", "prod-db-backup-v2"),
					resource.TestCheckResourceAttr("iaas_db_backup_policy.test", "retention_full_count", "14"),
					resource.TestCheckResourceAttr("iaas_db_backup_policy.test", "retention_incremental_days", "30"),
					resource.TestCheckResourceAttr("iaas_db_backup_policy.test", "database_ids.#", "1"),
					resource.TestCheckTypeSetElemAttr("iaas_db_backup_policy.test", "database_ids.*", "db-b"),
					// Credentials preserved in state after update.
					resource.TestCheckResourceAttr("iaas_db_backup_policy.test", "s3_access_key", accessKey),
					resource.TestCheckResourceAttr("iaas_db_backup_policy.test", "s3_secret_key", secretKey),
				),
			},
		},
	})

	// Assert call counts.
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.attachCalls != 2 {
		t.Errorf("attachCalls = %d; want 2 (1 on create + 1 on update)", store.attachCalls)
	}
	if store.detachCalls != 1 {
		t.Errorf("detachCalls = %d; want 1 (1 on update)", store.detachCalls)
	}

	// Assert the create POST body carried required S3 + schedule fields.
	creates := srv.Requests("POST", "/networking/db-backup-policies")
	if len(creates) < 1 {
		t.Fatalf("expected at least 1 POST /networking/db-backup-policies; got %d", len(creates))
	}
	var createBody map[string]any
	if err := json.Unmarshal(creates[0].Body, &createBody); err != nil {
		t.Fatalf("decoding create body: %v", err)
	}
	for _, field := range []string{"name", "s3_endpoint", "s3_bucket", "s3_region", "s3_access_key", "s3_secret_key",
		"full_backup_frequency", "full_backup_time", "incremental_frequency",
		"retention_full_count", "retention_incremental_days", "retention_pitr_hours"} {
		if createBody[field] == nil {
			t.Errorf("create body missing required field: %s", field)
		}
	}
	if createBody["s3_secret_key"] != secretKey {
		t.Errorf("create body s3_secret_key = %v; want %s", createBody["s3_secret_key"], secretKey)
	}
	// database_ids must NOT appear in the policy create body.
	if _, ok := createBody["database_ids"]; ok {
		t.Error("create body should not carry database_ids (attach is a separate call)")
	}

	// Assert the create attach call carried db-a.
	attaches := srv.Requests("POST", "/networking/db-backup-policy/"+policyID+"/attach")
	if len(attaches) < 1 {
		t.Fatalf("expected at least 1 attach call; got %d", len(attaches))
	}
	sawDbA := false
	for _, req := range attaches {
		var b map[string]any
		if err := json.Unmarshal(req.Body, &b); err != nil {
			t.Fatalf("decoding attach body: %v", err)
		}
		if b["managed_database_id"] == "db-a" {
			sawDbA = true
		}
	}
	if !sawDbA {
		t.Error("expected an attach call to carry managed_database_id=db-a")
	}
}
