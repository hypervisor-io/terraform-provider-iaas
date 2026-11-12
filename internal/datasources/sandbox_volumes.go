package datasources

import (
	"context"
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
	_ datasource.DataSource              = &sandboxVolumesDataSource{}
	_ datasource.DataSourceWithConfigure = &sandboxVolumesDataSource{}
)

// NewSandboxVolumesDataSource registers iaas_sandbox_volumes: the account's persistent data volumes for sandboxes.
func NewSandboxVolumesDataSource() datasource.DataSource { return &sandboxVolumesDataSource{} }

type sandboxVolumesDataSource struct{ client *client.Client }

type sandboxVolumesModel struct {
	Status  types.String `tfsdk:"status"`
	Name    types.String `tfsdk:"name"`
	Volumes types.List   `tfsdk:"volumes"`
}

var sandboxVolumeAttrTypes = map[string]attr.Type{
	"id":                       types.StringType,
	"name":                     types.StringType,
	"size_gib":                 types.Int64Type,
	"status":                   types.StringType,
	"location_id":              types.StringType,
	"sandbox_id":               types.StringType,
	"mount_path":               types.StringType,
	"delete_on_sandbox_delete": types.BoolType,
	"created_at":               types.StringType,
}

func (d *sandboxVolumesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_sandbox_volumes"
}

func (d *sandboxVolumesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	str := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, Description: desc}
	}
	resp.Schema = schema.Schema{
		Description: "Lists the account's persistent data volumes for sandboxes (iaas_sandbox_volume), with what each is attached to. Use an id in the volumes of iaas_microvm.",
		Attributes: map[string]schema.Attribute{
			"status": schema.StringAttribute{Optional: true, Description: "Only volumes in this status: available, attached, attaching, detaching, pending, deleting or error."},
			"name":   schema.StringAttribute{Optional: true, Description: "Only the volume with exactly this name."},
			"volumes": schema.ListNestedAttribute{
				Computed: true,
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":                       str("Volume UUID."),
					"name":                     str("Volume name."),
					"size_gib":                 schema.Int64Attribute{Computed: true, Description: "Size in GiB."},
					"status":                   str("available, attached, attaching, detaching, pending, deleting or error."),
					"location_id":              str("Location the volume lives in (on one node of it)."),
					"sandbox_id":               str("Sandbox that holds the volume; null while it is free."),
					"mount_path":               str("Where the holding sandbox mounts it; null while free."),
					"delete_on_sandbox_delete": schema.BoolAttribute{Computed: true, Description: "Whether the volume is deleted with the sandbox that holds it."},
					"created_at":               str("When the volume was created."),
				}},
			},
		},
	}
}

func (d *sandboxVolumesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	c, problem := configureClient(req.ProviderData)
	if problem != "" {
		resp.Diagnostics.AddError("Unexpected Provider Data Type", problem)
		return
	}
	d.client = c
}

func (d *sandboxVolumesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config sandboxVolumesModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	rows, err := d.client.ListSandboxVolumes(ctx, config.Status.ValueString())
	if err != nil {
		resp.Diagnostics.Append(tfdiag.FromErr("Error listing sandbox volumes", err))
		return
	}
	list, diags := sandboxVolumesToList(rows, config)
	resp.Diagnostics.Append(diags...)
	config.Volumes = list
	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}

func sandboxVolumesToList(rows []map[string]any, config sandboxVolumesModel) (types.List, diag.Diagnostics) {
	objectType := types.ObjectType{AttrTypes: sandboxVolumeAttrTypes}
	values := []attr.Value{}
	var diags diag.Diagnostics
	for _, row := range rows {
		if !config.Name.IsNull() && !config.Name.IsUnknown() && !strings.EqualFold(strField(row, "name"), config.Name.ValueString()) {
			continue
		}
		del, _ := row["delete_on_sandbox_delete"].(bool)
		value, valueDiags := types.ObjectValue(sandboxVolumeAttrTypes, map[string]attr.Value{
			"id":                       types.StringValue(strField(row, "id")),
			"name":                     types.StringValue(strField(row, "name")),
			"size_gib":                 snapshotInt(row["size_gib"]),
			"status":                   types.StringValue(strField(row, "status")),
			"location_id":              nullableString(row["location_id"]),
			"sandbox_id":               nullableString(row["sandbox_id"]),
			"mount_path":               nullableString(row["mount_path"]),
			"delete_on_sandbox_delete": types.BoolValue(del),
			"created_at":               nullableString(row["created_at"]),
		})
		diags.Append(valueDiags...)
		values = append(values, value)
	}
	list, listDiags := types.ListValue(objectType, values)
	diags.Append(listDiags...)
	return list, diags
}
