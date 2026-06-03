package resources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/iaas/terraform-provider-iaas/internal/client"
	"github.com/iaas/terraform-provider-iaas/internal/waiter"
)

// Interface assertions. iaas_volume is an ASYNC resource (copying instance.go's
// pattern) with an in-place attach/detach managed via diff and an in-place
// plan-based resize:
//   - CREATE records the row (status="pending") then waits on the SHOW status
//     reaching "available" via a StatePollerWithErrorTolerance waiter,
//   - the id is persisted to state BEFORE the wait so a failed wait still leaves
//     a destroyable resource,
//   - a timeouts nested block (create only — attach/detach/resize/delete are
//     synchronous from the API's perspective),
//   - instance_id is managed by attach/detach calls on diff (NOT RequiresReplace),
//   - volume_plan_id is grown in place via the resize endpoint (NOT RequiresReplace).
var (
	_ resource.Resource                = &volumeResource{}
	_ resource.ResourceWithConfigure   = &volumeResource{}
	_ resource.ResourceWithImportState = &volumeResource{}
)

// NewVolumeResource is the resource constructor registered with the provider.
func NewVolumeResource() resource.Resource {
	return &volumeResource{}
}

// volumeResource manages an iaas_volume — a Cloud Service block storage volume.
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
			"instance_id. The name, hypervisor group, and project are immutable.",
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
					"changing this is NOT a replace — the resource issues a resize. A " +
					"cross-class/cross-type change is rejected by the API.",
				// Intentionally no RequiresReplace: a plan change is applied in place
				// via the resize endpoint (Update → ResizeVolume).
			},
			"hypervisor_group_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the hypervisor group the volume is provisioned in. Immutable; " +
					"a volume cannot move groups, so changing this forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
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
			// UseStateForUnknown to server-mutable computed fields — it would copy the
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
			// Only create is async (waits for status="available"); attach/detach/
			// resize/delete are synchronous from the API's perspective, so only the
			// create timeout is meaningful — the block still exposes all three for
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
		"name":                plan.Name.ValueString(),
		"volume_plan_id":      plan.VolumePlanID.ValueString(),
		"hypervisor_group_id": plan.HypervisorGroupID.ValueString(),
	}
	if !plan.ProjectID.IsNull() && !plan.ProjectID.IsUnknown() {
		body["project_id"] = plan.ProjectID.ValueString()
	}

	created, err := r.client.CreateVolume(ctx, body)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating volume", err))
		return
	}
	id, _ := created["id"].(string)
	if id == "" {
		resp.Diagnostics.AddError("Error creating volume", "create response did not include a volume id")
		return
	}

	// Persist the id immediately so a failed provisioning/wait still tracks the
	// resource for cleanup on the next destroy.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// ── ASYNC convergence: poll the volume SHOW until status="available" ──────
	// Tolerance=3: tolerate up to 3 consecutive transport blips that bypass the
	// client's 429/5xx retry, so a brief hiccup during provisioning does not abort
	// the whole create.
	waitErr := waiter.WaitFor(ctx, waiter.Options{
		Interval: pollInterval(),
		Timeout:  createTimeout,
		Refresh: waiter.StatePollerWithErrorTolerance(
			func() (map[string]any, error) { return r.client.GetVolume(ctx, id) },
			"status",
			[]string{"available"},
			[]string{"failed", "error"},
			3,
		),
	})
	if waitErr != nil {
		resp.Diagnostics.AddError(
			"Error waiting for volume provisioning",
			fmt.Sprintf("volume %s did not become available: %s", id, waitErr.Error()),
		)
		return
	}

	// ── optional attach ──────────────────────────────────────────────────────
	obj, err := r.client.GetVolume(ctx, id)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading volume after provisioning", err))
		return
	}
	if !plan.InstanceID.IsNull() && !plan.InstanceID.IsUnknown() && plan.InstanceID.ValueString() != "" {
		attached, err := r.client.AttachVolume(ctx, id, map[string]any{"instance_id": plan.InstanceID.ValueString()})
		if err != nil {
			resp.Diagnostics.Append(diagFromErr("Error attaching volume", err))
			return
		}
		obj = attached
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, volumeStateFromAPI(obj, plan))...)
}

// Read refreshes state from the API. A 404 means the volume was deleted out of
// band — remove it from state so Terraform plans a recreate.
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

// Update applies the two in-place mutations the API supports:
//
//   - RESIZE: if volume_plan_id changed, issue a plan-based resize.
//   - ATTACH/DETACH: if instance_id changed, detach the old instance (if any)
//     and/or attach the new one (if any). Changing the target = detach + attach.
//
// All other inputs are RequiresReplace, so they never reach here. After applying
// the changes the resource Reads back via SHOW to rehydrate computed fields,
// since the last mutating call's response may not reflect the final state.
func (r *volumeResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state volumeModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()

	// RESIZE — plan change.
	if !plan.VolumePlanID.Equal(state.VolumePlanID) {
		resizeResp, err := r.client.ResizeVolume(ctx, id, map[string]any{
			"volume_plan_id": plan.VolumePlanID.ValueString(),
		})
		if err != nil {
			resp.Diagnostics.Append(diagFromErr("Error resizing volume", err))
			return
		}
		// The API returns is_downgrade:true when the selected plan implies a
		// smaller size than the current plan. Warn rather than error so the
		// apply still proceeds (the operator may have intentionally selected a
		// smaller plan), but surface the risk clearly.
		if downgrade, _ := resizeResp["is_downgrade"].(bool); downgrade {
			resp.Diagnostics.AddWarning(
				"Volume downgrade",
				"The selected volume_plan_id implies a smaller size than the current plan. "+
					"This may be destructive and can truncate data on some storage backends.",
			)
		}
	}

	// ATTACH/DETACH — instance_id change. Detach the old target first, then
	// attach the new one (a change of target = detach + attach).
	if !plan.InstanceID.Equal(state.InstanceID) {
		oldAttached := !state.InstanceID.IsNull() && state.InstanceID.ValueString() != ""
		newAttached := !plan.InstanceID.IsNull() && !plan.InstanceID.IsUnknown() && plan.InstanceID.ValueString() != ""

		if oldAttached {
			if _, err := r.client.DetachVolume(ctx, id); err != nil {
				resp.Diagnostics.Append(diagFromErr("Error detaching volume", err))
				return
			}
		}
		if newAttached {
			if _, err := r.client.AttachVolume(ctx, id, map[string]any{
				"instance_id": plan.InstanceID.ValueString(),
			}); err != nil {
				resp.Diagnostics.Append(diagFromErr("Error attaching volume", err))
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

// Delete removes the volume. DELETE soft-deletes the row immediately (and the
// service detaches first if attached, then dispatches a slave delete task), so a
// subsequent SHOW 404s right away — no delete waiter is required. A precondition
// failure (e.g. detach-before-delete failed) surfaces as success:false at 422.
func (r *volumeResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state volumeModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteVolume(ctx, state.ID.ValueString()); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deleting volume", err))
		return
	}
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
		HypervisorGroupID: stringOrPrior(obj, "hypervisor_group_id", prior.HypervisorGroupID),
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
// value to "" so the Computed attribute is always known after apply — mirroring
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
