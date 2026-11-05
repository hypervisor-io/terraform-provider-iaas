package resources

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// s3Obj builds an iaas_s3_bucket object value with only name set (every other attribute null).
func s3Obj(t *testing.T, r *s3BucketResource, name *string) tftypes.Value {
	t.Helper()
	var sresp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &sresp)
	typ := sresp.Schema.Type().TerraformType(context.Background()).(tftypes.Object)
	vals := map[string]tftypes.Value{}
	for k, at := range typ.AttributeTypes {
		vals[k] = tftypes.NewValue(at, nil)
	}
	if name != nil {
		vals["name"] = tftypes.NewValue(tftypes.String, *name)
	}
	return tftypes.NewValue(typ, vals)
}

func planFor(t *testing.T, cfgName string, stateName *string) resource.ModifyPlanResponse {
	t.Helper()
	r := &s3BucketResource{}
	var sresp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &sresp)
	cfg := s3Obj(t, r, &cfgName)
	req := resource.ModifyPlanRequest{
		Config: tfsdk.Config{Schema: sresp.Schema, Raw: cfg},
		Plan:   tfsdk.Plan{Schema: sresp.Schema, Raw: cfg},
		State:  tfsdk.State{Schema: sresp.Schema, Raw: tftypes.NewValue(cfg.Type(), nil)},
	}
	if stateName != nil {
		req.State.Raw = s3Obj(t, r, stateName)
	}
	resp := resource.ModifyPlanResponse{}
	r.ModifyPlan(context.Background(), req, &resp)
	return resp
}

func TestS3BucketModifyPlan_namingRulesOnlyOnCreateOrChange(t *testing.T) {
	legacy := "sthree-assets"
	good := "good-name-1"

	if planFor(t, "sthree-assets", nil).Diagnostics.HasError() == false {
		t.Fatal("create with a bad name must fail at plan")
	}
	if planFor(t, good, nil).Diagnostics.HasError() {
		t.Fatal("create with a good name must plan clean")
	}
	if planFor(t, legacy, &legacy).Diagnostics.HasError() {
		t.Fatal("an existing legacy name with an unchanged config must plan clean")
	}
	if planFor(t, "xn--new-bad", &legacy).Diagnostics.HasError() == false {
		t.Fatal("changing the name to a bad one must fail")
	}
	if planFor(t, good, &legacy).Diagnostics.HasError() {
		t.Fatal("changing a legacy name to a good one must plan clean (replacement)")
	}
}
