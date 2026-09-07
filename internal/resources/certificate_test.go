package resources_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

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
// live panel. There is NO update step: the certificate API has no update
// route, so every configurable attribute is RequiresReplace. Delete is
// implicit teardown after the final step, not an explicit Step.
//
// resource.UnitTest needs a terraform/opentofu binary on PATH or via
// TF_ACC_TERRAFORM_PATH; if none is found the test is skipped with a clear
// binary-not-found message (see ensureTFBinary in ssh_key_test.go).
// ---------------------------------------------------------------------------
func TestUnitCertificate_lifecycle(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)

	const (
		certID   = "88888888-8888-8888-8888-888888888888"
		domain   = "example.test"
		sanA     = "www.example.test"
		fpSha256 = "AA:BB:CC:DD:EE:FF"
		certPEM  = "-----BEGIN CERTIFICATE-----\nMIIB...\n-----END CERTIFICATE-----"
		keyPEM   = "-----BEGIN PRIVATE KEY-----\nMIIE...\n-----END PRIVATE KEY-----"
		expires  = "2027-01-01T00:00:00Z"
	)

	// CREATE - POST /certificates returns 200 + {success,message,certificate}.
	// certificate/private_key/chain are NEVER echoed back.
	srv.Handle("POST", "/certificates", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success":     true,
			"message":     "Certificate uploaded successfully.",
			"certificate": certificateObject(certID, domain, sanA, fpSha256, expires, "active"),
		})
	})

	// READ - GET /certificate/{id} scans the single-object SHOW route.
	srv.Handle("GET", "/certificate/"+certID, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success":     true,
			"certificate": certificateObject(certID, domain, sanA, fpSha256, expires, "active"),
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
			// Import the existing resource and verify state matches (write-only
			// PEM fields cannot be recovered from the API).
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
}

// certificateObject builds a serialised certificate object matching the API
// CREATE/SHOW shape (never includes certificate/private_key/chain).
func certificateObject(id, domain, sanDomain, fingerprint, expiresAt, status string) map[string]any {
	return map[string]any{
		"id":                 id,
		"name":               "example",
		"domain":             domain,
		"san_domains":        []any{sanDomain},
		"type":               "manual",
		"status":             status,
		"expires_at":         expiresAt,
		"fingerprint_sha256": fingerprint,
		"usage_count":        0,
	}
}
