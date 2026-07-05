package resources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hypervisor-io/terraform-provider-iaas/internal/client"
)

// Interface assertions - iaas_instance_vpc_attachment is a STANDALONE resource
// keyed on instance_id (Gap G3). An instance can have AT MOST ONE VPC
// interface (InstanceService::createVpcInterface refuses a second type=vpc
// interface), so this is NOT a child-of-instance list resource - it is a
// singleton per instance, with no id of its own beyond the instance's.
//
// DEVIATIONS FROM THE ORIGINAL TASK SKETCH (`{instance_id, vpc_id?,
// subnet_id?, ips (set, computed+optional), primary_ip}`), both forced by the
// real controller (InstanceVpcController/InstanceService, verified by
// reading the Master source directly):
//
//  1. vpc_id AND vpc_subnet_id are both REQUIRED (not optional) on enable -
//     EnableVpcRequest validates `vpc_id => required|uuid|exists:vpc,id` and
//     `vpc_subnet_id => required|uuid|exists:vpc_subnets,id`; there is no
//     default/auto-selected subnet. Both are RequiresReplace: changing either
//     is only achievable via disable+enable (a full detach/reattach), which a
//     plain in-place Update cannot express safely (the instance would be
//     briefly VPC-less mid-apply on error), so it is modelled as a replace.
//  2. A single "ips" attribute cannot be BOTH Optional (user-supplied) and
//     Computed (accurately reflecting the server) at once, because `enable`
//     ALWAYS auto-assigns the LOWEST FREE ip in the subnet as the instance's
//     first (primary) ip - there is no request field to choose or omit it.
//     If "ips" were Optional+Computed and a user's first config didn't happen
//     to already name that unpredictable address, every subsequent plan would
//     show a perpetual diff trying to remove "drift" that is actually the
//     server's own mandatory behavior (and the API refuses to remove an
//     instance's LAST ip via DELETE ip/{id} - "Cannot remove the last VPC IP.
//     Disable VPC instead" - making that diff unresolvable, not just noisy).
//     This resource instead splits the concept into three attributes:
//     - auto_assigned_ip (Computed only): the server-chosen address from
//     enable, informational and never targeted by add/remove.
//     - additional_ips (Optional, set of dotted-quad strings): the SECONDARY
//     addresses this resource explicitly attaches/detaches via ip/add and
//     DELETE ip/{id}, diffed the same way iaas_nat_gateway diffs
//     subnet_ids.
//     - ips (Computed, set of dotted-quad strings): the full realized set
//     (auto_assigned_ip ∪ additional_ips) for convenient downstream
//     reference (e.g. firewall rules), rebuilt from the API on every Read.
//
// Async note: enable/disable additionally fire an un-awaited hot-(re)configure
// command to the hypervisor when the instance is currently running - but
// every DB write this resource's Read observes (instances.vpc_id/
// vpc_subnet_id, vpc_subnet_ips rows) commits synchronously before the HTTP
// response returns, and neither endpoint returns a task_id to poll even when
// one is enqueued. So this resource does NOT use internal/waiter: DB-level
// state is safe to treat as synchronous, and there is no task handle to wait
// on for the live-guest-network side even if it wanted to.
var (
	_ resource.Resource                = &instanceVpcAttachmentResource{}
	_ resource.ResourceWithConfigure   = &instanceVpcAttachmentResource{}
	_ resource.ResourceWithImportState = &instanceVpcAttachmentResource{}
)

// NewInstanceVpcAttachmentResource is the resource constructor registered
// with the provider.
func NewInstanceVpcAttachmentResource() resource.Resource {
	return &instanceVpcAttachmentResource{}
}

// instanceVpcAttachmentResource manages an instance's single VPC network
// interface attachment.
type instanceVpcAttachmentResource struct {
	client *client.Client
}

// instanceVpcAttachmentModel maps the Terraform state/plan for
// iaas_instance_vpc_attachment. See the package doc comment above for why
// auto_assigned_ip / additional_ips / ips are split the way they are.
type instanceVpcAttachmentModel struct {
	ID          types.String `tfsdk:"id"`
	InstanceID  types.String `tfsdk:"instance_id"`
	VPCID       types.String `tfsdk:"vpc_id"`
	VPCSubnetID types.String `tfsdk:"vpc_subnet_id"`

	AdditionalIPs types.Set    `tfsdk:"additional_ips"`
	PrimaryIP     types.String `tfsdk:"primary_ip"`

	AutoAssignedIP types.String `tfsdk:"auto_assigned_ip"`
	IPs            types.Set    `tfsdk:"ips"`
}

