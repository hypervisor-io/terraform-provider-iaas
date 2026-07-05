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

	"github.com/hypervisor-io/terraform-provider-iaas/client"
)

// Interface assertions. iaas_vpn_peering is a CHILD resource of a VPN gateway
// (Wave C, gap-fill T8) that links it to ANOTHER iaas_vpn_gateway owned by the
// SAME account, in a DIFFERENT VPC - VpnGatewayController::createPeering /
// VpnGatewayService::createVpcPeering. It is DISTINCT from iaas_vpn_peer
// (vpn_peer.go): a peer's road_warrior/site_to_site flavours accept an
// arbitrary third-party public_key/endpoint/allowed_ips, whereas a peering only
// takes remote_gateway_id and the server derives everything else from the two
// gateway rows (a shared preshared_key, each side's public_key/endpoint, tunnel
// IPs, and allowed_ips = the remote VPC's CIDR + tunnel subnet). Both flavours
// persist to the SAME vpn_gateway_peers table - a peering is simply a peer row
// tagged type="vpc_peering" - so this resource reuses the parent gateway's
// embedded peers[] for Read (client.GetVpnPeering) and the generic peer-removal
// endpoint for Delete (client.DeleteVpnPeering), exactly like vpn_peer.go's
// read-by-scan / delete-by-scan shape.
//
//   - the parent vpn_gateway_id lives in the URL path (Required + RequiresReplace);
//   - the ONLY other input is remote_gateway_id (Required + RequiresReplace) -
//     there is no update route for a peering, so every input is immutable;
//   - there is NO individual peering SHOW/DELETE route - Read scans the
//     gateway SHOW's embedded peers[] (type == "vpc_peering") and Delete reuses
//     the generic DELETE .../peer/{peerId};
//   - the create is SYNCHRONOUS (no task/poll) - NO waiter;
//   - the preshared_key the server generates for the pair is $hidden +
//     encrypted, never returned by ANY response, and is not an accepted create
//     input either, so it is NOT modelled as an attribute at all;
//   - import takes a COMPOSITE id "<vpn_gateway_id>/<peering_id>".
var (
	_ resource.Resource                = &vpnPeeringResource{}
	_ resource.ResourceWithConfigure   = &vpnPeeringResource{}
	_ resource.ResourceWithImportState = &vpnPeeringResource{}
)

// NewVpnPeeringResource is the resource constructor registered with the provider.
func NewVpnPeeringResource() resource.Resource {
	return &vpnPeeringResource{}
}

// vpnPeeringResource manages an iaas_vpn_peering - a VPC-to-VPC WireGuard
// peering between two VPN gateways owned by the same account.
type vpnPeeringResource struct {
	client *client.Client
}

// vpnPeeringModel maps the Terraform state/plan for iaas_vpn_peering.
//
//   - vpn_gateway_id (path, the LOCAL gateway) and remote_gateway_id (the ONLY
//     create body field) are Required + RequiresReplace - there is no update
//     route, so both are immutable.
//   - every other attribute is plain Computed (never accepted as create input;
//     100% server-derived) - name, type (always "vpc_peering"), public_key
//     (the REMOTE gateway's public key), endpoint (the remote gateway's
//     public IP:port), tunnel_ip (allocated on the LOCAL gateway's tunnel
//     subnet), allowed_ips (the remote VPC's CIDR + tunnel subnet), dns,
//     keepalive, enabled.
type vpnPeeringModel struct {
	ID              types.String `tfsdk:"id"`
	VPNGatewayID    types.String `tfsdk:"vpn_gateway_id"`
	RemoteGatewayID types.String `tfsdk:"remote_gateway_id"`

	Name       types.String `tfsdk:"name"`
	Type       types.String `tfsdk:"type"`
	PublicKey  types.String `tfsdk:"public_key"`
	Endpoint   types.String `tfsdk:"endpoint"`
	TunnelIP   types.String `tfsdk:"tunnel_ip"`
	AllowedIPs types.Set    `tfsdk:"allowed_ips"`
	DNS        types.String `tfsdk:"dns"`
	Keepalive  types.Int64  `tfsdk:"keepalive"`
	Enabled    types.Bool   `tfsdk:"enabled"`
}

// Metadata sets the resource type name → "iaas_vpn_peering".
func (r *vpnPeeringResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vpn_peering"
}

