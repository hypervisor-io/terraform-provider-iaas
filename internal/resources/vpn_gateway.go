package resources

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/iaas/terraform-provider-iaas/internal/client"
	"github.com/iaas/terraform-provider-iaas/internal/waiter"
)

// Interface assertions — iaas_vpn_gateway is a CHILD + ASYNC resource, combining
// the established patterns:
//
//   - CHILD (from vpc_subnet/nat_gateway): the parent vpc_id lives in the CREATE
//     URL path, so it is Required + RequiresReplace, and import takes a COMPOSITE
//     "<vpc_id>/<id>". NOTE the route asymmetry: ONLY create is nested under the
//     VPC — Read/Delete use the FLAT /vpn-gateway/{id} path, so the gateway id
//     alone drives every operation after create.
//   - ASYNC (from nat_gateway/load_balancer): CREATE records the row
//     (status="deploying"), then waits for the SHOW status to reach "active" via a
//     StatePollerWithErrorTolerance waiter; the id is persisted to state BEFORE
//     the wait so a failed wait still leaves a destroyable resource; a timeouts
//     block is exposed.
//
// The VPN gateway has NO update endpoint of its own (the only mutable surface is
// its PEERS, modelled as the separate iaas_vpn_peer resource), so EVERY input
// attribute is RequiresReplace — the vpc no-update pattern. Update is therefore a
// read-back no-op.
var (
	_ resource.Resource                = &vpnGatewayResource{}
	_ resource.ResourceWithConfigure   = &vpnGatewayResource{}
	_ resource.ResourceWithImportState = &vpnGatewayResource{}
)

// NewVPNGatewayResource is the resource constructor registered with the provider.
func NewVPNGatewayResource() resource.Resource {
	return &vpnGatewayResource{}
}

// vpnGatewayResource manages an iaas_vpn_gateway — the (single) WireGuard VPN
// gateway of a VPC, backed by a real VM instance, giving remote clients and sites
// encrypted access into the VPC.
type vpnGatewayResource struct {
	client *client.Client
}

// vpnGatewayModel maps the Terraform state/plan for iaas_vpn_gateway.
//
// Field groups:
//   - PARENT path id: vpc_id (Required, RequiresReplace — part of the CREATE path).
//   - create inputs (all RequiresReplace — the gateway has no update endpoint):
//     vpngw_plan_id (Required), vpc_subnet_id (Required, WRITE-ONLY — consumed at
//     deploy, never returned by SHOW), name / tunnel_subnet / listen_port
//     (Optional+Computed — server defaults).
//   - computed read-only: status (server-mutable lifecycle), public_key + vpc_ip
//   - public_ip (stable after create).
type vpnGatewayModel struct {
	ID           types.String `tfsdk:"id"`
	VPCID        types.String `tfsdk:"vpc_id"`
	VPNGWPlanID  types.String `tfsdk:"vpngw_plan_id"`
	VPCSubnetID  types.String `tfsdk:"vpc_subnet_id"`
	Name         types.String `tfsdk:"name"`
	TunnelSubnet types.String `tfsdk:"tunnel_subnet"`
	ListenPort   types.Int64  `tfsdk:"listen_port"`

	// Computed read-only.
	Status    types.String `tfsdk:"status"`
	PublicKey types.String `tfsdk:"public_key"`
	VPCIP     types.String `tfsdk:"vpc_ip"`
	PublicIP  types.String `tfsdk:"public_ip"`

	Timeouts timeouts.Value `tfsdk:"timeouts"`
}

// Metadata sets the resource type name → "<provider>_vpn_gateway".
func (r *vpnGatewayResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vpn_gateway"
}

