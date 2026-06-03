package resources

import (
	"context"
	"fmt"
	"strings"

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

// Interface assertions. iaas_volume_snapshot is an ASYNC CHILD resource — it
// combines vpc_subnet's child pattern (parent id in path + composite import)
// with instance.go's async pattern (waiter + timeouts). Snapshots have no
// individual SHOW route, so the parent volume's embedded snapshots[] array is
// the read/poll source (see client.GetVolumeSnapshot / FindVolumeSnapshotByName).
var (
	_ resource.Resource                = &volumeSnapshotResource{}
	_ resource.ResourceWithConfigure   = &volumeSnapshotResource{}
	_ resource.ResourceWithImportState = &volumeSnapshotResource{}
)

// NewVolumeSnapshotResource is the resource constructor registered with the provider.
func NewVolumeSnapshotResource() resource.Resource {
	return &volumeSnapshotResource{}
}

// volumeSnapshotResource manages an iaas_volume_snapshot — a point-in-time
// snapshot of a parent volume.
type volumeSnapshotResource struct {
	client *client.Client
}

// volumeSnapshotModel maps the Terraform state/plan for iaas_volume_snapshot.
//
// volume_id is part of the API path (Required + RequiresReplace). name is
// Required (it is the key used to resolve the server-assigned snapshot id after
// create, since the CREATE endpoint returns a queue, not the snapshot) and is
// immutable (no snapshot-update endpoint → RequiresReplace). status/size are
// server-managed computed.
type volumeSnapshotModel struct {
	ID       types.String   `tfsdk:"id"`
	VolumeID types.String   `tfsdk:"volume_id"`
	Name     types.String   `tfsdk:"name"`
	Status   types.String   `tfsdk:"status"`
	Size     types.Int64    `tfsdk:"size"`
	Timeouts timeouts.Value `tfsdk:"timeouts"`
}

// Metadata sets the resource type name → "iaas_volume_snapshot".
func (r *volumeSnapshotResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_volume_snapshot"
}

// Schema describes the iaas_volume_snapshot resource.
func (r *volumeSnapshotResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a point-in-time snapshot of a volume. A snapshot is a child of a " +
			"volume: its parent volume_id is part of the API path, so changing it forces a " +
			"new resource. Creation is asynchronous — the snapshot is enqueued and this " +
			"resource waits for it to become \"available\". Snapshots are immutable (there " +
			"is no update endpoint); changing the name forces a new snapshot. Import with a " +
			"composite id: \"<volume_id>/<snapshot_id>\".",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the snapshot, assigned by the API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"volume_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the parent volume this snapshot belongs to. This value is part " +
					"of the API request path, so changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"name": schema.StringAttribute{
				Required: true,
				Description: "Name of the snapshot. Required and immutable: it is the key used to " +
					"resolve the server-assigned snapshot id after creation (the create " +
					"endpoint returns a job queue, not the snapshot), and there is no " +
					"snapshot-update endpoint, so changing it forces a new resource. Use a " +
					"unique name per volume.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			// status / size are SERVER-MUTABLE computed: status transitions
			// pending→creating→available; size is populated once the backend
			// reports the captured bytes. Per the guardrail, NO UseStateForUnknown.
			"status": schema.StringAttribute{
				Computed: true,
				Description: "Lifecycle status: \"pending\", \"creating\", \"available\", " +
					"\"restoring\", \"failed\", \"deleting\". Server-mutable.",
			},
			"size": schema.Int64Attribute{
				Computed:    true,
				Description: "Captured size of the snapshot in bytes, populated by the backend. Server-mutable.",
			},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{
				Create: true,
				Delete: true,
			}),
		},
	}
}

