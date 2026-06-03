package resources_test

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/iaas/terraform-provider-iaas/internal/acctest"
)

func TestAccLBFrontend_basic(t *testing.T) {
	t.Skip("TestAccLBFrontend_basic: acceptance test runs only with TF_ACC + a real load balancer id (manual staging gate)")
}

// TestUnitLBFrontend_lifecycle drives the full CHILD lifecycle:
// create (asserts port/protocol body, not bind_port) → read(scan) → import →
// update(PATCH name) → delete.
func TestUnitLBFrontend_lifecycle(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)

	const (
		lbID       = "11111111-1111-1111-1111-111111111111"
		frontendID = "44444444-4444-4444-4444-444444444444"
	)

	var mu sync.Mutex
	exists := false
	name := "http"

	embedded := func() []any {
		mu.Lock()
		defer mu.Unlock()
		if !exists {
			return []any{}
		}
		return []any{
			map[string]any{
				"id":       frontendID,
				"name":     name,
				"port":     80,
				"protocol": "http",
				"mode":     "http",
				"enabled":  1,
			},
		}
	}

	srv.Handle("GET", "/load-balancer/"+lbID, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"load_balancer": map[string]any{
				"id":        lbID,
				"status":    "active",
				"frontends": embedded(),
			},
		})
	})

	srv.Handle("POST", "/load-balancer/"+lbID+"/frontends", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		exists = true
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"success":  true,
			"message":  "Frontend created.",
			"frontend": map[string]any{"id": frontendID, "name": name, "port": 80, "protocol": "http", "mode": "http", "enabled": 1},
		})
	})

	srv.Handle("PATCH", "/load-balancer/"+lbID+"/frontend/"+frontendID, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		if v, ok := body["name"].(string); ok {
			name = v
		}
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"success":  true,
			"message":  "Frontend updated.",
			"frontend": map[string]any{"id": frontendID, "name": name, "port": 80, "protocol": "http", "mode": "http", "enabled": 1},
		})
	})

	srv.Handle("DELETE", "/load-balancer/"+lbID+"/frontend/"+frontendID, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		exists = false
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Frontend deleted."})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	createCfg := providerCfg + `
resource "iaas_lb_frontend" "test" {
  load_balancer_id = "` + lbID + `"
  name             = "http"
  port             = 80
  protocol         = "http"
}
`
	updateCfg := providerCfg + `
resource "iaas_lb_frontend" "test" {
  load_balancer_id = "` + lbID + `"
  name             = "http-renamed"
  port             = 80
  protocol         = "http"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: createCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_lb_frontend.test", "id", frontendID),
					resource.TestCheckResourceAttr("iaas_lb_frontend.test", "load_balancer_id", lbID),
					resource.TestCheckResourceAttr("iaas_lb_frontend.test", "name", "http"),
					resource.TestCheckResourceAttr("iaas_lb_frontend.test", "port", "80"),
					resource.TestCheckResourceAttr("iaas_lb_frontend.test", "protocol", "http"),
					resource.TestCheckResourceAttr("iaas_lb_frontend.test", "mode", "http"),
					resource.TestCheckResourceAttr("iaas_lb_frontend.test", "enabled", "true"),
				),
			},
			{
				ResourceName:      "iaas_lb_frontend.test",
				ImportState:       true,
				ImportStateId:     lbID + "/" + frontendID,
				ImportStateVerify: true,
			},
			{
				Config: updateCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_lb_frontend.test", "name", "http-renamed"),
				),
			},
		},
	})

	creates := srv.Requests("POST", "/load-balancer/"+lbID+"/frontends")
	if len(creates) == 0 {
		t.Fatal("expected at least one POST .../frontends")
	}
	var createBody map[string]any
	if err := json.Unmarshal(creates[0].Body, &createBody); err != nil {
		t.Fatalf("decoding frontend create body: %v", err)
	}
	if createBody["port"] != float64(80) || createBody["protocol"] != "http" || createBody["name"] != "http" {
		t.Errorf("frontend create body = %v; want name=http port=80 protocol=http", createBody)
	}
	if _, present := createBody["bind_port"]; present {
		t.Errorf("frontend create body must use 'port', not 'bind_port': %v", createBody)
	}

	patches := srv.Requests("PATCH", "/load-balancer/"+lbID+"/frontend/"+frontendID)
	if len(patches) == 0 {
		t.Fatal("expected at least one PATCH .../frontend/{id}")
	}
	var patchBody map[string]any
	if err := json.Unmarshal(patches[len(patches)-1].Body, &patchBody); err != nil {
		t.Fatalf("decoding frontend patch body: %v", err)
	}
	if patchBody["name"] != "http-renamed" {
		t.Errorf("frontend patch body name = %v; want http-renamed", patchBody["name"])
	}
}
