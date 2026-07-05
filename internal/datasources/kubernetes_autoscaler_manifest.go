package datasources

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
	"github.com/hypervisor-io/terraform-provider-iaas/internal/tfdiag"
)

// Interface assertions - kubernetes_autoscaler_manifest is a DIRECT FETCH by
// cluster id, like kubernetes_kubeconfig.
var (
	_ datasource.DataSource              = &kubernetesAutoscalerManifestDataSource{}
	_ datasource.DataSourceWithConfigure = &kubernetesAutoscalerManifestDataSource{}
)

// NewKubernetesAutoscalerManifestDataSource is the constructor registered with
// the provider.
func NewKubernetesAutoscalerManifestDataSource() datasource.DataSource {
	return &kubernetesAutoscalerManifestDataSource{}
}

// kubernetesAutoscalerManifestDataSource renders the cluster-autoscaler manifest
// the user applies to their cluster with `kubectl apply -f -`. The manifest
// embeds a freshly-minted controller JWT (base64) inline as a Kubernetes Secret,
// so it carries a live bearer credential and is returned as a sensitive string.
// Re-reading ROTATES the active token.
type kubernetesAutoscalerManifestDataSource struct {
	client *client.Client
}

// kubernetesAutoscalerManifestModel maps the data-source state. cluster_id is the
// required input; manifest is the sensitive computed output.
type kubernetesAutoscalerManifestModel struct {
	ClusterID types.String `tfsdk:"cluster_id"`
	Manifest  types.String `tfsdk:"manifest"`
}

func (d *kubernetesAutoscalerManifestDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_autoscaler_manifest"
}

func (d *kubernetesAutoscalerManifestDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Renders the self-contained cluster-autoscaler manifest for a managed " +
			"Kubernetes cluster - apply it with `kubectl apply -f -`. The manifest embeds a " +
			"freshly-minted controller JWT base64-encoded inline as a Kubernetes Secret, so " +
			"the `manifest` output is marked sensitive; every read ROTATES the active token " +
			"(the running autoscaler picks the new token up on its next reload). The cluster " +
			"must be in the `running` state with worker autoscaling enabled, otherwise the " +
			"read errors.",
		Attributes: map[string]schema.Attribute{
			"cluster_id": schema.StringAttribute{
				Required:    true,
				Description: "UUID of the Kubernetes cluster whose autoscaler manifest to render.",
			},
			"manifest": schema.StringAttribute{
				Computed:  true,
				Sensitive: true,
				Description: "The rendered cluster-autoscaler manifest YAML. Sensitive: it embeds " +
					"a freshly-minted controller JWT (a live bearer credential) inline as a " +
					"Secret. Re-reading rotates the active token.",
			},
		},
	}
}

// Configure pulls the shared *client.Client from the provider (nil-guard +
// typed-mismatch error), identically to resources.
func (d *kubernetesAutoscalerManifestDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	c, problem := configureClient(req.ProviderData)
	if problem != "" {
		resp.Diagnostics.AddError("Unexpected Provider Data Type", problem)
		return
	}
	d.client = c
}

// Read renders the cluster's autoscaler manifest and stores it (sensitive).
func (d *kubernetesAutoscalerManifestDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg kubernetesAutoscalerManifestModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	manifest, err := d.client.GetAutoscalerManifest(ctx, cfg.ClusterID.ValueString())
	if err != nil {
		resp.Diagnostics.Append(tfdiag.FromErr("Error rendering autoscaler manifest", err))
		return
	}

	cfg.Manifest = types.StringValue(manifest)
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}