// Schema describes the iaas_vpn_peering resource.
func (r *vpnPeeringResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a VPC-to-VPC WireGuard peering between two iaas_vpn_gateway resources " +
			"OWNED BY THE SAME ACCOUNT, in DIFFERENT VPCs (VpnGatewayController::createPeering). This " +
			"is distinct from iaas_vpn_peer's \"site_to_site\" flavour, which connects to an arbitrary " +
			"THIRD-PARTY endpoint you supply yourself (public_key/endpoint/allowed_ips). A peering only " +
			"takes the remote gateway's id: the server derives everything else (a shared pre-shared " +
			"key, each side's public_key/endpoint, tunnel IPs, and allowed_ips = the remote VPC's CIDR " +
			"+ tunnel subnet) and creates a SYMMETRIC PAIR of peer rows, one on each gateway. This " +
			"resource models the row on THIS (vpn_gateway_id) side only; peer this gateway's remote " +
			"back with a second iaas_vpn_peering resource (remote_gateway_id/vpn_gateway_id swapped) to " +
			"manage the other side as Terraform-owned too. There is no update route: both inputs are " +
			"immutable, and the derived attributes cannot be changed independently of them. Creation is " +
			"SYNCHRONOUS (no task/polling). The generated pre-shared key is never returned by the API " +
			"(encrypted + hidden server-side) and is not a create input either, so it is not exposed by " +
			"this resource at all. Import with a composite id: \"<vpn_gateway_id>/<peering_id>\".",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the peering (the underlying vpn_gateway_peers row id on this side), assigned by the API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"vpn_gateway_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the LOCAL VPN gateway this peering belongs to. This value is part " +
					"of the API request path, so changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"remote_gateway_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the REMOTE iaas_vpn_gateway to peer with. Must be owned by the " +
					"same account and deployed in a DIFFERENT VPC than vpn_gateway_id (the API rejects " +
					"same-VPC peering, and CIDR/tunnel-subnet overlap between the two VPCs). Immutable; " +
					"changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},

			// ── Server-derived computed (never accepted as create input) ──────
			"name": schema.StringAttribute{
				Computed:    true,
				Description: "Server-generated display name, e.g. \"vpc-peering-<remote-vpc-id-prefix>\".",
			},
			"type": schema.StringAttribute{
				Computed:    true,
				Description: "Always \"vpc_peering\" - distinguishes this peer row from a road_warrior/site_to_site iaas_vpn_peer.",
			},
			"public_key": schema.StringAttribute{
				Computed:    true,
				Description: "The REMOTE gateway's WireGuard public key (not a secret), used by this gateway to authenticate the peer.",
			},
			"endpoint": schema.StringAttribute{
				Computed:    true,
				Description: "The remote gateway's dial-out endpoint (\"<public_ip>:<listen_port>\"). Empty if the remote gateway has no public IP yet.",
			},
			"tunnel_ip": schema.StringAttribute{
				Computed:    true,
				Description: "This side's IP inside the LOCAL gateway's tunnel subnet, auto-allocated by the server.",
				PlanModifiers: []planmodifier.String{
					// Stable once allocated; the API has no update route for it.
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"allowed_ips": schema.SetAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "CIDRs routed through this peering (WireGuard AllowedIPs): the remote VPC's CIDR plus the remote gateway's tunnel subnet.",
			},
			"dns": schema.StringAttribute{
				Computed:    true,
				Description: "DNS server advertised for this peering. Always empty - createPeering never sets it (DNS only applies to road_warrior client peers).",
			},
			"keepalive": schema.Int64Attribute{
				Computed:    true,
				Description: "WireGuard PersistentKeepalive interval in seconds. Always 25 for a peering.",
			},
			"enabled": schema.BoolAttribute{
				Computed:    true,
				Description: "Whether the peering is enabled (included in the gateway's WireGuard config). Always true when freshly created.",
			},
		},
	}
}

// Configure pulls the shared *client.Client from the provider (nil-guarded).
func (r *vpnPeeringResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create peers vpn_gateway_id with remote_gateway_id. The create is
// synchronous; the response carries the new (local-side) peering object with
// its id and every server-derived field. We persist the id immediately so a
// failed read-back still tracks the resource for cleanup, then read back by
// scanning the gateway SHOW so state reflects the authoritative row.
func (r *vpnPeeringResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan vpnPeeringModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	gwID := plan.VPNGatewayID.ValueString()
	remoteID := plan.RemoteGatewayID.ValueString()

	created, err := r.client.CreateVpnPeering(ctx, gwID, remoteID)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating VPN peering", err))
		return
	}
	id, _ := created["id"].(string)
	if id == "" {
		resp.Diagnostics.AddError("Error creating VPN peering", "the create response did not include a peering id")
		return
	}

	// Persist the id immediately so a failed read-back still tracks the
	// resource for cleanup on the next destroy.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Read-back by scanning the gateway SHOW so state reflects the
	// authoritative row. Fall back to the create response if the scan can't
	// find it yet (defensive - the write is synchronous).
	obj, err := r.client.GetVpnPeering(ctx, gwID, id)
	if err != nil {
		obj = created
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, vpnPeeringStateFromAPI(obj, plan))...)
}

// Read refreshes state by scanning the parent gateway SHOW's embedded peers[]
// (type == "vpc_peering"). A 404 (peering or its gateway gone) removes the
// resource from state.
func (r *vpnPeeringResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state vpnPeeringModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	obj, err := r.client.GetVpnPeering(ctx, state.VPNGatewayID.ValueString(), state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("Error reading VPN peering", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, vpnPeeringStateFromAPI(obj, state))...)
}

