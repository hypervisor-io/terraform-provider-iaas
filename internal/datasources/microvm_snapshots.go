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
	_ datasource.DataSource              = &microvmSnapshotsDataSource{}
	_ datasource.DataSourceWithConfigure = &microvmSnapshotsDataSource{}
)

// NewMicrovmSnapshotsDataSource registers iaas_microvm_snapshots: the account's sandbox disk snapshots.
func NewMicrovmSnapshotsDataSource() datasource.DataSource { return &microvmSnapshotsDataSource{} }

type microvmSnapshotsDataSource struct{ client *client.Client }

type microvmSnapshotsModel struct {
	SandboxID types.String `tfsdk:"sandbox_id"`
	Status    types.String `tfsdk:"status"`
	Name      types.String `tfsdk:"name"`
	Snapshots types.List   `tfsdk:"snapshots"`
}

var microvmSnapshotAttrTypes = map[string]attr.Type{
	"id":                types.StringType,
	"name":              types.StringType,
	"description":       types.StringType,
	"kind":              types.StringType,
	"status":            types.StringType,
	"source_sandbox_id": types.StringType,
	"source_state":      types.StringType,
	"consistency":       types.StringType,
	"size_bytes":        types.Int64Type,
	"used_bytes":        types.Int64Type,
	"disk_gib":          types.Int64Type,
	"vcpu":              types.Int64Type,
	"mem_mib":           types.Int64Type,
	"location_id":       types.StringType,
	"image_id":          types.StringType,
	"capture_method":    types.StringType,
	"paused_ms":         types.Int64Type,
	"volumes_excluded":  types.Int64Type,
	"billable_gib":      types.Int64Type,
	"price_gib_hour":    types.Float64Type,
	"failure_reason":    types.StringType,
	"created_at":        types.StringType,
	"ready_at":          types.StringType,
}

func (d *microvmSnapshotsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_microvm_snapshots"
}

func (d *microvmSnapshotsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	str := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, Description: desc}
	}
	num := func(desc string) schema.Int64Attribute {
		return schema.Int64Attribute{Computed: true, Description: desc}
	}
	resp.Schema = schema.Schema{
		Description: "Lists the account's sandbox disk snapshots, newest first. Use an id as snapshot_id on iaas_microvm to start a sandbox from it.",
		Attributes: map[string]schema.Attribute{
			"sandbox_id": schema.StringAttribute{Optional: true, Description: "Only snapshots taken from this sandbox."},
			"status":     schema.StringAttribute{Optional: true, Description: "Only snapshots in this status: creating, ready, failed, deleting or unavailable."},
			"name":       schema.StringAttribute{Optional: true, Description: "Only the snapshot with exactly this name."},
			"snapshots": schema.ListNestedAttribute{
				Computed: true,
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":                str("Snapshot UUID."),
					"name":              str("Snapshot name."),
					"description":       str("Free text."),
					"kind":              str("disk."),
					"status":            str("creating, ready, failed, deleting or unavailable."),
					"source_sandbox_id": str("Sandbox the snapshot was taken from; null once that sandbox is deleted."),
					"source_state":      str("running, paused or stopped when it was taken."),
					"consistency":       str("quiesced, crash or clean."),
					"size_bytes":        num("Disk size in bytes."),
					"used_bytes":        num("Bytes actually stored."),
					"disk_gib":          num("Disk size in GiB; a plan must have at least this much to start a sandbox from the snapshot."),
					"vcpu":              num("vCPUs of the source sandbox."),
					"mem_mib":           num("Memory of the source sandbox in MiB."),
					"location_id":       str("Location the snapshot is stored in."),
					"image_id":          str("Image the source sandbox ran."),
					"capture_method":    str("reflink, paused_copy or offline_copy."),
					"paused_ms":         num("Milliseconds the sandbox was paused."),
					"volumes_excluded":  num("How many data volumes of the sandbox are NOT in this snapshot (it holds the root disk only)."),
					"billable_gib":      num("Whole GiB billed per hour."),
					"price_gib_hour":    schema.Float64Attribute{Computed: true, Description: "Price per GiB-hour of stored space."},
					"failure_reason":    str("Why a failed snapshot failed."),
					"created_at":        str("When it was requested."),
					"ready_at":          str("When it became ready."),
				}},
			},
		},
	}
}

func (d *microvmSnapshotsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	c, problem := configureClient(req.ProviderData)
	if problem != "" {
		resp.Diagnostics.AddError("Unexpected Provider Data Type", problem)
		return
	}
	d.client = c
}

func (d *microvmSnapshotsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config microvmSnapshotsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	rows, err := d.client.ListMicrovmSnapshots(ctx, config.SandboxID.ValueString(), config.Status.ValueString(), "")
	if err != nil {
		resp.Diagnostics.Append(tfdiag.FromErr("Error listing MicroVM snapshots", err))
		return
	}
	list, diags := microvmSnapshotsToList(rows, config)
	resp.Diagnostics.Append(diags...)
	config.Snapshots = list
	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}

func microvmSnapshotsToList(rows []map[string]any, config microvmSnapshotsModel) (types.List, diag.Diagnostics) {
	objectType := types.ObjectType{AttrTypes: microvmSnapshotAttrTypes}
	values := []attr.Value{}
	var diags diag.Diagnostics
	for _, row := range rows {
		if !config.Name.IsNull() && !config.Name.IsUnknown() && !strings.EqualFold(strField(row, "name"), config.Name.ValueString()) {
			continue
		}
		price := types.Float64Null()
		if p, ok := row["price_gib_hour"].(float64); ok {
			price = types.Float64Value(p)
		}
		value, valueDiags := types.ObjectValue(microvmSnapshotAttrTypes, map[string]attr.Value{
			"id":                types.StringValue(strField(row, "id")),
			"name":              types.StringValue(strField(row, "name")),
			"description":       nullableString(row["description"]),
			"kind":              types.StringValue(strField(row, "kind")),
			"status":            types.StringValue(strField(row, "status")),
			"source_sandbox_id": nullableString(row["source_sandbox_id"]),
			"source_state":      nullableString(row["source_state"]),
			"consistency":       nullableString(row["consistency"]),
			"size_bytes":        snapshotInt(row["size_bytes"]),
			"used_bytes":        snapshotInt(row["used_bytes"]),
			"disk_gib":          snapshotInt(row["disk_gib"]),
			"vcpu":              snapshotInt(row["vcpu"]),
			"mem_mib":           snapshotInt(row["mem_mib"]),
			"location_id":       nullableString(row["location_id"]),
			"image_id":          nullableString(row["image_id"]),
			"capture_method":    nullableString(row["capture_method"]),
			"paused_ms":         snapshotInt(row["paused_ms"]),
			"volumes_excluded":  snapshotInt(row["volumes_excluded"]),
			"billable_gib":      snapshotInt(row["billable_gib"]),
			"price_gib_hour":    price,
			"failure_reason":    nullableString(row["failure_reason"]),
			"created_at":        nullableString(row["created_at"]),
			"ready_at":          nullableString(row["ready_at"]),
		})
		diags.Append(valueDiags...)
		values = append(values, value)
	}
	list, listDiags := types.ListValue(objectType, values)
	diags.Append(listDiags...)
	return list, diags
}

func snapshotInt(raw any) types.Int64 {
	if n, ok := raw.(float64); ok {
		return types.Int64Value(int64(n))
	}
	return types.Int64Null()
}
