package resources_test

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/hypervisor-io/terraform-provider-iaas/internal/acctest"
)

func TestAccLBRoutingRule_basic(t *testing.T) {
	t.Skip("TestAccLBRoutingRule_basic: acceptance test runs only with TF_ACC + a real frontend (manual staging gate)")
}

// TestUnitLBRoutingRule_lifecycle drives the full 3-level CHILD lifecycle:
// create (asserts match_*/lb_backend_id body, not condition_*) → read(scan
// frontends[].routing_rules[]) → 3-part import → update(PATCH match_value) → delete.
func TestUnitLBRoutingRule_lifecycle(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)

	const (
		lbID       = "11111111-1111-1111-1111-111111111111"
		frontendID = "44444444-4444-4444-4444-444444444444"
		ruleID     = "55555555-5555-5555-5555-555555555555"
		backendID  = "22222222-2222-2222-2222-222222222222"
	)

	var mu sync.Mutex
	exists := false
	matchValue := "/api"

	embeddedRules := func() []any {
		mu.Lock()
		defer mu.Unlock()
		if !exists {
			return []any{}
		}
		return []any{
			map[string]any{
				"id":            ruleID,
				"lb_backend_id": backendID,
				"match_type":    "path_prefix",
				"match_value":   matchValue,
				"priority":      100,
				"enabled":       1,
			},
		}
	}

	srv.Handle("GET", "/load-balancer/"+lbID, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"load_balancer": map[string]any{
				"id":     lbID,
				"status": "active",
				"frontends": []any{
					map[string]any{
						"id":            frontendID,
						"name":          "http",
						"routing_rules": embeddedRules(),
					},
				},
			},
		})
	})

	srv.Handle("POST", "/load-balancer/"+lbID+"/frontend/"+frontendID+"/rules", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		exists = true
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Routing rule created.",
			"rule":    map[string]any{"id": ruleID, "lb_backend_id": backendID, "match_type": "path_prefix", "match_value": matchValue, "priority": 100, "enabled": 1},
		})
	})

	srv.Handle("PATCH", "/load-balancer/"+lbID+"/frontend/"+frontendID+"/rule/"+ruleID, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		if v, ok := body["match_value"].(string); ok {
			matchValue = v
		}
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Routing rule updated.",
			"rule":    map[string]any{"id": ruleID, "lb_backend_id": backendID, "match_type": "path_prefix", "match_value": matchValue, "priority": 100, "enabled": 1},
		})
	})

	srv.Handle("DELETE", "/load-balancer/"+lbID+"/frontend/"+frontendID+"/rule/"+ruleID, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		exists = false
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Routing rule deleted."})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	createCfg := providerCfg + `
resource "iaas_lb_routing_rule" "test" {
  load_balancer_id = "` + lbID + `"
  frontend_id      = "` + frontendID + `"
  backend_id       = "` + backendID + `"
  match_type       = "path_prefix"
  match_value      = "/api"
}
`
	updateCfg := providerCfg + `
resource "iaas_lb_routing_rule" "test" {
  load_balancer_id = "` + lbID + `"
  frontend_id      = "` + frontendID + `"
  backend_id       = "` + backendID + `"
  match_type       = "path_prefix"
  match_value      = "/v2"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: createCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_lb_routing_rule.test", "id", ruleID),
					resource.TestCheckResourceAttr("iaas_lb_routing_rule.test", "frontend_id", frontendID),
					resource.TestCheckResourceAttr("iaas_lb_routing_rule.test", "backend_id", backendID),
					resource.TestCheckResourceAttr("iaas_lb_routing_rule.test", "match_type", "path_prefix"),
					resource.TestCheckResourceAttr("iaas_lb_routing_rule.test", "match_value", "/api"),
					resource.TestCheckResourceAttr("iaas_lb_routing_rule.test", "priority", "100"),
					resource.TestCheckResourceAttr("iaas_lb_routing_rule.test", "enabled", "true"),
				),
			},
			{
				ResourceName:      "iaas_lb_routing_rule.test",
				ImportState:       true,
				ImportStateId:     lbID + "/" + frontendID + "/" + ruleID,
				ImportStateVerify: true,
			},
			{
				Config: updateCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_lb_routing_rule.test", "match_value", "/v2"),
				),
			},
		},
	})

	creates := srv.Requests("POST", "/load-balancer/"+lbID+"/frontend/"+frontendID+"/rules")
	if len(creates) == 0 {
		t.Fatal("expected at least one POST .../rules")
	}
	var createBody map[string]any
	if err := json.Unmarshal(creates[0].Body, &createBody); err != nil {
		t.Fatalf("decoding rule create body: %v", err)
	}
	if createBody["match_value"] != "/api" || createBody["lb_backend_id"] != backendID {
		t.Errorf("rule create body = %v; want match_value=/api lb_backend_id=%s", createBody, backendID)
	}
	for _, stray := range []string{"condition_type", "condition_value", "backend_id"} {
		if _, present := createBody[stray]; present {
			t.Errorf("rule create body must NOT use legacy field %q: %v", stray, createBody)
		}
	}

	patches := srv.Requests("PATCH", "/load-balancer/"+lbID+"/frontend/"+frontendID+"/rule/"+ruleID)
	if len(patches) == 0 {
		t.Fatal("expected at least one PATCH .../rule/{id}")
	}
	var patchBody map[string]any
	if err := json.Unmarshal(patches[len(patches)-1].Body, &patchBody); err != nil {
		t.Fatalf("decoding rule patch body: %v", err)
	}
	if patchBody["match_value"] != "/v2" {
		t.Errorf("rule patch body match_value = %v; want /v2", patchBody["match_value"])
	}
}
