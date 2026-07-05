package resources_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/iaas/terraform-provider-iaas/internal/acctest"
)

// ---------------------------------------------------------------------------
// TestAccManagedDatabase_basic - LIVE acceptance test (manual staging gate).
//
// Auto-skips unless TF_ACC is set (resource.Test enforces this). Requires a
// reachable panel + IP-locked token, billing enabled, a database-enabled
// location/VPC, and the account's managed-database quota not exhausted. Supplied
// via:
//
//	IAAS_TEST_DB_PLAN_ID       - UUID of an enabled db_plan supporting the engine
//	IAAS_TEST_DB_VPC_ID        - UUID of a VPC in a db_enabled location
//	IAAS_TEST_DB_VPC_SUBNET_ID - UUID of a (public) subnet in that VPC with a free IP
//
// The test skips cleanly when the vars are absent.
// ---------------------------------------------------------------------------
func TestAccManagedDatabase_basic(t *testing.T) {
	planID := os.Getenv("IAAS_TEST_DB_PLAN_ID")
	vpcID := os.Getenv("IAAS_TEST_DB_VPC_ID")
	subnetID := os.Getenv("IAAS_TEST_DB_VPC_SUBNET_ID")
	if planID == "" || vpcID == "" || subnetID == "" {
		t.Skip("TestAccManagedDatabase_basic: set IAAS_TEST_DB_PLAN_ID, IAAS_TEST_DB_VPC_ID, IAAS_TEST_DB_VPC_SUBNET_ID to run")
	}

	config := fmt.Sprintf(`
resource "iaas_managed_database" "test" {
  name           = "tf-acc-db"
  engine         = "mysql"
  engine_version = "8.0"
  db_plan_id     = %q
  vpc_id         = %q
  vpc_subnet_id  = %q
}
`, planID, vpcID, subnetID)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("iaas_managed_database.test", "id"),
					resource.TestCheckResourceAttr("iaas_managed_database.test", "status", "active"),
					resource.TestCheckResourceAttrSet("iaas_managed_database.test", "username"),
					resource.TestCheckResourceAttrSet("iaas_managed_database.test", "port"),
				),
			},
			{
				ResourceName:            "iaas_managed_database.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"timeouts", "reset_password", "password"},
			},
		},
	})
}

