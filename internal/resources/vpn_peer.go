package resources

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hypervisor-io/terraform-provider-iaas/internal/client"
)

// Interface assertions. iaas_vpn_peer is a CHILD resource of a VPN gateway. It
// follows the lb_backend read-by-scan child pattern:
//   - the parent vpn_gateway_id lives in the URL path (Required + RequiresReplace),
//   - there is NO individual peer SHOW route, so Read scans the gateway SHOW's
//     embedded peers[] array (client.GetVpnPeer),
//   - import takes a COMPOSITE "<gateway_id>/<peer_id>".
//
// Peer writes are SYNCHRONOUS (the service mutates the row and pushes the
// regenerated WireGuard config to the gateway VM in-line), so there is NO waiter.
// The peer's pre-shared key is encrypted + $hidden server-side (never returned by
// SHOW) → it is Sensitive + write-only (echoed from config, preserved in Read).
var (
	_ resource.Resource                = &vpnPeerResource{}
	_ resource.ResourceWithConfigure   = &vpnPeerResource{}
	_ resource.ResourceWithImportState = &vpnPeerResource{}
)

// NewVPNPeerResource is the resource constructor registered with the provider.
func NewVPNPeerResource() resource.Resource {
	return &vpnPeerResource{}
}

// vpnPeerResource manages an iaas_vpn_peer - a WireGuard peer of a VPN gateway.
type vpnPeerResource struct {
	client *client.Client
}

// vpnPeerModel maps the Terraform state/plan for iaas_vpn_peer.
//
// Mutability (from the service's updatePeer, which accepts name/public_key/
// endpoint/allowed_ips/preshared_key/keepalive/enabled):
//   - In-place updatable: name, public_key, endpoint, allowed_ips, preshared_key,
//     keepalive, enabled.
//   - Create-only (RequiresReplace - NOT accepted by updatePeer): type, tunnel_ip,
//     dns.
//   - Path id (RequiresReplace): vpn_gateway_id.
type vpnPeerModel struct {
	ID           types.String `tfsdk:"id"`
	VPNGatewayID types.String `tfsdk:"vpn_gateway_id"`
	Type         types.String `tfsdk:"type"`
	Name         types.String `tfsdk:"name"`
	PublicKey    types.String `tfsdk:"public_key"`
	Endpoint     types.String `tfsdk:"endpoint"`
	TunnelIP     types.String `tfsdk:"tunnel_ip"`
	AllowedIPs   types.Set    `tfsdk:"allowed_ips"`
	DNS          types.String `tfsdk:"dns"`
	Keepalive    types.Int64  `tfsdk:"keepalive"`
	Enabled      types.Bool   `tfsdk:"enabled"`
	PresharedKey types.String `tfsdk:"preshared_key"`
}

// Metadata sets the resource type name → "iaas_vpn_peer".
func (r *vpnPeerResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vpn_peer"
}

// Schema describes the iaas_vpn_peer resource.
func (r *vpnPeerResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a WireGuard peer of a VPN gateway. A peer is a child of an " +
			"iaas_vpn_gateway: its parent vpn_gateway_id is part of the API path, so changing " +
			"it forces a new resource. A peer represents a remote client (road_warrior) or a " +
			"remote site (site_to_site) allowed to connect through the gateway. The peer's " +
			"public_key, endpoint, allowed_ips, keepalive, preshared_key and enabled flag can " +
			"be changed in place; the type, tunnel_ip and dns are fixed at creation. For a " +
			"road_warrior peer, the downloadable client configuration is available via the " +
			"iaas_vpn_peer_config data source. Import with a composite id: " +
			"\"<gateway_id>/<peer_id>\".",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the peer, assigned by the API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"vpn_gateway_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the parent VPN gateway this peer belongs to. This value is part " +
					"of the API request path, so changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"type": schema.StringAttribute{
				Required: true,
				Description: "Peer type: \"road_warrior\" (a remote client device - its client config " +
					"can be downloaded via iaas_vpn_peer_config) or \"site_to_site\" (a remote network). " +
					"Fixed at creation; changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"name": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Display name for the peer. Updatable in place.",
			},
			"public_key": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "The peer's WireGuard PUBLIC key. For a road_warrior client, this is the " +
					"client device's public key; for a site_to_site link it is the remote endpoint's " +
					"public key. Updatable in place. (This is a public key, not a secret.)",
			},
			"endpoint": schema.StringAttribute{
				Optional: true,
				Description: "Remote endpoint address (host:port) for a site_to_site peer the gateway " +
					"should dial out to, e.g. \"203.0.113.1:51820\". Omitted for road_warrior peers " +
					"(the client dials in). Updatable in place.",
			},
			"tunnel_ip": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "The peer's IP inside the gateway's tunnel subnet. Auto-allocated from the " +
					"gateway's tunnel_subnet when omitted. Fixed at creation; changing it forces a new " +
					"resource.",
				PlanModifiers: []planmodifier.String{
					// Stable after create + RequiresReplace: UseStateForUnknown keeps
					// the server-allocated value across plans when the user omits it,
					// so an unset tunnel_ip does NOT re-plan as unknown and spuriously
					// force replacement.
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"allowed_ips": schema.SetAttribute{
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
				Description: "The CIDRs routed to this peer through the tunnel (WireGuard AllowedIPs), " +
					"as an order-independent set. Defaults to the peer's tunnel_ip/32 when omitted. " +
					"Updatable in place.",
			},
			"dns": schema.StringAttribute{
				Optional: true,
				Description: "DNS server to advertise to a road_warrior client in its generated config. " +
					"Fixed at creation; changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"keepalive": schema.Int64Attribute{
				Optional: true,
				Computed: true,
				Description: "WireGuard PersistentKeepalive interval in seconds (0-65535). Defaults to " +
					"25. Updatable in place.",
			},
			"enabled": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Description: "Whether the peer is enabled (included in the gateway's WireGuard config). " +
					"Defaults to true. Updatable in place.",
			},
			// preshared_key is encrypted + $hidden server-side: it is NEVER returned
			// by the gateway SHOW, so it is Sensitive + WRITE-ONLY. It is echoed from
			// configuration on Create/Update and preserved verbatim in Read (never
			// overwritten from the API). It IS updatable in place (updatePeer accepts
			// it), so it is NOT RequiresReplace; add it to ImportStateVerifyIgnore in
			// tests (it cannot be recovered on import).
			"preshared_key": schema.StringAttribute{
				Optional:  true,
				Sensitive: true,
				Description: "Optional WireGuard pre-shared key (an extra symmetric secret layered on " +
					"top of the public-key crypto). Write-only and sensitive: it is stored encrypted " +
					"server-side and never returned, so it is preserved from configuration and cannot " +
					"be recovered on import. Updatable in place.",
			},
		},
	}
}