// Metadata sets the resource type name → "<provider>_instance_vpc_attachment".
func (r *instanceVpcAttachmentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_instance_vpc_attachment"
}

// Schema describes the iaas_instance_vpc_attachment resource.
func (r *instanceVpcAttachmentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Attaches an instance to a VPC subnet (the instance's single VPC network " +
			"interface). An instance may have AT MOST ONE VPC interface at a time - enabling a " +
			"second one fails server-side - so this is a STANDALONE resource keyed on instance_id, " +
			"not a nested block on iaas_instance. Enabling ALWAYS auto-assigns the lowest free ip " +
			"in vpc_subnet_id as the instance's first (primary) ip; that address is exposed " +
			"read-only as auto_assigned_ip. Any further secondary addresses are managed via " +
			"additional_ips (an order-independent set of free addresses from the subnet's pool), " +
			"and primary_ip selects which attached address is primary. Destroying this resource " +
			"disables the VPC entirely (releases every attached ip).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
				Description: "Same value as instance_id. The attachment has no id of its own - it " +
					"is identified entirely by which instance it belongs to.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"instance_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the instance to attach a VPC to. Also acts as this resource's " +
					"unique key (an instance has at most one VPC interface). Changing it forces a " +
					"new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"vpc_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the VPC to attach. Must be owned by the instance's account and " +
					"available in the instance's hypervisor group. Required by the enable endpoint " +
					"(there is no default/auto-selected VPC); changing it forces a new resource " +
					"(disable, then re-enable).",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"vpc_subnet_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the VPC subnet to place the instance's vpc interface in. A " +
					"PRIVATE subnet cannot be used if the instance already has a public IP. " +
					"Changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"additional_ips": schema.SetAttribute{
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
				Description: "Extra dotted-quad addresses - beyond the server auto-assigned " +
					"auto_assigned_ip - to attach from vpc_subnet_id's free pool, as an " +
					"order-independent set. Adding or removing an address here attaches or detaches " +
					"it in place (the API's ip/add and DELETE ip/{id} endpoints). Each address must " +
					"currently be FREE in the subnet's pool; one already in use, or outside the " +
					"subnet, fails at apply time with a clear error. Never list auto_assigned_ip " +
					"here: it is tracked separately and the API itself refuses to remove an " +
					"instance's LAST vpc ip (destroy this resource instead to release everything). " +
					"Modelled Optional+Computed (like iaas_instance's hostname) so an omitted value " +
					"- meaning \"just the auto-assigned ip, no extras\" - round-trips cleanly instead " +
					"of tripping a plan-time consistency error.",
			},
			"primary_ip": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "Which attached address (auto_assigned_ip, or one of additional_ips) " +
					"should be marked primary. Defaults to the server auto-assigned ip when omitted. " +
					"Must reference an address that is already attached - add it via additional_ips " +
					"in the same apply (or an earlier one) before naming it here.",
			},
			"auto_assigned_ip": schema.StringAttribute{
				Computed: true,
				Description: "The dotted-quad address the API auto-assigned (the lowest free ip in " +
					"vpc_subnet_id at the time) when the VPC was enabled. Not user-controlled, and " +
					"not independently removable - destroy this resource to release it.",
			},
			"ips": schema.SetAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "Every address currently attached to the instance's vpc interface " +
					"(auto_assigned_ip plus additional_ips), as observed from the API. Read-only " +
					"convenience output.",
			},
		},
	}
}