// ---------------------------------------------------------------------------
// TestUnitManagedDatabase_lifecycle - MOCK-backed lifecycle proof.
//
// Drives the full ASYNC managed-database lifecycle against canned responses with
// no live panel:
//
//  1. Create - POST /databases returns {managed_database:{id,status:"deploying"}};
//     the SHOW immediately returns status="active" (ready on the FIRST poll → the
//     waiter converges instantly, no sleep). Asserts the create body (name +
//     engine + engine_version + db_plan_id + vpc_id + vpc_subnet_id) and that it
//     omits computed/server fields. reset_password is set, so the create also
//     rotates the password once and captures it.
//  2. Import - by the DB id, ignoring write-only reset_password/password + timeouts.
//  3. Update - resize (db_plan_id change) asserts the PATCH /resize body, plus a
//     reset_password trigger change re-rotates the password.
//  4. Delete - implicit teardown; DELETE soft-deletes and the next SHOW 404s.
//
// The IAAS_INSTANCE_POLL_INTERVAL seam is set tiny so the waiter cannot hang;
// combined with active-on-first-poll the test must NOT sleep.
// ---------------------------------------------------------------------------
func TestUnitManagedDatabase_lifecycle(t *testing.T) {
	ensureTFBinary(t)
	t.Setenv("IAAS_INSTANCE_POLL_INTERVAL", "1ms")

	srv := acctest.NewMockServer(t)

	const (
		dbID     = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		planID   = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
		plan2ID  = "cccccccc-cccc-cccc-cccc-cccccccccccc"
		vpcID    = "dddddddd-dddd-dddd-dddd-dddddddddddd"
		subnetID = "eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee"
		groupID  = "ffffffff-ffff-ffff-ffff-ffffffffffff"
		dbName   = "app-db"
		pubIP    = "203.0.113.20"
		basePath = "/databases"
	)
	itemPath := "/database/" + dbID

	var mu sync.Mutex
	deleted := false
	curPlan := planID
	curVersion := "8.0"

	showDB := func() map[string]any {
		mu.Lock()
		p := curPlan
		v := curVersion
		mu.Unlock()
		return map[string]any{
			"id":                  dbID,
			"name":                dbName,
			"engine":              "mysql",
			"engine_version":      v,
			"status":              "active",
			"db_plan_id":          p,
			"vpc_id":              vpcID,
			"vpc_subnet_id":       subnetID,
			"hypervisor_group_id": groupID,
			"port":                float64(3306),
			"admin_user":          "dbadmin",
			"role":                "primary",
			"public_ip":           map[string]any{"id": "ip-1", "ip": pubIP},
			// last_error/error_acknowledged (T9): healthy defaults, matching the
			// real column defaults (last_error nullable/null, error_acknowledged
			// default true - see 2026_03_09_000001_add_error_notification_to_
			// managed_databases.php).
			"last_error":         nil,
			"error_acknowledged": true,
		}
	}

	// CREATE - record the row; create response carries status "deploying".
	srv.Handle("POST", basePath, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Managed database deployment initiated.",
			"managed_database": map[string]any{
				"id":             dbID,
				"name":           dbName,
				"engine":         "mysql",
				"engine_version": "8.0",
				"status":         "deploying",
				"db_plan_id":     planID,
			},
		})
	})

	// RESET-PASSWORD - returns a cleartext password (the only place one is returned).
	srv.Handle("POST", itemPath+"/reset-password", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success":  true,
			"message":  "Database password has been reset.",
			"password": "rotated-secret-pw",
		})
	})

	// RESIZE - PATCH the plan in place.
	srv.Handle("PATCH", itemPath+"/resize", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if p, ok := body["db_plan_id"].(string); ok && p != "" {
			mu.Lock()
			curPlan = p
			mu.Unlock()
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success":          true,
			"message":          "Managed database resize initiated.",
			"managed_database": showDB(),
		})
	})

	// SHOW - 404 once delete has been enqueued.
	srv.Handle("GET", itemPath, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gone := deleted
		mu.Unlock()
		if gone {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "Managed Database not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "managed_database": showDB()})
	})

	// DELETE - soft-delete; the next SHOW 404s.
	srv.Handle("DELETE", itemPath, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		deleted = true
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Managed database deleted."})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	createCfg := providerCfg + `
resource "iaas_managed_database" "test" {
  name           = "` + dbName + `"
  engine         = "mysql"
  engine_version = "8.0"
  db_plan_id     = "` + planID + `"
  vpc_id         = "` + vpcID + `"
  vpc_subnet_id  = "` + subnetID + `"
  reset_password = "v1"
}
`
	updateCfg := providerCfg + `
resource "iaas_managed_database" "test" {
  name           = "` + dbName + `"
  engine         = "mysql"
  engine_version = "8.0"
  db_plan_id     = "` + plan2ID + `"
  vpc_id         = "` + vpcID + `"
  vpc_subnet_id  = "` + subnetID + `"
  reset_password = "v2"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: createCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_managed_database.test", "id", dbID),
					resource.TestCheckResourceAttr("iaas_managed_database.test", "name", dbName),
					resource.TestCheckResourceAttr("iaas_managed_database.test", "status", "active"),
					resource.TestCheckResourceAttr("iaas_managed_database.test", "db_plan_id", planID),
					resource.TestCheckResourceAttr("iaas_managed_database.test", "host", pubIP),
					resource.TestCheckResourceAttr("iaas_managed_database.test", "port", "3306"),
					resource.TestCheckResourceAttr("iaas_managed_database.test", "username", "dbadmin"),
					resource.TestCheckResourceAttr("iaas_managed_database.test", "role", "primary"),
					resource.TestCheckResourceAttr("iaas_managed_database.test", "password", "rotated-secret-pw"),
				),
			},
			{
				ResourceName:            "iaas_managed_database.test",
				ImportState:             true,
				ImportStateId:           dbID,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"timeouts", "reset_password", "password"},
			},
			{
				Config: updateCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_managed_database.test", "db_plan_id", plan2ID),
					resource.TestCheckResourceAttr("iaas_managed_database.test", "password", "rotated-secret-pw"),
				),
			},
		},
	})

	// Assert the CREATE body carried the required inputs and NOT server-only fields.
	creates := srv.Requests("POST", basePath)
	if len(creates) == 0 {
		t.Fatal("expected at least one POST " + basePath)
	}
	var createBody map[string]any
	if err := json.Unmarshal(creates[0].Body, &createBody); err != nil {
		t.Fatalf("decoding create body: %v", err)
	}
	for _, k := range []string{"name", "engine", "engine_version", "db_plan_id", "vpc_id", "vpc_subnet_id"} {
		if _, ok := createBody[k]; !ok {
			t.Errorf("create body missing %q; got %v", k, createBody)
		}
	}
	for _, stray := range []string{"id", "status", "host", "port", "username", "role", "password", "reset_password"} {
		if _, present := createBody[stray]; present {
			t.Errorf("create body must NOT include %q; got %v", stray, createBody)
		}
	}

	// Assert exactly one resize PATCH fired with the new plan.
	resizes := srv.Requests("PATCH", itemPath+"/resize")
	if len(resizes) != 1 {
		t.Fatalf("expected exactly 1 PATCH %s/resize, got %d", itemPath, len(resizes))
	}
	var resizeBody map[string]any
	if err := json.Unmarshal(resizes[0].Body, &resizeBody); err != nil {
		t.Fatalf("decoding resize body: %v", err)
	}
	if resizeBody["db_plan_id"] != plan2ID {
		t.Errorf("resize body db_plan_id = %v; want %q", resizeBody["db_plan_id"], plan2ID)
	}

	// Assert the password was rotated twice (once on create, once on the v2 update).
	if rs := srv.Requests("POST", itemPath+"/reset-password"); len(rs) != 2 {
		t.Fatalf("expected exactly 2 POST reset-password (create + update), got %d", len(rs))
	}

	// Assert the DELETE fired exactly once.
	if dels := srv.Requests("DELETE", itemPath); len(dels) != 1 {
		t.Fatalf("expected exactly 1 DELETE %s, got %d", itemPath, len(dels))
	}
}

