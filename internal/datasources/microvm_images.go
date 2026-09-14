package datasources

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
	"github.com/hypervisor-io/terraform-provider-iaas/internal/tfdiag"
)

var (
	_ datasource.DataSource              = &microvmImagesDataSource{}
	_ datasource.DataSourceWithConfigure = &microvmImagesDataSource{}
)

func NewMicrovmImagesDataSource() datasource.DataSource {
	return &microvmImagesDataSource{}
}

type microvmImagesDataSource struct {
	client *client.Client
}

type microvmImagesModel struct {
	Search types.String `tfsdk:"search"`
	Kind   types.String `tfsdk:"kind"`
	Status types.String `tfsdk:"status"`
	Images types.List   `tfsdk:"images"`
}

var microvmImageDataAttrTypes = map[string]attr.Type{
	"id":                 types.StringType,
	"name":               types.StringType,
	"description":        types.StringType,
	"source_kind":        types.StringType,
	"status":             types.StringType,
	"template_name":      types.StringType,
	"current_version_id": types.StringType,
}

func (d *microvmImagesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_microvm_images"
}

func (d *microvmImagesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists platform and account MicroVM images, with optional local filters.",
		Attributes: map[string]schema.Attribute{
			"search": schema.StringAttribute{Optional: true, Description: "Case-insensitive image-name substring."},
			"kind":   schema.StringAttribute{Optional: true, Description: "Exact source kind: base, dockerfile, oci, or git."},
			"status": schema.StringAttribute{Optional: true, Description: "Exact image status."},
			"images": schema.ListNestedAttribute{
				Computed: true,
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":                 schema.StringAttribute{Computed: true},
					"name":               schema.StringAttribute{Computed: true},
					"description":        schema.StringAttribute{Computed: true},
					"source_kind":        schema.StringAttribute{Computed: true},
					"status":             schema.StringAttribute{Computed: true},
					"template_name":      schema.StringAttribute{Computed: true},
					"current_version_id": schema.StringAttribute{Computed: true},
				}},
			},
		},
	}
}

func (d *microvmImagesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	c, problem := configureClient(req.ProviderData)
	if problem != "" {
		resp.Diagnostics.AddError("Unexpected Provider Data Type", problem)
		return
	}
	d.client = c
}

func (d *microvmImagesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config microvmImagesModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	images, err := d.client.ListMicrovmImages(ctx, "", "", "")
	if err != nil {
		resp.Diagnostics.Append(tfdiag.FromErr("Error listing MicroVM images", err))
		return
	}
	list, diags := microvmImagesToList(images, config)
	resp.Diagnostics.Append(diags...)
	config.Images = list
	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}

func microvmImagesToList(images []map[string]any, config microvmImagesModel) (types.List, diag.Diagnostics) {
	objectType := types.ObjectType{AttrTypes: microvmImageDataAttrTypes}
	values := []attr.Value{}
	var diags diag.Diagnostics
	for _, image := range images {
		name := strField(image, "name")
		if !config.Search.IsNull() && !strings.Contains(strings.ToLower(name), strings.ToLower(config.Search.ValueString())) {
			continue
		}
		if !config.Kind.IsNull() && strField(image, "source_kind") != config.Kind.ValueString() {
			continue
		}
		if !config.Status.IsNull() && strField(image, "status") != config.Status.ValueString() {
			continue
		}
		value, valueDiags := types.ObjectValue(microvmImageDataAttrTypes, map[string]attr.Value{
			"id":                 types.StringValue(strField(image, "id")),
			"name":               types.StringValue(name),
			"description":        nullableString(image["description"]),
			"source_kind":        types.StringValue(strField(image, "source_kind")),
			"status":             types.StringValue(strField(image, "status")),
			"template_name":      types.StringValue(strField(image, "template_name")),
			"current_version_id": nullableString(image["current_version_id"]),
		})
		diags.Append(valueDiags...)
		values = append(values, value)
	}
	list, listDiags := types.ListValue(objectType, values)
	diags.Append(listDiags...)
	return list, diags
}

func nullableString(raw any) types.String {
	if raw == nil {
		return types.StringNull()
	}
	if value, ok := raw.(string); ok {
		return types.StringValue(value)
	}
	return types.StringValue(fmt.Sprintf("%v", raw))
}