// Configure pulls the shared *client.Client from the provider (nil-guarded).
func (r *vpnPeerResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Provider Data Type",
			fmt.Sprintf("Expected *client.Client, got: %T. This is a provider bug; please report it.", req.ProviderData),
		)
		return
	}
	r.client = c
}

// Create adds the peer to its parent gateway. The create is synchronous; the
// response carries the new peer object with its id (and the server-allocated
// tunnel_ip / default allowed_ips). We then read-back by scanning the gateway
// SHOW so state reflects those server-applied values; the write-only preshared_key
// is echoed from the plan (the SHOW never returns it).
func (r *vpnPeerResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan vpnPeerModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := map[string]any{
		"type": plan.Type.ValueString(),
	}
	if !plan.Name.IsNull() && !plan.Name.IsUnknown() {
		body["name"] = plan.Name.ValueString()
	}
	if !plan.PublicKey.IsNull() && !plan.PublicKey.IsUnknown() {
		body["public_key"] = plan.PublicKey.ValueString()
	}
	if !plan.Endpoint.IsNull() && !plan.Endpoint.IsUnknown() {
		body["endpoint"] = plan.Endpoint.ValueString()
	}
	if !plan.TunnelIP.IsNull() && !plan.TunnelIP.IsUnknown() {
		body["tunnel_ip"] = plan.TunnelIP.ValueString()
	}
	if !plan.AllowedIPs.IsNull() && !plan.AllowedIPs.IsUnknown() {
		ips, d := stringsFromSet(ctx, plan.AllowedIPs)
		resp.Diagnostics.Append(d...)
		if resp.Diagnostics.HasError() {
			return
		}
		body["allowed_ips"] = ips
	}
	if !plan.DNS.IsNull() && !plan.DNS.IsUnknown() {
		body["dns"] = plan.DNS.ValueString()
	}
	if !plan.Keepalive.IsNull() && !plan.Keepalive.IsUnknown() {
		body["keepalive"] = plan.Keepalive.ValueInt64()
	}
	if !plan.Enabled.IsNull() && !plan.Enabled.IsUnknown() {
		body["enabled"] = plan.Enabled.ValueBool()
	}
	if !plan.PresharedKey.IsNull() && !plan.PresharedKey.IsUnknown() {
		body["preshared_key"] = plan.PresharedKey.ValueString()
	}

	gwID := plan.VPNGatewayID.ValueString()
	created, err := r.client.AddVpnPeer(ctx, gwID, body)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating VPN peer", err))
		return
	}
	id, _ := created["id"].(string)
	if id == "" {
		resp.Diagnostics.AddError("Error creating VPN peer", "the create response did not include a peer id")
		return
	}

	// Read-back by scanning the gateway SHOW so server-applied values (tunnel_ip,
	// default allowed_ips, defaults) are reflected. Fall back to the create
	// response if the scan can't find it yet (defensive - the write is synchronous).
	obj, err := r.client.GetVpnPeer(ctx, gwID, id)
	if err != nil {
		obj = created
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, vpnPeerStateFromAPI(obj, plan))...)
}