// Schema describes the iaas_vpn_gateway resource.
func (r *vpnGatewayResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a VPC VPN gateway — a WireGuard endpoint backed by a dedicated VM " +
			"instance, deployed into one of the VPC's PUBLIC subnets, giving remote clients " +
			"(road-warrior) and remote sites (site-to-site / VPC peering) encrypted access to " +
			"the VPC's private networks. A VPC can have AT MOST ONE VPN gateway. The parent " +
			"vpc_id is part of the create API path, so changing it forces a new resource. " +
			"Creation is ASYNCHRONOUS: the gateway record is created (status=\"deploying\"), a " +
			"public IP is allocated, a backing VM is deployed, and this resource waits for the " +
			"slave to report status=\"active\" (a failed deploy ends in status=\"error\"). The " +
			"gateway itself has no in-place updates — every input forces replacement; its peers " +
			"are managed via separate iaas_vpn_peer resources. The feature must be enabled for " +
			"the VPC's location; if it is not (or the per-account VPN gateway quota is reached, " +
			"or no public IP is available) the create fails with a clear message.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the VPN gateway, assigned by the API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"vpc_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the parent VPC this VPN gateway belongs to. This value is part " +
					"of the create API request path, so changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"vpngw_plan_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the VPN gateway plan (sizing/pricing of the backing VM). Use the " +
					"VPN gateway plans endpoint to discover available plan ids. The gateway has no " +
					"update endpoint, so changing the plan forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"vpc_subnet_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the VPC PUBLIC subnet to deploy the gateway's backing VM into " +
					"(it must be a public subnet with at least one free IP). This is a WRITE-ONLY " +
					"input consumed at deploy time — it is NOT returned by the gateway read endpoint, " +
					"so it is preserved from configuration and ignored on import. Changing it forces a " +
					"new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"name": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "Display name for the gateway. Defaults to a server-assigned " +
					"\"vpngw-<random>\" when omitted. The gateway has no update endpoint, so changing " +
					"the name forces a new resource. Modelled Optional+Computed so an omitted name " +
					"round-trips against the server default without showing spurious drift.",
				PlanModifiers: []planmodifier.String{
					// Optional+Computed+RequiresReplace MUST carry UseStateForUnknown:
					// without it an omitted (config-null) Computed value re-plans as
					// unknown, and unknown + RequiresReplace would spuriously force a
					// replace every apply. USFU keeps the create-only server value stable.
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"tunnel_subnet": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "WireGuard tunnel subnet (CIDR) from which peer tunnel IPs are allocated " +
					"(the gateway itself takes .1). Defaults to \"10.99.0.0/24\" when omitted. Must not " +
					"overlap the VPC CIDR or any VPC subnet. Changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"listen_port": schema.Int64Attribute{
				Optional: true,
				Computed: true,
				Description: "WireGuard UDP listen port on the gateway (1024-65535). Defaults to 51820 " +
					"when omitted. Changing it forces a new resource.",
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
					int64planmodifier.RequiresReplace(),
				},
			},
			// status is a SERVER-MUTABLE computed field (deploying → active, or
			// error on failure), so per the golden guardrail it does NOT use
			// UseStateForUnknown — that would copy the stale prior value into the
			// plan and mask real drift.
			"status": schema.StringAttribute{
				Computed: true,
				Description: "Lifecycle status: \"deploying\" (provisioning), \"active\" (ready), " +
					"\"error\" (deploy failed — recoverable via the gateway retry action). " +
					"Server-mutable.",
			},
			// public_key is the gateway's WireGuard PUBLIC key, generated server-side
			// at create and stable thereafter, so UseStateForUnknown is safe. (The
			// matching PRIVATE key is encrypted + $hidden server-side and is NEVER
			// returned, so it is not modelled.)
			"public_key": schema.StringAttribute{
				Computed: true,
				Description: "The gateway's WireGuard public key, generated server-side at create time. " +
					"Peers use it to encrypt traffic to the gateway. Stable after creation. (The " +
					"matching private key is held only on the server and is never exposed.)",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			// vpc_ip / public_ip are assigned at create and stable thereafter.
			"vpc_ip": schema.StringAttribute{
				Computed: true,
				Description: "The gateway's IP address on the VPC network (its interface inside the " +
					"VPC). Stable after creation.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"public_ip": schema.StringAttribute{
				Computed: true,
				Description: "The public IPv4 address of the gateway's backing VM — the WireGuard " +
					"endpoint address remote peers connect to. Stable after creation.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
		Blocks: map[string]schema.Block{
			// Only create is async (waits for status="active"); there is no update
			// endpoint and delete is synchronous-from-the-API's-view, so only the
			// create timeout is truly meaningful — the block exposes all three for
			// consistency with the async-resource pattern.
			"timeouts": timeouts.Block(ctx, timeouts.Opts{
				Create: true,
				Update: true,
				Delete: true,
			}),
		},
	}
}

