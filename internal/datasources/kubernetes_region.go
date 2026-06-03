package datasources

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/iaas/terraform-provider-iaas/internal/client"
	"github.com/iaas/terraform-provider-iaas/internal/tfdiag"
)

// Interface assertions — kubernetes_region is a lookup-by-name catalog data
// source backed by the FLAT Select2 /kubernetes/search/regions endpoint.
var (
	_ datasource.DataSource              = &kubernetesRegionDataSource{}
	_ datasource.DataSourceWithConfigure = &kubernetesRegionDataSource{}
)

// NewKubernetesRegionDataSource is the constructor registered with the provider.
func NewKubernetesRegionDataSource() datasource.DataSource {
	return &kubernetesRegionDataSource{}
}

// kubernetesRegionDataSource resolves the UUID of a hypervisor group eligible to
// host a Kubernetes cluster (kubernetes_enabled AND vpc_enabled AND lb_enabled)
// by its name or slug, for use as hypervisor_group_id when creating a cluster.
// Unlike the generic iaas_location data source, this one returns ONLY
// k8s-eligible regions, so a match guarantees the region can host a cluster.
type kubernetesRegionDataSource struct {
	client *client.Client
}

// kubernetesRegionModel maps the data-source state. name is the input filter
// (region name or slug); id/slug are computed outputs.
type kubernetesRegionModel struct {
	Name types.String `tfsdk:"name"`
	ID   types.String `tfsdk:"id"`
	Slug types.String `tfsdk:"slug"`
}

func (d *kubernetesRegionDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_region"
}

func (d *kubernetesRegionDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Looks up a region (hypervisor group) eligible to host a Kubernetes cluster " +
			"by its display name or slug, resolving the `id` you pass as `hypervisor_group_id` " +
			"on an `iaas_kubernetes_cluster`. Only regions with Kubernetes, VPC, AND Load " +
			"Balancer features enabled are returned, so a match guarantees the region can host " +
			"a cluster. Matches the region display name or its slug; exactly one region must " +
			"match.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Required: true,
				Description: "Region to look up. Matches the region display name (e.g. `NYC1`) or " +
					"its slug (e.g. `nyc1`). Exactly one region must match.",
			},
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the matched region (use as `hypervisor_group_id`).",
			},
			"slug": schema.StringAttribute{
				Computed:    true,
				Description: "Slug of the matched region.",
			},
		},
	}
}

func (d *kubernetesRegionDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	c, problem := configureClient(req.ProviderData)
	if problem != "" {
		resp.Diagnostics.AddError("Unexpected Provider Data Type", problem)
		return
	}
	d.client = c
}

// Read searches the eligible-region catalog and resolves a single region by exact
// name (text) or slug match.
func (d *kubernetesRegionDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg kubernetesRegionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	name := cfg.Name.ValueString()

	regions, err := d.client.SearchK8sRegions(ctx, name)
	if err != nil {
		resp.Diagnostics.Append(tfdiag.FromErr("Error searching Kubernetes regions", err))
		return
	}

	match, err := findUnique(regions, "kubernetes region", name, func(r map[string]any) bool {
		return strField(r, "text") == name || strField(r, "slug") == name
	})
	if err != nil {
		resp.Diagnostics.AddError("Kubernetes region lookup failed", err.Error())
		return
	}

	cfg.ID = types.StringValue(strField(match, "id"))
	cfg.Slug = types.StringValue(strField(match, "slug"))
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}
