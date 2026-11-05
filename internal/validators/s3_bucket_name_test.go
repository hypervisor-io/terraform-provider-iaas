package validators_test

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hypervisor-io/terraform-provider-iaas/internal/validators"
)

func TestS3BucketName(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		value     string
		wantError bool
	}{
		"plain":                 {"acme-assets", false},
		"min length":            {"abc", false},
		"max length":            {strings.Repeat("a", 63), false},
		"dots":                  {"my.bucket-1", false},
		"digit edges":           {"1abc1", false},
		"xn- is not xn--":       {"xn-ok", false},
		"sthree without hyphen": {"sthree", false},
		"s3alias inside":        {"my-s3alias-x", false},
		"double hyphen inside":  {"a--b", false},
		"too short":             {"ab", true},
		"too long":              {strings.Repeat("a", 64), true},
		"uppercase":             {"Abc-def", true},
		"underscore":            {"abc_def", true},
		"leading dot":           {".abc", true},
		"trailing dot":          {"abc.", true},
		"leading hyphen":        {"-abc", true},
		"trailing hyphen":       {"abc-", true},
		"adjacent dots":         {"a..b", true},
		"ip address":            {"192.168.5.4", true},
		"prefix xn--":           {"xn--abc", true},
		"prefix sthree-":        {"sthree-abc", true},
		"prefix amzn-s3-demo-":  {"amzn-s3-demo-abc", true},
		"suffix -s3alias":       {"abc-s3alias", true},
		"suffix --ol-s3":        {"abc--ol-s3", true},
		"suffix .mrap":          {"abc.mrap", true},
		"suffix --x-s3":         {"abc--x-s3", true},
		"suffix --table-s3":     {"abc--table-s3", true},
		"non ascii":             {"abcé", true},
		"slash":                 {"a/b", true},
		"empty":                 {"", true},
	}

	for name, tc := range tests {
		tc := tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			req := validator.StringRequest{Path: path.Root("name"), ConfigValue: types.StringValue(tc.value)}
			resp := &validator.StringResponse{}
			validators.S3BucketName().ValidateString(context.Background(), req, resp)
			if got := resp.Diagnostics.HasError(); got != tc.wantError {
				t.Fatalf("value %q: error=%v, want %v (%v)", tc.value, got, tc.wantError, resp.Diagnostics)
			}
		})
	}

	// null and unknown (a reference to another resource) are never judged at plan time.
	for _, v := range []types.String{types.StringNull(), types.StringUnknown()} {
		resp := &validator.StringResponse{}
		validators.S3BucketName().ValidateString(context.Background(), validator.StringRequest{Path: path.Root("name"), ConfigValue: v}, resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("%v must not be validated", v)
		}
	}
}