// Configure pulls the shared *client.Client from the provider (nil-guarded).
func (r *vpnGatewayResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create provisions the VPN gateway and waits for it to become active:
//
//  1. CreateVpnGateway records the row (status="deploying"), allocates a public
//     IP, and deploys the backing VM — all in one async call.
//  2. The id is saved into state BEFORE the wait, so a provisioning failure or
//     timeout still tracks the gateway for a subsequent destroy.
//  3. WaitFor polls GetVpnGateway until status=="active" (fail on "error").
//  4. Read back so state reflects the server-assigned public key + endpoint IPs.
func (r *vpnGatewayResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan vpnGatewayModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	createTimeout, diags := plan.Timeouts.Create(ctx, defaultCreateTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	vpcID := plan.VPCID.ValueString()

	body := map[string]any{
		"vpngw_plan_id": plan.VPNGWPlanID.ValueString(),
		"vpc_subnet_id": plan.VPCSubnetID.ValueString(),
	}
	// Send name/tunnel_subnet/listen_port only when the user set them (omit, don't
	// send null) so the server applies its own defaults.
	if !plan.Name.IsNull() && !plan.Name.IsUnknown() {
		body["name"] = plan.Name.ValueString()
	}
	if !plan.TunnelSubnet.IsNull() && !plan.TunnelSubnet.IsUnknown() {
		body["tunnel_subnet"] = plan.TunnelSubnet.ValueString()
	}
	if !plan.ListenPort.IsNull() && !plan.ListenPort.IsUnknown() {
		body["listen_port"] = plan.ListenPort.ValueInt64()
	}

	created, err := r.client.CreateVpnGateway(ctx, vpcID, body)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating VPN gateway", err))
		return
	}
	id, _ := created["id"].(string)
	if id == "" {
		resp.Diagnostics.AddError("Error creating VPN gateway", "the create response did not include a gateway id")
		return
	}

	// Persist the id (and parent vpc_id + the write-only vpc_subnet_id) immediately
	// so a failed provisioning/wait still tracks the resource for cleanup on the
	// next destroy.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("vpc_id"), vpcID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("vpc_subnet_id"), plan.VPCSubnetID.ValueString())...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// ── ASYNC convergence: poll the gateway SHOW until status="active" ─────────
	// Ready="active"; fail="error" (a failed deploy is recoverable via the
	// gateway's retry action, but from Terraform's view it is a create failure).
	// Tolerance=3 absorbs transient transport blips during provisioning that
	// bypass the client's 429/5xx retry.
	waitErr := waiter.WaitFor(ctx, waiter.Options{
		Interval: pollInterval(),
		Timeout:  createTimeout,
		Refresh: waiter.StatePollerWithErrorTolerance(
			func() (map[string]any, error) { return r.client.GetVpnGateway(ctx, id) },
			"status",
			[]string{"active"},
			[]string{"error"},
			3,
		),
	})
	if waitErr != nil {
		resp.Diagnostics.AddError(
			"Error waiting for VPN gateway provisioning",
			fmt.Sprintf("VPN gateway %s did not become active: %s", id, waitErr.Error()),
		)
		return
	}

	// Read back so state reflects the public key + endpoint IPs.
	obj, err := r.client.GetVpnGateway(ctx, id)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading VPN gateway after provisioning", err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, vpnGatewayStateFromAPI(obj, plan))...)
}

// Read refreshes state from the API via the FLAT /vpn-gateway/{id} path (the
// parent vpc is NOT in the path). A 404 means the gateway was deleted out of band
// — remove it from state so Terraform plans a recreate.
func (r *vpnGatewayResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state vpnGatewayModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	obj, err := r.client.GetVpnGateway(ctx, state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("Error reading VPN gateway", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, vpnGatewayStateFromAPI(obj, state))...)
}

// Update is a no-op read-back: the VPN gateway has NO update endpoint, so every
// input attribute is RequiresReplace and nothing mutable ever reaches here (only
// the timeouts block, which is state-only, can change). We re-read to keep the
// computed fields fresh. (The vpc no-update pattern.)
func (r *vpnGatewayResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan vpnGatewayModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	obj, err := r.client.GetVpnGateway(ctx, plan.ID.ValueString())
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading VPN gateway after update", err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, vpnGatewayStateFromAPI(obj, plan))...)
}

// Delete removes the VPN gateway. The service bills final hours, destroys the
// backing instance (releasing its public IP), deletes the peers, and soft-deletes
// the row immediately, so a subsequent SHOW 404s right away — no delete waiter is
// required.
func (r *vpnGatewayResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state vpnGatewayModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteVpnGateway(ctx, state.ID.ValueString()); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deleting VPN gateway", err))
		return
	}
}

