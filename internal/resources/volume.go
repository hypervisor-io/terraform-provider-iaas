package resources

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
	"github.com/hypervisor-io/terraform-provider-iaas/volumeop"
	"github.com/hypervisor-io/terraform-provider-iaas/waiter"
)

// Interface assertions. iaas_volume is an ASYNC resource (copying instance.go's
// pattern) with an in-place attach/detach managed via diff and an in-place
// plan-based resize:
//   - CREATE records the row (status="pending") then waits on the SHOW status
//     reaching "available" via a StatePollerWithErrorTolerance waiter,
//   - the id is persisted to state BEFORE the wait so a failed wait still leaves
//     a destroyable resource,
//   - attach, detach, resize and delete are ALSO asynchronous: the API records
//     the operation (the volume's active_operation names it) and the size, plan,
//     attachment or the volume itself change only when the node confirms. Every
//     one of them goes through package volumeop, which waits for the confirmed
//     outcome and reports a node failure, a typed refusal (reason code) or a
//     timeout distinctly,
//   - a timeouts nested block (create, update, delete all bound real waits),
//   - instance_id is managed by attach/detach calls on diff (NOT RequiresReplace),
//   - volume_plan_id is grown in place via the resize endpoint (NOT RequiresReplace).
var (
	_ resource.Resource                = &volumeResource{}
	_ resource.ResourceWithConfigure   = &volumeResource{}
	_ resource.ResourceWithImportState = &volumeResource{}
)

// volumeDefaultUpdateTimeout bounds an update. It is longer than the generic
// update default because an update can chain up to three confirmed operations
// (resize, detach, attach) and the API enforces a per-volume cooldown (five
// minutes on the platform today) after each completed one.
const volumeDefaultUpdateTimeout = 30 * time.Minute

// volumeOpOptions builds the confirmed-operation options for one call.
func volumeOpOptions(timeout time.Duration) volumeop.Options {
	return volumeop.Options{Interval: pollInterval(), Timeout: timeout}
}

// NewVolumeResource is the resource constructor registered with the provider.
func NewVolumeResource() resource.Resource {
	return &volumeResource{}
}

// volumeResource manages an iaas_volume - a Cloud Service block storage volume.
type volumeResource struct {
	client *client.Client
}

// volumeModel maps the Terraform state/plan for iaas_volume.
//
// Field groups:
//   - REPLACE inputs (name, hypervisor_group_id, project_id): immutable; changing
//     any forces a new volume (no update endpoint for them).
//   - IN-PLACE updatable inputs:
//     volume_plan_id → resized in place via the resize endpoint,
//     instance_id    → attached/detached in place via the attach/detach endpoints.
//   - server-managed computed (size, status, deployed, dev, path).
type volumeModel struct {
	ID                types.String `tfsdk:"id"`
	Name              types.String `tfsdk:"name"`
	VolumePlanID      types.String `tfsdk:"volume_plan_id"`
	HypervisorGroupID types.String `tfsdk:"hypervisor_group_id"`
	LocationID        types.String `tfsdk:"location_id"`
	ProjectID         types.String `tfsdk:"project_id"`
	InstanceID        types.String `tfsdk:"instance_id"`

	// Computed read-only.
	Size     types.Int64  `tfsdk:"size"`
	Status   types.String `tfsdk:"status"`
	Deployed types.Bool   `tfsdk:"deployed"`
	Dev      types.String `tfsdk:"dev"`
	Path     types.String `tfsdk:"path"`

	Timeouts timeouts.Value `tfsdk:"timeouts"`
}

// Metadata sets the resource type name → "<provider>_volume" → "iaas_volume".
func (r *volumeResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_volume"
}

