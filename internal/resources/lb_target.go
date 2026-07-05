package resources

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hypervisor-io/terraform-provider-iaas/internal/client"
)

// Interface assertions. iaas_lb_target is a 3-LEVEL CHILD resource: a target
// belongs to a backend, which belongs to a load balancer. BOTH parent ids
// (load_balancer_id, backend_id) are in the API path, so both are Required +
// RequiresReplace, and import is a 3-part composite. Read scans the LB SHOW
// backends[].targets[]. Writes are SYNCHRONOUS (no waiter).
var (
	_ resource.Resource                = &lbTargetResource{}
	_ resource.ResourceWithConfigure   = &lbTargetResource{}
	_ resource.ResourceWithImportState = &lbTargetResource{}
)

// NewLBTargetResource is the resource constructor registered with the provider.
func NewLBTargetResource() resource.Resource {
	return &lbTargetResource{}
}

// lbTargetResource manages an iaas_lb_target - a backend member of a load balancer.
type lbTargetResource struct {
	client *client.Client
}

// lbTargetModel maps the Terraform state/plan for iaas_lb_target.
//
// load_balancer_id + backend_id are in the path (Required + RequiresReplace).
// target_ip + target_port form the backend-unique key (immutable in practice;
// changing either is effectively a new member → RequiresReplace). instance_id is
// an optional link (RequiresReplace). weight/enabled are updatable in place.
type lbTargetModel struct {
	ID             types.String `tfsdk:"id"`
	LoadBalancerID types.String `tfsdk:"load_balancer_id"`
	BackendID      types.String `tfsdk:"backend_id"`
	InstanceID     types.String `tfsdk:"instance_id"`
	TargetIP       types.String `tfsdk:"target_ip"`
	TargetPort     types.Int64  `tfsdk:"target_port"`
	Weight         types.Int64  `tfsdk:"weight"`
	Enabled        types.Bool   `tfsdk:"enabled"`
}

// Metadata sets the resource type name → "iaas_lb_target".
func (r *lbTargetResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_lb_target"
}

// Schema describes the iaas_lb_target resource.
func (r *lbTargetResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a target (backend member) of a load balancer backend. A target is a " +
			"child of a backend, which is a child of a load balancer, so BOTH the " +
			"load_balancer_id and backend_id are part of the API path and changing either " +
			"forces a new resource. A target is uniquely identified within its backend by " +
			"(target_ip, target_port). Optionally link it to an instance via instance_id. The " +
			"weight and enabled flag can be changed in place. Import with a 3-part composite " +
			"id: \"<load_balancer_id>/<backend_id>/<target_id>\".",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the target, assigned by the API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"load_balancer_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the parent load balancer. Part of the API path; changing it " +
					"forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"backend_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the parent backend this target belongs to. Part of the API path; " +
					"changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"instance_id": schema.StringAttribute{
				Optional: true,
				Description: "Optional UUID of the instance this target points at (link/tracking only - " +
					"the API does NOT derive target_ip from it). Changing it forces a new resource. " +
					"(API field: target_instance_id.)",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"target_ip": schema.StringAttribute{
				Required: true,
				Description: "IP address of the target server. Together with target_port it forms the " +
					"backend-unique key, so changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"target_port": schema.Int64Attribute{
				Required: true,
				Description: "Port on the target server. Together with target_ip it forms the " +
					"backend-unique key, so changing it forces a new resource.",
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
				},
			},
			"weight": schema.Int64Attribute{
				Optional:    true,
				Computed:    true,
				Description: "Relative weight for load balancing (1-256, default 100). Updatable in place.",
			},
			"enabled": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Whether the target receives traffic. Defaults to true. Updatable in place.",
			},
		},
	}
}

