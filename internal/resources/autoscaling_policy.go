package resources

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/iaas/terraform-provider-iaas/internal/client"
)

// Interface assertions - iaas_autoscaling_policy is a CHILD of an autoscaling
// group: the parent group id is part of every request path, so it is Required +
// RequiresReplace, and import is the composite "<group_id>/<policy_id>". There is
// NO individual policy SHOW route - Read scans the group SHOW's policies[]
// (read-by-scan, mirroring the LB target). Writes are SYNCHRONOUS (no waiter).
var (
	_ resource.Resource                = &autoscalingPolicyResource{}
	_ resource.ResourceWithConfigure   = &autoscalingPolicyResource{}
	_ resource.ResourceWithImportState = &autoscalingPolicyResource{}
)

// NewAutoscalingPolicyResource is the resource constructor registered with the provider.
func NewAutoscalingPolicyResource() resource.Resource {
	return &autoscalingPolicyResource{}
}

// autoscalingPolicyResource manages an iaas_autoscaling_policy - a metric→scale
// rule attached to an autoscaling group.
type autoscalingPolicyResource struct {
	client *client.Client
}

// autoscalingPolicyModel maps the Terraform state/plan for iaas_autoscaling_policy.
//
// group_id is in the path (Required + RequiresReplace). metric +
// scale_up_threshold + scale_down_threshold are required inputs; the steps,
// cooldowns, and evaluation windows are Optional+Computed (the server supplies
// defaults). All non-path fields are updatable in place via PATCH.
type autoscalingPolicyModel struct {
	ID                 types.String `tfsdk:"id"`
	GroupID            types.String `tfsdk:"group_id"`
	Metric             types.String `tfsdk:"metric"`
	ScaleUpThreshold   types.Int64  `tfsdk:"scale_up_threshold"`
	ScaleDownThreshold types.Int64  `tfsdk:"scale_down_threshold"`
	ScaleUpStep        types.Int64  `tfsdk:"scale_up_step"`
	ScaleDownStep      types.Int64  `tfsdk:"scale_down_step"`
	ScaleUpCooldown    types.Int64  `tfsdk:"scale_up_cooldown"`
	ScaleDownCooldown  types.Int64  `tfsdk:"scale_down_cooldown"`
	EvaluationInterval types.Int64  `tfsdk:"evaluation_interval"`
	EvaluationWindow   types.Int64  `tfsdk:"evaluation_window"`
}

// Metadata sets the resource type name → "<provider>_autoscaling_policy".
func (r *autoscalingPolicyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_autoscaling_policy"
}

// Schema describes the iaas_autoscaling_policy resource.
func (r *autoscalingPolicyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a scaling policy attached to an autoscaling group. A policy drives " +
			"scale-up / scale-down on a metric (cpu or memory) when it crosses the configured " +
			"thresholds. The policy is a child of its group: the group_id is part of the API path, " +
			"so changing it forces a new resource. Import with the composite id " +
			"\"<group_id>/<policy_id>\".\n\n" +
			"Routes are gated by subuser permissions: `autoscaling.manage` (create/update), " +
			"`autoscaling.delete` (delete).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the policy, assigned by the API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"group_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the parent autoscaling group. Part of the API path; changing it " +
					"forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"metric": schema.StringAttribute{
				Required:    true,
				Description: "Metric driving the policy: `cpu` or `memory`. Updatable in place.",
			},
			"scale_up_threshold": schema.Int64Attribute{
				Required: true,
				Description: "Metric percentage (1-100) that triggers a scale-up event. Updatable in " +
					"place.",
			},
			"scale_down_threshold": schema.Int64Attribute{
				Required: true,
				Description: "Metric percentage (0-99) that triggers a scale-down event. Updatable in " +
					"place.",
			},
			"scale_up_step": schema.Int64Attribute{
				Optional:    true,
				Computed:    true,
				Description: "Number of instances added per scale-up event. Defaults to 1.",
			},
			"scale_down_step": schema.Int64Attribute{
				Optional:    true,
				Computed:    true,
				Description: "Number of instances removed per scale-down event. Defaults to 1.",
			},
			"scale_up_cooldown": schema.Int64Attribute{
				Optional: true,
				Computed: true,
				Description: "Seconds to wait after a scale-up before another is allowed (min 30). " +
					"Defaults to 300.",
			},
			"scale_down_cooldown": schema.Int64Attribute{
				Optional: true,
				Computed: true,
				Description: "Seconds to wait after a scale-down before another is allowed (min 30). " +
					"Defaults to 600.",
			},
			"evaluation_interval": schema.Int64Attribute{
				Optional:    true,
				Computed:    true,
				Description: "Seconds between policy evaluations (min 10). Defaults to 30.",
			},
			"evaluation_window": schema.Int64Attribute{
				Optional: true,
				Computed: true,
				Description: "Seconds of metric history considered per evaluation (min 30). " +
					"Defaults to 120.",
			},
		},
	}
}

