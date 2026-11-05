package validators

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

// S3BucketName validates a NEW bucket name against the AWS S3 general purpose bucket naming rules, the same rules the
// Master API enforces on bucket create (source: https://docs.aws.amazon.com/AmazonS3/latest/userguide/bucketnamingrules.html).
// Uniqueness across the platform cannot be checked at plan time; the API answers 422 with errors.name for a taken name.
//
// The attribute validator form judges every plan, including resources that already exist; the iaas_s3_bucket resource
// therefore does NOT attach it and instead calls S3BucketNameProblem from ModifyPlan, only when the bucket is created or
// its name changes (a replacement). Names the API accepted before the rules existed stay valid while they do not change.
func S3BucketName() validator.String {
	return s3BucketNameValidator{}
}

var (
	s3NameChars = regexp.MustCompile(`^[a-z0-9.\-]+$`)
	s3NameEdge  = regexp.MustCompile(`^[a-z0-9](.*[a-z0-9])?$`)

	s3ForbiddenPrefixes = []string{"xn--", "sthree-", "amzn-s3-demo-"}
	s3ForbiddenSuffixes = []string{"-s3alias", "--ol-s3", ".mrap", "--x-s3", "--table-s3"}
)

// S3BucketNameProblem is the exported form of the naming check: the first broken rule, or "" when the name is acceptable.
func S3BucketNameProblem(name string) string { return s3BucketNameViolation(name) }

// s3BucketNameViolation returns the first broken rule, or "" when the name is acceptable.
func s3BucketNameViolation(name string) string {
	if len(name) < 3 || len(name) > 63 {
		return "must be between 3 and 63 characters long"
	}
	if !s3NameChars.MatchString(name) {
		return "may only contain lowercase letters, digits, dots (.) and hyphens (-)"
	}
	if !s3NameEdge.MatchString(name) {
		return "must begin and end with a letter or digit"
	}
	if strings.Contains(name, "..") {
		return "must not contain two adjacent dots"
	}
	if ip := net.ParseIP(name); ip != nil && ip.To4() != nil && strings.Count(name, ".") == 3 {
		return "must not be formatted as an IP address (for example 192.168.5.4)"
	}
	for _, p := range s3ForbiddenPrefixes {
		if strings.HasPrefix(name, p) {
			return fmt.Sprintf("must not start with the reserved prefix %s", p)
		}
	}
	for _, s := range s3ForbiddenSuffixes {
		if strings.HasSuffix(name, s) {
			return fmt.Sprintf("must not end with the reserved suffix %s", s)
		}
	}

	return ""
}

type s3BucketNameValidator struct{}

func (v s3BucketNameValidator) Description(_ context.Context) string {
	return "AWS S3 bucket naming rules: 3-63 characters; lowercase letters, digits, dots and hyphens; begin and end with a letter or digit; " +
		"no adjacent dots; not an IP address; no xn--, sthree- or amzn-s3-demo- prefix; no -s3alias, --ol-s3, .mrap, --x-s3 or --table-s3 suffix"
}

func (v s3BucketNameValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v s3BucketNameValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if msg := s3BucketNameViolation(req.ConfigValue.ValueString()); msg != "" {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid S3 bucket name", fmt.Sprintf("%s %s", req.Path, msg))
	}
}
