package resources_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/iaas/terraform-provider-iaas/internal/acctest"
)

// ensureTFBinary makes resource.UnitTest runnable across CI and dev machines.
//
// resource.UnitTest needs a terraform/opentofu binary. The plugin-testing
// library only auto-discovers a binary at TF_ACC_TERRAFORM_PATH or a
// "terraform" executable on PATH - it does NOT look for "tofu". This helper:
//
//   - leaves an explicit TF_ACC_TERRAFORM_PATH untouched;
//   - otherwise falls back to a "tofu" (then "terraform") binary on PATH and
//     points TF_ACC_TERRAFORM_PATH at it;
//   - when the resolved binary is OpenTofu, sets the provider namespace/host
//     env vars the reattach (dev-override) mechanism needs so the apply phase
//     can instantiate the in-process provider;
//   - skips the test with a clear message when no binary can be found, so CI
//     (which has no TF binary) stays green instead of hard-failing.
func ensureTFBinary(t *testing.T) {
	t.Helper()

	bin := os.Getenv("TF_ACC_TERRAFORM_PATH")
	if bin == "" {
		for _, name := range []string{"tofu", "terraform"} {
			if p, err := exec.LookPath(name); err == nil {
				bin = p
				t.Setenv("TF_ACC_TERRAFORM_PATH", p)
				break
			}
		}
	}
	if bin == "" {
		t.Skip("no terraform/opentofu binary found (set TF_ACC_TERRAFORM_PATH or put tofu/terraform on PATH); skipping mock-backed lifecycle test")
	}

	// OpenTofu rejects the testing library's default "-" provider namespace and
	// resolves providers against registry.opentofu.org; align the reattach
	// mapping so the apply phase finds the in-process provider.
	if isOpenTofu(t, bin) {
		if os.Getenv("TF_ACC_PROVIDER_NAMESPACE") == "" {
			t.Setenv("TF_ACC_PROVIDER_NAMESPACE", "hashicorp")
		}
		if os.Getenv("TF_ACC_PROVIDER_HOST") == "" {
			t.Setenv("TF_ACC_PROVIDER_HOST", "registry.opentofu.org")
		}
	}
}

// isOpenTofu reports whether the binary at path is OpenTofu (vs Terraform).
func isOpenTofu(t *testing.T, path string) bool {
	t.Helper()
	out, err := exec.Command(path, "version").Output()
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(out)), "opentofu")
}

// The external test package (resources_test) avoids an import cycle: importing
// internal/acctest (→ internal/provider → internal/resources) from the same
// package as the resource under test would be cyclic; an external test package
// is permitted to depend on the package it tests.

