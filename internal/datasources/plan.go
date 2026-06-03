package datasources

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/iaas/terraform-provider-iaas/internal/client"
	"github.com/iaas/terraform-provider-iaas/internal/tfdiag"
)

var (
	_ datasource.DataSource              = &planDataSource{}
	_ datasource.DataSourceWithConfigure = &planDataSource{}
)

// NewPlanDataSource is the constructor registered with the provider.
func NewPlanDataSource() datasource.DataSource {
	return &planDataSource{}
}

// planDataSource looks up a cloud-service plan by name within a location. It
// HIDES the nested catalog from the user: the API splits plans across plan
// groups (location → plan-groups → plans), but the user only supplies the plan
// name (and optionally a plan_group name to disambiguate). The data source walks
// the groups and matches by plan name.
type planDataSource struct {
	client *client.Client
}

// planModel maps the data-source state. location_id + name are required inputs;
// plan_group is an optional disambiguator; the rest are computed outputs.
type planModel struct {
	LocationID  types.String `tfsdk:"location_id"`
	Name        types.String `tfsdk:"name"`
	PlanGroup   types.String `tfsdk:"plan_group"`
	ID          types.String `tfsdk:"id"`
	CPUCores    types.Int64  `tfsdk:"cpu_cores"`
	RAM         types.Int64  `tfsdk:"ram"`
	Storage     types.Int64  `tfsdk:"storage"`
	Bandwidth   types.Int64  `tfsdk:"bandwidth"`
	PlanGroupID types.String `tfsdk:"plan_group_id"`
}

func (d *planDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_plan"
}

func (d *planDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Looks up a cloud-service plan by name within a location. Plans are " +
			"organised under plan groups in the API; this data source hides that nesting " +
			"and matches purely by plan name (optionally narrowed to a plan group). " +
			"Exactly one plan must match.",
		Attributes: map[string]schema.Attribute{
			"location_id": schema.StringAttribute{
				Required:    true,
				Description: "UUID of the location (hypervisor group) whose catalog to search.",
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Name of the plan to look up. Exactly one plan must match.",
			},
			"plan_group": schema.StringAttribute{
				Optional: true,
				Description: "Optional plan-group slug (e.g. `general`) to disambiguate when " +
					"the same plan name exists in multiple groups. This must be the slug " +
					"`name` field of the plan group, not its human display name.",
			},
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the matched plan.",
			},
			"cpu_cores": schema.Int64Attribute{
				Computed:    true,
				Description: "Number of vCPU cores the plan provides.",
			},
			"ram": schema.Int64Attribute{
				Computed:    true,
				Description: "RAM the plan provides, in MB.",
			},
			"storage": schema.Int64Attribute{
				Computed:    true,
				Description: "Root storage the plan provides, in GB.",
			},
			"bandwidth": schema.Int64Attribute{
				Computed:    true,
				Description: "Monthly bandwidth allowance the plan provides.",
			},
			"plan_group_id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the plan group the matched plan belongs to.",
			},
		},
	}
}

func (d *planDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	c, problem := configureClient(req.ProviderData)
	if problem != "" {
		resp.Diagnostics.AddError("Unexpected Provider Data Type", problem)
		return
	}
	d.client = c
}

// Read walks the location's plan groups (filtered to plan_group when set),
// collects every plan, and finds the unique match by name. Each collected plan
// carries the synthetic key "__plan_group_id" so the matched plan can report the
// group it came from.
func (d *planDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg planModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	locationID := cfg.LocationID.ValueString()
	name := cfg.Name.ValueString()
	groupFilter := cfg.PlanGroup.ValueString() // "" when unset → no filter

	groups, err := d.client.ListPlanGroups(ctx, locationID)
	if err != nil {
		resp.Diagnostics.Append(tfdiag.FromErr("Error reading plan groups", err))
		return
	}

	var allPlans []map[string]any
	for _, group := range groups {
		if groupFilter != "" && strField(group, "name") != groupFilter {
			continue
		}
		groupID := strField(group, "id")
		plans, err := d.client.ListPlans(ctx, locationID, groupID)
		if err != nil {
			resp.Diagnostics.Append(tfdiag.FromErr("Error reading plans", err))
			return
		}
		for _, p := range plans {
			// __-prefixed keys are synthetic sidecar fields injected by the provider
			// (not real API fields); this one carries the owning plan-group id through findUnique.
			p["__plan_group_id"] = groupID
			allPlans = append(allPlans, p)
		}
	}

	match, err := findUnique(allPlans, "plan", name, func(p map[string]any) bool {
		return strField(p, "name") == name
	})
	if err != nil {
		resp.Diagnostics.AddError("Plan lookup failed", err.Error())
		return
	}

	cfg.ID = types.StringValue(strField(match, "id"))
	cfg.CPUCores = types.Int64Value(int64Field(match, "cpu_cores"))
	cfg.RAM = types.Int64Value(int64Field(match, "ram"))
	cfg.Storage = types.Int64Value(int64Field(match, "storage"))
	cfg.Bandwidth = types.Int64Value(int64Field(match, "bandwidth"))
	cfg.PlanGroupID = types.StringValue(strField(match, "__plan_group_id"))

	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}
