package resources

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
	"github.com/hypervisor-io/terraform-provider-iaas/waiter"
)

var (
	_ resource.Resource                = &sandboxVolumeResource{}
	_ resource.ResourceWithConfigure   = &sandboxVolumeResource{}
	_ resource.ResourceWithImportState = &sandboxVolumeResource{}
)

const sandboxVolumeDeleteTimeout = 15 * time.Minute

// sandboxVolumePollInterval is a variable only so tests can shorten it.
var sandboxVolumePollInterval = 3 * time.Second

// NewSandboxVolumeResource registers iaas_sandbox_volume: a persistent data volume for sandboxes (POST /storage/volumes
// with for="sandbox"). It is a different kind of object from iaas_volume (a KVM block volume from a volume plan), so it
// is its own resource.
func NewSandboxVolumeResource() resource.Resource { return &sandboxVolumeResource{} }

type sandboxVolumeResource struct{ client *client.Client }

type sandboxVolumeModel struct {
	ID         types.String   `tfsdk:"id"`
	Name       types.String   `tfsdk:"name"`
	SizeGib    types.Int64    `tfsdk:"size_gib"`
	LocationID types.String   `tfsdk:"location_id"`
	Status     types.String   `tfsdk:"status"`
	SandboxID  types.String   `tfsdk:"sandbox_id"`
	MountPath  types.String   `tfsdk:"mount_path"`
	CreatedAt  types.String   `tfsdk:"created_at"`
	Timeouts   timeouts.Value `tfsdk:"timeouts"`
}

func (r *sandboxVolumeResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_sandbox_volume"
}

func (r *sandboxVolumeResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Description: "A persistent data volume for sandboxes: a disk that keeps its data when a sandbox stops, pauses, resumes and (unless the attachment says delete_on_sandbox_delete) when the sandbox is deleted. " +
			"It lives on ONE node of its location and the sandbox that uses it runs on that node. Attach it with the volumes argument of iaas_microvm. It is billed per GiB-hour of its size in the location until it is destroyed. " +
			"Every argument forces a new volume (and loses the data): there is no resize in this release. Destroying a volume that a sandbox holds fails with volume_in_use; remove it from that sandbox (or destroy the sandbox) first. " +
			"Import by id. Snapshots of a sandbox do not contain its volumes.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, Description: "Volume UUID.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"name": schema.StringAttribute{
				Required:      true,
				Description:   "Display name. It also gives the default mount path (/mnt/<name>). Changing it forces a new volume.",
				PlanModifiers: replace,
			},
			"size_gib": schema.Int64Attribute{
				Required:      true,
				Description:   "Size in GiB (1-1024). Changing it forces a new volume: the data is lost.",
				Validators:    []validator.Int64{int64validator.Between(1, 1024)},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()},
			},
			"location_id": schema.StringAttribute{
				Required:      true,
				Description:   "The location (a location with sandbox nodes that offer volumes). Changing it forces a new volume.",
				PlanModifiers: replace,
			},
			"status":     schema.StringAttribute{Computed: true, Description: "available, attached, attaching, detaching, pending, deleting or error."},
			"sandbox_id": schema.StringAttribute{Computed: true, Description: "The sandbox that holds the volume; null while it is free."},
			"mount_path": schema.StringAttribute{Computed: true, Description: "Where the holding sandbox mounts it; null while free."},
			"created_at": schema.StringAttribute{Computed: true, Description: "When the volume was created (billing starts)."},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Delete: true}),
		},
	}
}

func (r *sandboxVolumeResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = microvmManagedClient(req, &resp.Diagnostics)
}