// ---------------------------------------------------------------------------
// TestAccSSHKey_basic - LIVE acceptance test (manual staging gate).
//
// Auto-skips unless TF_ACC is set (resource.Test enforces this), so it never
// runs or blocks CI. Requires a reachable panel + IP-locked token via
// IAAS_API_ENDPOINT / IAAS_API_TOKEN (checked by acctest.PreCheck).
// ---------------------------------------------------------------------------
func TestAccSSHKey_basic(t *testing.T) {
	const config = `
resource "iaas_ssh_key" "test" {
  name       = "tf acc test key"
  public_key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILcl0K3kLJv6N4F2bWj2vJxQ1q3qY8m0Q3a2bC4d5E6F tf-acc@example.com"
}
`
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("iaas_ssh_key.test", "id"),
					resource.TestCheckResourceAttrSet("iaas_ssh_key.test", "fingerprint"),
					resource.TestCheckResourceAttr("iaas_ssh_key.test", "name", "tf acc test key"),
				),
			},
			{
				ResourceName:      "iaas_ssh_key.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// ---------------------------------------------------------------------------
// TestUnitSSHKey_lifecycle - MOCK-backed lifecycle proof.
//
// Drives the full resource lifecycle against canned API responses, with no
// live panel. The Steps execute in this order:
//
//  1. Create + read-back - applies createCfg; checks id, fingerprint, comments, name.
//  2. Import - imports the resource by UUID and verifies state matches the prior step.
//  3. Update - applies updateCfg (renamed name); checks id and new name.
//
// Delete is implicit teardown after the final step, not an explicit Step.
//
// resource.UnitTest needs a terraform/opentofu binary on PATH or via
// TF_ACC_TERRAFORM_PATH; if none is found the test is skipped with a clear
// binary-not-found message (see ensureTFBinary).
// ---------------------------------------------------------------------------
func TestUnitSSHKey_lifecycle(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)

	const (
		keyID   = "11111111-1111-1111-1111-111111111111"
		fpr     = "SHA256:abc123"
		comment = "user@host"
		pubKey  = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILcl0K3kLJv6N4F2bWj2vJxQ1q3qY8m0Q3a2bC4d5E6F user@host"
	)

	// currentName tracks the server-side name so READ reflects the latest value
	// set by create/update - exercising real drift-free read-back.
	currentName := "lifecycle key"

	// CREATE - POST /ssh-keys returns 200 + {success,ssh_key}.
	srv.Handle("POST", "/ssh-keys", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if n, ok := body["name"].(string); ok {
			currentName = n
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "SSH key created",
			"ssh_key": sshKeyObject(keyID, currentName, pubKey, fpr, comment),
		})
	})

	// UPDATE - PATCH /ssh-key/{id} (singular) applies the new name.
	srv.Handle("PATCH", "/ssh-key/"+keyID, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if n, ok := body["name"].(string); ok {
			currentName = n
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "SSH key updated",
			"ssh_key": sshKeyObject(keyID, currentName, pubKey, fpr, comment),
		})
	})

	// READ - GET /ssh-key/{id} (singular) reflects the latest name.
	srv.Handle("GET", "/ssh-key/"+keyID, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"ssh_key": sshKeyObject(keyID, currentName, pubKey, fpr, comment),
		})
	})

	// DELETE - DELETE /ssh-keys/{id} succeeds at 200.
	srv.Handle("DELETE", "/ssh-keys/"+keyID, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "SSH key deleted",
		})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	createCfg := providerCfg + `
resource "iaas_ssh_key" "test" {
  name       = "lifecycle key"
  public_key = "` + pubKey + `"
}
`
	updateCfg := providerCfg + `
resource "iaas_ssh_key" "test" {
  name       = "renamed key"
  public_key = "` + pubKey + `"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			// Create + read-back.
			{
				Config: createCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_ssh_key.test", "id", keyID),
					resource.TestCheckResourceAttr("iaas_ssh_key.test", "fingerprint", fpr),
					resource.TestCheckResourceAttr("iaas_ssh_key.test", "comments", comment),
					resource.TestCheckResourceAttr("iaas_ssh_key.test", "name", "lifecycle key"),
				),
			},
			// Import the existing resource and verify state matches.
			{
				ResourceName:      "iaas_ssh_key.test",
				ImportState:       true,
				ImportStateId:     keyID,
				ImportStateVerify: true,
			},
			// Update the name.
			{
				Config: updateCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_ssh_key.test", "id", keyID),
					resource.TestCheckResourceAttr("iaas_ssh_key.test", "name", "renamed key"),
				),
			},
		},
	})

	// Assert the create request sent name + public_key but NOT comments.
	creates := srv.Requests("POST", "/ssh-keys")
	if len(creates) == 0 {
		t.Fatal("expected at least one POST /ssh-keys")
	}
	var createBody map[string]any
	if err := json.Unmarshal(creates[0].Body, &createBody); err != nil {
		t.Fatalf("decoding create body: %v", err)
	}
	if createBody["name"] != "lifecycle key" {
		t.Errorf("create body name = %v; want %q", createBody["name"], "lifecycle key")
	}
	if createBody["public_key"] != pubKey {
		t.Errorf("create body public_key = %v; want the key material", createBody["public_key"])
	}
	if _, present := createBody["comments"]; present {
		t.Errorf("create body must NOT include comments; got %v", createBody)
	}
}

// sshKeyObject builds a serialized ssh_key object matching the API shape,
// including the nested user object the resource is expected to ignore.
func sshKeyObject(id, name, pubKey, fingerprint, comment string) map[string]any {
	return map[string]any{
		"id":          id,
		"name":        name,
		"public_key":  pubKey,
		"fingerprint": fingerprint,
		"comments":    comment,
		"user": map[string]any{
			"id":   "owner-uuid",
			"name": "Owner",
		},
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		panic(fmt.Sprintf("writeJSON: %v", err))
	}
}
