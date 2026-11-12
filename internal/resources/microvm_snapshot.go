package resources

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
	"github.com/hypervisor-io/terraform-provider-iaas/waiter"
)

var (
	_ resource.Resource                = &microvmSnapshotResource{}
	_ resource.ResourceWithConfigure   = &microvmSnapshotResource{}
	_ resource.ResourceWithImportState = &microvmSnapshotResource{}
)

const (
	microvmSnapshotCreateTimeout = 30 * time.Minute
	microvmSnapshotDeleteTimeout = 15 * time.Minute
)

// microvmSnapshotPollInterval is a variable only so tests can shorten it.
var microvmSnapshotPollInterval = 3 * time.Second

// NewMicrovmSnapshotResource registers iaas_microvm_snapshot: a disk snapshot of
// a sandbox. The platform takes it asynchronously; create waits until it is
// ready.
func NewMicrovmSnapshotResource() resource.Resource { return &microvmSnapshotResource{} }

type microvmSnapshotResource struct{ client *client.Client }

type microvmSnapshotModel struct {
	ID                  types.String   `tfsdk:"id"`
	SandboxID           types.String   `tfsdk:"sandbox_id"`
	Name                types.String   `tfsdk:"name"`
	Description         types.String   `tfsdk:"description"`
	Consistency         types.String   `tfsdk:"consistency"`
	Kind                types.String   `tfsdk:"kind"`
	Status              types.String   `tfsdk:"status"`
	SourceState         types.String   `tfsdk:"source_state"`
	ConsistencyAchieved types.String   `tfsdk:"consistency_achieved"`
	SizeBytes           types.Int64    `tfsdk:"size_bytes"`
	UsedBytes           types.Int64    `tfsdk:"used_bytes"`
	DiskGib             types.Int64    `tfsdk:"disk_gib"`
	Vcpu                types.Int64    `tfsdk:"vcpu"`
	MemMib              types.Int64    `tfsdk:"mem_mib"`
	LocationID          types.String   `tfsdk:"location_id"`
	ImageID             types.String   `tfsdk:"image_id"`
	CaptureMethod       types.String   `tfsdk:"capture_method"`
	PausedMs            types.Int64    `tfsdk:"paused_ms"`
	VolumesExcluded     types.Int64    `tfsdk:"volumes_excluded"`
	BillableGib         types.Int64    `tfsdk:"billable_gib"`
	PriceGibHour        types.Float64  `tfsdk:"price_gib_hour"`
	CreatedAt           types.String   `tfsdk:"created_at"`
	ReadyAt             types.String   `tfsdk:"ready_at"`
	Timeouts            timeouts.Value `tfsdk:"timeouts"`
}

func (r *microvmSnapshotResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_microvm_snapshot"
}

