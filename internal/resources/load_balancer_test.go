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
// TestAccLoadBalancer_basic - LIVE acceptance test (manual staging gate).
//
// Auto-skips unless TF_ACC is set (resource.Test enforces this). Requires a
// reachable panel + IP-locked token (IAAS_API_ENDPOINT / IAAS_API_TOKEN), a
// load-balancer-enabled location, and the account's LB quota not exhausted.
// Supplied via:
//
//	IAAS_TEST_LB_LOCATION_ID - UUID of a hypervisor group with lb_enabled=1
//	IAAS_TEST_LB_PLAN_ID     - UUID of an enabled lb_plan
//
// The test skips cleanly when the vars are absent so a bare TF_ACC=1 run does
// not fail.
// ---------------------------------------------------------------------------
func TestAccLoadBalancer_basic(t *testing.T) {
	locationID := os.Getenv("IAAS_TEST_LB_LOCATION_ID")
	planID := os.Getenv("IAAS_TEST_LB_PLAN_ID")
	if locationID == "" || planID == "" {
		t.Skip("TestAccLoadBalancer_basic: set IAAS_TEST_LB_LOCATION_ID and IAAS_TEST_LB_PLAN_ID to run this acceptance test")
	}

	config := fmt.Sprintf(`
resource "iaas_load_balancer" "test" {
  name                = "tf-acc-lb"
  lb_plan_id          = %q
  hypervisor_group_id = %q
}
`, planID, locationID)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("iaas_load_balancer.test", "id"),
					resource.TestCheckResourceAttr("iaas_load_balancer.test", "status", "active"),
					resource.TestCheckResourceAttrSet("iaas_load_balancer.test", "public_ip"),
					resource.TestCheckResourceAttrSet("iaas_load_balancer.test", "instance_id"),
				),
			},
			{
				ResourceName:            "iaas_load_balancer.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"timeouts", "vpc_subnet_id"},
			},
		},
	})
}

