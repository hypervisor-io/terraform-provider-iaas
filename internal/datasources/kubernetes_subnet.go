package datasources

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hypervisor-io/terraform-provider-iaas/internal/client"
	"github.com/hypervisor-io/terraform-provider-iaas/internal/tfdiag"
)

var (
	_ datasource.DataSource              = &kubernetesSubnetDataSource{}
	_ datasource.DataSourceWithConfigure = &kubernetesSubnetDataSource{}
)

// NewKubernetesSubnetDataSource is the constructor registered with the provider.
func NewKubernetesSubnetDataSource() datasource.DataSource {
	return &kubernetesSubnetDataSource{}
}

// kubernetesSubnetDataSource resolves a subnet within a VPC by name, returning
// its id (use as subnet_id on iaas_kubernetes_cluster / node pool) plus cidr and
// type. The parent vpc_id is required; the owner check is enforced server-side
// via the parent VPC.
type kubernetesSubnetDataSource struct {
	client *client.Client
}

type kubernetesSubnetModel struct {
	VPCID types.String `tfsdk:"vpc_id"`
	Name  types.String `tfsdk:"name"`
	Type  types.String `tfsdk:"type"`
	ID    types.String `tfsdk:"id"`
	CIDR  types.String `tfsdk:"cidr"`
}

func (d *kubernetesSubnetDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_subnet"
}

func (d *kubernetesSubnetDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Looks up a subnet within a VPC by name, resolving the `id` you pass as " +
			"`subnet_id` on an `iaas_kubernetes_cluster` or node pool. Requires the parent " +
			"`vpc_id`; the owner check is enforced against that VPC. Optionally filter by subnet " +
			"`type`. Exactly one subnet must match.",
		Attributes: map[string]schema.Attribute{
			"vpc_id": schema.StringAttribute{
				Required:    true,
				Description: "UUID of the parent VPC to search within.",
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Exact subnet name to look up. Exactly one subnet must match.",
			},
			"type": schema.StringAttribute{
				Optional: true,
				Description: "Optional subnet type filter - `private` (control-plane subnets) or " +
					"`public`. Omit to search all types.",
				Validators: []validator.String{
					stringvalidator.OneOf("private", "public"),
				},
			},
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the matched subnet (use as `subnet_id`).",
			},
			"cidr": schema.StringAttribute{
				Computed:    true,
				Description: "CIDR block of the matched subnet.",
			},
		},
	}
}

func (d *kubernetesSubnetDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	c, problem := configureClient(req.ProviderData)
	if problem != "" {
		resp.Diagnostics.AddError("Unexpected Provider Data Type", problem)
		return
	}
	d.client = c
}

func (d *kubernetesSubnetDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg kubernetesSubnetModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	name := cfg.Name.ValueString()

	subnets, err := d.client.SearchK8sSubnets(ctx, cfg.VPCID.ValueString(), cfg.Type.ValueString(), name)
	if err != nil {
		resp.Diagnostics.Append(tfdiag.FromErr("Error searching Kubernetes subnets", err))
		return
	}

	match, err := findUnique(subnets, "kubernetes subnet", name, func(s map[string]any) bool {
		return strField(s, "name") == name
	})
	if err != nil {
		resp.Diagnostics.AddError("Kubernetes subnet lookup failed", err.Error())
		return
	}

	cfg.ID = types.StringValue(strField(match, "id"))
	cfg.CIDR = types.StringValue(strField(match, "cidr"))
	// Reflect the matched subnet's actual type (may be unset in config).
	cfg.Type = types.StringValue(strField(match, "type"))

	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}
