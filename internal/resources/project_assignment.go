package resources

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/iaas/terraform-provider-iaas/internal/client"
)

// Interface assertions. iaas_project_assignment is a STANDALONE resource
// (Wave C, gap-fill T10) linking ONE taggable resource (instance, vpc,
// load_balancer, s3_bucket, or managed_database) to ONE iaas_project, via
// ProjectController::assignResource (POST /project/assign-resource).
//
// MODEL DECISION — standalone assignment resource, NOT a project_id
// attribute added to iaas_instance/iaas_vpc/iaas_load_balancer/iaas_s3_bucket/
// iaas_managed_database:
//
//   - Every assignable type DOES carry project_id un-hidden on its own SHOW
//     response (verified against each Model's $hidden array on the Master:
//     Instance, VPC, LoadBalancer, ManagedDatabase all declare no $hidden
//     covering it; S3Bucket hides only secret_key). So a project_id
//     read-back attribute IS technically possible on all five.
//   - But NONE of the five CREATE endpoints accept project_id in their
//     create body (confirmed for instance's two-phase
//     CreateCSInstance/DeployInstance and vpc's CreateVPC — both bodies are
//     fixed sets with no project_id field) — assignment is ALWAYS a separate
//     POST /project/assign-resource call, regardless of which resource
//     holds the attribute. Bolting that side-effect onto five already-
//     shipped, independently-tested resources (each gaining an "if
//     project_id changed, call assign-resource instead of / in addition to
//     the normal PATCH" branch in Update, plus Create-time handling) is five
//     times the surface area and risk for the exact same amount of work a
//     single new resource does once.
//   - A standalone resource also matches how the API itself models the
//     concept — assign-resource is an orthogonal tagging action, not part of
//     any one resource's own lifecycle — and it can manage membership for a
//     resource NOT created by this Terraform run (e.g. one adopted via
//     `terraform import iaas_instance...`) without importing that resource's
//     entire other state.
//
// Given the above, this is the plan's "recommended, drift-safe" path.
//
//   - project_id / resource_type / resource_id are all Required +
//     RequiresReplace: there is no "move an assignment" operation, only
//     assign/unassign, so any change is delete-old (unassign) + create-new
//     (assign).
//   - resource_type is constrained to exactly the five values
//     ProjectController's $modelMap accepts.
//   - the ASSIGN endpoint returns NO object at all ({success,message}) — not
//     even the resource's own id — so there is nothing to synthesize a
//     server-assigned "id" from. The resource's own id is instead a
//     COMPOSITE of its three inputs: "<project_id>/<resource_type>/<resource_id>".
//   - there is no per-assignment SHOW/list-membership route. GET
//     /project/{id} embeds paginated (10/page) per-type resource lists that
//     are unsuitable for a targeted "is resource X still assigned"
//     check — the target could be past page 1. Read instead re-fetches the
//     TARGET RESOURCE's own SHOW and compares its project_id to what this
//     resource expects (client.GetResourceProjectID) — authoritative and
//     always a single, non-paginated lookup.
//   - Delete calls the SAME assign-resource endpoint with project_id = null
//     ("Set project_id to null to unassign" — the controller's own doc
//     comment) — there is no dedicated detach/DELETE route. A 404 on the
//     target resource during Delete (it was destroyed out of band) is
//     treated as a no-op success: nothing to unassign.
//   - import takes a 3-PART composite id "<project_id>/<resource_type>/<resource_id>"
//     (same shape as iaas_kubernetes_security_group_rule's cluster_id/scope/rule_id).
var (
	_ resource.Resource                = &projectAssignmentResource{}
	_ resource.ResourceWithConfigure   = &projectAssignmentResource{}
	_ resource.ResourceWithImportState = &projectAssignmentResource{}
)

// NewProjectAssignmentResource is the resource constructor registered with the provider.
func NewProjectAssignmentResource() resource.Resource {
	return &projectAssignmentResource{}
}

// projectAssignmentResource manages an iaas_project_assignment — the link
// between one taggable resource and one project.
type projectAssignmentResource struct {
	client *client.Client
}

