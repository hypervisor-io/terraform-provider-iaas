// Package validators holds custom terraform-plugin-framework schema.Attribute
// validators shared across resources in this provider. They exist to catch
// the same shapes the API's own server-side request validation rejects at
// plan time rather than at apply, matching the request-field contract the
// user_api enforces (see the Master repo's api.md tri-sync rule).
package validators

import (
	"context"
	"fmt"
	"regexp"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

// ipv4CIDRPattern is a direct port of the Master repo's server-side rule
// (app/Rules/Ipv4Cidr.php): four decimal octets and a decimal prefix,
// separated by "." and "/", ASCII digits only ([0-9], never a Unicode
// digit lookalike). Each captured segment is re-checked below for range
// and leading-zero. This deliberately does NOT use net.ParseCIDR: that
// accepts a leading-zero prefix (net.ParseCIDR("10.0.0.0/08", ...) is
// valid Go, but the server's regex requires no leading zero) and an
// IPv4-mapped IPv6 literal (::ffff:10.0.0.0/120, whose IP.To4() is
// non-nil even though the server's regex never matches it at all).
var ipv4CIDRPattern = regexp.MustCompile(`^([0-9]{1,3})\.([0-9]{1,3})\.([0-9]{1,3})\.([0-9]{1,3})/([0-9]{1,2})$`)

// isValidIPv4CIDRSegment reports whether s is a decimal integer with no
// leading zero (unless s is exactly "0") that does not exceed max -
// mirrors Ipv4Cidr::isValid()'s per-segment check:
// `(string) (int) $octet !== $octet || (int) $octet > 255`.
func isValidIPv4CIDRSegment(s string, max int) bool {
	if len(s) > 1 && s[0] == '0' {
		return false
	}

	n, err := strconv.Atoi(s)
	if err != nil {
		return false
	}

	return n <= max
}

// isValidIPv4CIDR is the pure, framework-free half of the strict IPv4 CIDR
// check - a direct port of App\Rules\Ipv4Cidr::isValid() in the Master
// repo, so the provider accepts EXACTLY what the API's own server-side
// request validation accepts: four octets 0-255 with no leading zero, a
// prefix 0-32 with no leading zero, and nothing else in the string. Host
// bits set are allowed (10.0.0.1/24 is valid).
func isValidIPv4CIDR(value string) bool {
	m := ipv4CIDRPattern.FindStringSubmatch(value)
	if m == nil {
		return false
	}

	for _, octet := range m[1:5] {
		if !isValidIPv4CIDRSegment(octet, 255) {
			return false
		}
	}

	return isValidIPv4CIDRSegment(m[5], 32)
}

// IPv4CIDR returns a validator.String that requires the configured value to
// be a strict IPv4 CIDR block (a.b.c.d/p, octets 0-255 with no leading
// zero, prefix 0-32 with no leading zero). It rejects IPv6 CIDRs and
// IPv4-mapped IPv6 literals, a bare address with no prefix, an
// out-of-range or leading-zero octet/prefix, and any malformed/injected
// string (the anchored regex requires an exact "ip/prefix" format, so
// trailing shell metacharacters, embedded whitespace or a newline all
// fail to match rather than silently truncating).
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

	if !isValidIPv4CIDR(value) {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Invalid IPv4 CIDR Block",
			fmt.Sprintf("%s must be a valid IPv4 CIDR block in the form a.b.c.d/p (octets 0-255, prefix 0-32, no leading zeros), got: %q", req.Path, value),
		)
	}
}
