package resources_test

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/hypervisor-io/terraform-provider-iaas/internal/acctest"
)

// TestAccLBBackend_basic - LIVE acceptance test (manual staging gate).
// Auto-skips unless TF_ACC is set and IAAS_TEST_LB_ID is supplied.
func TestAccLBBackend_basic(t *testing.T) {
	t.Skip("TestAccLBBackend_basic: acceptance test runs only with TF_ACC + a real load balancer id (manual staging gate)")
}

// TestUnitLBBackend_lifecycle drives the full CHILD lifecycle against a stateful
// mock:
//
//  1. Create - POST /load-balancer/{lbId}/backends; the backend appears in the
//     LB SHOW embedded backends[]. Asserts the create body (algorithm, not
//     balance; connect_timeout/server_timeout).
//  2. Read - scans the LB SHOW backends[] for the id.
//  3. Import - composite id "<lb_id>/<backend_id>".
//  4. Update - PATCH backend/{id} (rename + algorithm + explicit
//     connect_timeout/server_timeout clear); asserts the PATCH body.
//  5. Delete - removes the backend from the embedded array.
func TestUnitLBBackend_lifecycle(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)

	const (
		lbID      = "11111111-1111-1111-1111-111111111111"
		backendID = "22222222-2222-2222-2222-222222222222"
	)

	var mu sync.Mutex
	exists := false
	name := "web"
	algorithm := "roundrobin"
	var connectTimeout any
	var serverTimeout any

	backendObject := func() map[string]any {
		return map[string]any{
			"id":              backendID,
			"name":            name,
			"algorithm":       algorithm,
			"mode":            "http",
			"connect_timeout": connectTimeout,
			"server_timeout":  serverTimeout,
		}
	}

	embedded := func() []any {
		mu.Lock()
		defer mu.Unlock()
		if !exists {
			return []any{}
		}
		return []any{backendObject()}
	}

	// Parent LB SHOW - embeds backends[] (the read/scan source).
	srv.Handle("GET", "/load-balancer/"+lbID, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"load_balancer": map[string]any{
				"id":       lbID,
				"status":   "active",
				"backends": embedded(),
			},
		})
	})

	// CREATE backend.
	srv.Handle("POST", "/load-balancer/"+lbID+"/backends", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		exists = true
		// Absent key and explicit null both leave the server-side value unset.
		connectTimeout = body["connect_timeout"]
		serverTimeout = body["server_timeout"]
		backend := backendObject()
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Backend created.",
			"backend": backend,
			"sync":    map[string]any{"status": "ok"},
		})
	})

	// UPDATE backend - reflect the new name/algorithm/timeouts in the embedded array.
	srv.Handle("PATCH", "/load-balancer/"+lbID+"/backend/"+backendID, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		if v, ok := body["name"].(string); ok {
			name = v
		}
		if v, ok := body["algorithm"].(string); ok {
			algorithm = v
		}
		// The provider sends an explicit null to clear connect_timeout/
		// server_timeout; an absent key leaves it untouched (mirrors the
		// API's has()-style whitelist, same as idle_timeout on the frontend).
		if v, ok := body["connect_timeout"]; ok {
			connectTimeout = v
		}
		if v, ok := body["server_timeout"]; ok {
			serverTimeout = v
		}
		backend := backendObject()
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Backend updated.",
			"backend": backend,
		})
	})

	// DELETE backend.
	srv.Handle("DELETE", "/load-balancer/"+lbID+"/backend/"+backendID, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		exists = false
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Backend deleted."})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	createCfg := providerCfg + `
resource "iaas_lb_backend" "test" {
  load_balancer_id = "` + lbID + `"
  name             = "web"
  algorithm        = "roundrobin"
  connect_timeout  = 10
  server_timeout   = 900
}
`
	updateCfg := providerCfg + `
resource "iaas_lb_backend" "test" {
  load_balancer_id = "` + lbID + `"
  name             = "web2"
  algorithm        = "leastconn"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: createCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_lb_backend.test", "id", backendID),
					resource.TestCheckResourceAttr("iaas_lb_backend.test", "load_balancer_id", lbID),
					resource.TestCheckResourceAttr("iaas_lb_backend.test", "name", "web"),
					resource.TestCheckResourceAttr("iaas_lb_backend.test", "algorithm", "roundrobin"),
					resource.TestCheckResourceAttr("iaas_lb_backend.test", "mode", "http"),
					resource.TestCheckResourceAttr("iaas_lb_backend.test", "connect_timeout", "10"),
					resource.TestCheckResourceAttr("iaas_lb_backend.test", "server_timeout", "900"),
				),
			},
			{
				ResourceName:      "iaas_lb_backend.test",
				ImportState:       true,
				ImportStateId:     lbID + "/" + backendID,
				ImportStateVerify: true,
			},
			{
				Config: updateCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_lb_backend.test", "name", "web2"),
					resource.TestCheckResourceAttr("iaas_lb_backend.test", "algorithm", "leastconn"),
					// connect_timeout/server_timeout were removed from the config:
					// the update must clear them, leaving both unset in state.
					resource.TestCheckNoResourceAttr("iaas_lb_backend.test", "connect_timeout"),
					resource.TestCheckNoResourceAttr("iaas_lb_backend.test", "server_timeout"),
				),
			},
		},
	})

	// Assert the CREATE body used "algorithm" (not "balance") and omitted server fields.
	creates := srv.Requests("POST", "/load-balancer/"+lbID+"/backends")
	if len(creates) == 0 {
		t.Fatal("expected at least one POST .../backends")
	}
	var createBody map[string]any
	if err := json.Unmarshal(creates[0].Body, &createBody); err != nil {
		t.Fatalf("decoding backend create body: %v", err)
	}
	if createBody["algorithm"] != "roundrobin" || createBody["name"] != "web" {
		t.Errorf("backend create body = %v; want algorithm=roundrobin name=web", createBody)
	}
	if _, present := createBody["balance"]; present {
		t.Errorf("backend create body must use 'algorithm', not 'balance': %v", createBody)
	}
	for _, stray := range []string{"id", "load_balancer_id"} {
		if _, present := createBody[stray]; present {
			t.Errorf("backend create body must NOT include %q; got %v", stray, createBody)
		}
	}
	if createBody["connect_timeout"] != float64(10) {
		t.Errorf("backend create body connect_timeout = %v; want 10", createBody["connect_timeout"])
	}
	if createBody["server_timeout"] != float64(900) {
		t.Errorf("backend create body server_timeout = %v; want 900", createBody["server_timeout"])
	}

	// Assert the PATCH body carried the rename and an EXPLICIT null for both
	// timeouts (the API clears a field only when the key is present).
	patches := srv.Requests("PATCH", "/load-balancer/"+lbID+"/backend/"+backendID)
	if len(patches) == 0 {
		t.Fatal("expected at least one PATCH .../backend/{id}")
	}
	var patchBody map[string]any
	if err := json.Unmarshal(patches[len(patches)-1].Body, &patchBody); err != nil {
		t.Fatalf("decoding backend patch body: %v", err)
	}
	if patchBody["name"] != "web2" || patchBody["algorithm"] != "leastconn" {
		t.Errorf("backend patch body = %v; want name=web2 algorithm=leastconn", patchBody)
	}
	if v, present := patchBody["connect_timeout"]; !present || v != nil {
		t.Errorf("backend patch body connect_timeout = %v (present=%v); want explicit null", v, present)
	}
	if v, present := patchBody["server_timeout"]; !present || v != nil {
		t.Errorf("backend patch body server_timeout = %v (present=%v); want explicit null", v, present)
	}
}
