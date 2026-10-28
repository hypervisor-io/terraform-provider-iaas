package validators

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// IPsecPSK mirrors the server's App\Rules\IpsecPsk: 16 to 128 printable ASCII
// characters (0x21 to 0x7E, so no space and no control character) without a
// double quote or a backslash. The diagnostic NEVER echoes the value or any part
// of it: the string is the connection's pre-shared key, and a plan error is
// printed to terminals and CI logs.
func IPsecPSK() validator.String {
	return ipsecPSKValidator{}
}

type ipsecPSKValidator struct{}

func (ipsecPSKValidator) Description(_ context.Context) string {
	return "value must be 16 to 128 printable ASCII characters without spaces, double quotes or backslashes"
}

func (v ipsecPSKValidator) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

// IsValidIPsecPSK is the pure form of the check, shared with the MCP server.
func IsValidIPsecPSK(s string) bool {
	if len(s) < 16 || len(s) > 128 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x21 || c > 0x7E || c == '"' || c == '\\' {
			return false
		}
	}
	return true
}

func (v ipsecPSKValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if !IsValidIPsecPSK(req.ConfigValue.ValueString()) {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Invalid Pre-Shared Key",
			fmt.Sprintf("%s must be 16 to 128 printable ASCII characters without spaces, double quotes or backslashes "+
				"(the value is not shown here because it is a secret).", req.Path),
		)
	}
}

// IPv4CIDRPrefixBetween returns a validator.String that requires a strict IPv4
// CIDR (see IPv4CIDR) whose prefix length lies in [min, max]. The IPsec remote
// networks use 8 to 32, which also rejects 0.0.0.0/0.
func IPv4CIDRPrefixBetween(min, max int) validator.String {
	return ipv4CIDRPrefixValidator{min: min, max: max}
}

type ipv4CIDRPrefixValidator struct{ min, max int }

func (v ipv4CIDRPrefixValidator) Description(_ context.Context) string {
	return fmt.Sprintf("value must be an IPv4 CIDR block with a prefix length from /%d to /%d", v.min, v.max)
}

func (v ipv4CIDRPrefixValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v ipv4CIDRPrefixValidator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	value := req.ConfigValue.ValueString()
	if !isValidIPv4CIDR(value) {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid IPv4 CIDR Block",
			fmt.Sprintf("%s must be a valid IPv4 CIDR block in the form a.b.c.d/p, got: %q", req.Path, value))
		return
	}
	prefix, _ := strconv.Atoi(value[strings.IndexByte(value, '/')+1:])
	if prefix < v.min || prefix > v.max {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid IPv4 CIDR Prefix",
			fmt.Sprintf("%s must have a prefix length from /%d to /%d, got: %q", req.Path, v.min, v.max, value))
	}
}

// ipsecReserved are the ranges a remote selector may never touch (mirrors
// App\Rules\IpsecTrafficSelectors::RESERVED): 0/8, loopback, link-local,
// multicast and class E (which includes the broadcast address).
var ipsecReserved = []string{"0.0.0.0/8", "127.0.0.0/8", "169.254.0.0/16", "224.0.0.0/4", "240.0.0.0/4"}

// ipsecCIDR parses an already syntactically valid IPv4 CIDR into its first
// address, last address and prefix, and reports whether host bits are zero.
func ipsecCIDR(s string) (first, last uint32, prefix int, canonical, ok bool) {
	if !isValidIPv4CIDR(s) {
		return 0, 0, 0, false, false
	}
	slash := strings.IndexByte(s, '/')
	prefix, _ = strconv.Atoi(s[slash+1:])
	var ip uint32
	for _, o := range strings.Split(s[:slash], ".") {
		n, _ := strconv.Atoi(o)
		ip = ip<<8 | uint32(n)
	}
	var mask uint32
	if prefix > 0 {
		mask = ^uint32(0) << (32 - prefix)
	}
	return ip & mask, ip | ^mask, prefix, ip&mask == ip, true
}

func ipsecOverlap(a, b string) bool {
	af, al, _, _, aok := ipsecCIDR(a)
	bf, bl, _, _, bok := ipsecCIDR(b)
	return aok && bok && af <= bl && bf <= al
}

// IPsecSelectorProblem mirrors App\Rules\IpsecTrafficSelectors for a list of
// selectors that are already valid CIDRs: canonical form (host bits zero), no
// overlap inside the list and, for the remote list, no reserved range. It
// returns "" when the list is acceptable. Elements that are not valid CIDRs are
// left to the element validators.
func IPsecSelectorProblem(list []string, remote bool) string {
	for _, c := range list {
		if _, _, _, canonical, ok := ipsecCIDR(c); ok && !canonical {
			return fmt.Sprintf("%q is not in canonical form (host bits must be zero)", c)
		}
	}
	for i := range list {
		for j := i + 1; j < len(list); j++ {
			if ipsecOverlap(list[i], list[j]) {
				return fmt.Sprintf("%q and %q overlap", list[i], list[j])
			}
		}
	}
	if remote {
		for _, c := range list {
			for _, r := range ipsecReserved {
				if ipsecOverlap(c, r) {
					return fmt.Sprintf("%q touches the reserved range %s", c, r)
				}
			}
		}
	}
	return ""
}

// IPsecSelectorSet validates a whole selector set with IPsecSelectorProblem. It
// is IPsec specific on purpose: the generic IPv4CIDR validator is unchanged.
func IPsecSelectorSet(remote bool) validator.Set {
	return ipsecSelectorSetValidator{remote: remote}
}

type ipsecSelectorSetValidator struct{ remote bool }

func (v ipsecSelectorSetValidator) Description(_ context.Context) string {
	return "networks must be canonical IPv4 CIDRs that do not overlap each other" +
		map[bool]string{true: " or a reserved range", false: ""}[v.remote]
}

func (v ipsecSelectorSetValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v ipsecSelectorSetValidator) ValidateSet(ctx context.Context, req validator.SetRequest, resp *validator.SetResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	var list []string
	for _, e := range req.ConfigValue.Elements() {
		s, ok := e.(types.String)
		if !ok || s.IsUnknown() || s.IsNull() {
			return
		}
		list = append(list, s.ValueString())
	}
	if p := IPsecSelectorProblem(list, v.remote); p != "" {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid IPsec Networks", fmt.Sprintf("%s: %s.", req.Path, p))
	}
}

// IsPlainOptionalString reports whether s survives the platform's request
// normalisation unchanged: not empty and without leading or trailing
// whitespace (TrimStrings and ConvertEmptyStringsToNull would otherwise turn
// it into a different value or null).
func IsPlainOptionalString(s string) bool {
	return s != "" && strings.TrimSpace(s) == s
}

// PlainOptionalString rejects an empty or edge-whitespace value: the platform
// stores "" as null and trims edge whitespace, so the applied result would not
// match the plan. Unset the attribute (null) to clear it.
func PlainOptionalString() validator.String {
	return plainOptionalStringValidator{}
}

type plainOptionalStringValidator struct{}

func (plainOptionalStringValidator) Description(_ context.Context) string {
	return "value must not be empty or have leading or trailing whitespace (unset the attribute instead)"
}

func (v plainOptionalStringValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (plainOptionalStringValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if !IsPlainOptionalString(req.ConfigValue.ValueString()) {
		resp.Diagnostics.AddAttributeError(req.Path, "Empty Or Padded Value",
			fmt.Sprintf("%s must not be empty or have leading or trailing whitespace: the platform stores an empty string as null and trims padding, so the applied value would differ from the configured one. Remove the attribute (null) to leave it unset or clear it.", req.Path))
	}
}