// Schema describes the iaas_volume resource.
func (r *volumeResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a Cloud Service block storage volume. Creation is asynchronous: " +
			"the volume record is created synchronously, then the backing volume is " +
			"provisioned on a hypervisor and this resource waits for its status to become " +
			"\"available\". Sizing is plan-based: volume_plan_id selects the size/IO tier and " +
			"can be grown in place by selecting a larger plan (the resize endpoint). A volume " +
			"can be attached to / detached from an instance in place by setting / clearing " +
			"instance_id. The name, hypervisor group, and project are immutable. Attach, " +
			"detach, resize and delete are confirmed operations: the API accepts them, the " +
			"node performs them, and this resource waits until the node confirmed the new " +
			"attachment, plan and size (or the deletion). A node failure, a refusal (with its " +
			"reason code) or a timeout fails the apply and leaves the volume in its previous, " +
			"still billed state. Destroying an attached volume detaches it first. The platform " +
			"enforces a short cooldown between operations on one volume; the resource waits " +
			"it out within the configured timeout.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the volume, assigned by the API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required: true,
				Description: "Display name for the volume. Immutable (there is no update endpoint " +
					"for it); changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"volume_plan_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the volume plan, which determines the size and IO limits. " +
					"Sizing is PLAN-BASED (not a free-form size_gb): to resize, select a plan " +
					"with the desired capacity. Resizing in place is supported by the API's " +
					"resize endpoint (same storage class and datastore type required), so " +
					"changing this is NOT a replace - the resource issues a resize. A " +
					"cross-class/cross-type change is rejected by the API.",
				// Intentionally no RequiresReplace: a plan change is applied in place
				// via the resize endpoint (Update → ResizeVolume).
			},
			"hypervisor_group_id": schema.StringAttribute{
				Optional: true,
				Computed: true,
				DeprecationMessage: "Use location_id instead. hypervisor_group_id is deprecated " +
					"and will be removed in the next release.",
				Description: "UUID of the hypervisor group the volume is provisioned in. Immutable; " +
					"a volume cannot move groups, so changing this forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplaceIfConfigured(),
				},
			},
			"location_id": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "UUID of the location (hypervisor group) the volume is provisioned in. " +
					"Immutable; a volume cannot move locations, so changing this forces a new resource. " +
					"Canonical replacement for hypervisor_group_id; exactly one of the two must be set.",
				Validators: []validator.String{
					stringvalidator.ExactlyOneOf(path.MatchRoot("hypervisor_group_id")),
				},
				PlanModifiers: []planmodifier.String{
					locationIDFromAliasModifier{},
					stringplanmodifier.RequiresReplaceIfConfigured(),
				},
			},
			"project_id": schema.StringAttribute{
				Optional: true,
				Description: "Optional UUID of a project to organise the volume under. Immutable " +
					"(no update endpoint); changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"instance_id": schema.StringAttribute{
				Optional: true,
				Description: "Optional UUID of an instance to attach the volume to. Attaching and " +
					"detaching are done IN PLACE via the attach/detach endpoints, so this is " +
					"NOT a replace: setting it attaches the volume, clearing it detaches it, " +
					"and changing it detaches then re-attaches. The instance must be in the " +
					"same hypervisor group as the volume.",
				// Intentionally no RequiresReplace: managed via attach/detach on diff.
			},
			// size / status / deployed / dev are SERVER-MUTABLE computed fields:
			// resize changes size, attach/detach changes status+dev, the slave deploy
			// flips deployed. Per the golden guardrail, do NOT attach
			// UseStateForUnknown to server-mutable computed fields - it would copy the
			// stale prior value into the plan and MASK real drift.
			"size": schema.Int64Attribute{
				Computed: true,
				Description: "Size of the volume in GB, derived from the plan. Server-mutable " +
					"(changes on resize).",
			},
			"status": schema.StringAttribute{
				Computed: true,
				Description: "Lifecycle status: \"pending\" (provisioning), \"available\" (ready, " +
					"detached), \"attached\" (attached to an instance), \"deleting\". " +
					"Server-mutable.",
			},
			"deployed": schema.BoolAttribute{
				Computed: true,
				Description: "Whether the backing volume has been provisioned on the hypervisor " +
					"(the API's int 0/1 mapped to a bool). Server-mutable.",
			},
			"dev": schema.StringAttribute{
				Computed: true,
				Description: "Guest device name (e.g. \"xvda\") assigned when the volume is attached, " +
					"empty when detached. Server-mutable.",
			},
			"path": schema.StringAttribute{
				Computed: true,
				Description: "Backend storage path of the volume, derived server-side at create. " +
					"Stable after creation.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
		Blocks: map[string]schema.Block{
			// create bounds provisioning plus the optional first attach; update
			// bounds resize/detach/attach including cooldown waits (default 30m);
			// delete bounds an optional detach plus the confirmed deletion.
			"timeouts": timeouts.Block(ctx, timeouts.Opts{
				Create: true,
				Update: true,
				Delete: true,
			}),
		},
	}
}

