package datasources

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
	"github.com/hypervisor-io/terraform-provider-iaas/internal/tfdiag"
)

var (
	_ datasource.DataSource              = &isoDataSource{}
	_ datasource.DataSourceWithConfigure = &isoDataSource{}
)

// NewISODataSource is the constructor registered with the provider.
func NewISODataSource() datasource.DataSource {
	return &isoDataSource{}
}

// isoDataSource looks up a mountable ISO by exact name. The ISO-search endpoint
// returns a paginator; the search query narrows the page, and this data source
// then matches by exact name to resolve a single ISO.
type isoDataSource struct {
	client *client.Client
}

// isoModel maps the data-source state. name is the input filter; id/filename are
// computed outputs.
type isoModel struct {
	Name     types.String `tfsdk:"name"`
	ID       types.String `tfsdk:"id"`
	Filename types.String `tfsdk:"filename"`
}

func (d *isoDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_iso"
}

func (d *isoDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Looks up a mountable ISO by exact name. Exactly one ISO must match.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Exact name of the ISO to look up. Exactly one ISO must match.",
			},
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the matched ISO.",
			},
			"filename": schema.StringAttribute{
				Computed:    true,
				Description: "Stored filename of the matched ISO.",
			},
		},
	}
}

func (d *isoDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	c, problem := configureClient(req.ProviderData)
	if problem != "" {
		resp.Diagnostics.AddError("Unexpected Provider Data Type", problem)
		return
	}
	d.client = c
}

// Read searches ISOs by name and resolves the unique exact-name match.
func (d *isoDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg isoModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	name := cfg.Name.ValueString()

	isos, err := d.client.ListISOs(ctx, name)
	if err != nil {
		resp.Diagnostics.Append(tfdiag.FromErr("Error searching ISOs", err))
		return
	}

	match, err := findUnique(isos, "iso", name, func(iso map[string]any) bool {
		return strField(iso, "name") == name
	})
	if err != nil {
		resp.Diagnostics.AddError("ISO lookup failed", err.Error())
		return
	}

	cfg.ID = types.StringValue(strField(match, "id"))
	cfg.Filename = types.StringValue(strField(match, "filename"))

	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}
