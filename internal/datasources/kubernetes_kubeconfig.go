package datasources

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/iaas/terraform-provider-iaas/internal/client"
	"github.com/iaas/terraform-provider-iaas/internal/tfdiag"
)

// Interface assertions — kubernetes_kubeconfig is a DIRECT FETCH by cluster id
// (like vpn_peer_config), not a list-and-match: it downloads a freshly-minted
// admin kubeconfig for a single cluster.
var (
	_ datasource.DataSource              = &kubernetesKubeconfigDataSource{}
	_ datasource.DataSourceWithConfigure = &kubernetesKubeconfigDataSource{}
)

// NewKubernetesKubeconfigDataSource is the constructor registered with the
// provider.
func NewKubernetesKubeconfigDataSource() datasource.DataSource {
	return &kubernetesKubeconfigDataSource{}
}

// kubernetesKubeconfigDataSource downloads the admin kubeconfig YAML for a
// managed Kubernetes cluster. The endpoint mints a FRESH cluster-admin client
// certificate per call and embeds it inline (nothing is persisted server-side),
// so the rendered config carries live admin credentials and is returned as a
// sensitive string.
type kubernetesKubeconfigDataSource struct {
	client *client.Client
}

// kubernetesKubeconfigModel maps the data-source state. cluster_id is the
// required input; kubeconfig is the sensitive computed output.
type kubernetesKubeconfigModel struct {
	ClusterID  types.String `tfsdk:"cluster_id"`
	Kubeconfig types.String `tfsdk:"kubeconfig"`
}

func (d *kubernetesKubeconfigDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_kubeconfig"
}

func (d *kubernetesKubeconfigDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Downloads the admin kubeconfig for a managed Kubernetes cluster. The " +
			"server mints a FRESH cluster-admin client certificate on every read and embeds " +
			"it inline — nothing is persisted, and each read issues an independent " +
			"credential — so the `kubeconfig` output is marked sensitive. The cluster must " +
			"have finished bootstrapping (reached the `running` state) before a kubeconfig is " +
			"available; reading too early errors. Write the result to a file with " +
			"`local_sensitive_file` or feed it to the Kubernetes/Helm providers.",
		Attributes: map[string]schema.Attribute{
			"cluster_id": schema.StringAttribute{
				Required:    true,
				Description: "UUID of the Kubernetes cluster whose admin kubeconfig to download.",
			},
			"kubeconfig": schema.StringAttribute{
				Computed:  true,
				Sensitive: true,
				Description: "The rendered kubeconfig YAML. Sensitive: it embeds a freshly-minted " +
					"cluster-admin client certificate and key granting full cluster access.",
			},
		},
	}
}

// Configure pulls the shared *client.Client from the provider (nil-guard +
// typed-mismatch error), identically to resources.
func (d *kubernetesKubeconfigDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	c, problem := configureClient(req.ProviderData)
	if problem != "" {
		resp.Diagnostics.AddError("Unexpected Provider Data Type", problem)
		return
	}
	d.client = c
}

// Read downloads the cluster's admin kubeconfig and stores it (sensitive).
func (d *kubernetesKubeconfigDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg kubernetesKubeconfigModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	kubeconfig, err := d.client.GetKubeconfig(ctx, cfg.ClusterID.ValueString())
	if err != nil {
		resp.Diagnostics.Append(tfdiag.FromErr("Error downloading kubeconfig", err))
		return
	}

	cfg.Kubeconfig = types.StringValue(kubeconfig)
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}
