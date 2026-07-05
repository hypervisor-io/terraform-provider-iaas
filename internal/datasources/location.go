package datasources

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hypervisor-io/terraform-provider-iaas/internal/client"
	"github.com/hypervisor-io/terraform-provider-iaas/internal/tfdiag"
)

// Interface assertions - location is the golden data source and implements the
// Configure-capable data-source contract every later data source copies.
var (
	_ datasource.DataSource              = &locationDataSource{}
	_ datasource.DataSourceWithConfigure = &locationDataSource{}
)

// NewLocationDataSource is the constructor registered with the provider.
func NewLocationDataSource() datasource.DataSource {
	return &locationDataSource{}
}

// locationDataSource looks up a cloud-service location (hypervisor group) by
// name. Locations are the deploy targets every instance-essential data source
// is ultimately scoped to.
type locationDataSource struct {
	client *client.Client
}

// locationModel maps the data-source state. name is the input filter; the rest
// are computed outputs resolved from the matched location.
type locationModel struct {
	Name        types.String `tfsdk:"name"`
	ID          types.String `tfsdk:"id"`
	DisplayName types.String `tfsdk:"display_name"`
	Country     types.String `tfsdk:"country"`
}

func (d *locationDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_location"
}

func (d *locationDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Looks up a cloud-service location (a hypervisor group, i.e. a deploy " +
			"target) by name. Matches either the location slug (`name`) or its human " +
			"`display_name`. Exactly one location must match.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Required: true,
				Description: "The location to look up. Matches the location slug (e.g. " +
					"`nyc`) or its display name (e.g. `New York`). Exactly one location " +
					"must match or the data source errors.",
			},
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the matched location (hypervisor group).",
			},
			"display_name": schema.StringAttribute{
				Computed:    true,
				Description: "Human-readable name of the location.",
			},
			"country": schema.StringAttribute{
				Computed:    true,
				Description: "Country code of the location.",
			},
		},
	}
}

// Configure pulls the shared *client.Client from the provider (nil-guard +
// typed-mismatch error), identically to resources.
func (d *locationDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	c, problem := configureClient(req.ProviderData)
	if problem != "" {
		resp.Diagnostics.AddError("Unexpected Provider Data Type", problem)
		return
	}
	d.client = c
}

// Read lists all locations, finds the unique match on slug or display_name, and
// populates the computed attributes.
func (d *locationDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg locationModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	name := cfg.Name.ValueString()

	locations, err := d.client.ListLocations(ctx)
	if err != nil {
		resp.Diagnostics.Append(tfdiag.FromErr("Error reading locations", err))
		return
	}

	match, err := findUnique(locations, "location", name, func(loc map[string]any) bool {
		return strField(loc, "name") == name || strField(loc, "display_name") == name
	})
	if err != nil {
		resp.Diagnostics.AddError("Location lookup failed", err.Error())
		return
	}

	cfg.ID = types.StringValue(strField(match, "id"))
	cfg.DisplayName = types.StringValue(strField(match, "display_name"))
	cfg.Country = types.StringValue(strField(match, "country"))

	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}
