package resources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/iaas/terraform-provider-iaas/internal/client"
	"github.com/iaas/terraform-provider-iaas/internal/waiter"
)

// Interface assertions. iaas_image captures a custom image FROM an instance
// (Gap G4) - distinct from the existing iaas_image DATA SOURCE (catalog
// lookup by name). A resource and a data source may share the type name
// "iaas_image": Resources() and DataSources() are two separate registries in
// terraform-plugin-framework, keyed independently by Terraform Core, so
// `resource "iaas_image" "x" {}` and `data "iaas_image" "y" {}` coexist with no
// conflict.
//
// There is no SHOW route for user images, so Read lists and matches by id
// (user_script.go pattern). Capture is asynchronous (instance.go/
// volume_snapshot.go waiter pattern): create returns the Image row
// synchronously with status "creating"; this resource waits for status to
// converge to "available" (ready) or "error" (fail). There is no update
// route, so every input is RequiresReplace (volume_snapshot.go /
// lb_certificate.go no-update pattern) and Update is a formality that is
// never actually invoked with a diff. Delete is synchronous (the API hard
// deletes the row inline), so no delete-side waiter is needed.
var (
	_ resource.Resource                = &imageResource{}
	_ resource.ResourceWithConfigure   = &imageResource{}
	_ resource.ResourceWithImportState = &imageResource{}
)

// NewImageResource is the resource constructor registered with the provider.
func NewImageResource() resource.Resource {
	return &imageResource{}
}

// imageResource manages an iaas_image captured from an instance.
type imageResource struct {
	client *client.Client
}

// imageModel maps the Terraform state/plan for iaas_image.
//
// name/instance_id are Required+RequiresReplace (no update endpoint exists).
// cloudinit/type are Optional+Computed+RequiresReplace: the API fills a
// default from the source instance when omitted, and that server-assigned
// value is stable after create, so UseStateForUnknown is safe alongside
// RequiresReplace. status/size are SERVER-MUTABLE computed (status converges
// creating -> available/error; size is populated once capture completes) -
// per the golden guardrail (see instance.go) they do NOT get
// UseStateForUnknown, so a refreshed value is never masked.
type imageModel struct {
	ID         types.String `tfsdk:"id"`
	Name       types.String `tfsdk:"name"`
	InstanceID types.String `tfsdk:"instance_id"`
	Cloudinit  types.Bool   `tfsdk:"cloudinit"`
	Type       types.String `tfsdk:"type"`
	Status     types.String `tfsdk:"status"`
	Size       types.Int64  `tfsdk:"size"`

	Timeouts timeouts.Value `tfsdk:"timeouts"`
}

// Metadata sets the resource type name → "iaas_image".
func (r *imageResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_image"
}

// Schema describes the iaas_image resource.
func (r *imageResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Captures a custom image (snapshot) from one of your instances. Creation is " +
			"asynchronous: the image row is created immediately with status \"creating\" and " +
			"this resource waits for the hypervisor to finish the capture (status " +
			"\"available\") or report a failure (status \"error\"). There is no update " +
			"endpoint for images, so every input forces a new resource. Distinct from the " +
			"`iaas_image` data source, which looks up an existing catalog/user image by name.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the image, assigned by the API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required: true,
				Description: "Display name for the resulting image (max 255 characters). There is no " +
					"rename endpoint, so changing this forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"instance_id": schema.StringAttribute{
				Required:    true,
				Description: "UUID of an instance you own to snapshot. Changing this forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"cloudinit": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Description: "Whether the resulting image is marked cloud-init enabled. Defaults to " +
					"the source instance's image setting when omitted. Changing this forces a new resource.",
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.RequiresReplace(),
					boolplanmodifier.UseStateForUnknown(),
				},
			},
			"type": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "OS family override: `linux`, `windows`, or `other`. Defaults to the " +
					"source instance's image type when omitted. Changing this forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			// status / size are SERVER-MUTABLE computed: status transitions
			// creating -> available/error; size is populated once the hypervisor
			// reports the captured bytes. Per the golden guardrail, do NOT attach
			// UseStateForUnknown.
			"status": schema.StringAttribute{
				Computed:    true,
				Description: "Lifecycle status: \"creating\", \"available\", or \"error\". Server-mutable.",
			},
			"size": schema.Int64Attribute{
				Computed:    true,
				Description: "Captured size of the image in bytes, populated once capture completes. Server-mutable.",
			},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{
				Create: true,
			}),
		},
	}
}

