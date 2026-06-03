package resources_test

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/iaas/terraform-provider-iaas/internal/acctest"
)

func TestAccLBTarget_basic(t *testing.T) {
	t.Skip("TestAccLBTarget_basic: acceptance test runs only with TF_ACC + a real backend (manual staging gate)")
}

// TestUnitLBTarget_lifecycle drives the full 3-level CHILD lifecycle:
//
//  1. Create — POST .../backend/{bid}/targets; asserts the body uses target_ip /
//     target_port (not ip/port). The target appears in backends[].targets[].
//  2. Read — scans the LB SHOW backends[bid].targets[tid].
//  3. Import — 3-part composite "<lb>/<backend>/<target>".
//  4. Update — PATCH .../target/{tid} (weight); asserts the PATCH body.
//  5. Delete — removes the target from the embedded array.
func TestUnitLBTarget_lifecycle(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)

	const (
		lbID      = "11111111-1111-1111-1111-111111111111"
		backendID = "22222222-2222-2222-2222-222222222222"
		targetID  = "33333333-3333-3333-3333-333333333333"
	)

	var mu sync.Mutex
	exists := false
	weight := 100

	embeddedTargets := func() []any {
		mu.Lock()
		defer mu.Unlock()
		if !exists {
			return []any{}
		}
		return []any{
			map[string]any{
				"id":          targetID,
				"target_ip":   "10.0.0.5",
				"target_port": 8080,
				"weight":      weight,
				"enabled":     1,
			},
		}
	}

	srv.Handle("GET", "/load-balancer/"+lbID, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"load_balancer": map[string]any{
				"id":     lbID,
				"status": "active",
				"backends": []any{
					map[string]any{
						"id":      backendID,
						"name":    "web",
						"targets": embeddedTargets(),
					},
				},
			},
		})
	})

	srv.Handle("POST", "/load-balancer/"+lbID+"/backend/"+backendID+"/targets", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		exists = true
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Target added.",
			"target":  map[string]any{"id": targetID, "target_ip": "10.0.0.5", "target_port": 8080, "weight": 100, "enabled": 1},
		})
	})

	srv.Handle("PATCH", "/load-balancer/"+lbID+"/backend/"+backendID+"/target/"+targetID, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		if v, ok := body["weight"].(float64); ok {
			weight = int(v)
		}
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Target updated.",
			"target":  map[string]any{"id": targetID, "target_ip": "10.0.0.5", "target_port": 8080, "weight": weight, "enabled": 1},
		})
	})

	srv.Handle("DELETE", "/load-balancer/"+lbID+"/backend/"+backendID+"/target/"+targetID, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		exists = false
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Target removed."})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	createCfg := providerCfg + `
resource "iaas_lb_target" "test" {
  load_balancer_id = "` + lbID + `"
  backend_id       = "` + backendID + `"
  target_ip        = "10.0.0.5"
  target_port      = 8080
}
`
	updateCfg := providerCfg + `
resource "iaas_lb_target" "test" {
  load_balancer_id = "` + lbID + `"
  backend_id       = "` + backendID + `"
  target_ip        = "10.0.0.5"
  target_port      = 8080
  weight           = 200
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: createCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_lb_target.test", "id", targetID),
					resource.TestCheckResourceAttr("iaas_lb_target.test", "backend_id", backendID),
					resource.TestCheckResourceAttr("iaas_lb_target.test", "target_ip", "10.0.0.5"),
					resource.TestCheckResourceAttr("iaas_lb_target.test", "target_port", "8080"),
					resource.TestCheckResourceAttr("iaas_lb_target.test", "weight", "100"),
					resource.TestCheckResourceAttr("iaas_lb_target.test", "enabled", "true"),
				),
			},
			{
				ResourceName:      "iaas_lb_target.test",
				ImportState:       true,
				ImportStateId:     lbID + "/" + backendID + "/" + targetID,
				ImportStateVerify: true,
			},
			{
				Config: updateCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_lb_target.test", "weight", "200"),
				),
			},
		},
	})

	creates := srv.Requests("POST", "/load-balancer/"+lbID+"/backend/"+backendID+"/targets")
	if len(creates) == 0 {
		t.Fatal("expected at least one POST .../targets")
	}
	var createBody map[string]any
	if err := json.Unmarshal(creates[0].Body, &createBody); err != nil {
		t.Fatalf("decoding target create body: %v", err)
	}
	if createBody["target_ip"] != "10.0.0.5" || createBody["target_port"] != float64(8080) {
		t.Errorf("target create body = %v; want target_ip/target_port", createBody)
	}
	for _, stray := range []string{"ip", "port", "instance_id", "id"} {
		if _, present := createBody[stray]; present {
			t.Errorf("target create body must NOT include %q; got %v", stray, createBody)
		}
	}

	patches := srv.Requests("PATCH", "/load-balancer/"+lbID+"/backend/"+backendID+"/target/"+targetID)
	if len(patches) == 0 {
		t.Fatal("expected at least one PATCH .../target/{id}")
	}
	var patchBody map[string]any
	if err := json.Unmarshal(patches[len(patches)-1].Body, &patchBody); err != nil {
		t.Fatalf("decoding target patch body: %v", err)
	}
	if patchBody["weight"] != float64(200) {
		t.Errorf("target patch body weight = %v; want 200", patchBody["weight"])
	}
}
