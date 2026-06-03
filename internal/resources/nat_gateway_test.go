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
// TestAccNatGateway_basic — LIVE acceptance test (manual staging gate).
//
// Auto-skips unless TF_ACC is set (resource.Test enforces this). Requires a
// reachable panel + IP-locked token (IAAS_API_ENDPOINT / IAAS_API_TOKEN), a VPC
// in a NAT-gateway-enabled location that has at least one private subnet, and the
// account's NAT gateway quota not exhausted. Supplied via:
//
//	IAAS_TEST_VPC_ID — UUID of a VPC in a natgw-enabled location with a private subnet
//
// The test skips cleanly when the var is absent so a bare TF_ACC=1 run does not
// fail.
// ---------------------------------------------------------------------------
func TestAccNatGateway_basic(t *testing.T) {
	vpcID := os.Getenv("IAAS_TEST_VPC_ID")
	if vpcID == "" {
		t.Skip("TestAccNatGateway_basic: set IAAS_TEST_VPC_ID to run this acceptance test")
	}

	config := fmt.Sprintf(`
resource "iaas_nat_gateway" "test" {
  vpc_id = %q
  name   = "tf-acc-natgw"
}
`, vpcID)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("iaas_nat_gateway.test", "id"),
					resource.TestCheckResourceAttr("iaas_nat_gateway.test", "status", "active"),
					resource.TestCheckResourceAttrSet("iaas_nat_gateway.test", "public_ip"),
				),
			},
			{
				ResourceName: "iaas_nat_gateway.test",
				ImportState:  true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					return vpcID + "/" + s.RootModule().Resources["iaas_nat_gateway.test"].Primary.ID, nil
				},
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"timeouts"},
			},
		},
	})
}