// ---------------------------------------------------------------------------
// TestUnitLoadBalancer_lifecycle - MOCK-backed lifecycle proof.
//
// Drives the full ASYNC core-LB lifecycle against canned API responses with no
// live panel:
//
//  1. Create - POST /load-balancers returns {load_balancer:{id,status:"deploying"}};
//     the SHOW then immediately returns status="active" (ready on the FIRST poll →
//     the waiter converges instantly, no sleep). Asserts the create body
//     (name + lb_plan_id + hypervisor_group_id) and that it omits computed fields.
//  2. Import - by the LB id, verifies state matches (ignoring the write-only
//     vpc_subnet_id and timeouts).
//  3. Delete - implicit teardown; DELETE soft-deletes and the next SHOW 404s,
//     which the delete waiter converges on the FIRST poll.
//
// There is NO update step - the LB has no update endpoint, so every input is
// RequiresReplace and Terraform would recreate rather than update.
//
// The IAAS_INSTANCE_POLL_INTERVAL seam is set tiny so the waiter cannot hang;
// combined with active-on-first-poll the test must NOT sleep. resource.UnitTest
// needs an OpenTofu/Terraform binary (ensureTFBinary); absent one, it skips.
// ---------------------------------------------------------------------------
func TestUnitLoadBalancer_lifecycle(t *testing.T) {
	ensureTFBinary(t)

	// TEST-ONLY poll-interval seam: instant convergence.
	t.Setenv("IAAS_INSTANCE_POLL_INTERVAL", "1ms")

	srv := acctest.NewMockServer(t)

	const (
		lbID     = "11111111-1111-1111-1111-111111111111"
		instID   = "22222222-2222-2222-2222-222222222222"
		groupID  = "33333333-3333-3333-3333-333333333333"
		planID   = "44444444-4444-4444-4444-444444444444"
		lbName   = "web-lb"
		pubIP    = "203.0.113.9"
		basePath = "/load-balancers"
	)
	itemPath := "/load-balancer/" + lbID

	var mu sync.Mutex
	deleted := false

	// SHOW payload - already "active" so the create waiter converges on the
	// first poll (no sleep).
	showLB := func() map[string]any {
		return map[string]any{
			"id":                  lbID,
			"name":                lbName,
			"status":              "active",
			"lb_plan_id":          planID,
			"hypervisor_group_id": groupID,
			"instance_id":         instID,
			"public_ip":           map[string]any{"id": "ip-1", "ip": pubIP},
			"frontends":           []any{},
			"backends":            []any{},
			"certificates":        []any{},
		}
	}

	// CREATE - record the row; the create response carries status "deploying"
	// (the SHOW already reports "active" so the waiter converges immediately).
	srv.Handle("POST", basePath, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Load balancer deployment initiated.",
			"load_balancer": map[string]any{
				"id":                  lbID,
				"name":                lbName,
				"status":              "deploying",
				"lb_plan_id":          planID,
				"hypervisor_group_id": groupID,
			},
		})
	})

	// SHOW - 404 once delete has been enqueued.
	srv.Handle("GET", itemPath, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gone := deleted
		mu.Unlock()
		if gone {
			writeJSON(w, http.StatusNotFound, map[string]any{"message": "No query results for model [LoadBalancer]."})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "load_balancer": showLB()})
	})

	// DELETE - soft-delete; the next SHOW 404s.
	srv.Handle("DELETE", itemPath, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		deleted = true
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Load balancer deleted."})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	createCfg := providerCfg + `
resource "iaas_load_balancer" "test" {
  name                = "` + lbName + `"
  lb_plan_id          = "` + planID + `"
  hypervisor_group_id = "` + groupID + `"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			// Create + read-back (async wait converges immediately).
			{
				Config: createCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_load_balancer.test", "id", lbID),
					resource.TestCheckResourceAttr("iaas_load_balancer.test", "name", lbName),
					resource.TestCheckResourceAttr("iaas_load_balancer.test", "lb_plan_id", planID),
					resource.TestCheckResourceAttr("iaas_load_balancer.test", "hypervisor_group_id", groupID),
					resource.TestCheckResourceAttr("iaas_load_balancer.test", "status", "active"),
					resource.TestCheckResourceAttr("iaas_load_balancer.test", "public_ip", pubIP),
					resource.TestCheckResourceAttr("iaas_load_balancer.test", "instance_id", instID),
				),
			},
			// Import the existing resource by id and verify state matches.
			{
				ResourceName:            "iaas_load_balancer.test",
				ImportState:             true,
				ImportStateId:           lbID,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"timeouts", "vpc_subnet_id"},
			},
		},
	})

	// Assert the CREATE body carried name + lb_plan_id + hypervisor_group_id and
	// NOT server-only computed fields.
	creates := srv.Requests("POST", basePath)
	if len(creates) == 0 {
		t.Fatal("expected at least one POST " + basePath)
	}
	var createBody map[string]any
	if err := json.Unmarshal(creates[0].Body, &createBody); err != nil {
		t.Fatalf("decoding create body: %v", err)
	}
	if createBody["name"] != lbName {
		t.Errorf("create body name = %v; want %q", createBody["name"], lbName)
	}
	if createBody["lb_plan_id"] != planID {
		t.Errorf("create body lb_plan_id = %v; want %q", createBody["lb_plan_id"], planID)
	}
	if createBody["hypervisor_group_id"] != groupID {
		t.Errorf("create body hypervisor_group_id = %v; want %q", createBody["hypervisor_group_id"], groupID)
	}
	// Public mode: no vpc_id / vpc_subnet_id in the body.
	for _, stray := range []string{"id", "status", "public_ip", "instance_id", "vpc_id", "vpc_subnet_id"} {
		if _, present := createBody[stray]; present {
			t.Errorf("create body must NOT include %q; got %v", stray, createBody)
		}
	}

	// Assert the DELETE fired exactly once.
	if dels := srv.Requests("DELETE", itemPath); len(dels) != 1 {
		t.Fatalf("expected exactly 1 DELETE %s, got %d", itemPath, len(dels))
	}
}