func (r *microvmSnapshotResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	adopt := []planmodifier.String{snapshotReplaceUnlessAdopting{}}
	computedString := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, Description: desc}
	}
	resp.Schema = schema.Schema{
		Description: "Saves the disk of a sandbox as a snapshot and keeps it until destroyed. Memory is not saved: a sandbox started from the snapshot (iaas_microvm snapshot_id) boots cold from the saved disk. " +
			"The platform takes the snapshot asynchronously; create waits until it is ready and fails with the platform's reason if it fails. A snapshot is stored in the location it was taken in, billed per GiB-hour of the space it uses until it is destroyed, and survives the deletion of its sandbox. " +
			"A snapshot cannot be changed: every argument forces a new snapshot. Import by id; import reads sandbox_id from the platform (null when the sandbox is deleted) and leaves description and consistency null until configured, which adopts the configured value without replacing the snapshot.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, Description: "Snapshot UUID.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"sandbox_id": schema.StringAttribute{
				Required:      true,
				Description:   "UUID of the sandbox to snapshot (running, paused or stopped). Changing it forces a new snapshot.",
				PlanModifiers: adopt,
			},
			"name": schema.StringAttribute{
				Required:      true,
				Description:   "Unique per account: 1-64 characters of lowercase letters, digits, '.', '_' and '-'. Changing it forces a new snapshot.",
				Validators:    []validator.String{stringvalidator.LengthBetween(1, 64), stringvalidator.RegexMatches(regexp.MustCompile(`^[a-z0-9._-]+$`), "must contain only lowercase letters, digits, '.', '_' and '-'")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"description": schema.StringAttribute{
				Optional:      true,
				Description:   "Free text, up to 255 characters. The platform does not return it on import; configure it to adopt it.",
				Validators:    []validator.String{stringvalidator.LengthAtMost(255)},
				PlanModifiers: adopt,
			},
			"consistency": schema.StringAttribute{
				Optional:      true,
				Description:   "Requested consistency: auto (default), quiesced (fails when the file systems cannot be quiesced) or crash. The platform reports what it achieved in consistency_achieved.",
				Validators:    []validator.String{stringvalidator.OneOf("auto", "quiesced", "crash")},
				PlanModifiers: adopt,
			},
			"kind":                 computedString("Snapshot kind: disk."),
			"status":               computedString("creating, ready, failed, deleting or unavailable. A failed or deleting snapshot is dropped from state on refresh."),
			"source_state":         computedString("State of the sandbox when it was snapshotted: running, paused or stopped."),
			"consistency_achieved": computedString("quiesced, crash or clean."),
			"size_bytes":           schema.Int64Attribute{Computed: true, Description: "Disk size of the snapshot in bytes."},
			"used_bytes":           schema.Int64Attribute{Computed: true, Description: "Bytes the snapshot actually stores (what is billed, rounded up to whole GiB)."},
			"disk_gib":             schema.Int64Attribute{Computed: true, Description: "Disk size in GiB; a sandbox started from the snapshot needs a plan with at least this much disk."},
			"vcpu":                 schema.Int64Attribute{Computed: true, Description: "vCPUs of the source sandbox."},
			"mem_mib":              schema.Int64Attribute{Computed: true, Description: "Memory of the source sandbox in MiB."},
			"location_id":          computedString("Location the snapshot is stored in; a sandbox started from it runs there."),
			"image_id":             computedString("Image the source sandbox ran."),
			"capture_method":       computedString("reflink, paused_copy or offline_copy; empty when the location does not report it."),
			"paused_ms":            schema.Int64Attribute{Computed: true, Description: "Milliseconds the sandbox was paused for the snapshot; null when the location does not report it."},
			"volumes_excluded":     schema.Int64Attribute{Computed: true, Description: "How many data volumes the sandbox had attached that are NOT in this snapshot (a snapshot holds the root disk only); 0 when none."},
			"billable_gib":         schema.Int64Attribute{Computed: true, Description: "Whole GiB billed per hour."},
			"price_gib_hour":       schema.Float64Attribute{Computed: true, Description: "Price per GiB-hour of stored snapshot space in the snapshot's location (0 = free)."},
			"created_at":           computedString("When the snapshot was requested."),
			"ready_at":             computedString("When the snapshot became ready (billing starts)."),
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Delete: true}),
		},
	}
}

func (r *microvmSnapshotResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = microvmManagedClient(req, &resp.Diagnostics)
}