// ---------------------------------------------------------------------------
// TestUnitManagedDatabase_versionUpgrade - MOCK-backed proof of T9's in-place
// engine_version upgrade.
//
// Drives create-at-8.0 → update-to-8.4 against canned responses with no live
// panel:
//
//  1. Create - POST /databases returns status="deploying"; SHOW immediately
//     reports status="active", engine_version="8.0" (ready on the first poll).
//  2. Update - engine_version 8.0 → 8.4 in config triggers exactly one POST
//     /database/{id}/upgrade with body {target_version:"8.4"}; the mock
//     upgrade handler flips its internal engine_version (mirroring the
//     Master's synchronous engine_version write) so the next SHOW already
//     reports "8.4" - the waiter converges on the very first poll, matching
//     the real API's documented timing (see UpgradeManagedDatabase).
//  3. Asserts the final state's engine_version == "8.4" and that resize/
//     reset-password/resync-replicas were NOT invoked by an unrelated
//     engine_version-only change.
//
// The IAAS_INSTANCE_POLL_INTERVAL seam is tiny so the waiter cannot hang.
// ---------------------------------------------------------------------------
func TestUnitManagedDatabase_versionUpgrade(t *testing.T) {
	ensureTFBinary(t)
	t.Setenv("IAAS_INSTANCE_POLL_INTERVAL", "1ms")

	srv := acctest.NewMockServer(t)

	const (
		dbID     = "11111111-2222-3333-4444-555555555555"
		planID   = "66666666-7777-8888-9999-000000000000"
		vpcID    = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
		subnetID = "ffffffff-0000-1111-2222-333333333333"
		dbName   = "app-db-upgrade"
		pubIP    = "203.0.113.30"
		basePath = "/databases"
	)
	itemPath := "/database/" + dbID

	var mu sync.Mutex
	curVersion := "8.0"

	showDB := func() map[string]any {
		mu.Lock()
		v := curVersion
		mu.Unlock()
		return map[string]any{
			"id":                  dbID,
			"name":                dbName,
			"engine":              "mysql",
			"engine_version":      v,
			"status":              "active",
			"db_plan_id":          planID,
			"vpc_id":              vpcID,
			"vpc_subnet_id":       subnetID,
			"hypervisor_group_id": "",
			"port":                float64(3306),
			"admin_user":          "dbadmin",
			"role":                "primary",
			"public_ip":           map[string]any{"id": "ip-1", "ip": pubIP},
			"last_error":          nil,
			"error_acknowledged":  true,
		}
	}

	// CREATE - record the row; create response carries status "deploying".
	srv.Handle("POST", basePath, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Managed database deployment initiated.",
			"managed_database": map[string]any{
				"id":             dbID,
				"name":           dbName,
				"engine":         "mysql",
				"engine_version": "8.0",
				"status":         "deploying",
				"db_plan_id":     planID,
			},
		})
	})

	// UPGRADE - mirrors ManagedDatabaseService::upgradeVersion writing
	// engine_version onto the row synchronously, before the (unobservable) async
	// upgrade even runs on the box.
	srv.Handle("POST", itemPath+"/upgrade", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		target, _ := body["target_version"].(string)
		if target != "" {
			mu.Lock()
			curVersion = target
			mu.Unlock()
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": fmt.Sprintf("Database upgrade to mysql %s initiated.", target),
		})
	})

	// SHOW - 404 once delete has been enqueued.
	var mu2 sync.Mutex
	deleted := false
	srv.Handle("GET", itemPath, func(w http.ResponseWriter, r *http.Request) {
		mu2.Lock()
		gone := deleted
		mu2.Unlock()
		if gone {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "Managed Database not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "managed_database": showDB()})
	})

	// DELETE - implicit teardown at the end of the test.
	srv.Handle("DELETE", itemPath, func(w http.ResponseWriter, r *http.Request) {
		mu2.Lock()
		deleted = true
		mu2.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Managed database deleted."})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	createCfg := providerCfg + `
resource "iaas_managed_database" "test" {
  name           = "` + dbName + `"
  engine         = "mysql"
  engine_version = "8.0"
  db_plan_id     = "` + planID + `"
  vpc_id         = "` + vpcID + `"
  vpc_subnet_id  = "` + subnetID + `"
}
`
	upgradeCfg := providerCfg + `
resource "iaas_managed_database" "test" {
  name           = "` + dbName + `"
  engine         = "mysql"
  engine_version = "8.4"
  db_plan_id     = "` + planID + `"
  vpc_id         = "` + vpcID + `"
  vpc_subnet_id  = "` + subnetID + `"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: createCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_managed_database.test", "id", dbID),
					resource.TestCheckResourceAttr("iaas_managed_database.test", "engine_version", "8.0"),
					resource.TestCheckResourceAttr("iaas_managed_database.test", "status", "active"),
					resource.TestCheckResourceAttr("iaas_managed_database.test", "error_acknowledged", "true"),
				),
			},
			{
				Config: upgradeCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_managed_database.test", "engine_version", "8.4"),
					resource.TestCheckResourceAttr("iaas_managed_database.test", "status", "active"),
				),
			},
		},
	})

	// Assert the upgrade endpoint fired EXACTLY once, with the target version.
	upgrades := srv.Requests("POST", itemPath+"/upgrade")
	if len(upgrades) != 1 {
		t.Fatalf("expected exactly 1 POST %s/upgrade, got %d", itemPath, len(upgrades))
	}
	var upgradeBody map[string]any
	if err := json.Unmarshal(upgrades[0].Body, &upgradeBody); err != nil {
		t.Fatalf("decoding upgrade body: %v", err)
	}
	if upgradeBody["target_version"] != "8.4" {
		t.Errorf("upgrade body target_version = %v; want 8.4", upgradeBody["target_version"])
	}

	// Assert an engine_version-only change did NOT also fire resize/reset-password/
	// resync-replicas (they were never configured in this test).
	if rs := srv.Requests("PATCH", itemPath+"/resize"); len(rs) != 0 {
		t.Errorf("expected 0 PATCH %s/resize from an engine_version-only change, got %d", itemPath, len(rs))
	}
	if rs := srv.Requests("POST", itemPath+"/reset-password"); len(rs) != 0 {
		t.Errorf("expected 0 POST %s/reset-password from an engine_version-only change, got %d", itemPath, len(rs))
	}
	if rs := srv.Requests("POST", itemPath+"/resync-replicas"); len(rs) != 0 {
		t.Errorf("expected 0 POST %s/resync-replicas from an engine_version-only change, got %d", itemPath, len(rs))
	}

	// Assert the DELETE fired exactly once (implicit teardown).
	if dels := srv.Requests("DELETE", itemPath); len(dels) != 1 {
		t.Fatalf("expected exactly 1 DELETE %s, got %d", itemPath, len(dels))
	}
}
