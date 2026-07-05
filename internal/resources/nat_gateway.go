package resources

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/iaas/terraform-provider-iaas/internal/client"
	"github.com/iaas/terraform-provider-iaas/internal/waiter"
)

// Interface assertions - iaas_nat_gateway is a CHILD + ASYNC resource. It
// combines two established patterns:
//
//   - CHILD (from vpc_subnet): the parent vpc_id lives in the URL path, so it is
//     Required + RequiresReplace, and import takes a COMPOSITE "<vpc_id>/<id>".
//   - ASYNC (from volume): CREATE records the row (status="pending") then waits
//     for the SHOW status to reach "active" via a StatePollerWithErrorTolerance
//     waiter; the id is persisted to state BEFORE the wait so a failed wait still
//     leaves a destroyable resource; a timeouts block is exposed.
//
// In addition it owns a set of attached subnet ids, managed via per-subnet
// attach/detach on diff (the simple string-set variant of the security_group
// instance_ids pattern - here each child is a bare subnet UUID, not a nested
// object, since the API takes/returns plain ids).
var (
	_ resource.Resource                = &natGatewayResource{}
	_ resource.ResourceWithConfigure   = &natGatewayResource{}
	_ resource.ResourceWithImportState = &natGatewayResource{}
)

// NewNATGatewayResource is the resource constructor registered with the provider.
func NewNATGatewayResource() resource.Resource {
	return &natGatewayResource{}
}

// natGatewayResource manages an iaas_nat_gateway - the (single) NAT gateway of a
// VPC, providing outbound internet for the VPC's private subnets.
type natGatewayResource struct {
	client *client.Client
}

// natGatewayModel maps the Terraform state/plan for iaas_nat_gateway.
//
// Field groups:
//   - PARENT path id: vpc_id (Required, RequiresReplace - part of every path).
//   - create inputs: name (Optional+Computed - server defaults to "natgw-<vpc>"),
//     nat_enabled (Optional+Computed - toggled in place via enable/disable or
//     PATCH; server defaults to true), subnet_ids (Optional set - attached private
//     subnets, managed by attach/detach on diff; server defaults to all private
//     subnets when omitted).
//   - computed read-only: status (server-mutable lifecycle), public_ip (the
//     auto-assigned public IP, stable after create).
type natGatewayModel struct {
	ID         types.String `tfsdk:"id"`
	VPCID      types.String `tfsdk:"vpc_id"`
	Name       types.String `tfsdk:"name"`
	NatEnabled types.Bool   `tfsdk:"nat_enabled"`
	SubnetIDs  types.Set    `tfsdk:"subnet_ids"`

	// Computed read-only.
	Status   types.String `tfsdk:"status"`
	PublicIP types.String `tfsdk:"public_ip"`

	Timeouts timeouts.Value `tfsdk:"timeouts"`
}

// Metadata sets the resource type name → "<provider>_nat_gateway".
func (r *natGatewayResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_nat_gateway"
}