func (r *microvmSnapshotResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan microvmSnapshotModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, diags := plan.Timeouts.Create(ctx, microvmSnapshotCreateTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := map[string]any{"name": plan.Name.ValueString(), "kind": "disk"}
	putOptionalString(body, "description", plan.Description)
	putOptionalString(body, "consistency", plan.Consistency)
	obj, err := r.client.CreateMicrovmSnapshot(ctx, plan.SandboxID.ValueString(), body, "")
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating MicroVM snapshot", err))
		return
	}
	id, _ := obj["id"].(string)
	if id == "" {
		resp.Diagnostics.AddError("Invalid snapshot response", "The create response omitted the snapshot ID. List the account's snapshots before retrying.")
		return
	}
	// Record the snapshot before waiting: a timeout then leaves a tainted resource that destroy can remove.
	state := microvmSnapshotFromAPI(obj, plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	final, err := r.waitReady(ctx, id, timeout)
	if err != nil {
		resp.Diagnostics.AddError("MicroVM snapshot did not become ready", err.Error())
		return
	}
	state = microvmSnapshotFromAPI(final, plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// waitReady polls until the snapshot is ready. failed, deleting and a vanished snapshot are terminal errors that
// carry the platform's own failure_reason.
func (r *microvmSnapshotResource) waitReady(ctx context.Context, id string, timeout time.Duration) (map[string]any, error) {
	var last map[string]any
	blips := 0
	err := waiter.WaitFor(ctx, waiter.Options{
		Interval: microvmSnapshotPollInterval,
		Timeout:  timeout,
		Refresh: func() (string, bool, error) {
			obj, err := r.client.GetMicrovmSnapshot(ctx, id)
			if err != nil {
				if client.IsNotFound(err) {
					return "gone", false, fmt.Errorf("the snapshot disappeared while it was being taken")
				}
				// A brief transport blip must not fail a wait that can last minutes (tolerance 3, as the other waiters).
				if blips++; blips <= 3 {
					return "unknown", false, nil
				}
				return "unknown", false, err
			}
			blips = 0
			last = obj
			status, _ := obj["status"].(string)
			switch status {
			case "ready":
				return status, true, nil
			case "failed":
				return status, false, fmt.Errorf("the platform could not take the snapshot (reason: %s)%s", microvmSnapshotReason(obj), microvmSnapshotDetail(obj))
			case "deleting":
				return status, false, fmt.Errorf("the snapshot is being deleted")
			case "unavailable":
				return status, false, fmt.Errorf("the location holding the snapshot is not available")
			}
			return status, false, nil
		},
	})
	return last, err
}

// microvmSnapshotDetail renders failure_detail (the platform's message, and the pause estimate and limit in
// milliseconds when the location reports them) for an error text. Empty when absent.
func microvmSnapshotDetail(obj map[string]any) string {
	detail, ok := obj["failure_detail"].(map[string]any)
	if !ok {
		return ""
	}
	out := ""
	if m, ok := detail["message"].(string); ok && m != "" {
		out += ": " + m
	}
	est, hasEst := detail["pause_estimate_ms"].(float64)
	limit, hasLimit := detail["pause_limit_ms"].(float64)
	if hasEst && hasLimit {
		out += fmt.Sprintf(" (estimated pause %d ms, limit %d ms)", int64(est), int64(limit))
	}
	return out
}

func microvmSnapshotReason(obj map[string]any) string {
	if reason, ok := obj["failure_reason"].(string); ok && reason != "" {
		return reason
	}
	return "unknown"
}

func (r *microvmSnapshotResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state microvmSnapshotModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	obj, err := r.client.GetMicrovmSnapshot(ctx, state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("Error reading MicroVM snapshot", err))
		return
	}
	switch status, _ := obj["status"].(string); status {
	case "failed":
		resp.Diagnostics.AddWarning("MicroVM snapshot failed", fmt.Sprintf("The platform could not take snapshot %s (reason: %s); it is dropped from state so the next apply takes it again.", state.ID.ValueString(), microvmSnapshotReason(obj)))
		resp.State.RemoveResource(ctx)
		return
	case "deleting":
		resp.State.RemoveResource(ctx)
		return
	case "unavailable":
		resp.Diagnostics.AddWarning("MicroVM snapshot unavailable", "The location holding this snapshot cannot be reached right now. Sandboxes cannot be started from it until it is available again; it is not billed meanwhile.")
	}
	state = microvmSnapshotFromAPI(obj, state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update only adopts configuration the platform cannot return (after an import) and refreshes the computed values;
// it never changes the snapshot, which has no update call.
func (r *microvmSnapshotResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan microvmSnapshotModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	obj, err := r.client.GetMicrovmSnapshot(ctx, plan.ID.ValueString())
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading MicroVM snapshot", err))
		return
	}
	state := microvmSnapshotFromAPI(obj, plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *microvmSnapshotResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state microvmSnapshotModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, diags := state.Timeouts.Delete(ctx, microvmSnapshotDeleteTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	requested := false
	err := waiter.WaitFor(ctx, waiter.Options{
		Interval: microvmSnapshotPollInterval,
		Timeout:  timeout,
		Refresh: func() (string, bool, error) {
			if !requested {
				if err := r.client.DeleteMicrovmSnapshot(ctx, id); err != nil {
					if client.IsNotFound(err) {
						return "gone", true, nil
					}
					var apiErr *client.APIError
					if errors.As(err, &apiErr) && apiErr.Reason == "snapshot_in_use" {
						// A sandbox is still being created from it: try again shortly.
						return "in_use", false, nil
					}
					return "delete_failed", false, err
				}
				requested = true
			}
			if _, err := r.client.GetMicrovmSnapshot(ctx, id); err != nil {
				if client.IsNotFound(err) {
					return "gone", true, nil
				}
				return "unknown", false, err
			}
			return "deleting", false, nil
		},
	})
	if err != nil {
		resp.Diagnostics.AddError("Error deleting MicroVM snapshot", err.Error())
	}
}

func (r *microvmSnapshotResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// microvmSnapshotFromAPI maps the snapshot object onto the model. prior supplies what the platform cannot return:
// the requested consistency, and the timeouts block. sandbox_id and description follow the platform when it reports
// them (null once the sandbox is deleted: then the configured value is kept so a destroyed sandbox is not drift).
func microvmSnapshotFromAPI(obj map[string]any, prior microvmSnapshotModel) microvmSnapshotModel {
	s := prior
	s.ID = stringFromAPI(obj, "id", prior.ID)
	if source, ok := obj["source_sandbox_id"].(string); ok && source != "" {
		s.SandboxID = types.StringValue(source)
	} else if prior.SandboxID.IsUnknown() {
		s.SandboxID = types.StringNull()
	}
	s.Name = stringFromAPI(obj, "name", prior.Name)
	if description, ok := obj["description"].(string); ok && description != "" {
		s.Description = types.StringValue(description)
	} else if prior.Description.IsUnknown() {
		s.Description = types.StringNull()
	}
	if prior.Consistency.IsUnknown() {
		s.Consistency = types.StringNull()
	}
	s.Kind = stringFromAPI(obj, "kind", types.StringValue("disk"))
	s.Status = stringFromAPI(obj, "status", types.StringValue(""))
	s.SourceState = optionalStringFromAPI(obj, "source_state", types.StringNull())
	s.ConsistencyAchieved = optionalStringFromAPI(obj, "consistency", types.StringNull())
	s.SizeBytes = optionalInt64FromAPI(obj, "size_bytes")
	s.UsedBytes = optionalInt64FromAPI(obj, "used_bytes")
	s.DiskGib = optionalInt64FromAPI(obj, "disk_gib")
	s.Vcpu = optionalInt64FromAPI(obj, "vcpu")
	s.MemMib = optionalInt64FromAPI(obj, "mem_mib")
	s.LocationID = optionalStringFromAPI(obj, "location_id", types.StringNull())
	s.ImageID = optionalStringFromAPI(obj, "image_id", types.StringNull())
	s.CaptureMethod = optionalStringFromAPI(obj, "capture_method", types.StringNull())
	s.PausedMs = optionalInt64FromAPI(obj, "paused_ms")
	s.VolumesExcluded = optionalInt64FromAPI(obj, "volumes_excluded")
	s.BillableGib = optionalInt64FromAPI(obj, "billable_gib")
	s.PriceGibHour = types.Float64Null()
	if price, ok := obj["price_gib_hour"].(float64); ok {
		s.PriceGibHour = types.Float64Value(price)
	}
	s.CreatedAt = optionalStringFromAPI(obj, "created_at", types.StringNull())
	s.ReadyAt = optionalStringFromAPI(obj, "ready_at", types.StringNull())

	return s
}

// snapshotReplaceUnlessAdopting forces a new snapshot when a configured argument changes, EXCEPT when the state holds
// nothing for it (an import: the platform does not return the requested description and consistency, and returns no
// sandbox_id once the sandbox is deleted). Then the configured value is adopted in place.
type snapshotReplaceUnlessAdopting struct{}

func (snapshotReplaceUnlessAdopting) Description(_ context.Context) string {
	return "Forces a new snapshot when the value changes, but adopts a configured value in place when state holds none."
}
func (m snapshotReplaceUnlessAdopting) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}
func (snapshotReplaceUnlessAdopting) PlanModifyString(_ context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return // create or destroy
	}
	if req.PlanValue.IsUnknown() || req.StateValue.Equal(req.PlanValue) {
		return
	}
	if req.StateValue.IsNull() {
		return // adopt
	}
	resp.RequiresReplace = true
}