// Configure pulls the shared *client.Client from the provider (nil-guarded).
func (r *imageResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create snapshots the instance and waits for the capture to converge:
//
//  1. CreateImage records the image row synchronously (status "creating") and
//     returns its id.
//  2. The id is persisted to state BEFORE the wait, so a capture failure or
//     timeout still tracks the (possibly still-creating) image for a
//     subsequent destroy.
//  3. WaitFor polls GetImage's status until "available" (fail on "error").
//  4. A final GetImage hydrates the computed fields (notably size).
func (r *imageResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan imageModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	createTimeout, diags := plan.Timeouts.Create(ctx, defaultCreateTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	created, err := r.client.CreateImage(ctx, imageCreateFields(plan))
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating image", err))
		return
	}
	id, _ := created["id"].(string)
	if id == "" {
		resp.Diagnostics.AddError(
			"Error creating image",
			"create response did not include an image id",
		)
		return
	}

	// Persist the id immediately so a failed/timed-out capture still tracks the
	// resource for cleanup on the next destroy.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// ── ASYNC convergence: poll until the capture finishes ──────────────────
	// Tolerance=3: up to 3 consecutive transport blips are silently skipped so a
	// brief network hiccup during a long capture does not abort the whole create.
	waitErr := waiter.WaitFor(ctx, waiter.Options{
		Interval: pollInterval(),
		Timeout:  createTimeout,
		Refresh: waiter.StatePollerWithErrorTolerance(
			func() (map[string]any, error) { return r.client.GetImage(ctx, id) },
			"status",
			[]string{"available"},
			[]string{"error"},
			3,
		),
	})
	if waitErr != nil {
		resp.Diagnostics.AddError(
			"Error waiting for image capture",
			fmt.Sprintf("image %s did not become available: %s", id, waitErr.Error()),
		)
		return
	}

	obj, err := r.client.GetImage(ctx, id)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading image after capture", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, imageStateFromAPI(obj, plan))...)
}

// Read refreshes state from the API. A 404 means the image was deleted out of
// band - remove it from state so Terraform plans a recreate (drift handling).
func (r *imageResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state imageModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	obj, err := r.client.GetImage(ctx, state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("Error reading image", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, imageStateFromAPI(obj, state))...)
}

// Update is unreachable in practice: every attribute is RequiresReplace, so
// Terraform Core forces a replacement instead of calling Update for any
// configuration change. It is implemented only to satisfy resource.Resource.
func (r *imageResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan imageModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

// Delete removes the image. Deletion is synchronous - deleteUserImage hard
// deletes the row inline before returning - so no delete waiter is needed.
func (r *imageResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state imageModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteImage(ctx, state.ID.ValueString()); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deleting image", err))
		return
	}
}

// ImportState lets `terraform import iaas_image.x <uuid>` adopt an existing
// image; the next Read populates the rest of the attributes.
func (r *imageResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// imageCreateFields builds the POST /images request body. cloudinit/type are
// only sent when set in config, letting the API apply its source-instance
// default when they are omitted.
func imageCreateFields(m imageModel) map[string]any {
	fields := map[string]any{
		"instance_id": m.InstanceID.ValueString(),
		"name":        m.Name.ValueString(),
	}
	if !m.Cloudinit.IsNull() && !m.Cloudinit.IsUnknown() {
		fields["cloudinit"] = m.Cloudinit.ValueBool()
	}
	if !m.Type.IsNull() && !m.Type.IsUnknown() {
		fields["type"] = m.Type.ValueString()
	}
	return fields
}

// imageStateFromAPI builds the model from an API image object (either the
// create response's bare Image object or a list-derived item - both carry the
// same base columns), falling back to the prior model's value for any field
// the response omits.
func imageStateFromAPI(obj map[string]any, prior imageModel) imageModel {
	return imageModel{
		ID:         stringFromAPI(obj, "id", prior.ID),
		Name:       stringFromAPI(obj, "name", prior.Name),
		InstanceID: stringOrPrior(obj, "instance_id", prior.InstanceID),
		Cloudinit:  boolFromIntAPI(obj, "cloudinit", prior.Cloudinit),
		Type:       stringFromAPI(obj, "type", prior.Type),
		Status:     stringFromAPI(obj, "status", prior.Status),
		Size:       int64FromAPI(obj, "size", prior.Size),
		Timeouts:   prior.Timeouts,
	}
}