// Configure pulls the shared *client.Client from the provider (nil-guarded).
func (r *autoscalingPolicyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// autoscalingPolicyBody builds the wire body from the plan, omitting unset
// Optional+Computed fields so the server applies its defaults.
func autoscalingPolicyBody(plan autoscalingPolicyModel) map[string]any {
	body := map[string]any{
		"metric":               plan.Metric.ValueString(),
		"scale_up_threshold":   plan.ScaleUpThreshold.ValueInt64(),
		"scale_down_threshold": plan.ScaleDownThreshold.ValueInt64(),
	}
	addInt := func(key string, v types.Int64) {
		if !v.IsNull() && !v.IsUnknown() {
			body[key] = v.ValueInt64()
		}
	}
	addInt("scale_up_step", plan.ScaleUpStep)
	addInt("scale_down_step", plan.ScaleDownStep)
	addInt("scale_up_cooldown", plan.ScaleUpCooldown)
	addInt("scale_down_cooldown", plan.ScaleDownCooldown)
	addInt("evaluation_interval", plan.EvaluationInterval)
	addInt("evaluation_window", plan.EvaluationWindow)
	return body
}

// Create adds the policy to its group (synchronous), then reads it back by scan
// to hydrate the server-defaulted fields.
func (r *autoscalingPolicyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan autoscalingPolicyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	groupID := plan.GroupID.ValueString()
	created, err := r.client.CreateAutoscalingPolicy(ctx, groupID, autoscalingPolicyBody(plan))
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating autoscaling policy", err))
		return
	}
	id, _ := created["id"].(string)
	if id == "" {
		resp.Diagnostics.AddError("Error creating autoscaling policy", "the create response did not include a policy id")
		return
	}

	// Read back by scan so the Optional+Computed defaults are populated. Fall back
	// to the create response if the scan fails (e.g. timing).
	obj, err := r.client.GetAutoscalingPolicy(ctx, groupID, id)
	if err != nil {
		obj = created
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, autoscalingPolicyStateFromAPI(obj, plan))...)
}

// Read refreshes state by scanning the group SHOW's policies[]. A 404 (group gone
// or policy absent) removes the resource so Terraform plans a recreate.
func (r *autoscalingPolicyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state autoscalingPolicyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	obj, err := r.client.GetAutoscalingPolicy(ctx, state.GroupID.ValueString(), state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("Error reading autoscaling policy", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, autoscalingPolicyStateFromAPI(obj, state))...)
}

// Update patches the mutable fields (all non-path fields). The group_id is
// RequiresReplace, so it never changes here.
func (r *autoscalingPolicyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state autoscalingPolicyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	groupID := state.GroupID.ValueString()
	id := state.ID.ValueString()
	if _, err := r.client.UpdateAutoscalingPolicy(ctx, groupID, id, autoscalingPolicyBody(plan)); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error updating autoscaling policy", err))
		return
	}

	obj, err := r.client.GetAutoscalingPolicy(ctx, groupID, id)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading autoscaling policy after update", err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, autoscalingPolicyStateFromAPI(obj, plan))...)
}

// Delete removes the policy from its group.
func (r *autoscalingPolicyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state autoscalingPolicyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteAutoscalingPolicy(ctx, state.GroupID.ValueString(), state.ID.ValueString()); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deleting autoscaling policy", err))
		return
	}
}

// ImportState implements COMPOSITE import "<group_id>/<policy_id>": both ids are
// required because the parent group id is part of the API path and is not
// derivable from the policy id alone.
func (r *autoscalingPolicyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	groupID, policyID, ok := strings.Cut(req.ID, "/")
	if !ok || groupID == "" || policyID == "" {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Expected import ID in the format \"group_id/policy_id\", got: %q. "+
				"Autoscaling policies are child resources, so both the parent group id and the "+
				"policy id are required to import.", req.ID),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("group_id"), groupID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), policyID)...)
}

// autoscalingPolicyStateFromAPI builds the model from an embedded policy object,
// falling back to the prior model for any omitted field. group_id is never in the
// object (it lives in the path), so it always falls back to the prior value.
func autoscalingPolicyStateFromAPI(obj map[string]any, prior autoscalingPolicyModel) autoscalingPolicyModel {
	return autoscalingPolicyModel{
		ID:                 stringFromAPI(obj, "id", prior.ID),
		GroupID:            prior.GroupID, // from the path
		Metric:             stringFromAPI(obj, "metric", prior.Metric),
		ScaleUpThreshold:   int64FromAPI(obj, "scale_up_threshold", prior.ScaleUpThreshold),
		ScaleDownThreshold: int64FromAPI(obj, "scale_down_threshold", prior.ScaleDownThreshold),
		ScaleUpStep:        int64FromAPI(obj, "scale_up_step", prior.ScaleUpStep),
		ScaleDownStep:      int64FromAPI(obj, "scale_down_step", prior.ScaleDownStep),
		ScaleUpCooldown:    int64FromAPI(obj, "scale_up_cooldown", prior.ScaleUpCooldown),
		ScaleDownCooldown:  int64FromAPI(obj, "scale_down_cooldown", prior.ScaleDownCooldown),
		EvaluationInterval: int64FromAPI(obj, "evaluation_interval", prior.EvaluationInterval),
		EvaluationWindow:   int64FromAPI(obj, "evaluation_window", prior.EvaluationWindow),
	}
}
