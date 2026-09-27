package validators_test

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hypervisor-io/terraform-provider-iaas/internal/validators"
)

func TestNoControlCharacters(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		value     string
		wantError bool
	}{
		"plain-name":            {value: "office-vpn", wantError: false},
		"endpoint-host-port":    {value: "203.0.113.1:51820", wantError: false},
		"cidr-allowed-ip":       {value: "10.0.0.0/24", wantError: false},
		"unicode":               {value: "büro-vpn", wantError: false},
		"empty":                 {value: "", wantError: false},
		"null-byte":             {value: "peer\x00name", wantError: true},
		"newline":               {value: "peer\nname", wantError: true},
		"carriage-return":       {value: "peer\rname", wantError: true},
		"tab":                   {value: "peer\tname", wantError: true},
		"escape":                {value: "peer\x1bname", wantError: true},
		"del":                   {value: "peer\x7fname", wantError: true},
		"trailing-semicolon-cr": {value: "10.0.0.0/24;\rrm -rf /", wantError: true},
	}

	for name, tc := range tests {
		tc := tc

		t.Run(name, func(t *testing.T) {
			t.Parallel()

			req := validator.StringRequest{
				Path:        path.Root("name"),
				ConfigValue: types.StringValue(tc.value),
			}
			resp := &validator.StringResponse{}

			validators.NoControlCharacters().ValidateString(context.Background(), req, resp)

			gotError := resp.Diagnostics.HasError()
			if gotError != tc.wantError {
				t.Fatalf("NoControlCharacters().ValidateString(%q) diagnostics HasError = %v, want %v (diags: %v)",
					tc.value, gotError, tc.wantError, resp.Diagnostics)
			}
		})
	}
}

func TestNoControlCharacters_NullAndUnknownSkipped(t *testing.T) {
	t.Parallel()

	for name, cfg := range map[string]types.String{
		"null":    types.StringNull(),
		"unknown": types.StringUnknown(),
	} {
		cfg := cfg

		t.Run(name, func(t *testing.T) {
			t.Parallel()

			req := validator.StringRequest{Path: path.Root("name"), ConfigValue: cfg}
			resp := &validator.StringResponse{}

			validators.NoControlCharacters().ValidateString(context.Background(), req, resp)

			if resp.Diagnostics.HasError() {
				t.Fatalf("NoControlCharacters().ValidateString(%s) should not error, got: %v", name, resp.Diagnostics)
			}
		})
	}
}
