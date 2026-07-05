package resources

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
	"github.com/hypervisor-io/terraform-provider-iaas/waiter"
)

// Interface assertions - dns_zone is the PARENT of the DNS resource family. It
// reuses the security_group instance_ids set-diff pattern for its attached VPCs
// (vpc_ids: a plain string set reconciled via attach/detach on diff, rebuilt from
// the SHOW on Read), and adds an async delete (the zone is queued for deletion and
// soft-deleted by a job, so Delete polls the SHOW to 404 like the instance).
var (
	_ resource.Resource                = &dnsZoneResource{}
	_ resource.ResourceWithConfigure   = &dnsZoneResource{}
	_ resource.ResourceWithImportState = &dnsZoneResource{}
)

// NewDNSZoneResource is the resource constructor registered with the provider.
func NewDNSZoneResource() resource.Resource {
	return &dnsZoneResource{}
}

// dnsZoneResource manages an iaas_dns_zone - an internal (per-VPC CoreDNS) DNS
// zone, owned by the account and attachable to 0..N VPCs.
//
// Route summary (verified against UserApi\VpcDnsZoneController + VpcDnsService +
// routes/user_api.php + the vpc_dns migration):
//
//	CREATE  POST   /dns-zones                      body {name (req), description?, vpc_ids?}
//	                                                → {success,message,zone:{id,...}}
//	SHOW    GET    /dns-zone/{id}                   → {zone:{...,vpcs:[{id,name}],record_sets:[...]}}
//	UPDATE  PATCH  /dns-zone/{id}                   body {description?} (name is immutable)
//	DELETE  DELETE /dns-zone/{id}                   (ASYNC: status→deleting + queued job)
//	ATTACH  POST   /dns-zone/{id}/attach-vpc        body {vpc_id} (singular)
//	DETACH  DELETE /dns-zone/{id}/detach-vpc/{vpcId}
//
// name is immutable (the service's updateZone only persists description), so it is
// RequiresReplace. description is updatable in place. vpc_ids is an Optional
// string set reconciled via attach/detach on diff and rebuilt from zone.vpcs[] on
// Read. status is server-mutable (active/pending/deleting), so it is Computed
// WITHOUT UseStateForUnknown.
type dnsZoneResource struct {
	client *client.Client
}

// dnsZoneModel maps the Terraform state/plan for iaas_dns_zone.
type dnsZoneModel struct {
	ID          types.String   `tfsdk:"id"`
	Name        types.String   `tfsdk:"name"`
	Description types.String   `tfsdk:"description"`
	VPCIDs      types.Set      `tfsdk:"vpc_ids"`
	Status      types.String   `tfsdk:"status"`
	Timeouts    timeouts.Value `tfsdk:"timeouts"`
}

// Metadata sets the resource type name → "iaas_dns_zone".
func (r *dnsZoneResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dns_zone"
}

// Schema describes the iaas_dns_zone resource.
func (r *dnsZoneResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages an internal DNS zone (per-VPC CoreDNS). A zone is owned by the " +
			"account and can be attached to one or more VPCs, where its records become " +
			"resolvable. The set of attached VPCs is managed via the `vpc_ids` attribute " +
			"(attach/detach in place). The zone name is immutable; only the description can " +
			"be changed in place. Deletion is asynchronous - the zone is queued for removal " +
			"and the provider waits for it to disappear.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the DNS zone, assigned by the API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required: true,
				Description: "The zone name (e.g. \"corp.internal\"). Must be lowercase " +
					"alphanumeric with dots and hyphens, max 63 chars. A bare public TLD " +
					"(\"com\", \"local\", ...) is rejected to avoid shadowing real DNS - use a " +
					"compound name like \"corp.internal\". Immutable: changing it forces a new " +
					"resource (the API has no rename).",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"description": schema.StringAttribute{
				Optional: true,
				Description: "Optional free-text description (max 500 chars). Set to null to " +
					"clear. This is the only scalar field that can be changed in place.",
			},
			"vpc_ids": schema.SetAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "UUIDs of the VPCs this zone is attached to, as an order-independent " +
					"set. Adding or removing an id attaches or detaches the zone from that VPC in " +
					"place (the zone's records become resolvable inside attached VPCs). Each VPC " +
					"must belong to your account.",
			},
			"status": schema.StringAttribute{
				Computed: true,
				Description: "Lifecycle status of the zone: \"active\", \"pending\", or " +
					"\"deleting\". Server-managed.",
				// No UseStateForUnknown: status is server-mutable, so masking it would
				// hide real drift.
			},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{
				Delete: true,
			}),
		},
	}
}