// Schema describes the iaas_nat_gateway resource.
func (r *natGatewayResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a VPC NAT gateway - the single egress gateway that gives a VPC's " +
			"PRIVATE subnets outbound internet access. The parent vpc_id is part of the API " +
			"path, so changing it forces a new resource (a VPC has at most one NAT gateway). " +
			"Creation is ASYNCHRONOUS: the gateway record is created (status=\"pending\"), a " +
			"public IP is auto-assigned, and this resource waits for the slave to provision the " +
			"gateway and report status=\"active\". The set of attached private subnets is managed " +
			"in place via subnet_ids (attach/detach on diff). NAT can be toggled in place via " +
			"nat_enabled. The feature must be enabled for the VPC's location; if it is not (or the " +
			"per-account NAT gateway quota is reached, or no public IP is available), the create " +
			"fails with a clear message.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the NAT gateway, assigned by the API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"vpc_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the parent VPC this NAT gateway belongs to. This value is part " +
					"of the API request path, so changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"name": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "Display name for the gateway (lowercase alphanumeric and dashes). " +
					"Defaults to \"natgw-<vpc name>\" when omitted. Updatable in place. Modelled " +
					"Optional+Computed so an omitted name round-trips against the server default " +
					"without showing spurious drift.",
			},
			"nat_enabled": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Description: "Whether NAT (outbound translation) is active on the gateway. Defaults " +
					"to true. Updatable in place (enable/disable). Note: the server may set this to " +
					"false out of band if the gateway is suspended for bandwidth overage, which would " +
					"surface as drift. Re-applying with nat_enabled = true while the gateway is " +
					"bandwidth-suspended will fail with a clear server message - resolve the bandwidth " +
					"issue first, then re-enable. Modelled Optional+Computed so an omitted value " +
					"round-trips against the server default.",
			},
			"subnet_ids": schema.SetAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "UUIDs of the VPC's PRIVATE subnets attached to this NAT gateway, as an " +
					"order-independent set. Adding or removing an id attaches or detaches that subnet " +
					"in place. When omitted at create, the server attaches ALL of the VPC's private " +
					"subnets; the resource then adopts that server-chosen set into state (so leave " +
					"this unset to manage attachments outside Terraform, or set it explicitly to " +
					"manage them here). Only private subnets may be attached.",
			},
			// status is a SERVER-MUTABLE computed field (pending → active, then
			// deleting on teardown), so per the golden guardrail it does NOT use
			// UseStateForUnknown - that would copy the stale prior value into the plan
			// and mask real drift.
			"status": schema.StringAttribute{
				Computed: true,
				Description: "Lifecycle status: \"pending\" (provisioning), \"active\" (ready), " +
					"\"deleting\" (being torn down). Server-mutable.",
			},
			// public_ip is auto-assigned at create and stable thereafter, so
			// UseStateForUnknown is safe (it is derived once and does not change).
			"public_ip": schema.StringAttribute{
				Computed: true,
				Description: "The public IPv4 address auto-assigned to the gateway at create time. " +
					"Stable after creation.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
		Blocks: map[string]schema.Block{
			// Only create is async (waits for status="active"); update/enable/disable/
			// attach/detach/delete are synchronous from the API's perspective, so only
			// the create timeout is truly meaningful - the block still exposes all three
			// for consistency with the async-resource pattern.
			"timeouts": timeouts.Block(ctx, timeouts.Opts{
				Create: true,
				Update: true,
				Delete: true,
			}),
		},
	}
}

// Configure pulls the shared *client.Client from the provider (nil-guarded).
func (r *natGatewayResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create provisions the NAT gateway and waits for it to become active:
//
//  1. CreateNatGateway records the row (status="pending"), auto-assigns a public
//     IP, and attaches the requested (or all private) subnets - all in one call.
//  2. The id is saved into state BEFORE the wait, so a provisioning failure or
//     timeout still tracks the gateway for a subsequent destroy.
//  3. WaitFor polls GetNatGateway until status=="active".
//  4. Read back so state reflects the server-assigned public IP and the (possibly
//     server-defaulted) attached subnet set.
func (r *natGatewayResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan natGatewayModel
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

	body := map[string]any{}
	if !plan.Name.IsNull() && !plan.Name.IsUnknown() {
		body["name"] = plan.Name.ValueString()
	}
	if !plan.NatEnabled.IsNull() && !plan.NatEnabled.IsUnknown() {
		body["nat_enabled"] = plan.NatEnabled.ValueBool()
	}
	// Only send subnet_ids when the user set the set explicitly; an unset (null)
	// set lets the server attach all private subnets (which Read then adopts).
	if !plan.SubnetIDs.IsNull() && !plan.SubnetIDs.IsUnknown() {
		ids, d := stringsFromSet(ctx, plan.SubnetIDs)
		resp.Diagnostics.Append(d...)
		if resp.Diagnostics.HasError() {
			return
		}
		body["subnet_ids"] = ids
	}

	created, err := r.client.CreateNatGateway(ctx, vpcID, body)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating NAT gateway", err))
		return
	}
	id, _ := created["id"].(string)
	if id == "" {
		resp.Diagnostics.AddError("Error creating NAT gateway", "the create response did not include a gateway id")
		return
	}

	// Persist the id (and parent vpc_id) immediately so a failed provisioning/wait
	// still tracks the resource for cleanup on the next destroy. The destroy needs
	// BOTH ids to build the DELETE path.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("vpc_id"), vpcID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// ── ASYNC convergence: poll the gateway SHOW until status="active" ─────────
	// The controller never sets a "failed" terminal status for a NAT gateway, so
	// the fail set is defensive only. Tolerance=3 absorbs transient transport blips
	// during provisioning that bypass the client's 429/5xx retry.
	waitErr := waiter.WaitFor(ctx, waiter.Options{
		Interval: pollInterval(),
		Timeout:  createTimeout,
		Refresh: waiter.StatePollerWithErrorTolerance(
			func() (map[string]any, error) { return r.client.GetNatGateway(ctx, vpcID, id) },
			"status",
			[]string{"active"},
			[]string{"failed", "error"},
			3,
		),
	})
	if waitErr != nil {
		resp.Diagnostics.AddError(
			"Error waiting for NAT gateway provisioning",
			fmt.Sprintf("NAT gateway %s did not become active: %s", id, waitErr.Error()),
		)
		return
	}

	// Read back so state reflects the public IP + the authoritative attached set.
	obj, err := r.client.GetNatGateway(ctx, vpcID, id)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading NAT gateway after provisioning", err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, natGatewayStateFromAPI(obj, plan))...)
}