// Configure pulls the shared *client.Client from the provider (nil-guarded).
func (r *volumeResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create provisions the volume and waits for it to become available:
//
//  1. CreateVolume records the row (status="pending") and returns the id.
//  2. The id is saved into state BEFORE the wait, so a provisioning failure or
//     timeout still tracks the volume for a subsequent destroy.
//  3. WaitFor polls GetVolume until status=="available" (fail on "failed"/"error").
//  4. If instance_id was set in the plan, the volume is attached after it becomes
//     available, then state is hydrated from the post-attach object.
func (r *volumeResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan volumeModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	createTimeout, diags := plan.Timeouts.Create(ctx, defaultCreateTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := map[string]any{
		"name":           plan.Name.ValueString(),
		"volume_plan_id": plan.VolumePlanID.ValueString(),
		"location_id":    effectiveLocationID(plan.LocationID, plan.HypervisorGroupID),
	}
	if !plan.ProjectID.IsNull() && !plan.ProjectID.IsUnknown() {
		body["project_id"] = plan.ProjectID.ValueString()
	}

	instanceID := ""
	if !plan.InstanceID.IsNull() && !plan.InstanceID.IsUnknown() {
		instanceID = plan.InstanceID.ValueString()
	}

	obj, id, summary, err := createVolumeAndAttach(ctx, r.client, body, instanceID, createTimeout,
		func(id string) bool {
			// Persist the id immediately so a failed provisioning/wait still
			// tracks the resource for cleanup on the next destroy.
			resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
			return !resp.Diagnostics.HasError()
		})
	if err != nil {
		if summary == "Error attaching volume" {
			// Record what the API reports (the volume exists, detached) so the
			// state is truthful, then fail the apply.
			r.setStateFromAPI(ctx, id, plan, &resp.State, &resp.Diagnostics)
			resp.Diagnostics.Append(volumeOpDiag(summary, err))
			return
		}
		resp.Diagnostics.Append(diagFromErr(summary, err))
		return
	}
	if obj == nil {
		return // persisting the id failed; the diagnostic is already set
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, volumeStateFromAPI(obj, plan))...)
}

// createVolumeAndAttach creates the volume, waits for provisioning and, when
// instanceID is set, performs a confirmed attach. ONE deadline (timeout) spans
// the whole sequence: the POST, the provisioning wait, the reads and the
// attach all share the remaining budget, so a create timeout is a bound on
// the whole create, never one full budget per stage. persistID is called with
// the new id before any wait; returning false stops (obj nil, err nil). On an
// error, summary names the failed stage for the diagnostic.
func createVolumeAndAttach(
	ctx context.Context,
	c *client.Client,
	body map[string]any,
	instanceID string,
	timeout time.Duration,
	persistID func(id string) bool,
) (obj map[string]any, id string, summary string, err error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	created, err := c.CreateVolume(ctx, body)
	if err != nil {
		return nil, "", "Error creating volume", err
	}
	id, _ = created["id"].(string)
	if id == "" {
		return nil, "", "Error creating volume", errors.New("create response did not include a volume id")
	}
	if !persistID(id) {
		return nil, id, "", nil
	}

	// ── ASYNC convergence: poll the volume SHOW until status="available" ──────
	// Tolerance=3: tolerate up to 3 consecutive transport blips that bypass the
	// client's 429/5xx retry, so a brief hiccup during provisioning does not abort
	// the whole create.
	waitErr := waiter.WaitFor(ctx, waiter.Options{
		Interval: pollInterval(),
		Refresh: waiter.StatePollerWithErrorTolerance(
			func() (map[string]any, error) { return c.GetVolume(ctx, id) },
			"status",
			[]string{"available"},
			[]string{"failed", "error"},
			3,
		),
	})
	if waitErr != nil {
		return nil, id, "Error waiting for volume provisioning",
			fmt.Errorf("volume %s did not become available: %s", id, waitErr.Error())
	}

	// ── optional attach (confirmed: waits for the node) ──────────────────────
	obj, err = c.GetVolume(ctx, id)
	if err != nil {
		return nil, id, "Error reading volume after provisioning", err
	}
	if instanceID != "" {
		attached, err := volumeop.AttachVolume(ctx, c, id, instanceID, volumeOpOptions(0))
		if err != nil {
			return nil, id, "Error attaching volume", err
		}
		obj = attached
	}
	return obj, id, "", nil
}

// Read refreshes state from the API. A 404 means the volume was deleted out of
// band - remove it from state so Terraform plans a recreate.
func (r *volumeResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state volumeModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	obj, err := r.client.GetVolume(ctx, state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("Error reading volume", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, volumeStateFromAPI(obj, state))...)
}

// Update applies the two in-place mutations the API supports, each as a
// CONFIRMED operation (package volumeop waits until the node confirmed it):
//
//   - RESIZE: if volume_plan_id changed, issue a plan-based resize and wait
//     until the API reports the new plan (size and plan switch together, only
//     on the node's confirmation).
//   - ATTACH/DETACH: if instance_id changed, detach the old instance (if any)
//     and/or attach the new one (if any). Changing the target = detach + attach.
//
// All other inputs are RequiresReplace, so they never reach here. On any
// failure the state is refreshed from the API before the error is returned, so
// a partially applied update (e.g. resize confirmed, attach refused) is
// recorded truthfully and the next plan shows exactly what is still missing.
func (r *volumeResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state volumeModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	updateTimeout, diags := plan.Timeouts.Update(ctx, volumeDefaultUpdateTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	// One budget for the whole update, shared by every confirmed step.
	ctx, cancel := context.WithTimeout(ctx, updateTimeout)
	defer cancel()
	opts := volumeOpOptions(0)

	id := state.ID.ValueString()

	fail := func(summary string, err error) {
		r.setStateFromAPI(ctx, id, plan, &resp.State, &resp.Diagnostics)
		resp.Diagnostics.Append(volumeOpDiag(summary, err))
	}

	// RESIZE - plan change.
	if !plan.VolumePlanID.Equal(state.VolumePlanID) {
		if _, err := volumeop.ResizeVolume(ctx, r.client, id, plan.VolumePlanID.ValueString(), opts); err != nil {
			fail("Error resizing volume", err)
			return
		}
	}

	// ATTACH/DETACH - instance_id change. Detach the old target first, then
	// attach the new one (a change of target = detach + attach).
	if !plan.InstanceID.Equal(state.InstanceID) {
		oldAttached := !state.InstanceID.IsNull() && state.InstanceID.ValueString() != ""
		newAttached := !plan.InstanceID.IsNull() && !plan.InstanceID.IsUnknown() && plan.InstanceID.ValueString() != ""

		if oldAttached {
			if _, err := volumeop.DetachVolume(ctx, r.client, id, opts); err != nil {
				fail("Error detaching volume", err)
				return
			}
		}
		if newAttached {
			if _, err := volumeop.AttachVolume(ctx, r.client, id, plan.InstanceID.ValueString(), opts); err != nil {
				fail("Error attaching volume", err)
				return
			}
		}
	}

	// Read back to rehydrate computed fields from the authoritative SHOW.
	obj, err := r.client.GetVolume(ctx, id)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading volume after update", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, volumeStateFromAPI(obj, plan))...)
}

// Delete removes the volume as a CONFIRMED operation. The API refuses to
// delete an attached volume (reason volume_attached), so an attached volume is
// detached first (confirmed), then the delete is sent and the resource waits
// until the node confirmed it (the SHOW then 404s). Until then the volume and
// its billing stay in place; a refusal, a node failure or a timeout fails the
// destroy and keeps the resource in state.
func (r *volumeResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state volumeModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	deleteTimeout, diags := state.Timeouts.Delete(ctx, defaultDeleteTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, deleteTimeout)
	defer cancel()
	opts := volumeOpOptions(0)
	id := state.ID.ValueString()

	// Decide on detach only from a SETTLED volume: while an earlier attach is
	// still pending the API reports the old (available, unattached) state, so
	// deciding first and waiting later would send DELETE to a volume that the
	// pending attach then attaches (refused volume_attached). A delete refused
	// volume_attached anyway (an attach started in between) is re-evaluated,
	// at most twice more, within the same budget.
	for attempt := 0; ; attempt++ {
		obj, err := volumeop.Settle(ctx, r.client, id, opts)
		if errors.Is(err, volumeop.ErrGone) {
			return // already gone
		}
		if err != nil {
			resp.Diagnostics.Append(volumeOpDiag("Error waiting for pending volume work before delete", err))
			return
		}
		if inst, _ := obj["instance_id"].(string); inst != "" || obj["status"] == "attached" {
			if _, err := volumeop.DetachVolume(ctx, r.client, id, opts); err != nil {
				resp.Diagnostics.Append(volumeOpDiag("Error detaching volume before delete", err))
				return
			}
		}

		err = volumeop.DeleteVolume(ctx, r.client, id, opts)
		var refused *volumeop.RefusedError
		if errors.As(err, &refused) && refused.Reason == "volume_attached" && attempt < 2 {
			continue
		}
		if err != nil {
			resp.Diagnostics.Append(volumeOpDiag("Error deleting volume", err))
		}
		return
	}
}

// setStateFromAPI best-effort refreshes state from the SHOW after a failed
// confirmed operation, so the recorded state matches what the API reports. A
// read failure leaves the prior state untouched (the error being returned is
// the important diagnostic).
func (r *volumeResource) setStateFromAPI(ctx context.Context, id string, prior volumeModel, st *tfsdk.State, d *diag.Diagnostics) {
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	obj, err := r.client.GetVolume(readCtx, id)
	if err != nil {
		return
	}
	d.Append(st.Set(ctx, volumeStateFromAPI(obj, prior))...)
}

// volumeOpDiag renders a confirmed-operation error. A refusal keeps the API's
// own message plus its reason code (tfdiag), a node failure and a timeout are
// rendered from their own text, which says what was kept and what may still
// complete.
func volumeOpDiag(summary string, err error) diag.Diagnostic {
	var refused *volumeop.RefusedError
	if errors.As(err, &refused) {
		return diagFromErr(summary, refused.Err)
	}
	return diag.NewErrorDiagnostic(summary, err.Error())
}

// ImportState lets `terraform import iaas_volume.x <uuid>` adopt an existing
// volume; the next Read populates the readable attributes.
func (r *volumeResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// volumeStateFromAPI builds the model from a SHOW/attach/detach volume object,
// falling back to the prior model's value for fields the response omits. The
// RequiresReplace inputs (name, hypervisor_group_id, project_id) and the
// in-place inputs (volume_plan_id) authoritative value is the plan/state; the
// computed fields come from the API. instance_id falls back to the prior value
// when absent/null (detached volumes return null instance_id).
func volumeStateFromAPI(obj map[string]any, prior volumeModel) volumeModel {
	return volumeModel{
		ID:                stringFromAPI(obj, "id", prior.ID),
		Name:              stringOrPrior(obj, "name", prior.Name),
		VolumePlanID:      stringOrPrior(obj, "volume_plan_id", prior.VolumePlanID),
		HypervisorGroupID: hypervisorGroupIDFromAPI(obj, prior.HypervisorGroupID),
		LocationID:        locationIDFromAPI(obj, prior.LocationID),
		ProjectID:         optionalStringFromAPI(obj, "project_id", prior.ProjectID),
		InstanceID:        optionalStringFromAPI(obj, "instance_id", prior.InstanceID),

		Size:     int64FromAPI(obj, "size", prior.Size),
		Status:   stringFromAPI(obj, "status", prior.Status),
		Deployed: boolFromIntAPI(obj, "deployed", prior.Deployed),
		Dev:      computedStringFromAPI(obj, "dev", prior.Dev),
		Path:     stringFromAPI(obj, "path", prior.Path),

		Timeouts: prior.Timeouts,
	}
}

// computedStringFromAPI reads a string field for a COMPUTED attribute that may be
// absent/null (e.g. "dev" is null while detached). Unlike optionalStringFromAPI
// (which is for Optional attrs and yields null), this settles an absent/null
// value to "" so the Computed attribute is always known after apply - mirroring
// nestedStringFromAPI's settle behaviour. A present non-empty string is used
// verbatim; otherwise the prior value is preserved (also settled to "").
func computedStringFromAPI(obj map[string]any, key string, fallback types.String) types.String {
	settle := func(v types.String) types.String {
		if v.IsNull() || v.IsUnknown() {
			return types.StringValue("")
		}
		return v
	}
	raw, ok := obj[key]
	if !ok || raw == nil {
		return settle(fallback)
	}
	if s, ok := raw.(string); ok {
		return types.StringValue(s)
	}
	return types.StringValue(fmt.Sprintf("%v", raw))
}
