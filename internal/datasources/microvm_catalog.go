package datasources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
	"github.com/hypervisor-io/terraform-provider-iaas/internal/tfdiag"
)

var (
	_ datasource.DataSource              = &microvmCatalogDataSource{}
	_ datasource.DataSourceWithConfigure = &microvmCatalogDataSource{}
)

func NewMicrovmCatalogDataSource() datasource.DataSource {
	return &microvmCatalogDataSource{}
}

type microvmCatalogDataSource struct {
	client *client.Client
}

type microvmCatalogModel struct {
	Locations      types.List   `tfsdk:"locations"`
	Images         types.List   `tfsdk:"images"`
	SecurityGroups types.List   `tfsdk:"security_groups"`
	VPCSubnets     types.List   `tfsdk:"vpc_subnets"`
	Limits         types.Object `tfsdk:"limits"`
}

var microvmCatalogPlanAttrTypes = map[string]attr.Type{
	"id":                  types.StringType,
	"name":                types.StringType,
	"vcpu":                types.Int64Type,
	"mem_mib":             types.Int64Type,
	"disk_gib":            types.Int64Type,
	"price_vcpu_second":   types.Float64Type,
	"price_mib_second":    types.Float64Type,
	"price_disk_gib_hour": types.Float64Type,
}

var microvmCatalogLocationAttrTypes = map[string]attr.Type{
	"id":        types.StringType,
	"name":      types.StringType,
	"country":   types.StringType,
	"available": types.BoolType,
	"locked":    types.BoolType,
	"plans":     types.ListType{ElemType: types.ObjectType{AttrTypes: microvmCatalogPlanAttrTypes}},
}

var microvmCatalogImageAttrTypes = map[string]attr.Type{
	"id":            types.StringType,
	"name":          types.StringType,
	"is_base":       types.BoolType,
	"status":        types.StringType,
	"exposed_ports": types.ListType{ElemType: types.Int64Type},
}

var microvmCatalogNamedAttrTypes = map[string]attr.Type{
	"id":   types.StringType,
	"name": types.StringType,
}

var microvmCatalogSubnetAttrTypes = map[string]attr.Type{
	"id":   types.StringType,
	"name": types.StringType,
	"cidr": types.StringType,
	"vpc":  types.StringType,
}

var microvmCatalogLimitsAttrTypes = map[string]attr.Type{
	"max_lifetime_seconds":     types.Int64Type,
	"default_lifetime_seconds": types.Int64Type,
	"hook_timeout_max":         types.Int64Type,
	"idle_timeout_min":         types.Int64Type,
	"max_microvms":             types.Int64Type,
}

func (d *microvmCatalogDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_microvm_catalog"
}

func (d *microvmCatalogDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads the unified MicroVM placement catalog, including locations, plans, ready images, network choices, and limits.",
		Attributes: map[string]schema.Attribute{
			"locations": schema.ListNestedAttribute{
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id":        schema.StringAttribute{Computed: true},
						"name":      schema.StringAttribute{Computed: true},
						"country":   schema.StringAttribute{Computed: true},
						"available": schema.BoolAttribute{Computed: true},
						"locked":    schema.BoolAttribute{Computed: true},
						"plans": schema.ListNestedAttribute{
							Computed: true,
							NestedObject: schema.NestedAttributeObject{
								Attributes: map[string]schema.Attribute{
									"id":                  schema.StringAttribute{Computed: true},
									"name":                schema.StringAttribute{Computed: true},
									"vcpu":                schema.Int64Attribute{Computed: true},
									"mem_mib":             schema.Int64Attribute{Computed: true},
									"disk_gib":            schema.Int64Attribute{Computed: true},
									"price_vcpu_second":   schema.Float64Attribute{Computed: true},
									"price_mib_second":    schema.Float64Attribute{Computed: true},
									"price_disk_gib_hour": schema.Float64Attribute{Computed: true},
								},
							},
						},
					},
				},
			},
			"images": schema.ListNestedAttribute{
				Computed: true,
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":            schema.StringAttribute{Computed: true},
					"name":          schema.StringAttribute{Computed: true},
					"is_base":       schema.BoolAttribute{Computed: true},
					"status":        schema.StringAttribute{Computed: true},
					"exposed_ports": schema.ListAttribute{Computed: true, ElementType: types.Int64Type},
				}},
			},
			"security_groups": schema.ListNestedAttribute{
				Computed: true,
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":   schema.StringAttribute{Computed: true},
					"name": schema.StringAttribute{Computed: true},
				}},
			},
			"vpc_subnets": schema.ListNestedAttribute{
				Computed: true,
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":   schema.StringAttribute{Computed: true},
					"name": schema.StringAttribute{Computed: true},
					"cidr": schema.StringAttribute{Computed: true},
					"vpc":  schema.StringAttribute{Computed: true},
				}},
			},
			"limits": schema.SingleNestedAttribute{
				Computed: true,
				Attributes: map[string]schema.Attribute{
					"max_lifetime_seconds":     schema.Int64Attribute{Computed: true},
					"default_lifetime_seconds": schema.Int64Attribute{Computed: true},
					"hook_timeout_max":         schema.Int64Attribute{Computed: true},
					"idle_timeout_min":         schema.Int64Attribute{Computed: true},
					"max_microvms":             schema.Int64Attribute{Computed: true},
				},
			},
		},
	}
}

