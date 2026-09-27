// Package validators holds custom terraform-plugin-framework schema.Attribute
// validators shared across resources in this provider. They exist to catch
// the same shapes the API's own server-side request validation rejects at
// plan time rather than at apply, matching the request-field contract the
// user_api enforces (see the Master repo's api.md tri-sync rule).
package validators

import (
	"context"
	"fmt"
	"net"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

// IPv4CIDR returns a validator.String that requires the configured value to
// be a strict IPv4 CIDR block (a.b.c.d/p, octets 0-255, prefix 0-32). It
// rejects IPv6 CIDRs, a bare address with no prefix, an out-of-range prefix
// or octet, and any malformed/injected string (net.ParseCIDR requires an
// exact "ip/prefix" format, so trailing shell metacharacters, embedded
// whitespace or a newline all fail to parse rather than silently truncating).
func IPv4CIDR() validator.String {
	return ipv4CIDRValidator{}
}

type ipv4CIDRValidator struct{}

func (v ipv4CIDRValidator) Description(_ context.Context) string {
	return "value must be a valid IPv4 CIDR block (e.g. 10.0.0.0/24)"
}

func (v ipv4CIDRValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v ipv4CIDRValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}

	value := req.ConfigValue.ValueString()

	ip, ipNet, err := net.ParseCIDR(value)
	if err != nil || ipNet == nil {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Invalid IPv4 CIDR Block",
			fmt.Sprintf("%s must be a valid IPv4 CIDR block in the form a.b.c.d/p (prefix 0-32), got: %q", req.Path, value),
		)

		return
	}

	if ip.To4() == nil || ipNet.IP.To4() == nil {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Invalid IPv4 CIDR Block",
			fmt.Sprintf("%s must be an IPv4 CIDR block, got an IPv6 value: %q", req.Path, value),
		)
	}
}
