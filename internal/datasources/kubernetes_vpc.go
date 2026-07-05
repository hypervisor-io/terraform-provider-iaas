package datasources

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hypervisor-io/terraform-provider-iaas/internal/client"
	"github.com/hypervisor-io/terraform-provider-iaas/internal/tfdiag"
)

var (
	_ datasource.DataSource              = &kubernetesVPCDataSource{}
	_ datasource.DataSourceWithConfigure = &kubernetesVPCDataSource{}
)

// NewKubernetesVPCDataSource is the constructor registered with the provider.
func NewKubernetesVPCDataSource() datasource.DataSource {
	return &kubernetesVPCDataSource{}
}

// kubernetesVPCDataSource resolves a VPC eligible to host a Kubernetes cluster by
// name, returning its id (use as vpc_id on iaas_kubernetes_cluster) plus cidr,
// region and NAT-gateway status. Only VPCs owned by the token's account are
// searched. An optional region filter disambiguates same-named VPCs across
// regions.
type kubernetesVPCDataSource struct {
	client *client.Client
}

type kubernetesVPCModel struct {
	Name              types.String `tfsdk:"name"`
	HypervisorGroupID types.String `tfsdk:"hypervisor_group_id"`
	ID                types.String `tfsdk:"id"`
	CIDR              types.String `tfsdk:"cidr"`
	HasNATGateway     types.Bool   `tfsdk:"has_nat_gateway"`
	NATPublicIP       types.String `tfsdk:"nat_public_ip"`
}

func (d *kubernetesVPCDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_vpc"
}

func (d *kubernetesVPCDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Looks up a VPC that can host a Kubernetes cluster by name, resolving the `id` " +
			"you pass as `vpc_id` on an `iaas_kubernetes_cluster`. Only VPCs owned by your account " +
			"are searched. `has_nat_gateway` reports whether the VPC has NAT egress - a private " +
			"control plane needs it for kubeadm image pulls. Exactly one VPC must match.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Exact VPC name to look up. Exactly one VPC must match.",
			},
			"hypervisor_group_id": schema.StringAttribute{
				Optional: true,
				Description: "Optional region (hypervisor group) UUID to constrain the search - use to " +
					"disambiguate identically-named VPCs across regions.",
			},
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the matched VPC (use as `vpc_id`).",
			},
			"cidr": schema.StringAttribute{
				Computed:    true,
				Description: "CIDR block of the matched VPC.",
			},
			"has_nat_gateway": schema.BoolAttribute{
				Computed:    true,
				Description: "Whether the VPC has an active NAT gateway providing egress.",
			},
			"nat_public_ip": schema.StringAttribute{
				Computed:    true,
				Description: "Public IP of the VPC's NAT gateway, if any.",
			},
		},
	}
}

func (d *kubernetesVPCDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	c, problem := configureClient(req.ProviderData)
	if problem != "" {
		resp.Diagnostics.AddError("Unexpected Provider Data Type", problem)
		return
	}
	d.client = c
}

func (d *kubernetesVPCDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg kubernetesVPCModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	name := cfg.Name.ValueString()

	vpcs, err := d.client.SearchK8sVpcs(ctx, cfg.HypervisorGroupID.ValueString(), name)
	if err != nil {
		resp.Diagnostics.Append(tfdiag.FromErr("Error searching Kubernetes VPCs", err))
		return
	}

	match, err := findUnique(vpcs, "kubernetes vpc", name, func(v map[string]any) bool {
		return strField(v, "name") == name
	})
	if err != nil {
		resp.Diagnostics.AddError("Kubernetes VPC lookup failed", err.Error())
		return
	}

	cfg.ID = types.StringValue(strField(match, "id"))
	cfg.CIDR = types.StringValue(strField(match, "cidr"))
	cfg.HasNATGateway = types.BoolValue(boolField(match, "has_nat_gateway"))
	cfg.NATPublicIP = types.StringValue(strField(match, "nat_public_ip"))

	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}
