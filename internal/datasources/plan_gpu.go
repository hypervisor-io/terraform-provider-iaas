package datasources

import (
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// gpuAttrTypes is the object type of the `gpu` block that plan payloads carry
// (user_api plan lists and the Kubernetes plan search): null for a plan with no
// GPU, else {count, vendor, vram_min_gb, mode, profile, label} plus `available`
// when the answer was location-scoped.
var gpuAttrTypes = map[string]attr.Type{
	"count":       types.Int64Type,
	"vendor":      types.StringType,
	"vram_min_gb": types.Int64Type,
	"mode":        types.StringType,
	"profile":     types.StringType,
	"label":       types.StringType,
	"available":   types.BoolType,
}

// gpuSchemaAttribute is the computed `gpu` block shared by the plan data sources.
func gpuSchemaAttribute(scoped bool) schema.Attribute {
	desc := "The GPU the plan comes with, or null for a plan without a GPU. " +
		"`label` is the customer-facing description (for example `1 × NVIDIA RTX A5000 · 24 GB`)."
	if scoped {
		desc += " `available` is true when a server in this location can currently take the plan."
	} else {
		desc += " `available` is always null here: this lookup is not scoped to one location."
	}
	return schema.SingleNestedAttribute{
		Computed:    true,
		Description: desc,
		Attributes: map[string]schema.Attribute{
			"count":       schema.Int64Attribute{Computed: true, Description: "Number of GPUs the plan provides."},
			"vendor":      schema.StringAttribute{Computed: true, Description: "Required GPU vendor, or null for any."},
			"vram_min_gb": schema.Int64Attribute{Computed: true, Description: "Minimum GPU memory per GPU in GB, or null."},
			"mode":        schema.StringAttribute{Computed: true, Description: "GPU mode: passthrough, mig or sriov."},
			"profile":     schema.StringAttribute{Computed: true, Description: "MIG or SR-IOV profile, or null."},
			"label":       schema.StringAttribute{Computed: true, Description: "Customer-facing GPU description."},
			"available":   schema.BoolAttribute{Computed: true, Description: "Whether the location can currently take the plan; null when no location is in scope."},
		},
	}
}

// gpuObject converts the API's `gpu` value (nil, or a JSON object) to the
// state object; a missing or null `gpu` is a null object.
func gpuObject(p map[string]any) types.Object {
	raw, ok := p["gpu"].(map[string]any)
	if !ok || raw == nil {
		return types.ObjectNull(gpuAttrTypes)
	}
	str := func(k string) types.String {
		if v, ok := raw[k]; ok && v != nil {
			return types.StringValue(strField(raw, k))
		}
		return types.StringNull()
	}
	num := func(k string) types.Int64 {
		if v, ok := raw[k]; ok && v != nil {
			return types.Int64Value(int64Field(raw, k))
		}
		return types.Int64Null()
	}
	avail := types.BoolNull()
	if v, ok := raw["available"]; ok && v != nil {
		avail = types.BoolValue(boolField(raw, "available"))
	}
	obj, _ := types.ObjectValue(gpuAttrTypes, map[string]attr.Value{
		"count":       num("count"),
		"vendor":      str("vendor"),
		"vram_min_gb": num("vram_min_gb"),
		"mode":        str("mode"),
		"profile":     str("profile"),
		"label":       str("label"),
		"available":   avail,
	})
	return obj
}
