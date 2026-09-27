package validators

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

// NoControlCharacters returns a validator.String that rejects any C0 control
// character (0x00-0x1F) or DEL (0x7F) in the configured value. This mirrors
// the user_api's server-side "invalid_control_characters" rule for VPN peer
// fields (name, endpoint, preshared_key, public_key, dns, allowed_ips
// elements): the goal is to fail the same bad input at plan time instead of
// letting it reach the API and, downstream, a root-privileged command line
// on the gateway VM.
func NoControlCharacters() validator.String {
	return noControlCharactersValidator{}
}

type noControlCharactersValidator struct{}

func (v noControlCharactersValidator) Description(_ context.Context) string {
	return "value must not contain control characters (0x00-0x1F or 0x7F)"
}

func (v noControlCharactersValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v noControlCharactersValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}

	value := req.ConfigValue.ValueString()

	for i, r := range value {
		if (r >= 0x00 && r <= 0x1F) || r == 0x7F {
			resp.Diagnostics.AddAttributeError(
				req.Path,
				"Invalid Control Character",
				fmt.Sprintf("%s must not contain control characters (0x00-0x1F or 0x7F); found byte 0x%02X at offset %d", req.Path, r, i),
			)

			return
		}
	}
}