// Configure pulls the shared *client.Client from the provider.
func (r *dnsZoneResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create provisions the zone (optionally attaching the configured VPCs in the same
// call via vpc_ids), then reads it back so state reflects the server status and the
// authoritative attached-VPC set.
func (r *dnsZoneResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan dnsZoneModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := map[string]any{"name": plan.Name.ValueString()}
	if !plan.Description.IsNull() && !plan.Description.IsUnknown() {
		body["description"] = plan.Description.ValueString()
	}

	// The create endpoint accepts vpc_ids and attaches them atomically, so send the
	// configured set directly (saves N attach round-trips).
	vpcIDs, diags := stringsFromSet(ctx, plan.VPCIDs)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if len(vpcIDs) > 0 {
		body["vpc_ids"] = vpcIDs
	}

	obj, err := r.client.CreateDnsZone(ctx, body)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating DNS zone", err))
		return
	}
	id, _ := obj["id"].(string)
	if id == "" {
		resp.Diagnostics.AddError("Error creating DNS zone", "the create response did not contain an id")
		return
	}

	// Read back so state carries the server status + the authoritative vpcs[] set.
	state, notFound, diags := r.readState(ctx, id, plan)
	resp.Diagnostics.Append(diags...)
	if notFound {
		resp.Diagnostics.AddError("Error creating DNS zone",
			"the DNS zone disappeared immediately after creation")
		return
	}
	if resp.Diagnostics.HasError() {
		return
	}
	state.Timeouts = plan.Timeouts
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Read refreshes state from the API. A 404 means the zone was deleted out of band.
func (r *dnsZoneResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state dnsZoneModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	newState, notFound, diags := r.readState(ctx, state.ID.ValueString(), state)
	if notFound {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	newState.Timeouts = state.Timeouts
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

// Update patches description if it changed, then diffs the vpc_ids set (attach
// added / detach removed), then reads back so state reflects the authoritative
// attached-VPC set.
func (r *dnsZoneResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state dnsZoneModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()

	// Patch description when it changed (the only mutable scalar; name is RequiresReplace).
	if !plan.Description.Equal(state.Description) {
		fields := map[string]any{}
		if plan.Description.IsNull() {
			fields["description"] = nil
		} else if !plan.Description.IsUnknown() {
			fields["description"] = plan.Description.ValueString()
		}
		if _, err := r.client.UpdateDnsZone(ctx, id, fields); err != nil {
			resp.Diagnostics.Append(diagFromErr("Error updating DNS zone", err))
			return
		}
	}

	// Diff the vpc_ids set: attach added, detach removed.
	plannedIDs, diags := stringsFromSet(ctx, plan.VPCIDs)
	resp.Diagnostics.Append(diags...)
	stateIDs, diags := stringsFromSet(ctx, state.VPCIDs)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	plannedSet := make(map[string]struct{}, len(plannedIDs))
	for _, v := range plannedIDs {
		plannedSet[v] = struct{}{}
	}
	stateSet := make(map[string]struct{}, len(stateIDs))
	for _, v := range stateIDs {
		stateSet[v] = struct{}{}
	}
	for _, v := range plannedIDs {
		if _, exists := stateSet[v]; !exists {
			if err := r.client.AttachDnsZoneVpc(ctx, id, v); err != nil {
				resp.Diagnostics.Append(diagFromErr("Error attaching VPC to DNS zone", err))
				return
			}
		}
	}
	for _, v := range stateIDs {
		if _, exists := plannedSet[v]; !exists {
			if err := r.client.DetachDnsZoneVpc(ctx, id, v); err != nil {
				resp.Diagnostics.Append(diagFromErr("Error detaching VPC from DNS zone", err))
				return
			}
		}
	}

	newState, notFound, diags := r.readState(ctx, id, plan)
	resp.Diagnostics.Append(diags...)
	if notFound {
		resp.Diagnostics.AddError("Error updating DNS zone", "the DNS zone disappeared during update")
		return
	}
	if resp.Diagnostics.HasError() {
		return
	}
	newState.Timeouts = plan.Timeouts
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

// Delete queues the zone for deletion (the service marks status="deleting" and a
// DeleteDnsZone job soft-deletes the row), then waits for the SHOW to 404 so the
// destroy completes cleanly. (The instance delete-poll-to-404 pattern.)
func (r *dnsZoneResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state dnsZoneModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	deleteTimeout, diags := state.Timeouts.Delete(ctx, 10*time.Minute)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()
	if err := r.client.DeleteDnsZone(ctx, id); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deleting DNS zone", err))
		return
	}

	// Converge by polling SHOW until it 404s.
	waitErr := waiter.WaitFor(ctx, waiter.Options{
		Interval: pollInterval(),
		Timeout:  deleteTimeout,
		Refresh: func() (string, bool, error) {
			_, err := r.client.GetDnsZone(ctx, id)
			if err != nil {
				if client.IsNotFound(err) {
					return "deleted", true, nil
				}
				return "", false, err
			}
			return "deleting", false, nil
		},
	})
	if waitErr != nil {
		resp.Diagnostics.AddError(
			"Error waiting for DNS zone deletion",
			fmt.Sprintf("DNS zone %s was not removed: %s", id, waitErr.Error()),
		)
		return
	}
}

// ImportState adopts an existing zone by its id; the next Read hydrates the
// scalars and rebuilds the vpc_ids set from the SHOW.
func (r *dnsZoneResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// readState GETs the zone and builds the model, rebuilding vpc_ids from the
// embedded vpcs[] array. The bool return is true when the zone was not found.
func (r *dnsZoneResource) readState(ctx context.Context, id string, prior dnsZoneModel) (dnsZoneModel, bool, diag.Diagnostics) {
	obj, err := r.client.GetDnsZone(ctx, id)
	if err != nil {
		if client.IsNotFound(err) {
			return dnsZoneModel{}, true, nil
		}
		var diags diag.Diagnostics
		diags.Append(diagFromErr("Error reading DNS zone", err))
		return dnsZoneModel{}, false, diags
	}
	m, diags := dnsZoneStateFromAPI(obj, prior)
	return m, false, diags
}

// dnsZoneStateFromAPI builds the model from the zone object, rebuilding vpc_ids
// from the embedded vpcs[] array (each element an object with an "id").
func dnsZoneStateFromAPI(obj map[string]any, prior dnsZoneModel) (dnsZoneModel, diag.Diagnostics) {
	m := dnsZoneModel{
		ID:          stringFromAPI(obj, "id", prior.ID),
		Name:        stringFromAPI(obj, "name", prior.Name),
		Description: optionalStringFromAPI(obj, "description", prior.Description),
		Status:      stringFromAPI(obj, "status", prior.Status),
	}
	vpcSet, diags := vpcIDSetFromAPI(obj["vpcs"], prior.VPCIDs)
	m.VPCIDs = vpcSet
	return m, diags
}

// vpcIDSetFromAPI converts the embedded "vpcs" JSON array (each element an object
// with an "id") into a types.Set of id strings. When the array is absent/empty AND
// the prior config had a null vpc_ids set, the result stays null so an
// unmanaged-attachments config does not show drift; otherwise an empty managed set
// becomes an empty (non-null) set.
func vpcIDSetFromAPI(raw any, prior types.Set) (types.Set, diag.Diagnostics) {
	arr, _ := raw.([]any)
	elems := make([]attr.Value, 0, len(arr))
	for _, item := range arr {
		o, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if idv, ok := o["id"].(string); ok && idv != "" {
			elems = append(elems, types.StringValue(idv))
		}
	}
	if len(elems) == 0 {
		if prior.IsNull() || prior.IsUnknown() {
			return types.SetNull(types.StringType), nil
		}
		return types.SetValue(types.StringType, []attr.Value{})
	}
	return types.SetValue(types.StringType, elems)
}