// ImportState implements COMPOSITE import for this child resource. The gateway
// SHOW does NOT return vpc_id or the write-only vpc_subnet_id, and vpc_id is a
// Required schema attribute, so `terraform import` must supply BOTH the parent
// VPC id and the gateway id joined by a slash:
//
//	terraform import iaas_vpn_gateway.x <vpc_id>/<gateway_id>
//
// We split req.ID on the FIRST "/" into vpc_id and gateway_id; the subsequent
// Read hydrates the remaining attributes. vpc_subnet_id (write-only) cannot be
// recovered and must be supplied in configuration (it is in the lifecycle test's
// ImportStateVerifyIgnore).
func (r *vpnGatewayResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	vpcID, gwID, ok := strings.Cut(req.ID, "/")
	if !ok || vpcID == "" || gwID == "" {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Expected import ID in the format \"vpc_id/gateway_id\", got: %q. "+
				"VPN gateways are child resources, so both the parent VPC id and the "+
				"gateway id are required to import.", req.ID),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("vpc_id"), vpcID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), gwID)...)
}

// vpnGatewayStateFromAPI builds the model from an API gateway object, falling
// back to the prior model's value for fields the response omits.
//
//   - vpc_id is never in the response top level (it lives in the create path); the
//     gateway SHOW DOES embed a nested vpc{id}, so vpc_id is recovered from there
//     when present (useful for import refresh), else falls back to prior.
//   - vpc_subnet_id is WRITE-ONLY (never in SHOW) → always falls back to prior
//     (preserving the configured value; null on a fresh import until set in HCL).
//   - public_ip is extracted from the backing instance's primary public IP, which
//     the SHOW exposes under instance.ips[]; we read a flattened "public_ip" if the
//     server provides one, else fall back to prior.
func vpnGatewayStateFromAPI(obj map[string]any, prior vpnGatewayModel) vpnGatewayModel {
	m := vpnGatewayModel{
		ID:           stringFromAPI(obj, "id", prior.ID),
		VPCID:        nestedStringFromAPI(obj, "vpc", "id", prior.VPCID),
		VPNGWPlanID:  stringFromAPI(obj, "vpngw_plan_id", prior.VPNGWPlanID),
		VPCSubnetID:  prior.VPCSubnetID, // write-only; never in the response
		Name:         stringFromAPI(obj, "name", prior.Name),
		TunnelSubnet: stringFromAPI(obj, "tunnel_subnet", prior.TunnelSubnet),
		ListenPort:   int64FromAPI(obj, "listen_port", prior.ListenPort),

		Status:    stringFromAPI(obj, "status", prior.Status),
		PublicKey: stringFromAPI(obj, "public_key", prior.PublicKey),
		VPCIP:     stringFromAPI(obj, "vpc_ip", prior.VPCIP),
		PublicIP:  vpnGatewayPublicIP(obj, prior.PublicIP),

		Timeouts: prior.Timeouts,
	}
	return m
}

// vpnGatewayPublicIP resolves the gateway's public endpoint IP from the SHOW
// payload. The controller does not expose a flat "public_ip" scalar on the
// gateway, so we derive it from the backing instance's interfaces: instance.ips[]
// holds the assigned IPs, and the public one is the road-warrior endpoint. We
// pick the first IP whose subnet type is "public" (falling back to the first IP),
// and finally fall back to the prior value when nothing is resolvable (e.g. a
// freshly-deploying gateway whose instance has no IPs yet).
func vpnGatewayPublicIP(obj map[string]any, prior types.String) types.String {
	// Prefer a flat field if the server ever provides one.
	if v, ok := obj["public_ip"].(string); ok && v != "" {
		return types.StringValue(v)
	}
	inst, ok := obj["instance"].(map[string]any)
	if !ok {
		return prior
	}
	ips, ok := inst["ips"].([]any)
	if !ok {
		return prior
	}
	var firstIP string
	for _, raw := range ips {
		ipObj, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		addr, _ := ipObj["ip"].(string)
		if addr == "" {
			continue
		}
		if firstIP == "" {
			firstIP = addr
		}
		if sub, ok := ipObj["subnet"].(map[string]any); ok {
			if t, _ := sub["type"].(string); t == "public" {
				return types.StringValue(addr)
			}
		}
	}
	if firstIP != "" {
		return types.StringValue(firstIP)
	}
	return prior
}
