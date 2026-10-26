package resources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
	"github.com/hypervisor-io/terraform-provider-iaas/internal/validators"
	"github.com/hypervisor-io/terraform-provider-iaas/waiter"
)

// iaas_vpn_ipsec_connection is a CHILD of a VPN gateway (the parent id is in the
// API path, import takes "<gateway_id>/<connection_id>") and the first resource
// whose writes are applied to the gateway ASYNCHRONOUSLY after being recorded:
//
//   - create/update/delete answer 200/201 at once; the connection's derived
//     apply_state goes pending -> applied (or failed). Create and update wait for
//     "applied"; a failed apply is an error. sa_state (the live tunnel) depends on
//     the remote peer and is exposed but never waited for.
//   - delete removes the row from every read at once, but the gateway keeps the
//     removed networks reserved (a new connection with the same remote_ts would be
//     refused as destination_overlap) until the node confirms the generation, so
//     delete waits for the gateway's applied generation to catch up.
//   - create is sent in exactly ONE attempt. A 5xx or transport error is an
//     unknown outcome: it is never replayed and never adopted (the API has no
//     idempotency key, so a same-name connection may belong to another writer);
//     the error names it and gives the terraform import command. A 409
//     ipsec_installing (the gateway is still installing its IPsec packages, for
//     instance for a sibling connection created in parallel) is a definite
//     refusal and is waited out within the timeout.
//
// The pre-shared key (psk) is Sensitive and write-only: the API never returns it.
// It is kept from configuration and, to detect a rotation done outside Terraform,
// the key's server-side version (psk_version) seen at the last write is kept in
// private state; if a read finds another version the stored key is dropped from
// state so the next plan sends the configured key again.
var (
	_ resource.Resource                   = &vpnIpsecConnectionResource{}
	_ resource.ResourceWithConfigure      = &vpnIpsecConnectionResource{}
	_ resource.ResourceWithImportState    = &vpnIpsecConnectionResource{}
	_ resource.ResourceWithValidateConfig = &vpnIpsecConnectionResource{}
)

const (
	vpnIpsecDefaultCreateTimeout = 30 * time.Minute
	vpnIpsecDefaultUpdateTimeout = 15 * time.Minute
	vpnIpsecDefaultDeleteTimeout = 15 * time.Minute

	// vpnIpsecPSKVersionKey is the private-state key holding the server-side
	// psk_version seen at the last write.
	vpnIpsecPSKVersionKey = "psk_version"
)