// Read refreshes state by scanning the parent gateway SHOW's embedded peers[]. A
// 404 (peer or its gateway gone) removes the resource from state. The write-only
// preshared_key is preserved from prior state (the SHOW never returns it).
func (r *vpnPeerResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state vpnPeerModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	obj, err := r.client.GetVpnPeer(ctx, state.VPNGatewayID.ValueString(), state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("Error reading VPN peer", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, vpnPeerStateFromAPI(obj, state))...)
}

// Update patches the mutable peer fields (name/public_key/endpoint/allowed_ips/
// preshared_key/keepalive/enabled). type/tunnel_ip/dns are RequiresReplace, so
// they never reach here. The write-only preshared_key is sent when set and echoed
// from the plan; we read-back by scan for a consistent view of the rest.
func (r *vpnPeerResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan vpnPeerModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := map[string]any{
		"name":       plan.Name.ValueString(),
		"public_key": plan.PublicKey.ValueString(),
		"endpoint":   plan.Endpoint.ValueString(),
		"keepalive":  plan.Keepalive.ValueInt64(),
		"enabled":    plan.Enabled.ValueBool(),
	}
	if !plan.AllowedIPs.IsNull() && !plan.AllowedIPs.IsUnknown() {
		ips, d := stringsFromSet(ctx, plan.AllowedIPs)
		resp.Diagnostics.Append(d...)
		if resp.Diagnostics.HasError() {
			return
		}
		body["allowed_ips"] = ips
	}
	if !plan.PresharedKey.IsNull() && !plan.PresharedKey.IsUnknown() {
		body["preshared_key"] = plan.PresharedKey.ValueString()
	}

	gwID := plan.VPNGatewayID.ValueString()
	if _, err := r.client.UpdateVpnPeer(ctx, gwID, plan.ID.ValueString(), body); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error updating VPN peer", err))
		return
	}

	obj, err := r.client.GetVpnPeer(ctx, gwID, plan.ID.ValueString())
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading VPN peer after update", err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, vpnPeerStateFromAPI(obj, plan))...)
}

// Delete removes the peer from its parent gateway.
func (r *vpnPeerResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state vpnPeerModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.RemoveVpnPeer(ctx, state.VPNGatewayID.ValueString(), state.ID.ValueString()); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deleting VPN peer", err))
		return
	}
}

// ImportState implements COMPOSITE import: "<gateway_id>/<peer_id>". The peer's
// write-only preshared_key cannot be recovered and must be supplied in config (it
// is in the lifecycle test's ImportStateVerifyIgnore).
func (r *vpnPeerResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	gwID, peerID, ok := strings.Cut(req.ID, "/")
	if !ok || gwID == "" || peerID == "" {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Expected import ID in the format \"gateway_id/peer_id\", got: %q. "+
				"VPN peers are child resources, so both the parent gateway id and the "+
				"peer id are required to import.", req.ID),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("vpn_gateway_id"), gwID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), peerID)...)
}

// vpnPeerStateFromAPI builds the model from an embedded peer object, falling back
// to the prior model's value for fields the response omits. vpn_gateway_id is
// authoritative from the path. preshared_key is write-only (never in SHOW) → it is
// always preserved from prior.
func vpnPeerStateFromAPI(obj map[string]any, prior vpnPeerModel) vpnPeerModel {
	m := vpnPeerModel{
		ID:           stringFromAPI(obj, "id", prior.ID),
		VPNGatewayID: prior.VPNGatewayID, // from the path
		Type:         stringFromAPI(obj, "type", prior.Type),
		Name:         stringFromAPI(obj, "name", prior.Name),
		PublicKey:    stringFromAPI(obj, "public_key", prior.PublicKey),
		Endpoint:     optionalStringFromAPI(obj, "endpoint", prior.Endpoint),
		TunnelIP:     stringFromAPI(obj, "tunnel_ip", prior.TunnelIP),
		DNS:          optionalStringFromAPI(obj, "dns", prior.DNS),
		Keepalive:    int64FromAPI(obj, "keepalive", prior.Keepalive),
		Enabled:      boolFromIntAPI(obj, "enabled", prior.Enabled),
		PresharedKey: prior.PresharedKey, // write-only; never in the response
	}
	m.AllowedIPs = vpnPeerAllowedIPsFromAPI(obj["allowed_ips"], prior.AllowedIPs)
	return m
}

// vpnPeerAllowedIPsFromAPI converts the embedded "allowed_ips" JSON array of CIDR
// strings into a types.Set. An absent/empty array falls back to the prior set
// (Computed, so it should always be present once the server allocates the
// default tunnel_ip/32).
func vpnPeerAllowedIPsFromAPI(raw any, prior types.Set) types.Set {
	arr, ok := raw.([]any)
	if !ok {
		return prior
	}
	elems := make([]attr.Value, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok && s != "" {
			elems = append(elems, types.StringValue(s))
		}
	}
	if len(elems) == 0 {
		return prior
	}
	return mustSetValue(types.StringType, elems)
}