// Configure pulls the shared *client.Client from the provider (nil-guarded).
func (r *instanceVpcAttachmentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create enables the VPC on the instance, discovers the server auto-assigned
// ip, attaches any configured additional_ips, applies a configured primary_ip
// override, then reads back the full attachment state.
func (r *instanceVpcAttachmentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan instanceVpcAttachmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	instanceID := plan.InstanceID.ValueString()

	if _, err := r.client.EnableInstanceVpc(ctx, instanceID, plan.VPCID.ValueString(), plan.VPCSubnetID.ValueString()); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error enabling VPC on instance", err))
		return
	}

	// Persist the id immediately so a subsequent failure (add/primary) still
	// leaves a destroyable resource (disable is always safe to retry/call).
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), instanceID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("instance_id"), instanceID)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Discover the auto-assigned ip: enable always attaches exactly one.
	rows, err := r.client.ListInstanceVpcIPs(ctx, instanceID)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading VPC attachment after enable", err))
		return
	}
	if len(rows) == 0 {
		resp.Diagnostics.AddError(
			"Error enabling VPC on instance",
			"enable succeeded but no ip was auto-assigned; this is unexpected - please report it",
		)
		return
	}
	autoIP, _ := rows[0]["ip"].(string)

	// Attach the requested additional ips, resolving each address to a free
	// pool row id via the available-ips listing.
	desired := stringSetValues(plan.AdditionalIPs)
	for _, addr := range desired {
		if addr == autoIP {
			continue // already attached (and primary) from enable
		}
		id, err := r.resolveAvailableIPID(ctx, instanceID, addr)
		if err != nil {
			resp.Diagnostics.Append(diagFromErr("Error adding VPC ip to instance", err))
			return
		}
		if _, err := r.client.AddInstanceVpcIP(ctx, instanceID, map[string]any{"ip_id": id}); err != nil {
			resp.Diagnostics.Append(diagFromErr("Error adding VPC ip to instance", err))
			return
		}
	}

	// Apply a configured primary override (no-op when it targets the
	// already-primary auto-assigned ip).
	if wantPrimary := plan.PrimaryIP; !wantPrimary.IsNull() && !wantPrimary.IsUnknown() &&
		wantPrimary.ValueString() != "" && wantPrimary.ValueString() != autoIP {
		if err := r.setPrimaryByAddress(ctx, instanceID, wantPrimary.ValueString()); err != nil {
			resp.Diagnostics.Append(diagFromErr("Error setting primary VPC ip", err))
			return
		}
	}

	state, notFound, diags := r.readState(ctx, instanceID, plan)
	resp.Diagnostics.Append(diags...)
	if notFound {
		resp.Diagnostics.AddError("Error creating VPC attachment", "the attachment disappeared immediately after creation")
		return
	}
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Read refreshes state from the API. An empty vpc/ips listing means no VPC is
// attached (the instance was detached out of band) - remove it from state so
// Terraform plans a recreate.
func (r *instanceVpcAttachmentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state instanceVpcAttachmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	newState, notFound, diags := r.readState(ctx, state.InstanceID.ValueString(), state)
	if notFound {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

// Update diffs additional_ips (add new / remove dropped, by address) and
// applies a primary_ip change, in that order - additions and removals settle
// the attached set BEFORE a primary override is applied, so the final primary
// is deterministic regardless of any transient auto-reassignment the API
// performs internally when the previously-primary ip is removed.
func (r *instanceVpcAttachmentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state instanceVpcAttachmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	instanceID := state.InstanceID.ValueString()

	plannedAdditional := stringSetValues(plan.AdditionalIPs)
	stateAdditional := stringSetValues(state.AdditionalIPs)
	plannedSet := toAddressSet(plannedAdditional)
	stateSet := toAddressSet(stateAdditional)

	// Add addresses newly present in the plan.
	for _, addr := range plannedAdditional {
		if _, exists := stateSet[addr]; exists {
			continue
		}
		id, err := r.resolveAvailableIPID(ctx, instanceID, addr)
		if err != nil {
			resp.Diagnostics.Append(diagFromErr("Error adding VPC ip to instance", err))
			return
		}
		if _, err := r.client.AddInstanceVpcIP(ctx, instanceID, map[string]any{"ip_id": id}); err != nil {
			resp.Diagnostics.Append(diagFromErr("Error adding VPC ip to instance", err))
			return
		}
	}

	// Remove addresses dropped from the plan. This never targets
	// auto_assigned_ip, since that address never appears in additional_ips by
	// construction (see instanceVpcAttachmentStateFromAPI).
	if len(stateAdditional) > 0 {
		rows, err := r.client.ListInstanceVpcIPs(ctx, instanceID)
		if err != nil {
			resp.Diagnostics.Append(diagFromErr("Error reading VPC attachment before removal", err))
			return
		}
		for _, addr := range stateAdditional {
			if _, keep := plannedSet[addr]; keep {
				continue
			}
			row := findIPRowByAddress(rows, addr)
			if row == nil {
				continue // already gone (drift) - nothing to do
			}
			id, _ := row["id"].(string)
			if id == "" {
				continue
			}
			if err := r.client.RemoveInstanceVpcIP(ctx, instanceID, id); err != nil {
				resp.Diagnostics.Append(diagFromErr("Error removing VPC ip from instance", err))
				return
			}
		}
	}

	// Apply a primary change last, once the attached set has settled.
	if !plan.PrimaryIP.Equal(state.PrimaryIP) && !plan.PrimaryIP.IsNull() && !plan.PrimaryIP.IsUnknown() &&
		plan.PrimaryIP.ValueString() != "" {
		if err := r.setPrimaryByAddress(ctx, instanceID, plan.PrimaryIP.ValueString()); err != nil {
			resp.Diagnostics.Append(diagFromErr("Error setting primary VPC ip", err))
			return
		}
	}

	newState, notFound, diags := r.readState(ctx, instanceID, plan)
	resp.Diagnostics.Append(diags...)
	if notFound {
		resp.Diagnostics.AddError("Error updating VPC attachment", "the attachment disappeared during update")
		return
	}
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

// Delete disables the VPC on the instance, releasing every attached ip.
func (r *instanceVpcAttachmentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state instanceVpcAttachmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DisableInstanceVpc(ctx, state.InstanceID.ValueString()); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error disabling VPC on instance", err))
		return
	}
}

// ImportState lets `terraform import iaas_instance_vpc_attachment.x <instance_id>`
// adopt an existing attachment. vpc_id/vpc_subnet_id/auto_assigned_ip/
// additional_ips/primary_ip/ips are all populated by the subsequent Read (they
// are all derivable from GET .../vpc/ips, unlike a pure path parameter such as
// a cluster_id that never appears in any response body).
//
// KNOWN LIMITATION: on a fresh import there is no prior state to tell
// auto_assigned_ip apart from additional_ips (the API does not record which
// ip came from enable vs a later ip/add) - readState falls back to treating
// the CURRENT primary address as auto_assigned_ip in that case. If the
// primary has since been moved to a different address than the one enable
// originally assigned, an import will attribute the wrong address to
// auto_assigned_ip (cosmetic only: the full "ips" set is still accurate, and
// this resource never removes auto_assigned_ip on Update anyway).
func (r *instanceVpcAttachmentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if req.ID == "" {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			"Expected the instance UUID (the attachment has no id of its own), got an empty string.",
		)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("instance_id"), req.ID)...)
}

