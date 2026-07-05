package resources_test

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/iaas/terraform-provider-iaas/internal/acctest"
)

func TestAccKubernetesSslCertificate_basic(t *testing.T) {
	t.Skip("TestAccKubernetesSslCertificate_basic: acceptance test runs only with TF_ACC + a real cluster id (manual staging gate)")
}

// TestUnitKubernetesSslCertificate_rejectCustomWithoutCertFields — NEGATIVE
// ConfigValidators test. source = "custom" without certificate/private_key
// must be rejected at PLAN time (mirroring the Master's
// required_if:source,custom validation) — no API call is ever made.
func TestUnitKubernetesSslCertificate_rejectCustomWithoutCertFields(t *testing.T) {
	ensureTFBinary(t)

	// A minimal mock server is required so the provider can configure itself;
	// no API calls will ever reach it because the validator errors at plan time.
	srv := acctest.NewMockServer(t)
	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: providerCfg + `
resource "iaas_kubernetes_ssl_certificate" "bad" {
  cluster_id = "11111111-1111-1111-1111-111111111111"
  source     = "custom"
  domain     = "api.example.com"
}
`,
				ExpectError: regexp.MustCompile(`(?i)certificate is required when source`),
			},
		},
	})
}

// TestUnitKubernetesSslCertificate_lifecycle drives the CHILD lifecycle (no
// update — every field is RequiresReplace):
//
//  1. Create — POST .../ssl-certificates with source/domain/certificate/
//     private_key; asserts the create body carried them AND the
//     Idempotency-Key header (idempotency.user). The cert then appears in the
//     cluster-scoped LIST but WITHOUT certificate/private_key/chain — the
//     cluster ssl-certificates index() query never selects them (unlike the
//     plain iaas_lb_certificate's LB-SHOW embed, which does return
//     certificate/chain). source is asserted to round-trip via the persisted
//     `type` ("manual" -> "custom").
//  2. Read — lists the cluster's ssl-certificates and matches by id;
//     certificate/private_key/chain are echoed from state (write-only).
//  3. Import — composite "<cluster_id>/<cert_id>"; certificate/private_key/
//     chain are in ImportStateVerifyIgnore because the LIST cannot return them.
//  4. Delete — DELETEs the SINGULAR ".../ssl-certificate/{certId}" path,
//     carrying the Idempotency-Key header too.
func TestUnitKubernetesSslCertificate_lifecycle(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)

	const (
		clusterID = "22222222-2222-2222-2222-222222222222"
		certID    = "77777777-7777-7777-7777-777777777777"
		domain    = "api.example.com"
		pemCrt    = "-----BEGIN CERTIFICATE-----\\nMOCK\\n-----END CERTIFICATE-----"
		pemKey    = "-----BEGIN PRIVATE KEY-----\\nMOCK\\n-----END PRIVATE KEY-----"
	)

	var mu sync.Mutex
	exists := false

	certs := func() []any {
		mu.Lock()
		defer mu.Unlock()
		if !exists {
			return []any{}
		}
		// NOTE: no certificate/private_key/chain — the cluster-scoped index()
		// query never selects them (metadata + LE status only).
		return []any{
			map[string]any{
				"id":     certID,
				"name":   domain,
				"type":   "manual",
				"domain": domain,
			},
		}
	}

	srv.Handle("GET", "/kubernetes/cluster/"+clusterID+"/ssl-certificates", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"certs":   certs(),
		})
	})

	srv.Handle("POST", "/kubernetes/cluster/"+clusterID+"/ssl-certificates", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		exists = true
		mu.Unlock()
		// certificate IS present here (only private_key is $hidden model-wide),
		// but the resource must NOT rely on it — it always echoes from the plan.
		writeJSON(w, http.StatusOK, map[string]any{
			"success":     true,
			"message":     "Certificate added.",
			"certificate": map[string]any{"id": certID, "name": domain, "domain": domain, "certificate": "-----BEGIN CERTIFICATE-----\nMOCK\n-----END CERTIFICATE-----"},
		})
	})

	srv.Handle("DELETE", "/kubernetes/cluster/"+clusterID+"/ssl-certificate/"+certID, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		exists = false
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Certificate deleted."})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	createCfg := providerCfg + `
resource "iaas_kubernetes_ssl_certificate" "test" {
  cluster_id  = "` + clusterID + `"
  source      = "custom"
  domain      = "` + domain + `"
  certificate = "` + pemCrt + `"
  private_key = "` + pemKey + `"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: createCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_kubernetes_ssl_certificate.test", "id", certID),
					resource.TestCheckResourceAttr("iaas_kubernetes_ssl_certificate.test", "cluster_id", clusterID),
					resource.TestCheckResourceAttr("iaas_kubernetes_ssl_certificate.test", "domain", domain),
					resource.TestCheckResourceAttr("iaas_kubernetes_ssl_certificate.test", "source", "custom"),
					resource.TestCheckResourceAttr("iaas_kubernetes_ssl_certificate.test", "type", "manual"),
					resource.TestCheckResourceAttr("iaas_kubernetes_ssl_certificate.test", "name", domain),
					// private_key is preserved from config (write-only).
					resource.TestCheckResourceAttrSet("iaas_kubernetes_ssl_certificate.test", "private_key"),
				),
			},
			{
				ResourceName:            "iaas_kubernetes_ssl_certificate.test",
				ImportState:             true,
				ImportStateId:           clusterID + "/" + certID,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"certificate", "private_key", "chain"},
			},
		},
	})

	// Assert the CREATE request hit the CLUSTER-scoped path (not a load-balancer
	// path) and carried source/domain/certificate/private_key + Idempotency-Key.
	creates := srv.Requests("POST", "/kubernetes/cluster/"+clusterID+"/ssl-certificates")
	if len(creates) == 0 {
		t.Fatal("expected at least one POST .../ssl-certificates")
	}
	var createBody map[string]any
	if err := json.Unmarshal(creates[0].Body, &createBody); err != nil {
		t.Fatalf("decoding certificate create body: %v", err)
	}
	if createBody["source"] != "custom" {
		t.Errorf("create body source = %v; want custom", createBody["source"])
	}
	if createBody["domain"] != domain {
		t.Errorf("create body domain = %v; want %s", createBody["domain"], domain)
	}
	if _, present := createBody["private_key"]; !present {
		t.Errorf("certificate create body MUST include private_key (write path): %v", createBody)
	}
	if _, present := createBody["certificate"]; !present {
		t.Errorf("certificate create body MUST include certificate: %v", createBody)
	}
	if creates[0].Header.Get("Idempotency-Key") == "" {
		t.Error("expected a non-empty Idempotency-Key header on create (idempotency.user)")
	}

	// Assert the DELETE request hit the SINGULAR cluster-scoped path.
	deletes := srv.Requests("DELETE", "/kubernetes/cluster/"+clusterID+"/ssl-certificate/"+certID)
	if len(deletes) == 0 {
		t.Fatal("expected at least one DELETE .../ssl-certificate/{id}")
	}
	if deletes[0].Header.Get("Idempotency-Key") == "" {
		t.Error("expected a non-empty Idempotency-Key header on delete (idempotency.user)")
	}

	// Belt-and-braces: every recorded create/delete request hit the
	// CLUSTER-scoped route — never a bare "/load-balancer/..." path (this
	// resource must not fall back to the plain LB certificate routes).
	for _, req := range append(append([]acctest.RecordedRequest{}, creates...), deletes...) {
		if !strings.Contains(req.Path, "/kubernetes/cluster/"+clusterID) {
			t.Errorf("request path %s did not hit the cluster-scoped route", req.Path)
		}
	}
}
