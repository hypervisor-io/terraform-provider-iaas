package datasources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/iaas/terraform-provider-iaas/internal/client"
	"github.com/iaas/terraform-provider-iaas/internal/tfdiag"
)

// Interface assertions — kubernetes_plan is a lookup-by-name catalog data source
// backed by the FLAT Select2 /kubernetes/search/{plans|cp-plans|lb-plans}
// endpoints, selected by the `kind` argument.
var (
	_ datasource.DataSource              = &kubernetesPlanDataSource{}
	_ datasource.DataSourceWithConfigure = &kubernetesPlanDataSource{}
)

// Plan kinds. worker/cp resolve to instance plans (the SAME underlying list —
// the server splits the route only for semantic clarity); lb resolves to LB
// plans (which have no storage).
const (
	k8sPlanKindWorker = "worker"
	k8sPlanKindCP     = "cp"
	k8sPlanKindLB     = "lb"
)

// NewKubernetesPlanDataSource is the constructor registered with the provider.
func NewKubernetesPlanDataSource() datasource.DataSource {
	return &kubernetesPlanDataSource{}
}

// kubernetesPlanDataSource resolves the UUID of a Kubernetes cluster plan by name
// for one of three pickers, selected by `kind`:
//
//	worker → worker instance plan  (cp_instance_plan_id / worker_instance_plan_id)
//	cp     → control-plane plan    (identical underlying list as worker)
//	lb     → control-plane LB plan (cp_lb_plan_id; no storage)
//
// A single data source with a `kind` argument is cleaner than three near-identical
// sources, and mirrors the real API where worker/cp share one list and lb is the
// only genuinely distinct catalog.
type kubernetesPlanDataSource struct {
	client *client.Client
}

// kubernetesPlanModel maps the data-source state. kind + name are inputs; the
// rest are computed. storage is null for lb plans (LB plans carry no storage).
type kubernetesPlanModel struct {
	Kind        types.String `tfsdk:"kind"`
	Name        types.String `tfsdk:"name"`
	ID          types.String `tfsdk:"id"`
	Description types.String `tfsdk:"description"`
	CPUCores    types.Int64  `tfsdk:"cpu_cores"`
	RAM         types.Int64  `tfsdk:"ram"`
	Storage     types.Int64  `tfsdk:"storage"`
	CreditValue types.Int64  `tfsdk:"credit_value"`
}

func (d *kubernetesPlanDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_plan"
}

func (d *kubernetesPlanDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Looks up a Kubernetes cluster plan by name from the cluster-create " +
			"catalog, resolving the `id` you pass on an `iaas_kubernetes_cluster`. The `kind` " +
			"argument selects the picker: `worker` and `cp` resolve instance plans (the same " +
			"underlying list — use the result as `worker_instance_plan_id` / " +
			"`cp_instance_plan_id`), and `lb` resolves the control-plane load-balancer plan " +
			"(use as `cp_lb_plan_id`). Only enabled plans are returned; exactly one plan must " +
			"match the given `name`.",
		Attributes: map[string]schema.Attribute{
			"kind": schema.StringAttribute{
				Required: true,
				Description: "Which plan catalog to search: `worker` (worker instance plan), `cp` " +
					"(control-plane instance plan — identical list to `worker`), or `lb` " +
					"(control-plane load-balancer plan). Any other value errors.",
			},
			"name": schema.StringAttribute{
				Required: true,
				Description: "Exact plan name to look up (e.g. `std-2`). Matched against the plan " +
					"name (not the specs-decorated display text). Exactly one plan must match.",
			},
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the matched plan.",
			},
			"description": schema.StringAttribute{
				Computed:    true,
				Description: "Human display text of the matched plan (name plus a spec summary).",
			},
			"cpu_cores": schema.Int64Attribute{
				Computed:    true,
				Description: "vCPU cores of the matched plan.",
			},
			"ram": schema.Int64Attribute{
				Computed:    true,
				Description: "RAM of the matched plan, in MB.",
			},
			"storage": schema.Int64Attribute{
				Computed: true,
				Description: "Root disk of the matched plan, in GB. Always 0 for `lb` plans " +
					"(load-balancer plans carry no storage).",
			},
			"credit_value": schema.Int64Attribute{
				Computed:    true,
				Description: "Hourly credit value of the matched plan.",
			},
		},
	}
}

func (d *kubernetesPlanDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	c, problem := configureClient(req.ProviderData)
	if problem != "" {
		resp.Diagnostics.AddError("Unexpected Provider Data Type", problem)
		return
	}
	d.client = c
}

// Read dispatches to the right catalog by kind, then resolves a single plan by
// exact name match.
func (d *kubernetesPlanDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg kubernetesPlanModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	kind := cfg.Kind.ValueString()
	name := cfg.Name.ValueString()

	var (
		plans []map[string]any
		err   error
	)
	switch kind {
	case k8sPlanKindWorker:
		plans, err = d.client.SearchK8sWorkerPlans(ctx, name)
	case k8sPlanKindCP:
		plans, err = d.client.SearchK8sControlPlanePlans(ctx, name)
	case k8sPlanKindLB:
		plans, err = d.client.SearchK8sLoadBalancerPlans(ctx, name)
	default:
		// Defensive — the schema validator already constrains kind.
		resp.Diagnostics.AddError("Invalid plan kind", fmt.Sprintf("unknown kind %q (want worker, cp, or lb)", kind))
		return
	}
	if err != nil {
		resp.Diagnostics.Append(tfdiag.FromErr("Error searching Kubernetes plans", err))
		return
	}

	// Match on the clean plan "name" (the "text" field is decorated with specs).
	match, err := findUnique(plans, "kubernetes plan", name, func(p map[string]any) bool {
		return strField(p, "name") == name
	})
	if err != nil {
		resp.Diagnostics.AddError("Kubernetes plan lookup failed", err.Error())
		return
	}

	cfg.ID = types.StringValue(strField(match, "id"))
	cfg.Description = types.StringValue(strField(match, "text"))
	cfg.CPUCores = types.Int64Value(int64Field(match, "cpu_cores"))
	cfg.RAM = types.Int64Value(int64Field(match, "ram"))
	cfg.Storage = types.Int64Value(int64Field(match, "storage"))
	cfg.CreditValue = types.Int64Value(int64Field(match, "credit_value"))

	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}
