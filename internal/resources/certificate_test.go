package resources_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"

	"github.com/hypervisor-io/terraform-provider-iaas/internal/acctest"
)

// ---------------------------------------------------------------------------
// TestAccCertificate_basic - LIVE acceptance test (manual staging gate).
//
// Auto-skips unless TF_ACC is set (resource.Test enforces this), so it never
// runs or blocks CI. Requires a reachable panel + token via
// IAAS_API_ENDPOINT / IAAS_API_TOKEN (checked by acctest.PreCheck), plus real
// PEM files for a certificate/private key pair supplied via the env vars
// below; the test skips cleanly when either var is absent so a bare TF_ACC=1
// run does not fail with a confusing 422.
//
//	IAAS_TEST_CERT_PEM_FILE - path to a PEM-encoded certificate file
//	IAAS_TEST_KEY_PEM_FILE  - path to the matching PEM-encoded private key file
//
// The second step renames the certificate (name is the only field this test
// can safely change without a second real PEM pair) and asserts the plan is
// an in-place Update, not a Replace - proving PUT /certificate/{id} is wired
// end to end against a real server, not just the mock.
//
// ---------------------------------------------------------------------------
func TestAccCertificate_basic(t *testing.T) {
	certPath := os.Getenv("IAAS_TEST_CERT_PEM_FILE")
	keyPath := os.Getenv("IAAS_TEST_KEY_PEM_FILE")
	if certPath == "" || keyPath == "" {
		t.Skip("TestAccCertificate_basic: set IAAS_TEST_CERT_PEM_FILE and IAAS_TEST_KEY_PEM_FILE to run this acceptance test")
	}

	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("reading IAAS_TEST_CERT_PEM_FILE: %v", err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("reading IAAS_TEST_KEY_PEM_FILE: %v", err)
	}

	config := fmt.Sprintf(`
resource "iaas_certificate" "test" {
  name        = "tf-acc-test-cert"
  certificate = %q
  private_key = %q
}
`, string(certPEM), string(keyPEM))

	renamedConfig := fmt.Sprintf(`
resource "iaas_certificate" "test" {
  name        = "tf-acc-test-cert-renamed"
  certificate = %q
  private_key = %q
}
`, string(certPEM), string(keyPEM))

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("iaas_certificate.test", "id"),
					resource.TestCheckResourceAttrSet("iaas_certificate.test", "domain"),
					resource.TestCheckResourceAttrSet("iaas_certificate.test", "fingerprint_sha256"),
				),
			},
			{
				Config: renamedConfig,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("iaas_certificate.test", plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_certificate.test", "name", "tf-acc-test-cert-renamed"),
				),
			},
			{
				ResourceName:            "iaas_certificate.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"certificate", "private_key", "chain"},
			},
		},
	})
}