// ---------------------------------------------------------------------------
// TestUnitNatGateway_lifecycle — MOCK-backed lifecycle proof.
//
// Drives the full CHILD + ASYNC resource lifecycle against canned API responses
// with no live panel:
//
//  1. Create — POST /vpc/{vpcId}/nat-gateway returns {gateway:{id,status:"pending"}};
//     the SHOW then immediately returns status="active" (ready on the FIRST poll →
//     the waiter converges instantly, no sleep). Asserts the create body
//     (name + nat_enabled + subnet_ids).
//  2. Import — by COMPOSITE id "<vpc_id>/<gateway_id>", verifies state matches.
//  3. Update — rename (PATCH), disable NAT (POST /disable), and swap the attached
//     subnet (detach sub-1 / attach sub-2). Asserts EACH of those fired.
//  4. Delete — implicit teardown; DELETE soft-deletes and the next SHOW 404s.
//
// The IAAS_INSTANCE_POLL_INTERVAL seam is set tiny so the waiter cannot hang;
// combined with active-on-first-poll the test must NOT sleep. resource.UnitTest
// needs an OpenTofu/Terraform binary (ensureTFBinary); absent one, it skips.
// ---------------------------------------------------------------------------
func TestUnitNatGateway_lifecycle(t *testing.T) {
	ensureTFBinary(t)

	// TEST-ONLY poll-interval seam: instant convergence.
	t.Setenv("IAAS_INSTANCE_POLL_INTERVAL", "1ms")

	srv := acctest.NewMockServer(t)

	const (
		vpcID   = "11111111-1111-1111-1111-111111111111"
		gwID    = "22222222-2222-2222-2222-222222222222"
		subnet1 = "33333333-3333-3333-3333-333333333333"
		subnet2 = "44444444-4444-4444-4444-444444444444"
		gwName  = "natgw-prod"
		gwName2 = "natgw-renamed"
		pubIP   = "203.0.113.7"
	)

	base := "/vpc/" + vpcID + "/nat-gateway"
	itemPath := base + "/" + gwID

	// Stateful server-side fields mutated by update/enable/disable/attach/detach.
	var mu sync.Mutex
	currentName := gwName
	currentStatus := "active"
	currentNatEnabled := true
	currentSubnets := map[string]struct{}{subnet1: {}}
	deleted := false

	subnetsArray := func() []map[string]any {
		out := make([]map[string]any, 0, len(currentSubnets))
		for id := range currentSubnets {
			out = append(out, map[string]any{"id": id})
		}
		return out
	}

	showGateway := func() map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return map[string]any{
			"id":          gwID,
			"name":        currentName,
			"status":      currentStatus,
			"nat_enabled": currentNatEnabled,
			"public_ip":   map[string]any{"ip": pubIP},
			"subnets":     subnetsArray(),
		}
	}

	// CREATE — record the row (status "pending" in the create response); the first
	// SHOW already reports "active" so the waiter converges on the first poll. The
	// requested subnet (subnet1) is recorded as the attached set.
	srv.Handle("POST", base, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "NAT gateway created successfully",
			"gateway": map[string]any{
				"id":          gwID,
				"name":        gwName,
				"status":      "pending",
				"nat_enabled": true,
			},
		})
	})

	// SHOW — 404 once delete has been enqueued.
	srv.Handle("GET", itemPath, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gone := deleted
		mu.Unlock()
		if gone {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "NAT Gateway not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "gateway": showGateway()})
	})

	// UPDATE — PATCH name (and/or nat_enabled).
	srv.Handle("PATCH", itemPath, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		if n, ok := body["name"].(string); ok {
			currentName = n
		}
		if e, ok := body["nat_enabled"].(bool); ok {
			currentNatEnabled = e
		}
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "updated", "gateway": showGateway()})
	})

	// DISABLE — POST /disable; clears nat_enabled.
	srv.Handle("POST", itemPath+"/disable", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		currentNatEnabled = false
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "disabled", "gateway": showGateway()})
	})

	// ENABLE — POST /enable; sets nat_enabled (registered for completeness).
	srv.Handle("POST", itemPath+"/enable", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		currentNatEnabled = true
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "enabled", "gateway": showGateway()})
	})

	// ATTACH SUBNET — POST /subnet body {subnet_id}.
	srv.Handle("POST", itemPath+"/subnet", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		if s, ok := body["subnet_id"].(string); ok {
			currentSubnets[s] = struct{}{}
		}
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "attached", "gateway": showGateway()})
	})

	// DETACH SUBNET — DELETE /subnet/{subnetId}.
	srv.Handle("DELETE", itemPath+"/subnet/"+subnet1, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		delete(currentSubnets, subnet1)
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "detached", "gateway": showGateway()})
	})

	// DELETE — soft-delete; the next SHOW 404s.
	srv.Handle("DELETE", itemPath, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		deleted = true
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "NAT gateway deleted successfully"})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	createCfg := providerCfg + `
resource "iaas_nat_gateway" "test" {
  vpc_id      = "` + vpcID + `"
  name        = "` + gwName + `"
  nat_enabled = true
  subnet_ids  = ["` + subnet1 + `"]
}
`
	// Update: rename, disable NAT, swap the attached subnet (sub-1 → sub-2).
	updateCfg := providerCfg + `
resource "iaas_nat_gateway" "test" {
  vpc_id      = "` + vpcID + `"
  name        = "` + gwName2 + `"
  nat_enabled = false
  subnet_ids  = ["` + subnet2 + `"]
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			// Create + read-back (async wait converges immediately).
			{
				Config: createCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_nat_gateway.test", "id", gwID),
					resource.TestCheckResourceAttr("iaas_nat_gateway.test", "vpc_id", vpcID),
					resource.TestCheckResourceAttr("iaas_nat_gateway.test", "name", gwName),
					resource.TestCheckResourceAttr("iaas_nat_gateway.test", "nat_enabled", "true"),
					resource.TestCheckResourceAttr("iaas_nat_gateway.test", "status", "active"),
					resource.TestCheckResourceAttr("iaas_nat_gateway.test", "public_ip", pubIP),
					resource.TestCheckResourceAttr("iaas_nat_gateway.test", "subnet_ids.#", "1"),
					resource.TestCheckTypeSetElemAttr("iaas_nat_gateway.test", "subnet_ids.*", subnet1),
				),
			},
			// Import the existing resource by composite id and verify state matches.
			{
				ResourceName:            "iaas_nat_gateway.test",
				ImportState:             true,
				ImportStateId:           vpcID + "/" + gwID,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"timeouts"},
			},
			// Update: rename + disable + swap subnet.
			{
				Config: updateCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_nat_gateway.test", "name", gwName2),
					resource.TestCheckResourceAttr("iaas_nat_gateway.test", "nat_enabled", "false"),
					resource.TestCheckResourceAttr("iaas_nat_gateway.test", "subnet_ids.#", "1"),
					resource.TestCheckTypeSetElemAttr("iaas_nat_gateway.test", "subnet_ids.*", subnet2),
				),
			},
		},
	})

	// Assert the CREATE body carried name + nat_enabled + subnet_ids and NOT
	// server-only computed fields.
	creates := srv.Requests("POST", base)
	if len(creates) == 0 {
		t.Fatal("expected at least one POST " + base)
	}
	var createBody map[string]any
	if err := json.Unmarshal(creates[0].Body, &createBody); err != nil {
		t.Fatalf("decoding create body: %v", err)
	}
	if createBody["name"] != gwName {
		t.Errorf("create body name = %v; want %q", createBody["name"], gwName)
	}
	if createBody["nat_enabled"] != true {
		t.Errorf("create body nat_enabled = %v; want true", createBody["nat_enabled"])
	}
	if ids, ok := createBody["subnet_ids"].([]any); !ok || len(ids) != 1 || ids[0] != subnet1 {
		t.Errorf("create body subnet_ids = %v; want [%q]", createBody["subnet_ids"], subnet1)
	}
	for _, stray := range []string{"id", "status", "public_ip"} {
		if _, present := createBody[stray]; present {
			t.Errorf("create body must NOT include %q; got %v", stray, createBody)
		}
	}

	// Assert the rename PATCH fired with the new name.
	patches := srv.Requests("PATCH", itemPath)
	if len(patches) != 1 {
		t.Fatalf("expected exactly 1 PATCH %s, got %d", itemPath, len(patches))
	}
	var patchBody map[string]any
	if err := json.Unmarshal(patches[0].Body, &patchBody); err != nil {
		t.Fatalf("decoding patch body: %v", err)
	}
	if patchBody["name"] != gwName2 {
		t.Errorf("patch body name = %v; want %q", patchBody["name"], gwName2)
	}

	// Assert NAT was disabled via the /disable endpoint (not enabled).
	if disables := srv.Requests("POST", itemPath+"/disable"); len(disables) != 1 {
		t.Fatalf("expected exactly 1 POST %s/disable, got %d", itemPath, len(disables))
	}
	if enables := srv.Requests("POST", itemPath+"/enable"); len(enables) != 0 {
		t.Errorf("expected 0 POST /enable, got %d", len(enables))
	}

	// Assert the subnet swap: attach sub-2, detach sub-1.
	attaches := srv.Requests("POST", itemPath+"/subnet")
	if len(attaches) != 1 {
		t.Fatalf("expected exactly 1 POST %s/subnet, got %d", itemPath, len(attaches))
	}
	var attachBody map[string]any
	if err := json.Unmarshal(attaches[0].Body, &attachBody); err != nil {
		t.Fatalf("decoding attach body: %v", err)
	}
	if attachBody["subnet_id"] != subnet2 {
		t.Errorf("attach body subnet_id = %v; want %q", attachBody["subnet_id"], subnet2)
	}
	if detaches := srv.Requests("DELETE", itemPath+"/subnet/"+subnet1); len(detaches) != 1 {
		t.Fatalf("expected exactly 1 DELETE %s/subnet/%s, got %d", itemPath, subnet1, len(detaches))
	}
}