// resolveAvailableIPID looks up addr in the instance's currently attached
// subnet's free-ip pool and returns its vpc_subnet_ips row id (what ip/add
// requires). Returns a descriptive error when addr is not currently free
// there (already in use, outside the subnet, or no VPC attached).
func (r *instanceVpcAttachmentResource) resolveAvailableIPID(ctx context.Context, instanceID, addr string) (string, error) {
	avail, err := r.client.ListInstanceAvailableVpcIPs(ctx, instanceID)
	if err != nil {
		return "", err
	}
	for _, row := range avail {
		if ip, _ := row["ip"].(string); ip == addr {
			if id, _ := row["id"].(string); id != "" {
				return id, nil
			}
		}
	}
	return "", fmt.Errorf(
		"address %q is not currently free in the instance's attached subnet pool "+
			"(it may already be in use, or outside the subnet's cidr)", addr,
	)
}

// setPrimaryByAddress finds addr among the instance's currently attached ips
// and marks it primary (a no-op if it already is). Returns a descriptive
// error when addr is not currently attached at all.
func (r *instanceVpcAttachmentResource) setPrimaryByAddress(ctx context.Context, instanceID, addr string) error {
	rows, err := r.client.ListInstanceVpcIPs(ctx, instanceID)
	if err != nil {
		return err
	}
	row := findIPRowByAddress(rows, addr)
	if row == nil {
		return fmt.Errorf(
			"address %q is not currently attached to the instance (it must be auto_assigned_ip "+
				"or one of additional_ips)", addr,
		)
	}
	if primary, _ := row["is_primary"].(bool); primary {
		return nil // already primary
	}
	id, _ := row["id"].(string)
	if id == "" {
		return fmt.Errorf("attached ip %q has no server id in the response", addr)
	}
	return r.client.SetPrimaryInstanceVpcIP(ctx, instanceID, id)
}

// readState fetches the instance's attached vpc ips and builds the full
// model. An empty listing means no VPC is attached (never enabled, or
// detached out of band) - the bool return signals the caller to
// RemoveResource; in that case the returned diagnostics are empty.
func (r *instanceVpcAttachmentResource) readState(ctx context.Context, instanceID string, prior instanceVpcAttachmentModel) (instanceVpcAttachmentModel, bool, diag.Diagnostics) {
	rows, err := r.client.ListInstanceVpcIPs(ctx, instanceID)
	if err != nil {
		if client.IsNotFound(err) {
			return instanceVpcAttachmentModel{}, true, nil
		}
		var diags diag.Diagnostics
		diags.Append(diagFromErr("Error reading VPC attachment", err))
		return instanceVpcAttachmentModel{}, false, diags
	}
	if len(rows) == 0 {
		return instanceVpcAttachmentModel{}, true, nil
	}

	m, diags := instanceVpcAttachmentStateFromAPI(ctx, rows, prior)
	m.ID = types.StringValue(instanceID)
	m.InstanceID = types.StringValue(instanceID)
	return m, false, diags
}

