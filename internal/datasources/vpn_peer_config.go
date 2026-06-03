package datasources

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/iaas/terraform-provider-iaas/internal/client"
	"github.com/iaas/terraform-provider-iaas/internal/tfdiag"
)

// Interface assertions — vpn_peer_config follows the golden data-source contract
// (Configure-capable, Read-only) but, unlike location, it is a DIRECT FETCH by id
// rather than a list-and-match: it downloads the WireGuard client configuration
// for a single road_warrior peer.
var (
	_ datasource.DataSource              = &vpnPeerConfigDataSource{}
	_ datasource.DataSourceWithConfigure = &vpnPeerConfigDataSource{}
)

// NewVPNPeerConfigDataSource is the constructor registered with the provider.
func NewVPNPeerConfigDataSource() datasource.DataSource {
	return &vpnPeerConfigDataSource{}
}

// vpnPeerConfigDataSource renders the downloadable WireGuard client configuration
// for a road_warrior VPN peer. The gateway exposes this as a text/plain
// attachment; the data source returns it as a sensitive string so it can be
// written to a file or passed to a client provisioner.
type vpnPeerConfigDataSource struct {
	client *client.Client
}

// vpnPeerConfigModel maps the data-source state. gateway_id + peer_id are the
// required inputs; config is the sensitive computed output.
type vpnPeerConfigModel struct {
	GatewayID types.String `tfsdk:"gateway_id"`
	PeerID    types.String `tfsdk:"peer_id"`
	Config    types.String `tfsdk:"config"`
}

func (d *vpnPeerConfigDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vpn_peer_config"
}

func (d *vpnPeerConfigDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Downloads the WireGuard CLIENT configuration for a road_warrior VPN peer. " +
			"Returns the rendered `.conf` text (the [Interface]/[Peer] block a client uses to " +
			"connect to the gateway). The rendered config uses a `[YOUR_PRIVATE_KEY]` " +
			"placeholder for the client's own private key — the server never generates or " +
			"stores it — but the config still contains the gateway's public key and endpoint, " +
			"so the output is marked sensitive. Only works for peers of type `road_warrior`; " +
			"a site_to_site peer yields an error.",
		Attributes: map[string]schema.Attribute{
			"gateway_id": schema.StringAttribute{
				Required:    true,
				Description: "UUID of the VPN gateway the peer belongs to.",
			},
			"peer_id": schema.StringAttribute{
				Required:    true,
				Description: "UUID of the road_warrior peer whose client config to download.",
			},
			"config": schema.StringAttribute{
				Computed:  true,
				Sensitive: true,
				Description: "The rendered WireGuard client configuration text. Sensitive: it " +
					"contains the gateway's public key and endpoint. Write it to a `.conf` file " +
					"(remembering to substitute the client's real private key for the " +
					"`[YOUR_PRIVATE_KEY]` placeholder).",
			},
		},
	}
}

// Configure pulls the shared *client.Client from the provider (nil-guard +
// typed-mismatch error), identically to resources.
func (d *vpnPeerConfigDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	c, problem := configureClient(req.ProviderData)
	if problem != "" {
		resp.Diagnostics.AddError("Unexpected Provider Data Type", problem)
		return
	}
	d.client = c
}

// Read downloads the peer's WireGuard client config and stores it (sensitive).
func (d *vpnPeerConfigDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg vpnPeerConfigModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	conf, err := d.client.DownloadVpnPeerConfig(ctx, cfg.GatewayID.ValueString(), cfg.PeerID.ValueString())
	if err != nil {
		resp.Diagnostics.Append(tfdiag.FromErr("Error downloading VPN peer config", err))
		return
	}

	cfg.Config = types.StringValue(conf)
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}
