package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/provider"
)

// ---------------------------------------------------------------------------
// resolveClient tests - pure unit tests, no network, no Terraform plumbing.
// ---------------------------------------------------------------------------

// TestResolveClient_TokenFromEnv verifies that a missing config token is
// satisfied by the IAAS_API_TOKEN environment variable.
func TestResolveClient_TokenFromEnv(t *testing.T) {
	t.Setenv("IAAS_API_TOKEN", "tok-xyz")

	c, diags := resolveClient("https://panel.example/api", "", 0, false)
	if diags.HasError() {
		t.Fatalf("expected no error diagnostics; got: %v", diags)
	}
	if c == nil {
		t.Fatal("expected non-nil *client.Client; got nil")
	}
}

// TestResolveClient_MissingToken verifies that missing token (no config, no env)
// produces an error diagnostic mentioning "IAAS_API_TOKEN".
func TestResolveClient_MissingToken(t *testing.T) {
	// Ensure the env var is absent.
	t.Setenv("IAAS_API_TOKEN", "")

	c, diags := resolveClient("https://panel.example/api", "", 0, false)
	if !diags.HasError() {
		t.Fatal("expected error diagnostics; got none")
	}
	// The error detail must mention the env var name so the user knows how to fix it.
	found := false
	for _, d := range diags {
		if d.Detail() != "" {
			// Check the detail string contains the env var name.
			if contains(d.Detail(), "IAAS_API_TOKEN") {
				found = true
				break
			}
		}
	}
	if !found {
		t.Errorf("expected diagnostic detail to mention IAAS_API_TOKEN; diagnostics: %v", diags)
	}
	_ = c // may be nil; we don't assert on it
}

// TestResolveClient_MissingEndpoint verifies that missing endpoint (no config,
// IAAS_API_ENDPOINT unset) produces an error diagnostic.
func TestResolveClient_MissingEndpoint(t *testing.T) {
	t.Setenv("IAAS_API_ENDPOINT", "")

	_, diags := resolveClient("", "some-token", 0, false)
	if !diags.HasError() {
		t.Fatal("expected error diagnostics for missing endpoint; got none")
	}
}

// TestResolveClient_EndpointFromEnv verifies that IAAS_API_ENDPOINT is used
// when the config endpoint argument is empty.
func TestResolveClient_EndpointFromEnv(t *testing.T) {
	t.Setenv("IAAS_API_ENDPOINT", "https://p/api")
	t.Setenv("IAAS_API_TOKEN", "") // ensure token comes from arg, not env

	c, diags := resolveClient("", "tok-from-arg", 0, false)
	if diags.HasError() {
		t.Fatalf("expected no error diagnostics; got: %v", diags)
	}
	if c == nil {
		t.Fatal("expected non-nil *client.Client; got nil")
	}
}

// TestResolveClient_DefaultTimeout verifies that a zero timeoutSecs still
// produces a valid client (the client internally defaults to 30 s).
func TestResolveClient_DefaultTimeout(t *testing.T) {
	c, diags := resolveClient("https://panel.example/api", "tok", 0, false)
	if diags.HasError() {
		t.Fatalf("expected no error diagnostics; got: %v", diags)
	}
	if c == nil {
		t.Fatal("expected non-nil *client.Client; got nil")
	}
}

// ---------------------------------------------------------------------------
// Schema sanity test - token attribute must be marked Sensitive.
// ---------------------------------------------------------------------------

func TestSchema_TokenIsSensitive(t *testing.T) {
	p := &IaasProvider{}
	var resp provider.SchemaResponse
	p.Schema(context.Background(), provider.SchemaRequest{}, &resp)

	tokenAttr, ok := resp.Schema.Attributes["token"]
	if !ok {
		t.Fatal("schema missing 'token' attribute")
	}
	// The framework schema attribute exposes IsSensitive() via the
	// schema.Attribute interface embedded in the concrete type.
	// We use a type assertion to the concrete *schema.StringAttribute
	// which has a Sensitive bool field.
	type sensitiver interface {
		IsSensitive() bool
	}
	if s, ok := tokenAttr.(sensitiver); ok {
		if !s.IsSensitive() {
			t.Error("expected token attribute to be Sensitive; got false")
		}
	} else {
		// Fallback: cast to StringAttribute directly via the framework types.
		// If the assertion fails the test fails informatively.
		t.Errorf("token attribute does not implement IsSensitive(); type=%T", tokenAttr)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