func (r *sandboxVolumeResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan sandboxVolumeModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	obj, err := r.client.CreateSandboxVolume(ctx, map[string]any{
		"for":         "sandbox",
		"name":        plan.Name.ValueString(),
		"size_gib":    plan.SizeGib.ValueInt64(),
		"location_id": plan.LocationID.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating sandbox volume", err))
		return
	}
	if id, _ := obj["id"].(string); id == "" {
		resp.Diagnostics.AddError("Invalid volume response", "The create response omitted the volume ID. List the account's volumes (GET /storage/volumes?for=sandbox) before retrying.")
		return
	}
	state := sandboxVolumeFromAPI(obj, plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *sandboxVolumeResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state sandboxVolumeModel
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
		resp.Diagnostics.Append(diagFromErr("Error reading sandbox volume", err))
		return
	}
	if kind, _ := obj["for"].(string); kind != "sandbox" {
		resp.Diagnostics.AddError("Not a sandbox volume", fmt.Sprintf("Volume %s is a KVM block volume; manage it with iaas_volume.", state.ID.ValueString()))
		return
	}
	switch status, _ := obj["status"].(string); status {
	case "deleting":
		resp.State.RemoveResource(ctx)
		return
	case "error":
		reason, _ := obj["failure_reason"].(string)
		resp.Diagnostics.AddWarning("Sandbox volume in error", fmt.Sprintf("The platform reports volume %s in error (reason: %s). Destroy it and create it again.", state.ID.ValueString(), reason))
	}
	state = sandboxVolumeFromAPI(obj, state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update never changes the volume: every argument forces a new one. It only refreshes the computed values (the
// timeouts block can change in place).
func (r *sandboxVolumeResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan sandboxVolumeModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	obj, err := r.client.GetVolume(ctx, plan.ID.ValueString())
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading sandbox volume", err))
		return
	}
	state := sandboxVolumeFromAPI(obj, plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *sandboxVolumeResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state sandboxVolumeModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, diags := state.Timeouts.Delete(ctx, sandboxVolumeDeleteTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	requested := false
	err := waiter.WaitFor(ctx, waiter.Options{
		Interval: sandboxVolumePollInterval,
		Timeout:  timeout,
		Refresh: func() (string, bool, error) {
			if !requested {
				if _, err := r.client.DeleteSandboxVolume(ctx, id); err != nil {
					if client.IsNotFound(err) {
						return "gone", true, nil
					}
					var apiErr *client.APIError
					if errors.As(err, &apiErr) && apiErr.Reason == "volume_in_use" {
						return "in_use", false, fmt.Errorf("a sandbox still holds this volume (volume_in_use): remove it from the sandbox's volumes or destroy the sandbox first")
					}
					// The node was not reachable: the platform kept the delete admitted and finishes it; look again.
					if errors.As(err, &apiErr) && apiErr.Reason == "volume_node_unavailable" {
						return "node_unavailable", false, nil
					}
					return "delete_failed", false, err
				}
				requested = true
			}
			if _, err := r.client.GetVolume(ctx, id); err != nil {
				if client.IsNotFound(err) {
					return "gone", true, nil
				}
				return "unknown", false, err
			}
			return "deleting", false, nil
		},
	})
	if err != nil {
		resp.Diagnostics.AddError("Error deleting sandbox volume", err.Error())
	}
}

func (r *sandboxVolumeResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// sandboxVolumeFromAPI maps the volume object onto the model. prior supplies the timeouts block and, before the
// first read, the configured values.
func sandboxVolumeFromAPI(obj map[string]any, prior sandboxVolumeModel) sandboxVolumeModel {
	s := prior
	s.ID = stringFromAPI(obj, "id", prior.ID)
	s.Name = stringFromAPI(obj, "name", prior.Name)
	s.SizeGib = optionalInt64FromAPI(obj, "size_gib")
	if s.SizeGib.IsNull() {
		s.SizeGib = prior.SizeGib
	}
	s.LocationID = stringFromAPI(obj, "location_id", prior.LocationID)
	s.Status = stringFromAPI(obj, "status", types.StringValue(""))
	s.SandboxID = optionalStringFromAPI(obj, "sandbox_id", types.StringNull())
	s.MountPath = optionalStringFromAPI(obj, "mount_path", types.StringNull())
	s.CreatedAt = optionalStringFromAPI(obj, "created_at", types.StringNull())

	return s
}
