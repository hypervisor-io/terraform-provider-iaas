package datasources

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
	"github.com/hypervisor-io/terraform-provider-iaas/internal/tfdiag"
)

// Interface assertions - kubernetes_version is a lookup-by-name catalog data
// source (the location/image findUnique pattern) backed by the FLAT Select2
// /kubernetes/search/versions endpoint.
var (
	_ datasource.DataSource              = &kubernetesVersionDataSource{}
	_ datasource.DataSourceWithConfigure = &kubernetesVersionDataSource{}
)

// NewKubernetesVersionDataSource is the constructor registered with the provider.
func NewKubernetesVersionDataSource() datasource.DataSource {
	return &kubernetesVersionDataSource{}
}

// kubernetesVersionDataSource resolves the UUID of an active Kubernetes version
// by its semantic version (e.g. "1.31.4"), for use as kubernetes_version_id when
// creating a cluster. The catalog endpoint returns a FLAT Select2 list whose
// text == semantic_version; this data source matches that exactly.
type kubernetesVersionDataSource struct {
	client *client.Client
}

// kubernetesVersionModel maps the data-source state. name is the input filter
// (the semantic version); id is the computed output.
type kubernetesVersionModel struct {
	Name types.String `tfsdk:"name"`
	ID   types.String `tfsdk:"id"`
}

func (d *kubernetesVersionDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_version"
}

func (d *kubernetesVersionDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Looks up an active Kubernetes version by its semantic version (e.g. " +
			"`1.31.4`) from the cluster-create catalog, resolving the `id` you pass as " +
			"`kubernetes_version_id` on an `iaas_kubernetes_cluster`. Only versions in the " +
			"`active` state are visible. Exactly one version must match.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Required: true,
				Description: "Semantic version to look up (e.g. `1.31.4`). Matched exactly against " +
					"the catalog's version list. Exactly one version must match.",
			},
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the matched Kubernetes version (use as `kubernetes_version_id`).",
			},
		},
	}
}

func (d *kubernetesVersionDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	c, problem := configureClient(req.ProviderData)
	if problem != "" {
		resp.Diagnostics.AddError("Unexpected Provider Data Type", problem)
		return
	}
	d.client = c
}

// Read searches the version catalog and resolves a single version by exact
// semantic-version (text) match.
func (d *kubernetesVersionDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg kubernetesVersionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	name := cfg.Name.ValueString()

	versions, err := d.client.SearchK8sVersions(ctx, name)
	if err != nil {
		resp.Diagnostics.Append(tfdiag.FromErr("Error searching Kubernetes versions", err))
		return
	}

	// text == semantic_version; either field resolves the exact match.
	match, err := findUnique(versions, "kubernetes version", name, func(v map[string]any) bool {
		return strField(v, "text") == name || strField(v, "semantic_version") == name
	})
	if err != nil {
		resp.Diagnostics.AddError("Kubernetes version lookup failed", err.Error())
		return
	}

	cfg.ID = types.StringValue(strField(match, "id"))
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}