// ipsecNameRE mirrors the server grammar of a connection name.
var ipsecNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}[a-z0-9]$`)

// vpnIpsecAdoptTimeout bounds the read-back after an uncertain create answer. It
// runs on a fresh context so an expired create deadline cannot turn a recorded
// connection into an orphan. A var only so tests can shrink it.
var vpnIpsecAdoptTimeout = 30 * time.Second

// NewVPNIpsecConnectionResource is the resource constructor registered with the provider.
func NewVPNIpsecConnectionResource() resource.Resource {
	return &vpnIpsecConnectionResource{}
}

type vpnIpsecConnectionResource struct {
	client *client.Client
}

type vpnIpsecConnectionModel struct {
	ID               types.String   `tfsdk:"id"`
	VPNGatewayID     types.String   `tfsdk:"vpn_gateway_id"`
	Name             types.String   `tfsdk:"name"`
	RemoteAddress    types.String   `tfsdk:"remote_address"`
	LocalIDType      types.String   `tfsdk:"local_id_type"`
	LocalID          types.String   `tfsdk:"local_id"`
	RemoteIDType     types.String   `tfsdk:"remote_id_type"`
	RemoteID         types.String   `tfsdk:"remote_id"`
	PSK              types.String   `tfsdk:"psk"`
	IKEVersion       types.Int64    `tfsdk:"ike_version"`
	ProposalPreset   types.String   `tfsdk:"proposal_preset"`
	IKEProposal      types.String   `tfsdk:"ike_proposal"`
	ESPProposal      types.String   `tfsdk:"esp_proposal"`
	LocalTS          types.Set      `tfsdk:"local_ts"`
	RemoteTS         types.Set      `tfsdk:"remote_ts"`
	Role             types.String   `tfsdk:"role"`
	DPDDelay         types.Int64    `tfsdk:"dpd_delay"`
	DPDAction        types.String   `tfsdk:"dpd_action"`
	IKELifetime      types.Int64    `tfsdk:"ike_lifetime"`
	ChildLifetime    types.Int64    `tfsdk:"child_lifetime"`
	UDPEncapsulation types.Bool     `tfsdk:"udp_encapsulation"`
	Enabled          types.Bool     `tfsdk:"enabled"`
	ConnName         types.String   `tfsdk:"conn_name"`
	SAState          types.String   `tfsdk:"sa_state"`
	ApplyState       types.String   `tfsdk:"apply_state"`
	LastErrorCode    types.String   `tfsdk:"last_error_code"`
	LastError        types.String   `tfsdk:"last_error"`
	RemoteSettings   types.String   `tfsdk:"remote_settings"`
	Timeouts         timeouts.Value `tfsdk:"timeouts"`
}

func (r *vpnIpsecConnectionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vpn_ipsec_connection"
}

func (r *vpnIpsecConnectionResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages an IPsec site-to-site connection (IKEv2 or IKEv1, pre-shared key) of a VPN " +
			"gateway. A connection is a child of an iaas_vpn_gateway: its parent vpn_gateway_id is part of " +
			"the API path, so changing it forces a new resource. Everything else, including the " +
			"pre-shared key, can be changed in place. Writes are recorded at once and applied to the " +
			"gateway asynchronously (the first connection also installs the IPsec packages in the " +
			"gateway): create and update wait until apply_state is \"applied\" and fail when the " +
			"gateway reports \"failed\". The live tunnel (sa_state) depends on the remote peer and is " +
			"not waited for. Delete waits until the gateway has confirmed the removal. The values to " +
			"enter on the other device are available in the computed remote_settings attribute. " +
			"The optional text attributes remote_address, local_id, remote_id, ike_proposal and " +
			"esp_proposal cannot be empty or padded with whitespace (the platform would store an empty " +
			"string as null); remove the attribute to leave it unset or to clear it. " +
			"Import with a composite id: \"<gateway_id>/<connection_id>\".",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the connection, assigned by the API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"vpn_gateway_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the parent VPN gateway. Part of the API request path, so changing it " +
					"forces a new resource. The gateway must be active.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"name": schema.StringAttribute{
				Required: true,
				Description: "Display name: 2 to 32 lower-case letters, digits or hyphens, starting and ending " +
					"with a letter or digit, unique per gateway. Never sent to the gateway.",
				Validators: []validator.String{
					stringvalidator.RegexMatches(
						ipsecNameRE,
						"must be 2 to 32 lower-case letters, digits or hyphens, starting and ending with a letter or digit",
					),
				},
			},
			"remote_address": schema.StringAttribute{
				Optional: true,
				Description: "Public IPv4 address or host name of the remote peer. Omit only for a responder " +
					"(role = \"responder\") that identifies the peer by fqdn, email, keyid or any (IKEv2 only).",
				Validators: []validator.String{
					validators.NoControlCharacters(),
					validators.PlainOptionalString(),
					stringvalidator.LengthAtMost(253),
				},
			},
			"local_id_type": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "How this side identifies itself: \"address\", \"fqdn\", \"email\" or \"keyid\". " +
					"Defaults to \"address\" (the gateway public IP).",
				Validators: []validator.String{
					stringvalidator.OneOf("address", "fqdn", "email", "keyid"),
				},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"local_id": schema.StringAttribute{
				Optional: true,
				Description: "Local identity value matching local_id_type. Defaults to the gateway public IP " +
					"when omitted.",
				Validators: []validator.String{
					validators.NoControlCharacters(),
					validators.PlainOptionalString(),
					stringvalidator.LengthAtMost(253),
				},
			},
			"remote_id_type": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "How the remote peer identifies itself: \"address\", \"fqdn\", \"email\", " +
					"\"keyid\" or \"any\" (any is for a responder only). Defaults to \"address\".",
				Validators: []validator.String{
					stringvalidator.OneOf("address", "fqdn", "email", "keyid", "any"),
				},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"remote_id": schema.StringAttribute{
				Optional: true,
				Description: "Remote identity value matching remote_id_type. Defaults to remote_address when " +
					"omitted.",
				Validators: []validator.String{
					validators.NoControlCharacters(),
					validators.PlainOptionalString(),
					stringvalidator.LengthAtMost(253),
				},
			},
			// psk is encrypted + hidden server-side and never returned by any read, so
			// it is Sensitive and write-only: kept from configuration, dropped from
			// state when a read sees a rotation done outside Terraform, and absent
			// after an import. It is updated IN PLACE (no replacement).
			"psk": schema.StringAttribute{
				Required:  true,
				Sensitive: true,
				Description: "Pre-shared key: 16 to 128 printable ASCII characters without spaces, double " +
					"quotes or backslashes. Sensitive and write-only: it is stored encrypted by the platform " +
					"and never returned, so it cannot be read back or recovered on import (set it in " +
					"configuration after an import; the next apply sends it). Changing it updates the key in " +
					"place and re-negotiates the tunnel; it does not replace the connection. If the key is " +
					"changed outside Terraform (detected through the platform's key version) the next plan " +
					"shows an update that sets it back to the configured value.",
				Validators: []validator.String{
					validators.IPsecPSK(),
				},
			},
			"ike_version": schema.Int64Attribute{
				Optional: true,
				Computed: true,
				Description: "IKE version, 1 or 2. Defaults to 2. IKEv1 needs proposal_preset = \"compat\" and " +
					"a remote_address.",
				Validators: []validator.Int64{
					int64validator.OneOf(1, 2),
				},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"proposal_preset": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "Cipher proposal preset: \"modern\" (default), \"compat\" (older devices, needed " +
					"for IKEv1) or \"custom\" (requires ike_proposal and esp_proposal).",
				Validators: []validator.String{
					stringvalidator.OneOf("modern", "compat", "custom"),
				},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"ike_proposal": schema.StringAttribute{
				Optional: true,
				Description: "IKE proposal list for proposal_preset = \"custom\", for example " +
					"\"aes256gcm16-prfsha384-ecp384\". Must be unset for the other presets.",
				Validators: []validator.String{
					validators.NoControlCharacters(),
					validators.PlainOptionalString(),
					stringvalidator.LengthAtMost(255),
				},
			},
			"esp_proposal": schema.StringAttribute{
				Optional: true,
				Description: "ESP proposal list for proposal_preset = \"custom\", for example " +
					"\"aes256gcm16-ecp384\". Must be unset for the other presets.",
				Validators: []validator.String{
					validators.NoControlCharacters(),
					validators.PlainOptionalString(),
					stringvalidator.LengthAtMost(255),
				},
			},
			"local_ts": schema.SetAttribute{
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
				Description: "Local networks exposed through the tunnel: the VPC range or one or more of its " +
					"subnets (IPv4 CIDRs, at most 16). Defaults to the whole VPC range.",
				Validators: []validator.Set{
					setvalidator.SizeBetween(1, 16),
					setvalidator.ValueStringsAre(validators.IPv4CIDR()),
					validators.IPsecSelectorSet(false),
				},
				PlanModifiers: []planmodifier.Set{setplanmodifier.UseStateForUnknown()},
			},
			"remote_ts": schema.SetAttribute{
				Required:    true,
				ElementType: types.StringType,
				Description: "Remote networks reachable through the tunnel (IPv4 CIDRs with a /8 to /32 prefix, " +
					"1 to 32 of them). They must not overlap any other destination of the gateway (its VPC, " +
					"tunnel subnet, WireGuard peers, VPC peerings or other IPsec connections); a removed " +
					"connection keeps its networks reserved until the gateway has confirmed the removal.",
				Validators: []validator.Set{
					setvalidator.SizeBetween(1, 32),
					setvalidator.ValueStringsAre(validators.IPv4CIDRPrefixBetween(8, 32)),
					validators.IPsecSelectorSet(true),
				},
			},
			"role": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "\"initiator\" (this gateway starts the tunnel; the default) or \"responder\" " +
					"(it waits for the peer).",
				Validators: []validator.String{
					stringvalidator.OneOf("initiator", "responder"),
				},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"dpd_delay": schema.Int64Attribute{
				Optional:    true,
				Computed:    true,
				Description: "Dead-peer-detection interval in seconds, 0 to 300 (0 disables it). Defaults to 30.",
				Validators: []validator.Int64{
					int64validator.Between(0, 300),
				},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"dpd_action": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "What to do when the peer stops answering: \"clear\", \"trap\" or \"restart\". " +
					"Defaults to \"restart\" for an initiator and \"clear\" for a responder.",
				Validators: []validator.String{
					stringvalidator.OneOf("clear", "trap", "restart"),
				},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"ike_lifetime": schema.Int64Attribute{
				Optional:    true,
				Computed:    true,
				Description: "IKE SA lifetime in seconds, 600 to 86400. Defaults to 14400.",
				Validators: []validator.Int64{
					int64validator.Between(600, 86400),
				},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"child_lifetime": schema.Int64Attribute{
				Optional: true,
				Computed: true,
				Description: "Child SA lifetime in seconds, 300 to 28800 and not above ike_lifetime. " +
					"Defaults to 3600.",
				Validators: []validator.Int64{
					int64validator.Between(300, 28800),
				},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"udp_encapsulation": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Description: "Use UDP encapsulation (only udp/500 and udp/4500). Defaults to true. Turning it " +
					"off needs raw ESP (IP protocol 50), which security groups cannot admit; the platform " +
					"warns when security groups are attached.",
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
			"enabled": schema.BoolAttribute{
				Optional:      true,
				Computed:      true,
				Description:   "Whether the connection is enabled (loaded on the gateway). Defaults to true.",
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
			"conn_name": schema.StringAttribute{
				Computed:    true,
				Description: "Technical connection name used on the gateway (vc- followed by 12 hex digits).",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"sa_state": schema.StringAttribute{
				Computed: true,
				Description: "Live tunnel state: unknown, down, connecting, partial (IKE up, a child SA " +
					"missing), or established. It depends on the remote peer and is refreshed on every read.",
			},
			"apply_state": schema.StringAttribute{
				Computed: true,
				Description: "Whether the gateway has applied the latest change: pending, applied or failed. " +
					"Create and update wait for applied.",
			},
			"last_error_code": schema.StringAttribute{
				Computed: true,
				Description: "Machine code of the last tunnel error observed on the gateway, for example " +
					"authentication_failed, no_proposal_chosen, ts_unacceptable or peer_unreachable " +
					"(null when none).",
			},
			"last_error": schema.StringAttribute{
				Computed:    true,
				Description: "Text of the last tunnel error observed on the gateway (null when none).",
			},
			"remote_settings": schema.StringAttribute{
				Computed: true,
				Description: "The values to enter on the other device, as a JSON document (use jsondecode()): " +
					"our_public_ip, our_identity, your_identity, ike_version, ike_proposals, esp_proposals, " +
					"local_networks, remote_networks, ike_lifetime, child_lifetime, pfs, dpd, nat_traversal, " +
					"instance_route_hint and vendor_notes. It never contains the pre-shared key. Refreshed on " +
					"every read; a failure to fetch it is a warning, not an error.",
			},
		},
		Blocks: map[string]schema.Block{
			// create bounds the install wait, the create and the apply wait; update
			// bounds the update and the apply wait; delete bounds the wait for the
			// gateway to confirm the removal.
			"timeouts": timeouts.Block(ctx, timeouts.Opts{
				Create: true,
				Update: true,
				Delete: true,
			}),
		},
	}
}

func (r *vpnIpsecConnectionResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// ValidateConfig mirrors the server's cross-field refusals that can be decided
// from the CONFIGURED values alone. A rule is checked only when every attribute it
// involves is set explicitly in configuration: an omitted Optional+Computed
// attribute keeps its stored value on update (and takes the server default on
// create), so substituting a create default for it would reject valid plans. The
// server stays the authority for combinations that involve an omitted attribute.
func (r *vpnIpsecConnectionResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg vpnIpsecConnectionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	set := func(v interface {
		IsNull() bool
		IsUnknown() bool
	}) bool {
		return !v.IsNull() && !v.IsUnknown()
	}

	if set(cfg.IKEVersion) && cfg.IKEVersion.ValueInt64() == 1 {
		if set(cfg.ProposalPreset) && cfg.ProposalPreset.ValueString() != "compat" {
			resp.Diagnostics.AddAttributeError(path.Root("ike_version"), "IKEv1 Needs The Compat Preset",
				"ike_version = 1 is available with proposal_preset = \"compat\" only.")
		}
		if cfg.RemoteAddress.IsNull() {
			resp.Diagnostics.AddAttributeError(path.Root("remote_address"), "IKEv1 Needs A Remote Address",
				"ike_version = 1 needs a static remote_address.")
		}
	}
	explicitInitiator := set(cfg.Role) && cfg.Role.ValueString() == "initiator"
	if explicitInitiator && cfg.RemoteAddress.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("remote_address"), "Remote Address Required",
			"remote_address is required for an initiator; only role = \"responder\" may omit it.")
	}
	if explicitInitiator && set(cfg.RemoteIDType) && cfg.RemoteIDType.ValueString() == "any" {
		resp.Diagnostics.AddAttributeError(path.Root("remote_id_type"), "Any Identity Is For A Responder",
			"remote_id_type = \"any\" is accepted for role = \"responder\" only.")
	}
	if set(cfg.ProposalPreset) {
		if cfg.ProposalPreset.ValueString() == "custom" {
			if cfg.IKEProposal.IsNull() {
				resp.Diagnostics.AddAttributeError(path.Root("ike_proposal"), "Custom Preset Needs Proposals",
					"proposal_preset = \"custom\" requires ike_proposal.")
			}
			if cfg.ESPProposal.IsNull() {
				resp.Diagnostics.AddAttributeError(path.Root("esp_proposal"), "Custom Preset Needs Proposals",
					"proposal_preset = \"custom\" requires esp_proposal.")
			}
		} else {
			if set(cfg.IKEProposal) {
				resp.Diagnostics.AddAttributeError(path.Root("ike_proposal"), "Proposal Needs The Custom Preset",
					"ike_proposal can be set with proposal_preset = \"custom\" only.")
			}
			if set(cfg.ESPProposal) {
				resp.Diagnostics.AddAttributeError(path.Root("esp_proposal"), "Proposal Needs The Custom Preset",
					"esp_proposal can be set with proposal_preset = \"custom\" only.")
			}
		}
	}
	if set(cfg.IKELifetime) && set(cfg.ChildLifetime) && cfg.ChildLifetime.ValueInt64() > cfg.IKELifetime.ValueInt64() {
		resp.Diagnostics.AddAttributeError(path.Root("child_lifetime"), "Child Lifetime Too Long",
			fmt.Sprintf("child_lifetime (%d) must not be above ike_lifetime (%d).", cfg.ChildLifetime.ValueInt64(), cfg.IKELifetime.ValueInt64()))
	}
	// Local and remote networks must not overlap each other.
	if set(cfg.LocalTS) && set(cfg.RemoteTS) {
		local, _ := stringsFromSet(ctx, cfg.LocalTS)
		remote, _ := stringsFromSet(ctx, cfg.RemoteTS)
		if p := validators.IPsecSelectorProblem(append(append([]string{}, local...), remote...), false); p != "" &&
			validators.IPsecSelectorProblem(local, false) == "" && validators.IPsecSelectorProblem(remote, false) == "" {
			resp.Diagnostics.AddAttributeError(path.Root("remote_ts"), "Local And Remote Networks Overlap",
				"local_ts and remote_ts must not overlap: "+p+".")
		}
	}
}

// ---------------------------------------------------------------------------
// CRUD
// ---------------------------------------------------------------------------

func (r *vpnIpsecConnectionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan vpnIpsecConnectionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	timeout, diags := plan.Timeouts.Create(ctx, vpnIpsecDefaultCreateTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	gwID := plan.VPNGatewayID.ValueString()
	body, d := vpnIpsecCreateBody(ctx, plan)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}

	// The set of connection ids that exist before the send: an uncertain answer is
	// resolved by adopting a same-name connection that is NOT in this set.
	before, _, err := r.client.ListVpnIpsecConnections(ctx, gwID)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating VPN IPsec connection", err))
		return
	}
	known := map[string]bool{}
	for _, c := range before {
		if id, _ := c["id"].(string); id != "" {
			known[id] = true
		}
	}

	conn, warnings, err := r.createWithInstallWait(ctx, gwID, body)
	if err != nil {
		if !vpnIpsecUncertain(err) {
			resp.Diagnostics.Append(diagFromErr("Error creating VPN IPsec connection", err))
			return
		}
		resp.Diagnostics.AddError("VPN IPsec connection create outcome unknown", r.uncertainCreateMessage(gwID, plan.Name.ValueString(), known, err))
		return
	}
	r.addWarnings(&resp.Diagnostics, warnings)

	id, _ := conn["id"].(string)
	if id == "" {
		resp.Diagnostics.AddError("Error creating VPN IPsec connection", "the create response did not include a connection id")
		return
	}

	// Save the id (and the key version) into state BEFORE the apply wait, so a
	// failed apply or a timeout still tracks the connection for a destroy.
	state := vpnIpsecStateFromAPI(conn, plan)
	state.RemoteSettings = types.StringNull()
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
	setPSKVersion(ctx, resp.Private, conn, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	final, err := r.waitApplied(ctx, gwID, id)
	if final != nil {
		state = vpnIpsecStateFromAPI(final, plan)
	}
	state.RemoteSettings = r.remoteSettings(ctx, gwID, id, types.StringNull(), &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error applying VPN IPsec connection", err))
	}
}

func (r *vpnIpsecConnectionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state vpnIpsecConnectionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	gwID, id := state.VPNGatewayID.ValueString(), state.ID.ValueString()

	obj, err := r.client.GetVpnIpsecConnection(ctx, gwID, id)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("Error reading VPN IPsec connection", err))
		return
	}

	next := vpnIpsecStateFromAPI(obj, state)

	// Rotation done outside Terraform: the server's key version moved past the one
	// this state last wrote. Drop the stored key so the next plan sends the
	// configured one again (psk is Required, so the plan shows an in-place update).
	if recorded, d := req.Private.GetKey(ctx, vpnIpsecPSKVersionKey); !d.HasError() && len(recorded) > 0 {
		if v, ok := pskVersionOf(obj); ok && string(recorded) != v && !next.PSK.IsNull() {
			next.PSK = types.StringNull()
			resp.Diagnostics.AddWarning("VPN IPsec pre-shared key changed outside Terraform",
				fmt.Sprintf("the pre-shared key of connection %q was changed outside Terraform; the next apply sets it back to the configured value.", state.Name.ValueString()))
		}
	}

	next.RemoteSettings = r.remoteSettings(ctx, gwID, id, state.RemoteSettings, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, next)...)
}

func (r *vpnIpsecConnectionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state vpnIpsecConnectionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	timeout, diags := plan.Timeouts.Update(ctx, vpnIpsecDefaultUpdateTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	gwID, id := state.VPNGatewayID.ValueString(), state.ID.ValueString()
	body, d := vpnIpsecUpdateBody(ctx, plan, state)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}

	var updated map[string]any
	if len(body) > 0 {
		conn, warnings, err := r.updateWithInstallWait(ctx, gwID, id, body)
		if err != nil {
			resp.Diagnostics.Append(diagFromErr("Error updating VPN IPsec connection", err))
			return
		}
		r.addWarnings(&resp.Diagnostics, warnings)
		updated = conn
		if _, sentPSK := body["psk"]; sentPSK {
			setPSKVersion(ctx, resp.Private, conn, &resp.Diagnostics)
		}
	}

	final, err := r.waitApplied(ctx, gwID, id)
	if final == nil {
		final = updated
	}
	if final == nil {
		resp.Diagnostics.Append(diagFromErr("Error reading VPN IPsec connection after update", err))
		return
	}
	next := vpnIpsecStateFromAPI(final, plan)
	next.RemoteSettings = r.remoteSettings(ctx, gwID, id, state.RemoteSettings, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, next)...)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error applying VPN IPsec connection", err))
	}
}

func (r *vpnIpsecConnectionResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state vpnIpsecConnectionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, diags := state.Timeouts.Delete(ctx, vpnIpsecDefaultDeleteTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	gwID := state.VPNGatewayID.ValueString()
	if err := r.client.DeleteVpnIpsecConnection(ctx, gwID, state.ID.ValueString()); err != nil {
		// A connection-level 404 may be the answer to a replayed DELETE whose first
		// attempt was recorded (a 5xx the client retried), so keep waiting for the
		// gateway; a gateway-level 404 is handled by waitGatewayApplied.
		if !client.IsNotFound(err) {
			resp.Diagnostics.Append(diagFromErr("Error deleting VPN IPsec connection", err))
			return
		}
	}

	// The row is gone from every read; wait for the gateway to confirm the removal
	// so the connection's networks are free again for a replacement. A failure or
	// a timeout here does not bring the row back, so it is a warning.
	if err := r.waitGatewayApplied(ctx, gwID); err != nil {
		resp.Diagnostics.AddWarning("VPN IPsec removal not yet confirmed by the gateway",
			fmt.Sprintf("connection %q was deleted but the gateway has not confirmed the removal: %v. "+
				"Its networks stay reserved until it does, so a new connection with the same remote_ts may be refused for a while.",
				state.Name.ValueString(), err))
	}
}

func (r *vpnIpsecConnectionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	gwID, id, ok := strings.Cut(req.ID, "/")
	if !ok || gwID == "" || id == "" || strings.Contains(id, "/") {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Expected import ID in the format \"gateway_id/connection_id\", got: %q. "+
				"IPsec connections are child resources, so both the parent gateway id and the "+
				"connection id are required to import.", req.ID),
		)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("vpn_gateway_id"), gwID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}

// ---------------------------------------------------------------------------
// Transport helpers
// ---------------------------------------------------------------------------

// createWithInstallWait sends the create once per attempt and waits out a 409
// ipsec_installing or a 429 refusal (both definite: nothing was recorded) until
// the deadline.
func (r *vpnIpsecConnectionResource) createWithInstallWait(ctx context.Context, gwID string, body map[string]any) (map[string]any, []string, error) {
	for {
		conn, warnings, err := r.client.CreateVpnIpsecConnection(ctx, gwID, body)
		wait, retry := retryWait(err)
		if !retry {
			return conn, warnings, err
		}
		if werr := sleepCtx(ctx, wait); werr != nil {
			return nil, nil, fmt.Errorf("timed out waiting to retry (the gateway is installing IPsec or the API is rate limiting): %w", err)
		}
	}
}

func (r *vpnIpsecConnectionResource) updateWithInstallWait(ctx context.Context, gwID, id string, body map[string]any) (map[string]any, []string, error) {
	for {
		conn, warnings, err := r.client.UpdateVpnIpsecConnection(ctx, gwID, id, body)
		wait, retry := retryWait(err)
		if !retry {
			return conn, warnings, err
		}
		if werr := sleepCtx(ctx, wait); werr != nil {
			return nil, nil, fmt.Errorf("timed out waiting to retry (the gateway is installing IPsec or the API is rate limiting): %w", err)
		}
	}
}

func isInstalling(err error) bool {
	var apiErr *client.APIError
	return errors.As(err, &apiErr) && apiErr.Reason == client.ReasonIpsecInstalling
}

// vpnIpsecMaxRetryWait bounds one server-named wait (Retry-After).
const vpnIpsecMaxRetryWait = 60 * time.Second

// retryWait reports whether err is a definite refusal that recorded nothing and
// can be retried after a wait: 409 ipsec_installing (poll interval) or 429
// (Retry-After when the API names one, bounded). The wait runs inside the
// operation's own timeout.
func retryWait(err error) (time.Duration, bool) {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		return 0, false
	}
	switch {
	case apiErr.Reason == client.ReasonIpsecInstalling:
		return pollInterval(), true
	case apiErr.Status == http.StatusTooManyRequests:
		d := apiErr.RetryAfter
		if d <= 0 {
			d = pollInterval()
		}
		if d > vpnIpsecMaxRetryWait {
			d = vpnIpsecMaxRetryWait
		}
		return d, true
	}
	return 0, false
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// vpnIpsecUncertain reports whether a create got no definite answer: a transport
// error, a context expiry, or a 5xx. A 4xx refusal (a 429 included) is definite.
func vpnIpsecUncertain(err error) bool {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status >= http.StatusInternalServerError
	}
	return true
}

// uncertainCreateMessage builds the error for a create that got no definite
// answer. The create is NOT adopted: the API has no idempotency key or client
// token for this route, so a connection of the same name that appeared after the
// pre-send list may belong to another writer, and adopting it would store this
// configuration's key next to somebody else's connection (and a later destroy
// would delete it). The user decides, with the exact import command.
func (r *vpnIpsecConnectionResource) uncertainCreateMessage(gwID, name string, known map[string]bool, cause error) string {
	ctx, cancel := context.WithTimeout(context.Background(), vpnIpsecAdoptTimeout)
	defer cancel()
	conns, _, err := r.client.ListVpnIpsecConnections(ctx, gwID)
	if err != nil {
		return fmt.Sprintf("the create request got no definite answer (%v) and the connection list could not be read (%v). "+
			"The connection may or may not exist: list the gateway's IPsec connections before applying again.", cause, err)
	}
	for _, c := range conns {
		id, _ := c["id"].(string)
		if n, _ := c["name"].(string); n == name && id != "" && !known[id] {
			return fmt.Sprintf("the create request got no definite answer (%v). A connection named %q (id %s) now exists on gateway %s, "+
				"but this provider cannot prove that it is the one this request created (the API has no request token). "+
				"If it is yours, adopt it with: terraform import <resource address> %s/%s (the key is then sent by the next apply). "+
				"If it is not, apply again.", cause, name, id, gwID, gwID, id)
		}
	}
	return fmt.Sprintf("the create request got no definite answer (%v) and no connection named %q exists on the gateway yet. "+
		"It was probably not recorded; apply again (a late-recorded connection would then be refused as name_taken).", cause, name)
}

// waitApplied polls the connection until the gateway has applied the latest
// generation. It returns the last connection object seen even when it fails.
func (r *vpnIpsecConnectionResource) waitApplied(ctx context.Context, gwID, id string) (map[string]any, error) {
	var last map[string]any
	err := waiter.WaitFor(ctx, waiter.Options{
		Interval: pollInterval(),
		Refresh: func() (string, bool, error) {
			obj, err := r.client.GetVpnIpsecConnection(ctx, gwID, id)
			if err != nil {
				return "", false, err
			}
			last = obj
			state, _ := obj["apply_state"].(string)
			switch state {
			case "applied":
				return state, true, nil
			case "failed":
				return state, false, fmt.Errorf("the gateway failed to apply the change%s", r.applyErrorSuffix(ctx, gwID))
			}
			// The gateway LIST poll is required: a failed IPsec install never flips
			// the connection's apply_state (the install state is separate), so a
			// pending change would otherwise wait for the whole timeout.
			if _, gw, err := r.client.ListVpnIpsecConnections(ctx, gwID); err == nil && gw != nil {
				if is, _ := gw["ipsec_state"].(string); is == "failed" {
					return state, false, fmt.Errorf("installing IPsec on the gateway failed (ipsec_state failed%s)", gatewayApplyCode(gw))
				}
			}
			return state, false, nil
		},
	})
	return last, err
}

func gatewayApplyCode(gw map[string]any) string {
	if code, _ := gw["ipsec_apply_error"].(string); code != "" {
		return ", " + code
	}
	return ""
}

func (r *vpnIpsecConnectionResource) applyErrorSuffix(ctx context.Context, gwID string) string {
	_, gw, err := r.client.ListVpnIpsecConnections(ctx, gwID)
	if err != nil || gw == nil {
		return ""
	}
	if code, _ := gw["ipsec_apply_error"].(string); code != "" {
		return " (" + code + ")"
	}
	return ""
}

// waitGatewayApplied waits until the gateway's applied generation has caught up
// with its generation (every change, a removal included, confirmed by the node).
func (r *vpnIpsecConnectionResource) waitGatewayApplied(ctx context.Context, gwID string) error {
	return waiter.WaitFor(ctx, waiter.Options{
		Interval: pollInterval(),
		Refresh: func() (string, bool, error) {
			_, gw, err := r.client.ListVpnIpsecConnections(ctx, gwID)
			if err != nil {
				if client.IsNotFound(err) {
					return "gone", true, nil
				}
				return "", false, err
			}
			state, _ := gw["ipsec_apply_state"].(string)
			if state == "failed" {
				code, _ := gw["ipsec_apply_error"].(string)
				return state, false, fmt.Errorf("the gateway failed to apply the change (%s)", code)
			}
			gen, ok1 := numberOf(gw["ipsec_generation"])
			applied, ok2 := numberOf(gw["ipsec_applied_generation"])
			if ok1 && ok2 && applied >= gen {
				return "applied", true, nil
			}
			return state, false, nil
		},
	})
}

func numberOf(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int64:
		return n, true
	case int:
		return int64(n), true
	}
	return 0, false
}

// remoteSettings fetches the remote settings as a canonical JSON string. A
// failure is a warning and keeps the prior value (null when there is none).
func (r *vpnIpsecConnectionResource) remoteSettings(ctx context.Context, gwID, id string, prior types.String, diags *diag.Diagnostics) types.String {
	obj, err := r.client.GetVpnIpsecRemoteSettings(ctx, gwID, id)
	if err != nil {
		diags.AddWarning("Could not read the VPN IPsec remote settings", err.Error())
		return prior
	}
	b, err := json.Marshal(obj)
	if err != nil {
		diags.AddWarning("Could not encode the VPN IPsec remote settings", err.Error())
		return prior
	}
	return types.StringValue(string(b))
}

func (r *vpnIpsecConnectionResource) addWarnings(diags *diag.Diagnostics, codes []string) {
	for _, c := range codes {
		switch c {
		case "security_group_blocks_esp":
			diags.AddWarning("Security groups cannot admit raw ESP",
				"udp_encapsulation is off but the gateway has security groups attached. Security groups cannot "+
					"admit raw ESP (IP protocol 50), so the tunnel will not carry traffic. Enable udp_encapsulation "+
					"or remove the security groups.")
		default:
			diags.AddWarning("VPN IPsec connection warning", c)
		}
	}
}

// ---------------------------------------------------------------------------
// Private state: the key version
// ---------------------------------------------------------------------------

// pskVersionOf returns the connection's psk_version as a string.
func pskVersionOf(obj map[string]any) (string, bool) {
	n, ok := numberOf(obj["psk_version"])
	if !ok {
		return "", false
	}
	return strconv.FormatInt(n, 10), true
}

// privateSetter is the part of the framework's private-state handle used here.
type privateSetter interface {
	SetKey(ctx context.Context, key string, value []byte) diag.Diagnostics
}

func setPSKVersion(ctx context.Context, p privateSetter, conn map[string]any, diags *diag.Diagnostics) {
	if v, ok := pskVersionOf(conn); ok {
		diags.Append(p.SetKey(ctx, vpnIpsecPSKVersionKey, []byte(v))...)
	}
}

// ---------------------------------------------------------------------------
// Request bodies
// ---------------------------------------------------------------------------

func sortedStrings(ctx context.Context, set types.Set) ([]string, diag.Diagnostics) {
	out, d := stringsFromSet(ctx, set)
	sort.Strings(out)
	return out, d
}

// vpnIpsecCreateBody sends name, psk and remote_ts plus every attribute the
// configuration set (unknown and null ones are left to the server defaults).
func vpnIpsecCreateBody(ctx context.Context, plan vpnIpsecConnectionModel) (map[string]any, diag.Diagnostics) {
	var diags diag.Diagnostics
	body := map[string]any{
		"name": plan.Name.ValueString(),
		"psk":  plan.PSK.ValueString(),
	}
	rts, d := sortedStrings(ctx, plan.RemoteTS)
	diags.Append(d...)
	body["remote_ts"] = rts

	setStr := func(key string, v types.String) {
		if !v.IsNull() && !v.IsUnknown() {
			body[key] = v.ValueString()
		}
	}
	setInt := func(key string, v types.Int64) {
		if !v.IsNull() && !v.IsUnknown() {
			body[key] = v.ValueInt64()
		}
	}
	setBool := func(key string, v types.Bool) {
		if !v.IsNull() && !v.IsUnknown() {
			body[key] = v.ValueBool()
		}
	}
	setStr("remote_address", plan.RemoteAddress)
	setStr("local_id_type", plan.LocalIDType)
	setStr("local_id", plan.LocalID)
	setStr("remote_id_type", plan.RemoteIDType)
	setStr("remote_id", plan.RemoteID)
	setInt("ike_version", plan.IKEVersion)
	setStr("proposal_preset", plan.ProposalPreset)
	setStr("ike_proposal", plan.IKEProposal)
	setStr("esp_proposal", plan.ESPProposal)
	if !plan.LocalTS.IsNull() && !plan.LocalTS.IsUnknown() {
		lts, d := sortedStrings(ctx, plan.LocalTS)
		diags.Append(d...)
		body["local_ts"] = lts
	}
	setStr("role", plan.Role)
	setInt("dpd_delay", plan.DPDDelay)
	setStr("dpd_action", plan.DPDAction)
	setInt("ike_lifetime", plan.IKELifetime)
	setInt("child_lifetime", plan.ChildLifetime)
	setBool("udp_encapsulation", plan.UDPEncapsulation)
	setBool("enabled", plan.Enabled)
	return body, diags
}

// vpnIpsecUpdateBody sends ONLY the fields that differ from state (the API merges
// the partial body with the stored connection; an omitted psk keeps the key). A
// nullable attribute removed from configuration is sent as null. The key is sent
// when the configured value differs from the state's (state holds no key after an
// import or after a rotation was detected).
func vpnIpsecUpdateBody(ctx context.Context, plan, state vpnIpsecConnectionModel) (map[string]any, diag.Diagnostics) {
	var diags diag.Diagnostics
	body := map[string]any{}

	str := func(key string, p, s types.String, nullable bool) {
		if p.IsUnknown() || p.Equal(s) {
			return
		}
		if p.IsNull() {
			if nullable {
				body[key] = nil
			}
			return
		}
		body[key] = p.ValueString()
	}
	num := func(key string, p, s types.Int64) {
		if p.IsUnknown() || p.IsNull() || p.Equal(s) {
			return
		}
		body[key] = p.ValueInt64()
	}
	flag := func(key string, p, s types.Bool) {
		if p.IsUnknown() || p.IsNull() || p.Equal(s) {
			return
		}
		body[key] = p.ValueBool()
	}
	set := func(key string, p, s types.Set) {
		if p.IsUnknown() || p.IsNull() || p.Equal(s) {
			return
		}
		v, d := sortedStrings(ctx, p)
		diags.Append(d...)
		body[key] = v
	}

	str("name", plan.Name, state.Name, false)
	str("remote_address", plan.RemoteAddress, state.RemoteAddress, true)
	str("local_id_type", plan.LocalIDType, state.LocalIDType, false)
	str("local_id", plan.LocalID, state.LocalID, true)
	str("remote_id_type", plan.RemoteIDType, state.RemoteIDType, false)
	str("remote_id", plan.RemoteID, state.RemoteID, true)
	num("ike_version", plan.IKEVersion, state.IKEVersion)
	str("proposal_preset", plan.ProposalPreset, state.ProposalPreset, false)
	str("ike_proposal", plan.IKEProposal, state.IKEProposal, true)
	str("esp_proposal", plan.ESPProposal, state.ESPProposal, true)
	set("local_ts", plan.LocalTS, state.LocalTS)
	set("remote_ts", plan.RemoteTS, state.RemoteTS)
	str("role", plan.Role, state.Role, false)
	num("dpd_delay", plan.DPDDelay, state.DPDDelay)
	str("dpd_action", plan.DPDAction, state.DPDAction, false)
	num("ike_lifetime", plan.IKELifetime, state.IKELifetime)
	num("child_lifetime", plan.ChildLifetime, state.ChildLifetime)
	flag("udp_encapsulation", plan.UDPEncapsulation, state.UDPEncapsulation)
	flag("enabled", plan.Enabled, state.Enabled)
	if !plan.PSK.IsNull() && !plan.PSK.IsUnknown() && !plan.PSK.Equal(state.PSK) {
		body["psk"] = plan.PSK.ValueString()
	}
	return body, diags
}

// ---------------------------------------------------------------------------
// State from the API
// ---------------------------------------------------------------------------

func vpnIpsecStateFromAPI(obj map[string]any, prior vpnIpsecConnectionModel) vpnIpsecConnectionModel {
	m := vpnIpsecConnectionModel{
		ID:               stringFromAPI(obj, "id", prior.ID),
		VPNGatewayID:     prior.VPNGatewayID, // from the path
		Name:             stringFromAPI(obj, "name", prior.Name),
		RemoteAddress:    optionalStringFromAPI(obj, "remote_address", prior.RemoteAddress),
		LocalIDType:      stringFromAPI(obj, "local_id_type", prior.LocalIDType),
		LocalID:          optionalStringFromAPI(obj, "local_id", prior.LocalID),
		RemoteIDType:     stringFromAPI(obj, "remote_id_type", prior.RemoteIDType),
		RemoteID:         optionalStringFromAPI(obj, "remote_id", prior.RemoteID),
		PSK:              prior.PSK, // write-only; never in a response
		IKEVersion:       int64FromAPI(obj, "ike_version", prior.IKEVersion),
		ProposalPreset:   stringFromAPI(obj, "proposal_preset", prior.ProposalPreset),
		IKEProposal:      optionalStringFromAPI(obj, "ike_proposal", prior.IKEProposal),
		ESPProposal:      optionalStringFromAPI(obj, "esp_proposal", prior.ESPProposal),
		LocalTS:          stringSetFromAPI(obj, "local_ts", prior.LocalTS),
		RemoteTS:         stringSetFromAPI(obj, "remote_ts", prior.RemoteTS),
		Role:             stringFromAPI(obj, "role", prior.Role),
		DPDDelay:         int64FromAPI(obj, "dpd_delay", prior.DPDDelay),
		DPDAction:        stringFromAPI(obj, "dpd_action", prior.DPDAction),
		IKELifetime:      int64FromAPI(obj, "ike_lifetime", prior.IKELifetime),
		ChildLifetime:    int64FromAPI(obj, "child_lifetime", prior.ChildLifetime),
		UDPEncapsulation: boolFromIntAPI(obj, "udp_encapsulation", prior.UDPEncapsulation),
		Enabled:          boolFromIntAPI(obj, "enabled", prior.Enabled),
		ConnName:         stringFromAPI(obj, "conn_name", prior.ConnName),
		SAState:          stringFromAPI(obj, "sa_state", prior.SAState),
		ApplyState:       stringFromAPI(obj, "apply_state", prior.ApplyState),
		LastErrorCode:    optionalStringFromAPI(obj, "last_error_code", types.StringNull()),
		LastError:        optionalStringFromAPI(obj, "last_error", types.StringNull()),
		RemoteSettings:   prior.RemoteSettings,
		Timeouts:         prior.Timeouts,
	}
	return m
}