func (d *microvmCatalogDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	c, problem := configureClient(req.ProviderData)
	if problem != "" {
		resp.Diagnostics.AddError("Unexpected Provider Data Type", problem)
		return
	}
	d.client = c
}

func (d *microvmCatalogDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	catalog, err := d.client.GetMicrovmCatalog(ctx)
	if err != nil {
		resp.Diagnostics.Append(tfdiag.FromErr("Error reading MicroVM catalog", err))
		return
	}
	state, diags := microvmCatalogState(catalog)
	resp.Diagnostics.Append(diags...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func microvmCatalogState(catalog map[string]any) (microvmCatalogModel, diag.Diagnostics) {
	var diags diag.Diagnostics
	locations := make([]attr.Value, 0)
	for _, location := range objectSlice(catalog["locations"]) {
		plans := make([]attr.Value, 0)
		for _, plan := range objectSlice(location["plans"]) {
			value, valueDiags := types.ObjectValue(microvmCatalogPlanAttrTypes, map[string]attr.Value{
				"id": types.StringValue(strField(plan, "id")), "name": types.StringValue(strField(plan, "name")),
				"vcpu": types.Int64Value(int64Field(plan, "vcpu")), "mem_mib": types.Int64Value(int64Field(plan, "mem_mib")), "disk_gib": types.Int64Value(int64Field(plan, "disk_gib")),
				"price_vcpu_second": types.Float64Value(float64Field(plan, "price_vcpu_second")), "price_mib_second": types.Float64Value(float64Field(plan, "price_mib_second")),
				"price_disk_gib_hour": types.Float64Value(float64Field(plan, "price_disk_gib_hour")),
			})
			diags.Append(valueDiags...)
			plans = append(plans, value)
		}
		planList, planDiags := types.ListValue(types.ObjectType{AttrTypes: microvmCatalogPlanAttrTypes}, plans)
		diags.Append(planDiags...)
		value, valueDiags := types.ObjectValue(microvmCatalogLocationAttrTypes, map[string]attr.Value{
			"id": types.StringValue(strField(location, "id")), "name": types.StringValue(strField(location, "name")), "country": nullableString(location["country"]),
			"available": types.BoolValue(boolField(location, "available")), "locked": types.BoolValue(boolField(location, "locked")), "plans": planList,
		})
		diags.Append(valueDiags...)
		locations = append(locations, value)
	}
	locationList, locationDiags := types.ListValue(types.ObjectType{AttrTypes: microvmCatalogLocationAttrTypes}, locations)
	diags.Append(locationDiags...)

	images := make([]attr.Value, 0)
	for _, image := range objectSlice(catalog["images"]) {
		ports := intList(image["exposed_ports"], &diags)
		value, valueDiags := types.ObjectValue(microvmCatalogImageAttrTypes, map[string]attr.Value{
			"id": types.StringValue(strField(image, "id")), "name": types.StringValue(strField(image, "name")), "is_base": types.BoolValue(boolField(image, "is_base")),
			"status": types.StringValue(strField(image, "status")), "exposed_ports": ports,
		})
		diags.Append(valueDiags...)
		images = append(images, value)
	}
	imageList, imageDiags := types.ListValue(types.ObjectType{AttrTypes: microvmCatalogImageAttrTypes}, images)
	diags.Append(imageDiags...)

	securityGroups := namedObjects(catalog["security_groups"], microvmCatalogNamedAttrTypes, &diags)
	subnets := make([]attr.Value, 0)
	for _, subnet := range objectSlice(catalog["vpc_subnets"]) {
		value, valueDiags := types.ObjectValue(microvmCatalogSubnetAttrTypes, map[string]attr.Value{
			"id": types.StringValue(strField(subnet, "id")), "name": types.StringValue(strField(subnet, "name")), "cidr": types.StringValue(strField(subnet, "cidr")), "vpc": nullableString(subnet["vpc"]),
		})
		diags.Append(valueDiags...)
		subnets = append(subnets, value)
	}
	subnetList, subnetDiags := types.ListValue(types.ObjectType{AttrTypes: microvmCatalogSubnetAttrTypes}, subnets)
	diags.Append(subnetDiags...)

	limits, limitDiags := types.ObjectValue(microvmCatalogLimitsAttrTypes, map[string]attr.Value{
		"max_lifetime_seconds":     types.Int64Value(int64Field(objectMap(catalog["limits"]), "max_lifetime_seconds")),
		"default_lifetime_seconds": types.Int64Value(int64Field(objectMap(catalog["limits"]), "default_lifetime_seconds")),
		"hook_timeout_max":         types.Int64Value(int64Field(objectMap(catalog["limits"]), "hook_timeout_max")),
		"idle_timeout_min":         types.Int64Value(int64Field(objectMap(catalog["limits"]), "idle_timeout_min")),
		"max_microvms":             types.Int64Value(microvmLimit(objectMap(catalog["limits"]))),
	})
	diags.Append(limitDiags...)
	return microvmCatalogModel{Locations: locationList, Images: imageList, SecurityGroups: securityGroups, VPCSubnets: subnetList, Limits: limits}, diags
}

func namedObjects(raw any, attrTypes map[string]attr.Type, diags *diag.Diagnostics) types.List {
	values := make([]attr.Value, 0)
	for _, item := range objectSlice(raw) {
		value, valueDiags := types.ObjectValue(attrTypes, map[string]attr.Value{"id": types.StringValue(strField(item, "id")), "name": types.StringValue(strField(item, "name"))})
		diags.Append(valueDiags...)
		values = append(values, value)
	}
	list, listDiags := types.ListValue(types.ObjectType{AttrTypes: attrTypes}, values)
	diags.Append(listDiags...)
	return list
}

func intList(raw any, diags *diag.Diagnostics) types.List {
	values := make([]attr.Value, 0)
	if items, ok := raw.([]any); ok {
		for _, item := range items {
			values = append(values, types.Int64Value(int64(number(item))))
		}
	}
	list, listDiags := types.ListValue(types.Int64Type, values)
	diags.Append(listDiags...)
	return list
}

func objectSlice(raw any) []map[string]any {
	items, _ := raw.([]any)
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if object, ok := item.(map[string]any); ok {
			result = append(result, object)
		}
	}
	return result
}

func objectMap(raw any) map[string]any {
	value, _ := raw.(map[string]any)
	return value
}

func float64Field(object map[string]any, key string) float64 {
	return number(object[key])
}

func number(raw any) float64 {
	switch value := raw.(type) {
	case float64:
		return value
	case int:
		return float64(value)
	case int64:
		return float64(value)
	case string:
		var valueNumber float64
		_, _ = fmt.Sscan(value, &valueNumber)
		return valueNumber
	default:
		return 0
	}
}

func microvmLimit(limits map[string]any) int64 {
	if value := int64Field(limits, "max_microvms"); value != 0 {
		return value
	}
	for key := range limits {
		if len(key) > 4 && key[:4] == "max_" && key != "max_lifetime_seconds" {
			return int64Field(limits, key)
		}
	}
	return 0
}
