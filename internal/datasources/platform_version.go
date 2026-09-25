package datasources

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
	"github.com/hypervisor-io/terraform-provider-iaas/internal/tfdiag"
)

// Interface assertions - platform_version is a SINGLETON data source, like
// account: there is exactly one master behind the configured provider token,
// not a catalog to search by name, so it has no input filter attribute.
var (
	_ datasource.DataSource              = &platformVersionDataSource{}
	_ datasource.DataSourceWithConfigure = &platformVersionDataSource{}
)

// NewPlatformVersionDataSource is the constructor registered with the provider.
func NewPlatformVersionDataSource() datasource.DataSource {
	return &platformVersionDataSource{}
}

// platformVersionDataSource resolves the master's own version, the API
// envelope version, and the minimum agent version the master currently
// requires (NUI-V-R20-VER1). Read calls GET /version (UserApi\VersionController).
type platformVersionDataSource struct {
	client *client.Client
}

type platformVersionModel struct {
	ID              types.String `tfsdk:"id"`
	Version         types.String `tfsdk:"version"`
	ApiEnvelope     types.String `tfsdk:"api_envelope"`
	MinAgentVersion types.String `tfsdk:"min_agent_version"`
}

func (d *platformVersionDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_platform_version"
}

func (d *platformVersionDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Returns the master's own platform version (SINGLETON - no input filter, " +
			"exactly one master behind the configured provider token). Use " +
			"`data.iaas_platform_version.current.min_agent_version` to gate agent-version-" +
			"dependent features (e.g. rescue mode) without hardcoding a version string in " +
			"configuration - it is null while the master enforces no agent-version floor.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "Same value as `version` (framework requires a stable id attribute).",
			},
			"version": schema.StringAttribute{
				Computed:    true,
				Description: "Master application version, e.g. `3.2.3.3`.",
			},
			"api_envelope": schema.StringAttribute{
				Computed:    true,
				Description: "Admin API response-envelope version (`v1`).",
			},
			"min_agent_version": schema.StringAttribute{
				Computed: true,
				Description: "Minimum hypervisor agent version the master currently requires, " +
					"or null when nothing enforces a floor yet.",
			},
		},
	}
}

// Configure pulls the shared *client.Client from the provider (nil-guard +
// typed-mismatch error), identically to every other data source.
func (d *platformVersionDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	c, problem := configureClient(req.ProviderData)
	if problem != "" {
		resp.Diagnostics.AddError("Unexpected Provider Data Type", problem)
		return
	}
	d.client = c
}

// Read calls GET /version and maps the returned object onto the singleton
// data-source state. There is no config input to read (no filter attribute
// exists).
func (d *platformVersionDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	payload, err := d.client.GetPlatformVersion(ctx)
	if err != nil {
		resp.Diagnostics.Append(tfdiag.FromErr("Error reading platform version", err))
		return
	}

	version := strField(payload, "version")

	state := platformVersionModel{
		ID:              types.StringValue(version),
		Version:         types.StringValue(version),
		ApiEnvelope:     types.StringValue(strField(payload, "api_envelope")),
		MinAgentVersion: nullableString(payload["min_agent_version"]),
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
