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
// TestAccDBReplica_basic — LIVE acceptance test (manual staging gate).
//
// Requires an existing, ACTIVE primary managed database (in a VPC) plus a replica
// plan and subnet:
//
//	IAAS_TEST_DB_PRIMARY_ID      — UUID of an active primary managed database
//	IAAS_TEST_DB_REPLICA_PLAN_ID — UUID of a db_plan (storage >= primary's)
//	IAAS_TEST_DB_VPC_SUBNET_ID   — UUID of a subnet in the primary's VPC
//
// ---------------------------------------------------------------------------
func TestAccDBReplica_basic(t *testing.T) {
	primaryID := os.Getenv("IAAS_TEST_DB_PRIMARY_ID")
	planID := os.Getenv("IAAS_TEST_DB_REPLICA_PLAN_ID")
	subnetID := os.Getenv("IAAS_TEST_DB_VPC_SUBNET_ID")
	if primaryID == "" || planID == "" || subnetID == "" {
		t.Skip("TestAccDBReplica_basic: set IAAS_TEST_DB_PRIMARY_ID, IAAS_TEST_DB_REPLICA_PLAN_ID, IAAS_TEST_DB_VPC_SUBNET_ID to run")
	}

	config := fmt.Sprintf(`
resource "iaas_db_replica" "test" {
  primary_id    = %q
  db_plan_id    = %q
  vpc_subnet_id = %q
}
`, primaryID, planID, subnetID)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("iaas_db_replica.test", "id"),
					resource.TestCheckResourceAttr("iaas_db_replica.test", "status", "active"),
				),
			},
			{
				ResourceName: "iaas_db_replica.test",
				ImportState:  true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					rs, ok := s.RootModule().Resources["iaas_db_replica.test"]
					if !ok {
						return "", fmt.Errorf("resource iaas_db_replica.test not found in state")
					}
					return rs.Primary.Attributes["primary_id"] + "/" + rs.Primary.ID, nil
				},
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"timeouts"},
			},
		},
	})
}

// ---------------------------------------------------------------------------
// TestUnitDBReplica_lifecycle — MOCK-backed lifecycle proof.
//
//  1. Create — POST /database/{primary}/replica returns {replica:{id,status:
//     "deploying"}}; the replica SHOW (GET /database/{replicaID}) returns
//     status="active" on the FIRST poll → instant convergence. Asserts the create
//     body (db_plan_id + vpc_subnet_id [+ name]).
//  2. Import — composite "primary_id/replica_id".
//  3. Update — resize (db_plan_id change) asserts the PATCH body.
//  4. Delete — DELETE /database/{replicaID}; next SHOW 404s.
//
// ---------------------------------------------------------------------------
func TestUnitDBReplica_lifecycle(t *testing.T) {
	ensureTFBinary(t)
	t.Setenv("IAAS_INSTANCE_POLL_INTERVAL", "1ms")

	srv := acctest.NewMockServer(t)

	const (
		primaryID = "11111111-2222-3333-4444-555555555555"
		replicaID = "66666666-7777-8888-9999-aaaaaaaaaaaa"
		planID    = "bbbbbbbb-cccc-dddd-eeee-ffffffffffff"
		plan2ID   = "12121212-3434-5656-7878-909090909090"
		subnetID  = "abababab-cdcd-efef-0101-232323232323"
		repName   = "app-db-replica"
		pubIP     = "203.0.113.30"
	)
	createPath := "/database/" + primaryID + "/replica"
	itemPath := "/database/" + replicaID

	var mu sync.Mutex
	deleted := false
	curPlan := planID

	showReplica := func() map[string]any {
		mu.Lock()
		p := curPlan
		mu.Unlock()
		return map[string]any{
			"id":                  replicaID,
			"name":                repName,
			"engine":              "mysql",
			"engine_version":      "8.0",
			"status":              "active",
			"replication_status":  "active",
			"db_plan_id":          p,
			"vpc_subnet_id":       subnetID,
			"primary_database_id": primaryID,
			"role":                "replica",
			"port":                float64(3306),
			"admin_user":          "dbadmin",
			"public_ip":           map[string]any{"id": "ip-2", "ip": pubIP},
		}
	}

	srv.Handle("POST", createPath, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Replica deployment initiated.",
			"replica": map[string]any{
				"id":                  replicaID,
				"name":                repName,
				"status":              "deploying",
				"db_plan_id":          planID,
				"primary_database_id": primaryID,
				"role":                "replica",
			},
		})
	})

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
			"managed_database": showReplica(),
		})
	})

	srv.Handle("GET", itemPath, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gone := deleted
		mu.Unlock()
		if gone {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "Managed Database not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "managed_database": showReplica()})
	})

	srv.Handle("DELETE", itemPath, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		deleted = true
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Managed database deleted."})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	createCfg := providerCfg + `
resource "iaas_db_replica" "test" {
  primary_id    = "` + primaryID + `"
  name          = "` + repName + `"
  db_plan_id    = "` + planID + `"
  vpc_subnet_id = "` + subnetID + `"
}
`
	updateCfg := providerCfg + `
resource "iaas_db_replica" "test" {
  primary_id    = "` + primaryID + `"
  name          = "` + repName + `"
  db_plan_id    = "` + plan2ID + `"
  vpc_subnet_id = "` + subnetID + `"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: createCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_db_replica.test", "id", replicaID),
					resource.TestCheckResourceAttr("iaas_db_replica.test", "primary_id", primaryID),
					resource.TestCheckResourceAttr("iaas_db_replica.test", "status", "active"),
					resource.TestCheckResourceAttr("iaas_db_replica.test", "replication_status", "active"),
					resource.TestCheckResourceAttr("iaas_db_replica.test", "db_plan_id", planID),
					resource.TestCheckResourceAttr("iaas_db_replica.test", "host", pubIP),
					resource.TestCheckResourceAttr("iaas_db_replica.test", "engine", "mysql"),
				),
			},
			{
				ResourceName:            "iaas_db_replica.test",
				ImportState:             true,
				ImportStateId:           primaryID + "/" + replicaID,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"timeouts"},
			},
			{
				Config: updateCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_db_replica.test", "db_plan_id", plan2ID),
				),
			},
		},
	})

	creates := srv.Requests("POST", createPath)
	if len(creates) == 0 {
		t.Fatal("expected at least one POST " + createPath)
	}
	var createBody map[string]any
	if err := json.Unmarshal(creates[0].Body, &createBody); err != nil {
		t.Fatalf("decoding create body: %v", err)
	}
	for _, k := range []string{"db_plan_id", "vpc_subnet_id", "name"} {
		if _, ok := createBody[k]; !ok {
			t.Errorf("replica create body missing %q; got %v", k, createBody)
		}
	}
	for _, stray := range []string{"id", "status", "primary_id", "primary_database_id"} {
		if _, present := createBody[stray]; present {
			t.Errorf("replica create body must NOT include %q; got %v", stray, createBody)
		}
	}

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

	if dels := srv.Requests("DELETE", itemPath); len(dels) != 1 {
		t.Fatalf("expected exactly 1 DELETE %s, got %d", itemPath, len(dels))
	}
}