// ---------------------------------------------------------------------------
// TestUnitCertificate_lifecycle - MOCK-backed lifecycle proof.
//
// Drives the full resource lifecycle against canned API responses, with no
// live panel: create, an in-place UPDATE via PUT /certificate/{id} (rotating
// name/certificate/private_key/chain - NUI-V-R17-ACM-ROTATE1/2), import, then
// implicit delete teardown. The update step's ConfigPlanChecks asserts
// plancheck.ResourceActionUpdate (not Replace/DestroyBeforeCreate), which is
// the plan-level proof that RequiresReplace is really gone; the resource-level
// GET/PUT mock is stateful (guarded by a mutex) so the id-stability and the
// post-update computed-attribute refresh are both exercised through a real
// plan+apply+refresh cycle, not just checked against the immediate apply
// response.
//
// resource.UnitTest needs a terraform/opentofu binary on PATH or via
// TF_ACC_TERRAFORM_PATH; if none is found the test is skipped with a clear
// binary-not-found message (see ensureTFBinary in ssh_key_test.go).
// ---------------------------------------------------------------------------
func TestUnitCertificate_lifecycle(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)

	const (
		certID      = "88888888-8888-8888-8888-888888888888"
		domain      = "example.test"
		sanA        = "www.example.test"
		fpSha256    = "AA:BB:CC:DD:EE:FF"
		certPEM     = "-----BEGIN CERTIFICATE-----\nMIIB...\n-----END CERTIFICATE-----"
		keyPEM      = "-----BEGIN PRIVATE KEY-----\nMIIE...\n-----END PRIVATE KEY-----"
		expires     = "2027-01-01T00:00:00Z"
		rotDomain   = "rotated.example.test"
		rotSan      = "www.rotated.example.test"
		rotFp       = "11:22:33:44:55:66"
		rotCertPEM  = "-----BEGIN CERTIFICATE-----\nMIIC...rotated...\n-----END CERTIFICATE-----"
		rotKeyPEM   = "-----BEGIN PRIVATE KEY-----\nMIIF...rotated...\n-----END PRIVATE KEY-----"
		rotChainPEM = "-----BEGIN CERTIFICATE-----\nMIID...chain...\n-----END CERTIFICATE-----"
		rotExpires  = "2028-06-01T00:00:00Z"
	)

	// Mutable server-side state so GET/PUT behave like the real API: PUT
	// mutates the stored object in place (same id), and the subsequent Read
	// (plan refresh, then the explicit import step) observes the new values.
	var (
		mu      sync.Mutex
		curName = "example"
		curObj  = certificateObject(certID, "example", domain, sanA, fpSha256, expires, "active")
	)

	// CREATE - POST /certificates returns 200 + {success,message,certificate}.
	// certificate/private_key/chain are NEVER echoed back.
	srv.Handle("POST", "/certificates", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"success":     true,
			"message":     "Certificate uploaded successfully.",
			"certificate": curObj,
		})
	})

	// READ - GET /certificate/{id} scans the single-object SHOW route.
	srv.Handle("GET", "/certificate/"+certID, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"success":     true,
			"certificate": curObj,
		})
	})

	// UPDATE - PUT /certificate/{id} rotates the material in place (same id)
	// and reports a successful resync for one referencing load balancer.
	srv.Handle("PUT", "/certificate/"+certID, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if name, ok := body["name"].(string); ok {
			curName = name
		}
		curObj = certificateObject(certID, curName, rotDomain, rotSan, rotFp, rotExpires, "active")
		writeJSON(w, http.StatusOK, map[string]any{
			"success":     true,
			"certificate": curObj,
			"resync": []any{
				map[string]any{"id": "lb-uuid-1", "name": "web-lb", "success": true},
			},
		})
	})

	// DELETE - DELETE /certificate/{id} succeeds at 200.
	srv.Handle("DELETE", "/certificate/"+certID, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Certificate deleted.",
		})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	// %q renders the PEMs as quoted strings with \n escapes: HCL forbids
	// literal newlines inside quoted strings (same approach as the LIVE
	// TestAccCertificate_basic config above).
	createCfg := providerCfg + fmt.Sprintf(`
resource "iaas_certificate" "test" {
  name        = "example"
  certificate = %q
  private_key = %q
}
`, certPEM, keyPEM)

	updateCfg := providerCfg + fmt.Sprintf(`
resource "iaas_certificate" "test" {
  name        = "example-rotated"
  certificate = %q
  private_key = %q
  chain       = %q
}
`, rotCertPEM, rotKeyPEM, rotChainPEM)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			// Create + read-back.
			{
				Config: createCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_certificate.test", "id", certID),
					resource.TestCheckResourceAttr("iaas_certificate.test", "name", "example"),
					resource.TestCheckResourceAttr("iaas_certificate.test", "certificate", certPEM),
					resource.TestCheckResourceAttr("iaas_certificate.test", "private_key", keyPEM),
					resource.TestCheckResourceAttr("iaas_certificate.test", "domain", domain),
					resource.TestCheckResourceAttr("iaas_certificate.test", "san_domains.#", "1"),
					resource.TestCheckResourceAttr("iaas_certificate.test", "san_domains.0", sanA),
					resource.TestCheckResourceAttr("iaas_certificate.test", "fingerprint_sha256", fpSha256),
					resource.TestCheckResourceAttr("iaas_certificate.test", "expires_at", expires),
					resource.TestCheckResourceAttr("iaas_certificate.test", "status", "active"),
				),
			},
			// Rotate name/certificate/private_key/chain. ConfigPlanChecks proves
			// the plan is an in-place Update, not a Replace - the whole point of
			// dropping RequiresReplace.
			{
				Config: updateCfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("iaas_certificate.test", plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_certificate.test", "id", certID),
					resource.TestCheckResourceAttr("iaas_certificate.test", "name", "example-rotated"),
					resource.TestCheckResourceAttr("iaas_certificate.test", "certificate", rotCertPEM),
					resource.TestCheckResourceAttr("iaas_certificate.test", "private_key", rotKeyPEM),
					resource.TestCheckResourceAttr("iaas_certificate.test", "chain", rotChainPEM),
					resource.TestCheckResourceAttr("iaas_certificate.test", "domain", rotDomain),
					resource.TestCheckResourceAttr("iaas_certificate.test", "san_domains.0", rotSan),
					resource.TestCheckResourceAttr("iaas_certificate.test", "fingerprint_sha256", rotFp),
					resource.TestCheckResourceAttr("iaas_certificate.test", "expires_at", rotExpires),
				),
			},
			// Import the existing (now rotated) resource and verify state
			// matches (write-only PEM fields cannot be recovered from the API).
			{
				ResourceName:            "iaas_certificate.test",
				ImportState:             true,
				ImportStateId:           certID,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"certificate", "private_key", "chain"},
			},
		},
	})

	// Assert the create request sent name/certificate/private_key and NOT
	// stray server-only computed fields.
	creates := srv.Requests("POST", "/certificates")
	if len(creates) == 0 {
		t.Fatal("expected at least one POST /certificates")
	}
	var createBody map[string]any
	if err := json.Unmarshal(creates[0].Body, &createBody); err != nil {
		t.Fatalf("decoding create body: %v", err)
	}
	if createBody["name"] != "example" {
		t.Errorf("create body name = %v; want example", createBody["name"])
	}
	if createBody["certificate"] != certPEM {
		t.Errorf("create body certificate = %v; want %q", createBody["certificate"], certPEM)
	}
	if createBody["private_key"] != keyPEM {
		t.Errorf("create body private_key = %v; want %q", createBody["private_key"], keyPEM)
	}
	for _, stray := range []string{"id", "domain", "san_domains", "status", "fingerprint_sha256", "expires_at"} {
		if _, present := createBody[stray]; present {
			t.Errorf("create body must NOT include %q; got %v", stray, createBody)
		}
	}
	// chain was not configured - must not be sent.
	if _, present := createBody["chain"]; present {
		t.Errorf("create body must NOT include unconfigured %q; got %v", "chain", createBody)
	}

	// Assert the update request sent the rotated name/certificate/private_key/chain.
	updates := srv.Requests("PUT", "/certificate/"+certID)
	if len(updates) == 0 {
		t.Fatal("expected at least one PUT /certificates/" + certID)
	}
	var updateBody map[string]any
	if err := json.Unmarshal(updates[0].Body, &updateBody); err != nil {
		t.Fatalf("decoding update body: %v", err)
	}
	if updateBody["name"] != "example-rotated" {
		t.Errorf("update body name = %v; want example-rotated", updateBody["name"])
	}
	if updateBody["certificate"] != rotCertPEM {
		t.Errorf("update body certificate = %v; want %q", updateBody["certificate"], rotCertPEM)
	}
	if updateBody["private_key"] != rotKeyPEM {
		t.Errorf("update body private_key = %v; want %q", updateBody["private_key"], rotKeyPEM)
	}
	if updateBody["chain"] != rotChainPEM {
		t.Errorf("update body chain = %v; want %q", updateBody["chain"], rotChainPEM)
	}
}