// Read refreshes state from the API. The parent vpc_id is read from prior state
// to build the request path. A 404 means the gateway (or its VPC) was deleted out
// of band - remove it from state so Terraform plans a recreate.
func (r *natGatewayResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state natGatewayModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	obj, err := r.client.GetNatGateway(ctx, state.VPCID.ValueString(), state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("Error reading NAT gateway", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, natGatewayStateFromAPI(obj, state))...)
}

// Update applies the in-place mutations the API supports:
//
//   - name / nat_enabled: PATCHed when changed. nat_enabled changes prefer the
//     dedicated enable/disable endpoints (which carry the bandwidth-suspension
//     guard) and fall back to nothing else - the PATCH also accepts nat_enabled,
//     but enable/disable is the semantically correct toggle.
//   - subnet_ids: diffed (attach added / detach removed) by id.
//
// vpc_id is RequiresReplace, so it never reaches here. After applying the changes
// the resource Reads back via SHOW to rehydrate computed fields.
func (r *natGatewayResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state natGatewayModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	vpcID := state.VPCID.ValueString()
	id := state.ID.ValueString()

	// name change → PATCH name.
	if !plan.Name.Equal(state.Name) && !plan.Name.IsNull() && !plan.Name.IsUnknown() {
		if _, err := r.client.UpdateNatGateway(ctx, vpcID, id, map[string]any{"name": plan.Name.ValueString()}); err != nil {
			resp.Diagnostics.Append(diagFromErr("Error updating NAT gateway name", err))
			return
		}
	}

	// nat_enabled change → enable/disable endpoint (carries the bandwidth guard).
	if !plan.NatEnabled.Equal(state.NatEnabled) && !plan.NatEnabled.IsNull() && !plan.NatEnabled.IsUnknown() {
		if plan.NatEnabled.ValueBool() {
			if _, err := r.client.EnableNatGateway(ctx, vpcID, id); err != nil {
				resp.Diagnostics.Append(diagFromErr("Error enabling NAT gateway", err))
				return
			}
		} else {
			if _, err := r.client.DisableNatGateway(ctx, vpcID, id); err != nil {
				resp.Diagnostics.Append(diagFromErr("Error disabling NAT gateway", err))
				return
			}
		}
	}

	// subnet_ids diff → attach added / detach removed.
	if !plan.SubnetIDs.Equal(state.SubnetIDs) {
		plannedIDs, d := stringsFromSet(ctx, plan.SubnetIDs)
		resp.Diagnostics.Append(d...)
		stateIDs, d := stringsFromSet(ctx, state.SubnetIDs)
		resp.Diagnostics.Append(d...)
		if resp.Diagnostics.HasError() {
			return
		}

		plannedSet := make(map[string]struct{}, len(plannedIDs))
		for _, s := range plannedIDs {
			plannedSet[s] = struct{}{}
		}
		stateSet := make(map[string]struct{}, len(stateIDs))
		for _, s := range stateIDs {
			stateSet[s] = struct{}{}
		}

		for _, s := range plannedIDs {
			if _, exists := stateSet[s]; !exists {
				if _, err := r.client.AttachNatGatewaySubnet(ctx, vpcID, id, s); err != nil {
					resp.Diagnostics.Append(diagFromErr("Error attaching subnet to NAT gateway", err))
					return
				}
			}
		}
		for _, s := range stateIDs {
			if _, exists := plannedSet[s]; !exists {
				if _, err := r.client.DetachNatGatewaySubnet(ctx, vpcID, id, s); err != nil {
					resp.Diagnostics.Append(diagFromErr("Error detaching subnet from NAT gateway", err))
					return
				}
			}
		}
	}

	// Read back to rehydrate computed fields from the authoritative SHOW.
	obj, err := r.client.GetNatGateway(ctx, vpcID, id)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading NAT gateway after update", err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, natGatewayStateFromAPI(obj, plan))...)
}

