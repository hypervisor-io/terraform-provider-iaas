package datasources_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The external test package (datasources_test) avoids an import cycle: importing
// internal/acctest (→ internal/provider → internal/datasources) from the same
// package as the code under test would be cyclic; an external test package is
// permitted to depend on the package it tests.
//
// ensureTFBinary / isOpenTofu / writeJSON mirror the helpers in
// internal/resources/ssh_key_test.go; they are duplicated here because that file
// is in the resources_test package and cannot be imported.

// ensureTFBinary resolves an OpenTofu/Terraform binary for resource.UnitTest:
//   - honours TF_ACC_TERRAFORM_PATH when already set;
//   - otherwise looks for tofu/terraform on PATH and points
//     TF_ACC_TERRAFORM_PATH at it;
//   - when the binary is OpenTofu, sets the namespace/host env vars the reattach
//     (dev-override) mechanism needs;
//   - skips with a clear message when no binary is found, so CI stays green.
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
		t.Skip("no terraform/opentofu binary found (set TF_ACC_TERRAFORM_PATH or put tofu/terraform on PATH); skipping mock-backed data-source test")
	}

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

// writeJSON encodes v as a JSON response with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		panic(fmt.Sprintf("writeJSON: %v", err))
	}
}