// instanceVpcAttachmentStateFromAPI builds the model from the vpc/ips LIST
// response. vpc_id/vpc_subnet_id come from the first row (every row shares the
// same subnet, since an instance has at most one); auto_assigned_ip is
// preserved from prior state when it is still attached, falling back to the
// current primary address (then to the first row) when there is no prior
// value to preserve (fresh import) or it is no longer attached (drift).
// additional_ips is every attached address EXCEPT auto_assigned_ip.
func instanceVpcAttachmentStateFromAPI(ctx context.Context, rows []map[string]any, prior instanceVpcAttachmentModel) (instanceVpcAttachmentModel, diag.Diagnostics) {
	var diags diag.Diagnostics

	first := rows[0]
	vpcSubnetID := stringOrPrior(first, "vpc_subnet_id", prior.VPCSubnetID)
	vpcID := prior.VPCID
	if subnet, ok := first["subnet"].(map[string]any); ok {
		if v, ok := subnet["vpc_id"].(string); ok && v != "" {
			vpcID = types.StringValue(v)
		}
	}

	addrs := make([]string, 0, len(rows))
	byAddr := make(map[string]map[string]any, len(rows))
	primaryAddr := ""
	for _, row := range rows {
		addr, _ := row["ip"].(string)
		if addr == "" {
			continue
		}
		addrs = append(addrs, addr)
		byAddr[addr] = row
		if isPrimary, _ := row["is_primary"].(bool); isPrimary {
			primaryAddr = addr
		}
	}

	autoIP := prior.AutoAssignedIP.ValueString()
	if autoIP == "" || byAddr[autoIP] == nil {
		// No prior value to preserve (fresh create/import), or it is no
		// longer attached (drift): fall back to the current primary, then to
		// an arbitrary attached row.
		autoIP = primaryAddr
		if autoIP == "" && len(addrs) > 0 {
			autoIP = addrs[0]
		}
	}

	additional := make([]string, 0, len(addrs))
	for _, a := range addrs {
		if a != autoIP {
			additional = append(additional, a)
		}
	}

	additionalSet, d := stringSetOrNull(ctx, additional, prior.AdditionalIPs)
	diags.Append(d...)
	ipsSet, d := stringSetKnown(ctx, addrs)
	diags.Append(d...)

	return instanceVpcAttachmentModel{
		VPCID:          vpcID,
		VPCSubnetID:    vpcSubnetID,
		AdditionalIPs:  additionalSet,
		PrimaryIP:      types.StringValue(primaryAddr),
		AutoAssignedIP: types.StringValue(autoIP),
		IPs:            ipsSet,
	}, diags
}

// toAddressSet builds a lookup set from a slice of addresses.
func toAddressSet(addrs []string) map[string]struct{} {
	m := make(map[string]struct{}, len(addrs))
	for _, a := range addrs {
		m[a] = struct{}{}
	}
	return m
}

// findIPRowByAddress returns the row in rows whose "ip" field equals addr, or
// nil when absent.
func findIPRowByAddress(rows []map[string]any, addr string) map[string]any {
	for _, row := range rows {
		if ip, _ := row["ip"].(string); ip == addr {
			return row
		}
	}
	return nil
}

// stringSetOrNull builds a types.Set(String) from values, preserving a null
// result when values is empty AND prior was null/unknown - so an omitted
// additional_ips config does not show spurious drift as an empty set.
func stringSetOrNull(ctx context.Context, values []string, prior types.Set) (types.Set, diag.Diagnostics) {
	if len(values) == 0 {
		if prior.IsNull() || prior.IsUnknown() {
			return types.SetNull(types.StringType), nil
		}
		return types.SetValue(types.StringType, []attr.Value{})
	}
	return types.SetValueFrom(ctx, types.StringType, values)
}

// stringSetKnown builds a non-null types.Set(String) from values - used for
// the fully computed "ips" output, which is always Known once the resource
// exists (readState only builds a model when at least one row is attached).
func stringSetKnown(ctx context.Context, values []string) (types.Set, diag.Diagnostics) {
	if len(values) == 0 {
		return types.SetValue(types.StringType, []attr.Value{})
	}
	return types.SetValueFrom(ctx, types.StringType, values)
}
