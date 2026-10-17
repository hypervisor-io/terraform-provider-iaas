package datasources

import (
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Kit launcher metadata (category, icon, tagline, defaults, suggested secrets,
// sort order) as returned by the MicroVM catalog and image endpoints. Every
// field is null when the Master omits it (older Master).

var microvmSuggestedSecretAttrTypes = map[string]attr.Type{
	"name":        types.StringType,
	"label":       types.StringType,
	"help_url":    types.StringType,
	"provider":    types.StringType,
	"placeholder": types.StringType,
	"icon":        types.StringType,
	"role":        types.StringType,
}

var microvmKitAttrTypes = map[string]attr.Type{
	"category":                 types.StringType,
	"icon":                     types.StringType,
	"tagline":                  types.StringType,
	"default_plan_vcpu":        types.Int64Type,
	"default_lifetime_seconds": types.Int64Type,
	"suggested_secrets":        types.ListType{ElemType: types.ObjectType{AttrTypes: microvmSuggestedSecretAttrTypes}},
	"sort_order":               types.Int64Type,
}

// microvmKitSchemaAttributes returns the seven Computed kit attributes.
func microvmKitSchemaAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"category":                 schema.StringAttribute{Computed: true, Description: "Launcher category of the kit."},
		"icon":                     schema.StringAttribute{Computed: true, Description: "Launcher icon key."},
		"tagline":                  schema.StringAttribute{Computed: true, Description: "Short launcher tagline."},
		"default_plan_vcpu":        schema.Int64Attribute{Computed: true, Description: "Suggested vCPU count for the default plan."},
		"default_lifetime_seconds": schema.Int64Attribute{Computed: true, Description: "Suggested sandbox lifetime in seconds."},
		"suggested_secrets": schema.ListNestedAttribute{
			Computed:    true,
			Description: "Secrets the kit suggests setting at launch.",
			NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
				"name":        schema.StringAttribute{Computed: true},
				"label":       schema.StringAttribute{Computed: true},
				"help_url":    schema.StringAttribute{Computed: true},
				"provider":    schema.StringAttribute{Computed: true, Description: "Provider key the secret belongs to (for example anthropic). Null when the kit does not set it."},
				"placeholder": schema.StringAttribute{Computed: true, Description: "Placeholder text of the launcher field. Null when the kit does not set it."},
				"icon":        schema.StringAttribute{Computed: true, Description: "Icon key of the row. Null when the kit does not set it."},
				"role":        schema.StringAttribute{Computed: true, Description: "provider (feeds the secrets token) or repo (the repo access line). Null on a kit published before roles existed."},
			}},
		},
		"sort_order": schema.Int64Attribute{Computed: true, Description: "Launcher sort order."},
	}
}

func nullableInt64(raw any) types.Int64 {
	if raw == nil {
		return types.Int64Null()
	}
	switch raw.(type) {
	case float64, int, int64, string:
		return types.Int64Value(int64(number(raw)))
	}
	return types.Int64Null()
}

// microvmKitValues maps the seven kit fields of an API object. A missing key
// is null; suggested_secrets entries may be objects or plain secret names.
func microvmKitValues(obj map[string]any, diags *diag.Diagnostics) map[string]attr.Value {
	secretType := types.ObjectType{AttrTypes: microvmSuggestedSecretAttrTypes}
	secrets := types.ListNull(secretType)
	if raw, ok := obj["suggested_secrets"].([]any); ok {
		items := make([]attr.Value, 0, len(raw))
		for _, entry := range raw {
			var name, label, help, provider, placeholder, icon, role any
			switch e := entry.(type) {
			case map[string]any:
				name, label, help = e["name"], e["label"], e["help_url"]
				provider, placeholder, icon, role = e["provider"], e["placeholder"], e["icon"], e["role"]
			case string:
				name = e
			default:
				continue
			}
			value, d := types.ObjectValue(microvmSuggestedSecretAttrTypes, map[string]attr.Value{
				"name": nullableString(name), "label": nullableString(label), "help_url": nullableString(help),
				"provider": nullableString(provider), "placeholder": nullableString(placeholder),
				"icon": nullableString(icon), "role": nullableString(role),
			})
			diags.Append(d...)
			items = append(items, value)
		}
		list, d := types.ListValue(secretType, items)
		diags.Append(d...)
		secrets = list
	}
	return map[string]attr.Value{
		"category":                 nullableString(obj["category"]),
		"icon":                     nullableString(obj["icon"]),
		"tagline":                  nullableString(obj["tagline"]),
		"default_plan_vcpu":        nullableInt64(obj["default_plan_vcpu"]),
		"default_lifetime_seconds": nullableInt64(obj["default_lifetime_seconds"]),
		"suggested_secrets":        secrets,
		"sort_order":               nullableInt64(obj["sort_order"]),
	}
}

func mergeAttrTypes(base, extra map[string]attr.Type) map[string]attr.Type {
	out := map[string]attr.Type{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func mergeAttrValues(base, extra map[string]attr.Value) map[string]attr.Value {
	for k, v := range extra {
		base[k] = v
	}
	return base
}

func mergeSchemaAttrs(base, extra map[string]schema.Attribute) map[string]schema.Attribute {
	for k, v := range extra {
		base[k] = v
	}
	return base
}
