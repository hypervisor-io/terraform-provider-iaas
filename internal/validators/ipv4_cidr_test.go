package validators_test

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hypervisor-io/terraform-provider-iaas/internal/validators"
)

func TestIPv4CIDR(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		value     string
		wantError bool
	}{
		"class-c":                   {value: "10.0.0.0/24", wantError: false},
		"default-route":             {value: "0.0.0.0/0", wantError: false},
		"single-host":               {value: "10.99.0.0/32", wantError: false},
		"command-injection-suffix":  {value: "10.99.0.0/24 -j MASQUERADE", wantError: true},
		"shell-metacharacter-in-ip": {value: "touch /tmp/x; #", wantError: true},
		"missing-prefix":            {value: "10.0.0.0", wantError: true},
		"octet-out-of-range":        {value: "256.0.0.0/8", wantError: true},
		"prefix-out-of-range":       {value: "10.0.0.0/33", wantError: true},
		"ipv6":                      {value: "2001:db8::/32", wantError: true},
		"leading-space":             {value: " 10.0.0.0/24", wantError: true},
		"trailing-space":            {value: "10.0.0.0/24 ", wantError: true},
		"embedded-newline":          {value: "10.0.0.0/24\n", wantError: true},
		"empty":                     {value: "", wantError: true},
		"negative-prefix":           {value: "10.0.0.0/-1", wantError: true},
		"trailing-dot-no-prefix":    {value: "10.0.0.0.", wantError: true},

		// Shared server-parity table (Master app/Rules/Ipv4Cidr.php and its
		// tests/Unit/Rules/Ipv4CidrTest.php): the client must accept and
		// reject EXACTLY what the server's regex does, never merely a
		// syntactically-valid net.ParseCIDR value.
		"leading-zero-prefix-08":      {value: "10.0.0.0/08", wantError: true},
		"leading-zero-prefix-00":      {value: "0.0.0.0/00", wantError: true},
		"leading-zero-prefix-032":     {value: "10.0.0.0/032", wantError: true},
		"ipv4-mapped-ipv6-prefix-120": {value: "::ffff:10.0.0.0/120", wantError: true},
		"ipv4-mapped-ipv6-prefix-24":  {value: "::ffff:10.0.0.0/24", wantError: true},
		"leading-zero-octet":          {value: "010.0.0.0/24", wantError: true},
		"host-bits-set":               {value: "10.0.0.1/24", wantError: false},
		"full-tunnel":                 {value: "0.0.0.0/0", wantError: false},
		"max-octets-host":             {value: "255.255.255.255/32", wantError: false},
		"wide-slash-eight":            {value: "10.0.0.0/8", wantError: false},
		"prefix-128-ipv6-max":         {value: "10.99.0.0/128", wantError: true},
		"too-few-octets":              {value: "10.99.0/24", wantError: true},
		"too-many-octets":             {value: "10.99.0.0.1/24", wantError: true},
		"octet-over-255":              {value: "10.99.0.256/24", wantError: true},
	}

	for name, tc := range tests {
		tc := tc

		t.Run(name, func(t *testing.T) {
			t.Parallel()

			req := validator.StringRequest{
				Path:        path.Root("cidr"),
				ConfigValue: types.StringValue(tc.value),
			}
			resp := &validator.StringResponse{}

			validators.IPv4CIDR().ValidateString(context.Background(), req, resp)

			gotError := resp.Diagnostics.HasError()
			if gotError != tc.wantError {
				t.Fatalf("IPv4CIDR().ValidateString(%q) diagnostics HasError = %v, want %v (diags: %v)",
					tc.value, gotError, tc.wantError, resp.Diagnostics)
			}
		})
	}
}

func TestIPv4CIDR_NullAndUnknownSkipped(t *testing.T) {
	t.Parallel()

	for name, cfg := range map[string]types.String{
		"null":    types.StringNull(),
		"unknown": types.StringUnknown(),
	} {
		cfg := cfg

		t.Run(name, func(t *testing.T) {
			t.Parallel()

			req := validator.StringRequest{Path: path.Root("cidr"), ConfigValue: cfg}
			resp := &validator.StringResponse{}

			validators.IPv4CIDR().ValidateString(context.Background(), req, resp)

			if resp.Diagnostics.HasError() {
				t.Fatalf("IPv4CIDR().ValidateString(%s) should not error, got: %v", name, resp.Diagnostics)
			}
		})
	}
}