// projectAssignmentModel maps the Terraform state/plan for
// iaas_project_assignment. Every attribute is either the synthesized id or
// one of the three inputs — there is nothing server-derived beyond that.
type projectAssignmentModel struct {
	ID           types.String `tfsdk:"id"`
	ProjectID    types.String `tfsdk:"project_id"`
	ResourceType types.String `tfsdk:"resource_type"`
	ResourceID   types.String `tfsdk:"resource_id"`
}

// Metadata sets the resource type name → "iaas_project_assignment".
func (r *projectAssignmentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project_assignment"
}

// Schema describes the iaas_project_assignment resource.
func (r *projectAssignmentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Assigns a single resource (instance, vpc, load_balancer, s3_bucket, or " +
			"managed_database) to an iaas_project (ProjectController::assignResource, " +
			"POST /project/assign-resource). There is no \"move\" operation — every attribute is " +
			"immutable; changing any of them unassigns the old link and assigns a new one. Deleting " +
			"this resource unassigns the resource from the project (sets its project_id back to " +
			"null) rather than deleting the underlying resource or the project. Import with a " +
			"3-part composite id: \"<project_id>/<resource_type>/<resource_id>\".",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
				Description: "Composite id \"<project_id>/<resource_type>/<resource_id>\" — the API's " +
					"assign-resource endpoint returns no object of its own (and there is no " +
					"dedicated assignment row/id to read back), so the id is synthesized from the " +
					"three inputs.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"project_id": schema.StringAttribute{
				Required:    true,
				Description: "UUID of the iaas_project to assign the resource to. Immutable; changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"resource_type": schema.StringAttribute{
				Required: true,
				Description: "Type of resource being assigned: \"instance\", \"vpc\", \"load_balancer\", " +
					"\"s3_bucket\", or \"managed_database\" (exactly the set ProjectController's " +
					"$modelMap accepts). Immutable; changing it forces a new resource.",
				Validators: []validator.String{
					stringvalidator.OneOf("instance", "vpc", "load_balancer", "s3_bucket", "managed_database"),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"resource_id": schema.StringAttribute{
				Required:    true,
				Description: "UUID of the resource (of resource_type) to assign. Immutable; changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
		},
	}
}

// Configure pulls the shared *client.Client from the provider (nil-guarded).
func (r *projectAssignmentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create assigns resource_type/resource_id to project_id, persists the
// synthesized id immediately (so a failed verification still tracks the
// resource for cleanup on the next destroy), then verifies the assignment
// actually took effect by reading the TARGET RESOURCE back and comparing its
// project_id — the assign-resource response itself carries no object to
// confirm from.
func (r *projectAssignmentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan projectAssignmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	projectID := plan.ProjectID.ValueString()
	resourceType := plan.ResourceType.ValueString()
	resourceID := plan.ResourceID.ValueString()

	if err := r.client.AssignResourceToProject(ctx, resourceType, resourceID, projectID); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error assigning resource to project", err))
		return
	}

	id := projectAssignmentID(projectID, resourceType, resourceID)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	if resp.Diagnostics.HasError() {
		return
	}

	assigned, err := r.isAssigned(ctx, projectID, resourceType, resourceID)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error verifying project assignment", err))
		return
	}
	if !assigned {
		resp.Diagnostics.AddError(
			"Error assigning resource to project",
			fmt.Sprintf("assign-resource succeeded but %s %s does not show project_id %s on "+
				"read-back; check that the resource and project are both owned by the "+
				"authenticated account", resourceType, resourceID, projectID),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, projectAssignmentModel{
		ID:           types.StringValue(id),
		ProjectID:    types.StringValue(projectID),
		ResourceType: types.StringValue(resourceType),
		ResourceID:   types.StringValue(resourceID),
	})...)
}

