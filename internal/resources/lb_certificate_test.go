package resources_test

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/hypervisor-io/terraform-provider-iaas/internal/acctest"
)

func TestAccLBCertificate_basic(t *testing.T) {
	t.Skip("TestAccLBCertificate_basic: acceptance test runs only with TF_ACC + a real load balancer id (manual staging gate)")
}

// TestUnitLBCertificate_lifecycle drives the CHILD lifecycle (no update - every
// field is RequiresReplace):
//
//  1. Create - POST .../certificates with name/certificate/private_key; the cert
//     appears in certificates[] but WITHOUT private_key (it is $hidden). Asserts
//     the create body carried private_key.
//  2. Read - scans the LB SHOW certificates[]; private_key is echoed from plan.
//  3. Import - composite "<lb>/<cert>"; private_key is in ImportStateVerifyIgnore
//     because the SHOW cannot return it.
//  4. Delete - removes the cert from the embedded array.
func TestUnitLBCertificate_lifecycle(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)

	const (
		lbID   = "11111111-1111-1111-1111-111111111111"
		certID = "66666666-6666-6666-6666-666666666666"
		pemCrt = "-----BEGIN CERTIFICATE-----\\nMOCK\\n-----END CERTIFICATE-----"
		pemKey = "-----BEGIN PRIVATE KEY-----\\nMOCK\\n-----END PRIVATE KEY-----"
	)

	var mu sync.Mutex
	exists := false

	embedded := func() []any {
		mu.Lock()
		defer mu.Unlock()
		if !exists {
			return []any{}
		}
		// NOTE: no private_key - it is $hidden server-side.
		return []any{
			map[string]any{
				"id":          certID,
				"name":        "my-cert",
				"certificate": "-----BEGIN CERTIFICATE-----\nMOCK\n-----END CERTIFICATE-----",
			},
		}
	}

	srv.Handle("GET", "/load-balancer/"+lbID, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"load_balancer": map[string]any{
				"id":           lbID,
				"status":       "active",
				"certificates": embedded(),
			},
		})
	})

	srv.Handle("POST", "/load-balancer/"+lbID+"/certificates", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		exists = true
		mu.Unlock()
		// private_key NOT echoed back.
		writeJSON(w, http.StatusOK, map[string]any{
			"success":     true,
			"message":     "Certificate added.",
			"certificate": map[string]any{"id": certID, "name": "my-cert", "certificate": "-----BEGIN CERTIFICATE-----\nMOCK\n-----END CERTIFICATE-----"},
		})
	})

	srv.Handle("DELETE", "/load-balancer/"+lbID+"/certificate/"+certID, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		exists = false
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Certificate deleted."})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	createCfg := providerCfg + `
resource "iaas_lb_certificate" "test" {
  load_balancer_id = "` + lbID + `"
  name             = "my-cert"
  certificate      = "` + pemCrt + `"
  private_key      = "` + pemKey + `"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: createCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_lb_certificate.test", "id", certID),
					resource.TestCheckResourceAttr("iaas_lb_certificate.test", "load_balancer_id", lbID),
					resource.TestCheckResourceAttr("iaas_lb_certificate.test", "name", "my-cert"),
					// private_key is preserved from config (write-only).
					resource.TestCheckResourceAttrSet("iaas_lb_certificate.test", "private_key"),
				),
			},
			{
				ResourceName:            "iaas_lb_certificate.test",
				ImportState:             true,
				ImportStateId:           lbID + "/" + certID,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"private_key"},
			},
		},
	})

	// Assert the CREATE body carried the private_key (write path) and name+cert.
	creates := srv.Requests("POST", "/load-balancer/"+lbID+"/certificates")
	if len(creates) == 0 {
		t.Fatal("expected at least one POST .../certificates")
	}
	var createBody map[string]any
	if err := json.Unmarshal(creates[0].Body, &createBody); err != nil {
		t.Fatalf("decoding certificate create body: %v", err)
	}
	if createBody["name"] != "my-cert" {
		t.Errorf("certificate create body name = %v; want my-cert", createBody["name"])
	}
	if _, present := createBody["private_key"]; !present {
		t.Errorf("certificate create body MUST include private_key (write path): %v", createBody)
	}
	if _, present := createBody["certificate"]; !present {
		t.Errorf("certificate create body MUST include certificate: %v", createBody)
	}
}