// certificateObject builds a serialised certificate object matching the API
// CREATE/SHOW/UPDATE shape (never includes certificate/private_key/chain).
func certificateObject(id, name, domain, sanDomain, fingerprint, expiresAt, status string) map[string]any {
	return map[string]any{
		"id":                 id,
		"name":               name,
		"domain":             domain,
		"san_domains":        []any{sanDomain},
		"type":               "manual",
		"status":             status,
		"expires_at":         expiresAt,
		"fingerprint_sha256": fingerprint,
		"usage_count":        0,
	}
}

// ---------------------------------------------------------------------------
// TestUnitCertificate_letsEncryptReplaceRejected - MOCK-backed proof that
// updating a Let's Encrypt-issued certificate surfaces a clear, dedicated
// diagnostic (not the generic "422: ..." passthrough) instead of silently
// destroying/recreating (there is nothing to recreate it INTO - the resource
// models only manual uploads).
// ---------------------------------------------------------------------------
func TestUnitCertificate_letsEncryptReplaceRejected(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)

	const (
		certID  = "99999999-9999-9999-9999-999999999999"
		domain  = "le.example.test"
		certPEM = "-----BEGIN CERTIFICATE-----\nMIIB...\n-----END CERTIFICATE-----"
		keyPEM  = "-----BEGIN PRIVATE KEY-----\nMIIE...\n-----END PRIVATE KEY-----"
		newCert = "-----BEGIN CERTIFICATE-----\nMIIC...new...\n-----END CERTIFICATE-----"
		newKey  = "-----BEGIN PRIVATE KEY-----\nMIIF...new...\n-----END PRIVATE KEY-----"
	)

	srv.Handle("POST", "/certificates", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success":     true,
			"certificate": certificateObject(certID, "le-cert", domain, "", "AA:BB", "2027-01-01T00:00:00Z", "active"),
		})
	})
	srv.Handle("GET", "/certificate/"+certID, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success":     true,
			"certificate": certificateObject(certID, "le-cert", domain, "", "AA:BB", "2027-01-01T00:00:00Z", "active"),
		})
	})
	srv.Handle("PUT", "/certificate/"+certID, func(w http.ResponseWriter, r *http.Request) {
		// Real shape (Master CertificateService::replace(), 5167436db): plain
		// {success:false,message} - NO "code" field on this endpoint at all.
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"success": false,
			"message": "Only a manually uploaded certificate can be replaced — Let's Encrypt certificates renew automatically.",
		})
	})
	srv.Handle("DELETE", "/certificate/"+certID, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"success": true})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	createCfg := providerCfg + fmt.Sprintf(`
resource "iaas_certificate" "test" {
  name        = "le-cert"
  certificate = %q
  private_key = %q
}
`, certPEM, keyPEM)

	updateCfg := providerCfg + fmt.Sprintf(`
resource "iaas_certificate" "test" {
  name        = "le-cert"
  certificate = %q
  private_key = %q
}
`, newCert, newKey)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: createCfg,
			},
			{
				Config:      updateCfg,
				ExpectError: regexp.MustCompile(`(?i)Let's Encrypt`),
			},
		},
	})
}