// Read re-fetches the TARGET RESOURCE (not the project) and compares its
// current project_id to what this assignment expects. A 404 means the
// underlying resource itself was destroyed out of band — remove from state.
// A resource that exists but now shows a different (or no) project_id means
// the assignment itself was undone out of band (unassigned, reassigned, or
// bulk-reassigned elsewhere) — also remove from state so Terraform plans a
// re-create rather than silently drifting.
func (r *projectAssignmentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state projectAssignmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	projectID := state.ProjectID.ValueString()
	resourceType := state.ResourceType.ValueString()
	resourceID := state.ResourceID.ValueString()

	assigned, err := r.isAssigned(ctx, projectID, resourceType, resourceID)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("Error reading project assignment", err))
		return
	}
	if !assigned {
		resp.State.RemoveResource(ctx)
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Update is unreachable in practice: every attribute is RequiresReplace, so
// Terraform recreates rather than calling Update whenever any of them
// changes. It still must satisfy resource.Resource; it simply persists the
// plan (same pattern as iaas_vpn_peering / iaas_kubernetes_ssl_certificate —
// there is no update route to call).
func (r *projectAssignmentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan projectAssignmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

// Delete unassigns the resource from the project by calling the SAME
// assign-resource endpoint with project_id = null (the controller's own doc
// comment: "Set project_id to null to unassign") — there is no dedicated
// detach/DELETE route. A 404 on the target resource (destroyed out of band)
// is treated as a no-op success: there is nothing left to unassign. A 200
// response carrying success:false with a "not found"-shaped message is
// treated the same way (isNotFoundLikeError) — assign-resource's own error
// path (checkSuccessFlag in internal/client/decode.go) surfaces success:false
// as a PLAIN error, not an *APIError, so client.IsNotFound alone would not
// catch a target that was deleted out of band but reported this way instead
// of a genuine HTTP 404.
func (r *projectAssignmentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state projectAssignmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.client.AssignResourceToProject(ctx, state.ResourceType.ValueString(), state.ResourceID.ValueString(), "")
	if err != nil && !isNotFoundLikeError(err) {
		resp.Diagnostics.Append(diagFromErr("Error unassigning resource from project", err))
		return
	}
}

// isNotFoundLikeError reports whether err represents the assign-resource
// TARGET already being gone: either a genuine 404 (client.IsNotFound) or a
// 200 response carrying success:false with a "not found"-shaped message
// (decodeItem/checkSuccessFlag return that case as a plain fmt.Errorf(message),
// not an *APIError, so it does not satisfy client.IsNotFound on its own).
// Narrow by design — only a message containing "not found" is treated as
// benign — so unrelated assign-resource failures (auth, validation, server
// errors) still surface as real Delete errors instead of being swallowed.
func isNotFoundLikeError(err error) bool {
	if err == nil {
		return false
	}
	if client.IsNotFound(err) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "not found")
}

// ImportState implements 3-PART COMPOSITE import:
//
//	terraform import iaas_project_assignment.x <project_id>/<resource_type>/<resource_id>
func (r *projectAssignmentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Expected import ID in the format \"project_id/resource_type/resource_id\", "+
				"got: %q. iaas_project_assignment has no server-assigned id of its own, so all three "+
				"values are required to import.", req.ID),
		)
		return
	}
	if !client.IsValidProjectResourceType(parts[1]) {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("resource_type %q is not one of: instance, vpc, load_balancer, s3_bucket, managed_database.", parts[1]),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("resource_type"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("resource_id"), parts[2])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

// isAssigned reports whether resourceType/resourceID currently shows
// project_id == projectID on its own SHOW response. The underlying *APIError
// (including a 404 when the resource itself is gone) is returned unchanged
// so callers can distinguish "resource gone" (client.IsNotFound) from "still
// exists, just not assigned here" (false, nil).
func (r *projectAssignmentResource) isAssigned(ctx context.Context, projectID, resourceType, resourceID string) (bool, error) {
	current, err := r.client.GetResourceProjectID(ctx, resourceType, resourceID)
	if err != nil {
		return false, err
	}
	return current == projectID, nil
}

// projectAssignmentID synthesizes the composite id from the three inputs —
// the API has no id of its own to offer for this link.
func projectAssignmentID(projectID, resourceType, resourceID string) string {
	return projectID + "/" + resourceType + "/" + resourceID
}