// Configure pulls the shared *client.Client from the provider (nil-guarded).
func (r *lbTargetResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// targetBody builds the wire body from the plan, omitting unset optionals and
// mapping the schema names to the API column names (instance_id→target_instance_id).
func targetBody(plan lbTargetModel, includeKey bool) map[string]any {
	body := map[string]any{}
	if includeKey {
		body["target_ip"] = plan.TargetIP.ValueString()
		body["target_port"] = plan.TargetPort.ValueInt64()
	}
	if !plan.InstanceID.IsNull() && !plan.InstanceID.IsUnknown() && plan.InstanceID.ValueString() != "" {
		body["target_instance_id"] = plan.InstanceID.ValueString()
	}
	if !plan.Weight.IsNull() && !plan.Weight.IsUnknown() {
		body["weight"] = plan.Weight.ValueInt64()
	}
	if !plan.Enabled.IsNull() && !plan.Enabled.IsUnknown() {
		body["enabled"] = plan.Enabled.ValueBool()
	}
	return body
}

// Create adds the target to its backend (synchronous), then reads back by scan.
func (r *lbTargetResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan lbTargetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	lbID := plan.LoadBalancerID.ValueString()
	backendID := plan.BackendID.ValueString()
	created, err := r.client.CreateLBTarget(ctx, lbID, backendID, targetBody(plan, true))
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating load balancer target", err))
		return
	}
	id, _ := created["id"].(string)
	if id == "" {
		resp.Diagnostics.AddError("Error creating load balancer target", "the create response did not include a target id")
		return
	}

	obj, err := r.client.GetLBTarget(ctx, lbID, backendID, id)
	if err != nil {
		obj = created
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, lbTargetStateFromAPI(obj, plan))...)
}

// Read refreshes state by scanning the LB SHOW backends[].targets[]. A 404 removes it.
func (r *lbTargetResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state lbTargetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	obj, err := r.client.GetLBTarget(ctx, state.LoadBalancerID.ValueString(), state.BackendID.ValueString(), state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("Error reading load balancer target", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, lbTargetStateFromAPI(obj, state))...)
}

// Update patches the mutable fields (weight, enabled). target_ip/target_port and
// the parent ids are RequiresReplace, so they never reach here. We omit the key
// fields from the PATCH body (only the mutable ones are sent).
func (r *lbTargetResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan lbTargetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	lbID := plan.LoadBalancerID.ValueString()
	backendID := plan.BackendID.ValueString()
	body := map[string]any{}
	if !plan.Weight.IsNull() && !plan.Weight.IsUnknown() {
		body["weight"] = plan.Weight.ValueInt64()
	}
	if !plan.Enabled.IsNull() && !plan.Enabled.IsUnknown() {
		body["enabled"] = plan.Enabled.ValueBool()
	}

	if _, err := r.client.UpdateLBTarget(ctx, lbID, backendID, plan.ID.ValueString(), body); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error updating load balancer target", err))
		return
	}

	obj, err := r.client.GetLBTarget(ctx, lbID, backendID, plan.ID.ValueString())
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading load balancer target after update", err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, lbTargetStateFromAPI(obj, plan))...)
}

// Delete removes the target from its backend.
func (r *lbTargetResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state lbTargetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteLBTarget(ctx, state.LoadBalancerID.ValueString(), state.BackendID.ValueString(), state.ID.ValueString()); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deleting load balancer target", err))
		return
	}
}

// ImportState implements 3-PART COMPOSITE import:
// "<load_balancer_id>/<backend_id>/<target_id>".
func (r *lbTargetResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Expected import ID in the format \"load_balancer_id/backend_id/target_id\", got: %q. "+
				"Load balancer targets are nested child resources, so all three ids are required to import.", req.ID),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("load_balancer_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("backend_id"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[2])...)
}

// lbTargetStateFromAPI builds the model from an embedded target object. The parent
// ids come from the path. The embedded object carries target_instance_id (mapped
// to instance_id), target_ip, target_port, weight, enabled.
func lbTargetStateFromAPI(obj map[string]any, prior lbTargetModel) lbTargetModel {
	return lbTargetModel{
		ID:             stringFromAPI(obj, "id", prior.ID),
		LoadBalancerID: prior.LoadBalancerID, // from the path
		BackendID:      prior.BackendID,      // from the path
		InstanceID:     optionalStringFromAPI(obj, "target_instance_id", prior.InstanceID),
		TargetIP:       stringOrPrior(obj, "target_ip", prior.TargetIP),
		TargetPort:     int64FromAPI(obj, "target_port", prior.TargetPort),
		Weight:         int64FromAPI(obj, "weight", prior.Weight),
		Enabled:        boolFromIntAPI(obj, "enabled", prior.Enabled),
	}
}
