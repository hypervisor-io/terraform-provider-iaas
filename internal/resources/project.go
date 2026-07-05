package resources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hypervisor-io/terraform-provider-iaas/internal/client"
)

// Interface assertions - project follows the golden ssh_key resource pattern.
var (
	_ resource.Resource                = &projectResource{}
	_ resource.ResourceWithConfigure   = &projectResource{}
	_ resource.ResourceWithImportState = &projectResource{}
)

// NewProjectResource is the resource constructor registered with the provider.
func NewProjectResource() resource.Resource {
	return &projectResource{}
}

// projectResource manages an iaas_project.
//
// All three write operations (create, update, delete) are synchronous - no
// async task/waiter is required. Create returns the new object (with id) in the
// "project" envelope, so no list-and-match read-back is needed.
//
// Route summary (verified against the real controller):
//
//	INDEX   GET    /projects       (plural)
//	CREATE  POST   /projects       (plural)  body → {success,message,project}
//	SHOW    GET    /project/{id}   (singular) → {success,project,...}
//	UPDATE  PATCH  /project/{id}   (singular) body → {success,message,project}
//	DELETE  DELETE /project/{id}   (singular) → {success,message}
type projectResource struct {
	client *client.Client
}

// projectModel maps the Terraform state/plan for iaas_project.
//
// Description and Color are Optional+Nullable: the server stores null when
// omitted, and optionalStringFromAPI collapses null to types.StringNull() so
// the value round-trips without spurious drift.
type projectModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	Color       types.String `tfsdk:"color"`
}

// Metadata sets the resource type name → "<provider>_project" → "iaas_project".
func (r *projectResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project"
}

// Schema describes the iaas_project resource.
func (r *projectResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a project in your IaaS account. Projects are logical groupings " +
			"that allow you to organise resources (instances, VPCs, load balancers, S3 buckets, " +
			"managed databases) under a single label. All fields except `id` can be updated " +
			"in place without replacing the resource.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the project, assigned by the API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required: true,
				Description: "Display name for the project. Maximum 64 characters; only " +
					"letters, digits, spaces, dots, hyphens, and underscores are allowed " +
					"(case-insensitive, validated server-side).",
			},
			"description": schema.StringAttribute{
				Optional: true,
				Description: "Optional free-text description of the project. " +
					"Maximum 255 characters. Set to null to clear.",
			},
			"color": schema.StringAttribute{
				Optional: true,
				Description: "Optional hex color code for the project UI badge " +
					"(e.g. `#3B82F6`). Must match the pattern `#[0-9A-Fa-f]{6}`. " +
					"Set to null to clear.",
			},
		},
	}
}

// Configure pulls the shared *client.Client from the provider. It tolerates a
// nil ProviderData (the framework calls Configure once with nil data before the
// provider's own Configure has run).
func (r *projectResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create provisions the project. name is always sent; description and color are
// sent only when set by the user (omitted entirely when null/unknown so the
// server stores null rather than an empty string).
func (r *projectResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan projectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := map[string]any{
		"name": plan.Name.ValueString(),
	}
	if !plan.Description.IsNull() && !plan.Description.IsUnknown() {
		body["description"] = plan.Description.ValueString()
	}
	if !plan.Color.IsNull() && !plan.Color.IsUnknown() {
		body["color"] = plan.Color.ValueString()
	}

	obj, err := r.client.CreateProject(ctx, body)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating project", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, projectStateFromAPI(obj, plan))...)
}

// Read refreshes state from the API. A 404 means the project was deleted out of
// band - remove it from state so Terraform plans a recreate (drift handling).
func (r *projectResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state projectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	obj, err := r.client.GetProject(ctx, state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("Error reading project", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, projectStateFromAPI(obj, state))...)
}

// Update patches mutable fields. name is always sent (required by the server);
// description and color are sent as null when cleared, or with their value when
// set. The UPDATE response carries the fresh project object, which we use to
// rehydrate state.
func (r *projectResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan projectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := map[string]any{
		"name": plan.Name.ValueString(),
	}
	// Send explicit null for description/color when the user clears them so the
	// server nullifies the field rather than leaving the prior value.
	if plan.Description.IsNull() {
		body["description"] = nil
	} else if !plan.Description.IsUnknown() {
		body["description"] = plan.Description.ValueString()
	}
	if plan.Color.IsNull() {
		body["color"] = nil
	} else if !plan.Color.IsUnknown() {
		body["color"] = plan.Color.ValueString()
	}

	obj, err := r.client.UpdateProject(ctx, plan.ID.ValueString(), body)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error updating project", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, projectStateFromAPI(obj, plan))...)
}

// Delete removes the project. Resources assigned to the project have their
// project_id set to null by the server (no cascade destroy).
func (r *projectResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state projectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteProject(ctx, state.ID.ValueString()); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deleting project", err))
		return
	}
}

// ImportState lets `terraform import iaas_project.x <uuid>` adopt an existing
// project; the next Read populates the rest of the attributes.
func (r *projectResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// projectStateFromAPI builds the model from an API project object, falling back
// to the prior model's value for any field the response omits.
//
// description and color use optionalStringFromAPI so that a server-returned
// null collapses to types.StringNull() (not "") and round-trips without drift
// against config that omits those optional attributes.
func projectStateFromAPI(obj map[string]any, prior projectModel) projectModel {
	return projectModel{
		ID:          stringFromAPI(obj, "id", prior.ID),
		Name:        stringFromAPI(obj, "name", prior.Name),
		Description: optionalStringFromAPI(obj, "description", prior.Description),
		Color:       optionalStringFromAPI(obj, "color", prior.Color),
	}
}
