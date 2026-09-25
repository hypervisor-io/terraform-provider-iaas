package datasources

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
	"github.com/hypervisor-io/terraform-provider-iaas/internal/tfdiag"
)

var (
	_ datasource.DataSource              = &webhookEventKindsDataSource{}
	_ datasource.DataSourceWithConfigure = &webhookEventKindsDataSource{}
)

// NewWebhookEventKindsDataSource is the constructor registered with the provider.
func NewWebhookEventKindsDataSource() datasource.DataSource {
	return &webhookEventKindsDataSource{}
}

// webhookEventKindsDataSource is a full-list, no-filter catalog data source
// (the microvm_images / microvm_catalog shape) -- there is nothing to filter
// by, the whole published vocabulary is small and meant to be read in full.
type webhookEventKindsDataSource struct {
	client *client.Client
}

type webhookEventKindsModel struct {
	Kinds types.List `tfsdk:"kinds"`
}

var webhookEventKindAttrTypes = map[string]attr.Type{
	"kind":        types.StringType,
	"resource":    types.StringType,
	"description": types.StringType,
}

func (d *webhookEventKindsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_webhook_event_kinds"
}

func (d *webhookEventKindsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Published catalog of every event kind a webhook subscription's " +
			"`event_kinds` may name (or `\"*\"` for all). Validate a kind against this list " +
			"before referencing it in an `iaas_webhook_subscription` (or the API directly) " +
			"instead of hardcoding the vocabulary -- an unrecognised kind is rejected with a " +
			"422 at subscribe time.",
		Attributes: map[string]schema.Attribute{
			"kinds": schema.ListNestedAttribute{
				Computed:    true,
				Description: "Every published event kind.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"kind":        schema.StringAttribute{Computed: true, Description: "Event kind value, e.g. `cluster.created` or `instance.deleted`."},
					"resource":    schema.StringAttribute{Computed: true, Description: "The resource family the kind belongs to, e.g. `kubernetes_cluster` or `instance`."},
					"description": schema.StringAttribute{Computed: true, Description: "Human-readable description of what triggers this kind."},
				}},
			},
		},
	}
}

func (d *webhookEventKindsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	c, problem := configureClient(req.ProviderData)
	if problem != "" {
		resp.Diagnostics.AddError("Unexpected Provider Data Type", problem)
		return
	}
	d.client = c
}

func (d *webhookEventKindsDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	rows, err := d.client.GetWebhookEventKinds(ctx)
	if err != nil {
		resp.Diagnostics.Append(tfdiag.FromErr("Error reading webhook event kinds", err))
		return
	}

	list, diags := webhookEventKindsToList(rows)
	resp.Diagnostics.Append(diags...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &webhookEventKindsModel{Kinds: list})...)
}

func webhookEventKindsToList(rows []map[string]any) (types.List, diag.Diagnostics) {
	objectType := types.ObjectType{AttrTypes: webhookEventKindAttrTypes}
	values := []attr.Value{}
	var diags diag.Diagnostics
	for _, row := range rows {
		value, valueDiags := types.ObjectValue(webhookEventKindAttrTypes, map[string]attr.Value{
			"kind":        types.StringValue(strField(row, "kind")),
			"resource":    types.StringValue(strField(row, "resource")),
			"description": types.StringValue(strField(row, "description")),
		})
		diags.Append(valueDiags...)
		values = append(values, value)
	}
	list, listDiags := types.ListValue(objectType, values)
	diags.Append(listDiags...)
	return list, diags
}