// Delete removes the NAT gateway. The controller dispatches a slave teardown
// task, releases the public IP, detaches all subnets, and soft-deletes the row
// immediately, so a subsequent SHOW 404s right away - no delete waiter required.
func (r *natGatewayResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state natGatewayModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteNatGateway(ctx, state.VPCID.ValueString(), state.ID.ValueString()); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deleting NAT gateway", err))
		return
	}
}

// ImportState implements COMPOSITE import for this child resource. The parent
// vpc_id is required to build the API path (and is not derivable from the gateway
// id alone), so `terraform import` must supply BOTH ids joined by a slash:
//
//	terraform import iaas_nat_gateway.x <vpc_id>/<gateway_id>
//
// We split req.ID on the FIRST "/" into vpc_id and gateway_id; the subsequent
// Read hydrates the remaining attributes.
func (r *natGatewayResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	vpcID, gwID, ok := strings.Cut(req.ID, "/")
	if !ok || vpcID == "" || gwID == "" {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Expected import ID in the format \"vpc_id/gateway_id\", got: %q. "+
				"NAT gateways are child resources, so both the parent VPC id and the "+
				"gateway id are required to import.", req.ID),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("vpc_id"), vpcID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), gwID)...)
}

// natGatewayStateFromAPI builds the model from an API gateway object, falling
// back to the prior model's value for fields the response omits. vpc_id is never
// in the response body (it lives in the URL), so it always falls back to prior.
// public_ip is extracted from the nested public_ip{ip} object; subnet_ids is
// rebuilt from the embedded subnets[] array (each element an object with an id).
func natGatewayStateFromAPI(obj map[string]any, prior natGatewayModel) natGatewayModel {
	m := natGatewayModel{
		ID:         stringFromAPI(obj, "id", prior.ID),
		VPCID:      prior.VPCID, // never in the response body; from the path
		Name:       stringFromAPI(obj, "name", prior.Name),
		NatEnabled: boolFromIntAPI(obj, "nat_enabled", prior.NatEnabled),

		Status:   stringFromAPI(obj, "status", prior.Status),
		PublicIP: nestedStringFromAPI(obj, "public_ip", "ip", prior.PublicIP),

		Timeouts: prior.Timeouts,
	}
	m.SubnetIDs = natGatewaySubnetSetFromAPI(obj["subnets"], prior.SubnetIDs)
	return m
}

// natGatewaySubnetSetFromAPI converts the embedded "subnets" JSON array (each
// element an object with an "id") into a types.Set of subnet id strings. When the
// array is absent/empty AND the prior config had a null set, the result stays
// null so an unmanaged-attachments config (subnet_ids omitted) does not show
// drift; otherwise an empty managed set becomes an empty (non-null) set.
//
// Note: when subnet_ids is omitted at create the server attaches all private
// subnets, and this adopts that server-chosen set into state on the post-create
// Read - so the very first plan-after-create would show the attribute moving from
// null to a known set. That is intentional (the resource surfaces what the server
// actually attached); operators who want a stable null should leave subnet_ids
// unset AND accept the gateway managing its own attachments, in which case the
// set is read back as the managed truth.
func natGatewaySubnetSetFromAPI(raw any, prior types.Set) types.Set {
	arr, _ := raw.([]any)
	if len(arr) == 0 {
		if prior.IsNull() || prior.IsUnknown() {
			return types.SetNull(types.StringType)
		}
		return mustSetValue(types.StringType, []attr.Value{})
	}

	elems := make([]attr.Value, 0, len(arr))
	for _, item := range arr {
		so, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if idv, ok := so["id"].(string); ok && idv != "" {
			elems = append(elems, types.StringValue(idv))
		}
	}
	if len(elems) == 0 {
		if prior.IsNull() || prior.IsUnknown() {
			return types.SetNull(types.StringType)
		}
		return mustSetValue(types.StringType, []attr.Value{})
	}
	return mustSetValue(types.StringType, elems)
}

// mustSetValue builds a types.Set, discarding the conversion diagnostics (which
// can only error on a type mismatch - impossible here since every element is a
// String). It keeps the call sites terse where the inputs are statically correct.
func mustSetValue(elemType attr.Type, elems []attr.Value) types.Set {
	s, _ := types.SetValue(elemType, elems)
	return s
}