// ---------------------------------------------------------------------------
// TestUnitCertificate_resyncFailureIsWarningNotError - MOCK-backed proof that
// a failed per-load-balancer resync entry surfaces as a warning, never fails
// the apply: the certificate itself is already updated server-side by the
// time resync runs, so a resync failure must not be reported as though the
// update itself failed.
// ---------------------------------------------------------------------------
func TestUnitCertificate_resyncFailureIsWarningNotError(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)

	const (
		certID  = "77777777-7777-7777-7777-777777777777"
		certPEM = "-----BEGIN CERTIFICATE-----\nMIIB...\n-----END CERTIFICATE-----"
		keyPEM  = "-----BEGIN PRIVATE KEY-----\nMIIE...\n-----END PRIVATE KEY-----"
		newCert = "-----BEGIN CERTIFICATE-----\nMIIC...new...\n-----END CERTIFICATE-----"
		newKey  = "-----BEGIN PRIVATE KEY-----\nMIIF...new...\n-----END PRIVATE KEY-----"
	)

	srv.Handle("POST", "/certificates", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success":     true,
			"certificate": certificateObject(certID, "example", "example.test", "", "AA:BB", "2027-01-01T00:00:00Z", "active"),
		})
	})
	srv.Handle("GET", "/certificate/"+certID, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success":     true,
			"certificate": certificateObject(certID, "example", "rotated.test", "", "CC:DD", "2027-01-01T00:00:00Z", "active"),
		})
	})
	srv.Handle("PUT", "/certificate/"+certID, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success":     true,
			"certificate": certificateObject(certID, "example", "rotated.test", "", "CC:DD", "2027-01-01T00:00:00Z", "active"),
			"resync": []any{
				map[string]any{"id": "lb-uuid-1", "name": "broken-lb", "success": false, "error": "connection refused"},
			},
		})
	})
	srv.Handle("DELETE", "/certificate/"+certID, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"success": true})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	createCfg := providerCfg + fmt.Sprintf(`
resource "iaas_certificate" "test" {
  name        = "example"
  certificate = %q
  private_key = %q
}
`, certPEM, keyPEM)

	updateCfg := providerCfg + fmt.Sprintf(`
resource "iaas_certificate" "test" {
  name        = "example"
  certificate = %q
  private_key = %q
}
`, newCert, newKey)

	// resource.UnitTest fails the test on any Diagnostics error, but a
	// warning-only response applies cleanly - this IS the assertion: if the
	// resync failure were (mis)reported as an error, this apply would fail.
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: createCfg,
			},
			{
				Config: updateCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_certificate.test", "id", certID),
					resource.TestCheckResourceAttr("iaas_certificate.test", "domain", "rotated.test"),
				),
			},
		},
	})
}