// Update is unreachable in practice: vpn_gateway_id and remote_gateway_id are
// the only two attributes a caller can set, and both are RequiresReplace, so
// Terraform recreates the resource instead of calling Update whenever either
// changes. It still must satisfy resource.Resource; it simply persists the
// plan (identical to the kubernetes_ssl_certificate all-RequiresReplace
// pattern - there is no update route to call).
func (r *vpnPeeringResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan vpnPeeringModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

// Delete removes the peering via the generic peer-removal endpoint. This only
// deletes THIS side's row; the symmetric row on the remote gateway is left
// alone (it belongs to that gateway's own iaas_vpn_peering resource, if any).
func (r *vpnPeeringResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state vpnPeeringModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteVpnPeering(ctx, state.VPNGatewayID.ValueString(), state.ID.ValueString()); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deleting VPN peering", err))
		return
	}
}

// ImportState implements COMPOSITE import: "<vpn_gateway_id>/<peering_id>".
// remote_gateway_id (Required, non-Computed) is populated by the automatic
// Read that follows import - the embedded peer object's remote_gateway_id
// field - not by ImportState itself (mirrors kubernetes_ssl_certificate's
// domain/source, which are populated the same way).
func (r *vpnPeeringResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	gwID, peeringID, ok := strings.Cut(req.ID, "/")
	if !ok || gwID == "" || peeringID == "" {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Expected import ID in the format \"vpn_gateway_id/peering_id\", got: %q. "+
				"VPN peerings are child resources, so both the parent gateway id and the "+
				"peering id are required to import.", req.ID),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("vpn_gateway_id"), gwID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), peeringID)...)
}

// vpnPeeringStateFromAPI builds the model from an embedded peer object (type
// == "vpc_peering"), falling back to the prior model's value for fields the
// response omits. vpn_gateway_id is authoritative from the path;
// remote_gateway_id comes from the object's own remote_gateway_id field (set
// server-side to the OTHER gateway's id). tunnel_ip and allowed_ips are fully
// COMPUTED, non-Optional attributes with NO fallback to prior (see
// vpnPeeringTunnelIPFromAPI / vpnPeeringAllowedIPsFromAPI below) - on Create,
// prior is the PLAN, whose Computed fields are Unknown until read back, so
// falling back to prior for those two would leak an Unknown value into state
// the instant the API ever omitted/emptied them, tripping Terraform's
// "inconsistent result after apply" check.
func vpnPeeringStateFromAPI(obj map[string]any, prior vpnPeeringModel) vpnPeeringModel {
	m := vpnPeeringModel{
		ID:              stringFromAPI(obj, "id", prior.ID),
		VPNGatewayID:    prior.VPNGatewayID, // from the path
		RemoteGatewayID: stringFromAPI(obj, "remote_gateway_id", prior.RemoteGatewayID),
		Name:            stringFromAPI(obj, "name", prior.Name),
		Type:            stringFromAPI(obj, "type", prior.Type),
		PublicKey:       stringFromAPI(obj, "public_key", prior.PublicKey),
		Endpoint:        optionalStringFromAPI(obj, "endpoint", prior.Endpoint),
		TunnelIP:        vpnPeeringTunnelIPFromAPI(obj),
		DNS:             optionalStringFromAPI(obj, "dns", prior.DNS),
		Keepalive:       int64FromAPI(obj, "keepalive", prior.Keepalive),
		Enabled:         boolFromIntAPI(obj, "enabled", prior.Enabled),
	}
	m.AllowedIPs = vpnPeeringAllowedIPsFromAPI(obj["allowed_ips"])
	return m
}

// vpnPeeringTunnelIPFromAPI reads "tunnel_ip" - a fully COMPUTED, non-Optional
// attribute - resolving purely from the API response (mirrors
// letsencryptDomainsFromAPI in kubernetes_ssl_certificate.go: no "prior"
// fallback at all). An absent/null value settles to a KNOWN "" rather than a
// stale/Unknown prior value.
func vpnPeeringTunnelIPFromAPI(obj map[string]any) types.String {
	raw, ok := obj["tunnel_ip"]
	if !ok || raw == nil {
		return types.StringValue("")
	}
	if s, ok := raw.(string); ok {
		return types.StringValue(s)
	}
	return types.StringValue(fmt.Sprintf("%v", raw))
}

// vpnPeeringAllowedIPsFromAPI converts the embedded "allowed_ips" JSON array of
// CIDR strings into a types.Set. This is a fully COMPUTED, non-Optional
// attribute, so - mirroring stringSetKnown in instance_vpc_attachment.go - an
// absent/malformed/empty array settles to a KNOWN EMPTY set rather than
// falling back to "prior": on Create, prior is the PLAN, whose Computed
// fields are Unknown until read back, so returning it here would leak an
// Unknown value into state the moment the API ever omitted/emptied
// allowed_ips. Behavior is unchanged when the API returns a populated array.
func vpnPeeringAllowedIPsFromAPI(raw any) types.Set {
	arr, ok := raw.([]any)
	if !ok {
		return mustSetValue(types.StringType, []attr.Value{})
	}
	elems := make([]attr.Value, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok && s != "" {
			elems = append(elems, types.StringValue(s))
		}
	}
	return mustSetValue(types.StringType, elems)
}