// Configure pulls the shared *client.Client from the provider (nil-guarded).
func (r *volumeSnapshotResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create enqueues the snapshot and waits for it to become available:
//
//  1. CreateVolumeSnapshot enqueues the job (returns the queue, NOT the snapshot).
//  2. FindVolumeSnapshotByName resolves the server-assigned snapshot id from the
//     parent volume's embedded snapshots[] (matched by the unique name). The id is
//     persisted to state BEFORE the readiness wait so a failed wait still leaves a
//     destroyable resource.
//  3. WaitFor polls GetVolumeSnapshot until status=="available" (fail on "failed").
func (r *volumeSnapshotResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan volumeSnapshotModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	createTimeout, diags := plan.Timeouts.Create(ctx, defaultCreateTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	volumeID := plan.VolumeID.ValueString()
	name := plan.Name.ValueString()

	if _, err := r.client.CreateVolumeSnapshot(ctx, volumeID, map[string]any{"name": name}); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating volume snapshot", err))
		return
	}

	// Resolve the server-assigned snapshot id by name (the create returned only a
	// queue). The snapshot row exists immediately after enqueue, so a single
	// lookup suffices; tolerate a brief lag with the error-tolerant poller.
	var snapshotID string
	resolveErr := waiter.WaitFor(ctx, waiter.Options{
		Interval: pollInterval(),
		Timeout:  createTimeout,
		Refresh: func() (string, bool, error) {
			s, err := r.client.FindVolumeSnapshotByName(ctx, volumeID, name)
			if err != nil {
				if client.IsNotFound(err) {
					return "", false, nil // not visible yet; keep polling
				}
				return "", false, err
			}
			if id, ok := s["id"].(string); ok && id != "" {
				snapshotID = id
				return "resolved", true, nil
			}
			return "", false, nil
		},
	})
	if resolveErr != nil {
		resp.Diagnostics.AddError(
			"Error creating volume snapshot",
			fmt.Sprintf("could not resolve the snapshot %q on volume %s: %s", name, volumeID, resolveErr.Error()),
		)
		return
	}

	// Persist both the snapshot id AND the parent volume_id immediately so that
	// a failed readiness wait still tracks the resource for cleanup — a destroy
	// needs both ids to build the DELETE path; without volume_id, the
	// DeleteVolumeSnapshot call would receive an empty volume id and the
	// snapshot would be stranded.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), snapshotID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("volume_id"), volumeID)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Wait for the snapshot to finish capturing.
	waitErr := waiter.WaitFor(ctx, waiter.Options{
		Interval: pollInterval(),
		Timeout:  createTimeout,
		Refresh: waiter.StatePollerWithErrorTolerance(
			func() (map[string]any, error) { return r.client.GetVolumeSnapshot(ctx, volumeID, snapshotID) },
			"status",
			[]string{"available"},
			[]string{"failed"},
			3,
		),
	})
	if waitErr != nil {
		resp.Diagnostics.AddError(
			"Error waiting for volume snapshot",
			fmt.Sprintf("snapshot %s on volume %s did not become available: %s", snapshotID, volumeID, waitErr.Error()),
		)
		return
	}

	obj, err := r.client.GetVolumeSnapshot(ctx, volumeID, snapshotID)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading volume snapshot after creation", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, volumeSnapshotStateFromAPI(obj, plan))...)
}

// Read refreshes state from the parent volume's embedded snapshots[]. A 404
// (snapshot or its volume gone) removes the resource from state.
func (r *volumeSnapshotResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state volumeSnapshotModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	obj, err := r.client.GetVolumeSnapshot(ctx, state.VolumeID.ValueString(), state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("Error reading volume snapshot", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, volumeSnapshotStateFromAPI(obj, state))...)
}

// Update is unreachable: every configurable attribute (volume_id, name) is
// RequiresReplace, so the framework recreates rather than updating. It is
// implemented (no-op refresh) only to satisfy the resource.Resource interface.
func (r *volumeSnapshotResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan volumeSnapshotModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

// Delete enqueues snapshot deletion and waits for the snapshot row to disappear
// from the parent volume's snapshots[] (the 404 convergence signal). The
// snapshot flips to status="deleting" first, then is removed when the slave
// reports back.
func (r *volumeSnapshotResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state volumeSnapshotModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	deleteTimeout, diags := state.Timeouts.Delete(ctx, defaultDeleteTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	volumeID := state.VolumeID.ValueString()
	snapshotID := state.ID.ValueString()

	if err := r.client.DeleteVolumeSnapshot(ctx, volumeID, snapshotID); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deleting volume snapshot", err))
		return
	}

	waitErr := waiter.WaitFor(ctx, waiter.Options{
		Interval: pollInterval(),
		Timeout:  deleteTimeout,
		Refresh: func() (string, bool, error) {
			_, err := r.client.GetVolumeSnapshot(ctx, volumeID, snapshotID)
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
			"Error waiting for volume snapshot deletion",
			fmt.Sprintf("snapshot %s on volume %s was not removed: %s", snapshotID, volumeID, waitErr.Error()),
		)
		return
	}
}

// ImportState implements COMPOSITE import for this child resource: the parent
// volume id is required to build the API path, so `terraform import` must supply
// BOTH ids joined by a slash:
//
//	terraform import iaas_volume_snapshot.x <volume_id>/<snapshot_id>
func (r *volumeSnapshotResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	volumeID, snapshotID, ok := strings.Cut(req.ID, "/")
	if !ok || volumeID == "" || snapshotID == "" {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Expected import ID in the format \"volume_id/snapshot_id\", got: %q. "+
				"Volume snapshots are child resources, so both the parent volume id and the "+
				"snapshot id are required to import.", req.ID),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("volume_id"), volumeID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), snapshotID)...)
}

// volumeSnapshotStateFromAPI builds the model from an embedded snapshot object,
// falling back to the prior model's value for fields the response omits. volume_id
// and name are authoritative from the plan/state (name is the lookup key; the
// embedded object also carries it).
func volumeSnapshotStateFromAPI(obj map[string]any, prior volumeSnapshotModel) volumeSnapshotModel {
	return volumeSnapshotModel{
		ID:       stringFromAPI(obj, "id", prior.ID),
		VolumeID: prior.VolumeID, // from the path, not the embedded object
		Name:     stringOrPrior(obj, "name", prior.Name),
		Status:   stringFromAPI(obj, "status", prior.Status),
		Size:     int64FromAPI(obj, "size", prior.Size),
		Timeouts: prior.Timeouts,
	}
}
