package resources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/iaas/terraform-provider-iaas/internal/client"
	"github.com/iaas/terraform-provider-iaas/internal/waiter"
)

// Interface assertions - iaas_autoscaling_group manages a fleet of identical
// instances kept between min/max. It combines several golden patterns:
//   - WRITE-ONLY create-only fields (ssh_keys) echoed from plan,
//   - a `paused` bool toggled via dedicated pause/resume endpoints on diff,
//   - server-mutable computed status/current_count WITHOUT UseStateForUnknown,
//   - an ASYNC delete (background DestroyGroup job) polled until SHOW 404s,
//     so it carries a timeouts block (delete) like the instance resource.
var (
	_ resource.Resource                = &autoscalingGroupResource{}
	_ resource.ResourceWithConfigure   = &autoscalingGroupResource{}
	_ resource.ResourceWithImportState = &autoscalingGroupResource{}
)

// NewAutoscalingGroupResource is the resource constructor registered with the provider.
func NewAutoscalingGroupResource() resource.Resource {
	return &autoscalingGroupResource{}
}

// autoscalingGroupResource manages an iaas_autoscaling_group.
type autoscalingGroupResource struct {
	client *client.Client
}

// autoscalingGroupModel maps the Terraform state/plan for iaas_autoscaling_group.
//
// Field groups:
//   - REPLACE inputs (hypervisor_group_id, vpc_id, vpc_subnet_id,
//     load_balancer_id, lb_backend_id): part of create-only placement; the update
//     endpoint cannot change them, so they are RequiresReplace.
//   - WRITE-ONLY create-only (ssh_keys): consumed at create, RequiresReplace, and
//     preserved from prior in Read (SHOW returns ssh_key_ids but the value is
//     authoritative from config).
//   - MUTABLE (name, plan_id, image_id, min_instances, max_instances, cloud_init,
//     security_group_ids): updatable in place via PATCH.
//   - `paused`: Optional+Computed bool toggled via pause/resume endpoints; mirrors
//     the server `status` (paused → true, active/error → false).
//   - SERVER-MUTABLE computed (status, current_count): no UseStateForUnknown.
type autoscalingGroupModel struct {
	ID                types.String `tfsdk:"id"`
	Name              types.String `tfsdk:"name"`
	HypervisorGroupID types.String `tfsdk:"hypervisor_group_id"`
	PlanID            types.String `tfsdk:"plan_id"`
	ImageID           types.String `tfsdk:"image_id"`

	VPCID          types.String `tfsdk:"vpc_id"`
	VPCSubnetID    types.String `tfsdk:"vpc_subnet_id"`
	LoadBalancerID types.String `tfsdk:"load_balancer_id"`
	LBBackendID    types.String `tfsdk:"lb_backend_id"`

	MinInstances types.Int64 `tfsdk:"min_instances"`
	MaxInstances types.Int64 `tfsdk:"max_instances"`

	CloudInit        types.String `tfsdk:"cloud_init"`
	SSHKeys          types.List   `tfsdk:"ssh_keys"`
	SecurityGroupIDs types.Set    `tfsdk:"security_group_ids"`

	// paused: toggled via pause/resume; mirrors server status.
	Paused types.Bool `tfsdk:"paused"`

	// Server-mutable computed.
	Status       types.String `tfsdk:"status"`
	CurrentCount types.Int64  `tfsdk:"current_count"`

	Timeouts timeouts.Value `tfsdk:"timeouts"`
}

// Metadata sets the resource type name → "<provider>_autoscaling_group".
func (r *autoscalingGroupResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_autoscaling_group"
}

// Schema describes the iaas_autoscaling_group resource.
func (r *autoscalingGroupResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages an autoscaling group: a fleet of identical instances kept between " +
			"min_instances and max_instances. The launch placement (hypervisor_group_id, optional " +
			"VPC/subnet, optional load balancer backend) and the injected ssh_keys are fixed at " +
			"create time - changing any forces a new group. The launch template (plan_id, image_id, " +
			"cloud_init), the min/max bounds, the name, and the attached security_group_ids can be " +
			"changed in place. Set `paused` to true to stop the evaluator scaling the group (it calls " +
			"the pause endpoint); set it back to false to resume (which re-enforces min_instances). " +
			"Deletion is asynchronous: the API enqueues a job to destroy member instances and the " +
			"group, which this resource waits on by polling until the group disappears.\n\n" +
			"Routes are gated by subuser permissions: `autoscaling.view` (read), `autoscaling.manage` " +
			"(create/update/pause/resume), `autoscaling.delete` (destroy). The selected hypervisor " +
			"group must have autoscaling enabled, otherwise create fails.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the autoscaling group, assigned by the API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Friendly label for the group. Maximum 255 characters. Updatable in place.",
			},
			"hypervisor_group_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the hypervisor group new instances are launched into. The group " +
					"must have autoscaling enabled. Part of the launch placement; changing it forces a " +
					"new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"plan_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the instance plan used for every new instance. Updatable in place " +
					"(applies to future instances).",
			},
			"image_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the OS image used for every new instance. Updatable in place " +
					"(applies to future instances).",
			},
			"vpc_id": schema.StringAttribute{
				Optional: true,
				Description: "Optional UUID of a VPC to place instances in. Must be set together with " +
					"vpc_subnet_id. Part of the launch placement; changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"vpc_subnet_id": schema.StringAttribute{
				Optional: true,
				Description: "Optional UUID of a VPC subnet (within vpc_id) to place instances in. Must " +
					"be set together with vpc_id. Changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"load_balancer_id": schema.StringAttribute{
				Optional: true,
				Description: "Optional UUID of a load balancer for auto-registration of new instances. " +
					"Must be set together with lb_backend_id. Changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"lb_backend_id": schema.StringAttribute{
				Optional: true,
				Description: "Optional UUID of the load balancer backend new instances register with. " +
					"Must be set together with load_balancer_id. Changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"min_instances": schema.Int64Attribute{
				Optional: true,
				Computed: true,
				Description: "Minimum number of instances kept running. Defaults to 1. Updatable in " +
					"place; raising it scales up immediately, lowering it is enforced by the evaluator.",
			},
			"max_instances": schema.Int64Attribute{
				Optional: true,
				Computed: true,
				Description: "Maximum number of instances the group may scale to. Defaults to 5. " +
					"Updatable in place; lowering it below current_count scales down immediately.",
			},
			"cloud_init": schema.StringAttribute{
				Optional: true,
				Description: "Optional cloud-init user-data applied to every new instance. Updatable in " +
					"place (applies to future instances). The API stores it encrypted; it is echoed " +
					"from configuration and not refreshed from the server on read.",
			},
			"ssh_keys": schema.ListAttribute{
				ElementType: types.StringType,
				Optional:    true,
				Description: "Optional list of SSH key UUIDs injected into every new instance. WRITE-ONLY " +
					"create-only: the update endpoint does not accept it, so changing it forces a new " +
					"resource, and it is echoed from configuration rather than refreshed from the server.",
				PlanModifiers: []planmodifier.List{
					listplanmodifier.RequiresReplace(),
				},
			},
			"security_group_ids": schema.SetAttribute{
				ElementType: types.StringType,
				Optional:    true,
				Description: "UUIDs of the security groups attached to every new instance, as an " +
					"order-independent set. Updatable in place: the full desired set is sent and the " +
					"server replaces the attachment (pass an empty set to clear).",
			},
			"paused": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Description: "Whether the group is paused. When true the evaluator does not scale the " +
					"group. Toggling this calls the dedicated pause/resume endpoints (it is NOT sent in " +
					"the create/update body). Mirrors the server status (paused ⇒ true). Defaults to " +
					"false (active) on create.",
			},
			"status": schema.StringAttribute{
				Computed: true,
				Description: "Lifecycle status reported by the server: `active`, `paused`, or `error`. " +
					"Server-mutable.",
				// Server-mutable: NO UseStateForUnknown - the plan must always reflect
				// the refreshed value (e.g. the evaluator setting it to "error").
			},
			"current_count": schema.Int64Attribute{
				Computed: true,
				Description: "Number of instances the group currently tracks. Server-mutable; it changes " +
					"as the group scales up and down.",
				// Server-mutable: NO UseStateForUnknown.
			},
		},
		Blocks: map[string]schema.Block{
			// Only delete is async (background DestroyGroup job); create/update are
			// synchronous metadata writes. We still expose create/update timeouts for
			// symmetry with the other resources.
			"timeouts": timeouts.Block(ctx, timeouts.Opts{
				Create: true,
				Update: true,
				Delete: true,
			}),
		},
	}
}

// Configure pulls the shared *client.Client from the provider (nil-guarded).
func (r *autoscalingGroupResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create provisions the group. The create body carries the launch template +
// placement + bounds; `paused` is NOT part of the body (the group is created
// active). If the plan sets paused=true, we immediately call the pause endpoint
// after create so the resulting state matches the config.
func (r *autoscalingGroupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan autoscalingGroupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := map[string]any{
		"name":                plan.Name.ValueString(),
		"hypervisor_group_id": plan.HypervisorGroupID.ValueString(),
		"plan_id":             plan.PlanID.ValueString(),
		"image_id":            plan.ImageID.ValueString(),
	}
	if !plan.VPCID.IsNull() && !plan.VPCID.IsUnknown() {
		body["vpc_id"] = plan.VPCID.ValueString()
	}
	if !plan.VPCSubnetID.IsNull() && !plan.VPCSubnetID.IsUnknown() {
		body["vpc_subnet_id"] = plan.VPCSubnetID.ValueString()
	}
	if !plan.LoadBalancerID.IsNull() && !plan.LoadBalancerID.IsUnknown() {
		body["load_balancer_id"] = plan.LoadBalancerID.ValueString()
	}
	if !plan.LBBackendID.IsNull() && !plan.LBBackendID.IsUnknown() {
		body["lb_backend_id"] = plan.LBBackendID.ValueString()
	}
	if !plan.MinInstances.IsNull() && !plan.MinInstances.IsUnknown() {
		body["min_instances"] = plan.MinInstances.ValueInt64()
	}
	if !plan.MaxInstances.IsNull() && !plan.MaxInstances.IsUnknown() {
		body["max_instances"] = plan.MaxInstances.ValueInt64()
	}
	if !plan.CloudInit.IsNull() && !plan.CloudInit.IsUnknown() {
		body["cloud_init"] = plan.CloudInit.ValueString()
	}
	if keys := stringListValues(plan.SSHKeys); keys != nil {
		body["ssh_keys"] = keys
	}
	if sgs := stringSetValues(plan.SecurityGroupIDs); sgs != nil {
		body["security_group_ids"] = sgs
	}

	created, err := r.client.CreateAutoscalingGroup(ctx, body)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating autoscaling group", err))
		return
	}
	id, _ := created["id"].(string)
	if id == "" {
		resp.Diagnostics.AddError("Error creating autoscaling group", "the create response did not include a group id")
		return
	}

	// Persist the id immediately so a failed pause / readback still tracks the
	// resource for cleanup on the next destroy.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// The group is created active. If the config wants it paused, toggle now.
	obj := created
	if !plan.Paused.IsNull() && !plan.Paused.IsUnknown() && plan.Paused.ValueBool() {
		paused, err := r.client.PauseAutoscalingGroup(ctx, id)
		if err != nil {
			resp.Diagnostics.Append(diagFromErr("Error pausing autoscaling group after create", err))
			return
		}
		obj = paused
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, autoscalingGroupStateFromAPI(obj, plan))...)
}

// Read refreshes state from the SHOW endpoint. A 404 removes the resource from
// state. The write-only ssh_keys and the encrypted cloud_init are preserved from
// prior (authoritative from config).
func (r *autoscalingGroupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state autoscalingGroupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	obj, err := r.client.GetAutoscalingGroup(ctx, state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("Error reading autoscaling group", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, autoscalingGroupStateFromAPI(obj, state))...)
}

// Update applies the planned changes. The PATCH carries the mutable launch
// template + bounds + security_group_ids. The paused toggle is handled SEPARATELY
// via the pause/resume endpoints (it is not a PATCH field).
func (r *autoscalingGroupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state autoscalingGroupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()

	// Build the PATCH body from the mutable fields. min/max are Computed, so they
	// always have a known plan value; the others are sent when set.
	fields := map[string]any{
		"name":     plan.Name.ValueString(),
		"plan_id":  plan.PlanID.ValueString(),
		"image_id": plan.ImageID.ValueString(),
	}
	if !plan.MinInstances.IsNull() && !plan.MinInstances.IsUnknown() {
		fields["min_instances"] = plan.MinInstances.ValueInt64()
	}
	if !plan.MaxInstances.IsNull() && !plan.MaxInstances.IsUnknown() {
		fields["max_instances"] = plan.MaxInstances.ValueInt64()
	}
	if !plan.CloudInit.IsNull() && !plan.CloudInit.IsUnknown() {
		fields["cloud_init"] = plan.CloudInit.ValueString()
	}
	// security_group_ids: send the full desired set when the attribute is set
	// (including empty → clear). Null means "don't touch".
	if !plan.SecurityGroupIDs.IsNull() && !plan.SecurityGroupIDs.IsUnknown() {
		fields["security_group_ids"] = stringSetValues(plan.SecurityGroupIDs)
	}

	obj, err := r.client.UpdateAutoscalingGroup(ctx, id, fields)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error updating autoscaling group", err))
		return
	}

	// Handle the paused toggle via the dedicated endpoints when it changed.
	if !plan.Paused.Equal(state.Paused) && !plan.Paused.IsNull() && !plan.Paused.IsUnknown() {
		if plan.Paused.ValueBool() {
			obj, err = r.client.PauseAutoscalingGroup(ctx, id)
		} else {
			obj, err = r.client.ResumeAutoscalingGroup(ctx, id)
		}
		if err != nil {
			resp.Diagnostics.Append(diagFromErr("Error toggling autoscaling group pause state", err))
			return
		}
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, autoscalingGroupStateFromAPI(obj, plan))...)
}

// Delete enqueues destruction (background DestroyGroup job) and converges by
// polling SHOW until it 404s - the same pattern as the instance resource.
func (r *autoscalingGroupResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state autoscalingGroupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	deleteTimeout, diags := state.Timeouts.Delete(ctx, defaultDeleteTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()
	if err := r.client.DeleteAutoscalingGroup(ctx, id); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deleting autoscaling group", err))
		return
	}

	waitErr := waiter.WaitFor(ctx, waiter.Options{
		Interval: pollInterval(),
		Timeout:  deleteTimeout,
		Refresh: func() (string, bool, error) {
			_, err := r.client.GetAutoscalingGroup(ctx, id)
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
			"Error waiting for autoscaling group deletion",
			fmt.Sprintf("autoscaling group %s was not removed: %s", id, waitErr.Error()),
		)
		return
	}
}

// ImportState lets `terraform import iaas_autoscaling_group.x <uuid>` adopt an
// existing group; the next Read hydrates the rest. The write-only ssh_keys /
// cloud_init cannot be read back, so they are added to the lifecycle test's
// ImportStateVerifyIgnore.
func (r *autoscalingGroupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// autoscalingGroupStateFromAPI builds the model from a group object. Computed and
// mutable fields come from the API; the RequiresReplace placement inputs and the
// WRITE-ONLY ssh_keys / cloud_init are preserved from the prior model (the SHOW
// payload either omits them or returns them in a form authoritative from config).
func autoscalingGroupStateFromAPI(obj map[string]any, prior autoscalingGroupModel) autoscalingGroupModel {
	status := stringFromAPI(obj, "status", prior.Status)
	if status.IsNull() || status.IsUnknown() || status.ValueString() == "" {
		status = types.StringValue("active")
	}

	return autoscalingGroupModel{
		ID:   stringFromAPI(obj, "id", prior.ID),
		Name: stringFromAPI(obj, "name", prior.Name),

		// Placement / launch template - preserve plan for the RequiresReplace ones
		// (SHOW echoes them but config is authoritative); plan_id/image_id are
		// mutable, so refresh from the API.
		HypervisorGroupID: stringOrPrior(obj, "hypervisor_group_id", prior.HypervisorGroupID),
		PlanID:            stringFromAPI(obj, "plan_id", prior.PlanID),
		ImageID:           stringFromAPI(obj, "image_id", prior.ImageID),
		VPCID:             optionalStringFromAPI(obj, "vpc_id", prior.VPCID),
		VPCSubnetID:       optionalStringFromAPI(obj, "vpc_subnet_id", prior.VPCSubnetID),
		LoadBalancerID:    optionalStringFromAPI(obj, "load_balancer_id", prior.LoadBalancerID),
		LBBackendID:       optionalStringFromAPI(obj, "lb_backend_id", prior.LBBackendID),

		MinInstances: int64FromAPI(obj, "min_instances", prior.MinInstances),
		MaxInstances: int64FromAPI(obj, "max_instances", prior.MaxInstances),

		// WRITE-ONLY / config-authoritative - preserve prior verbatim.
		CloudInit: prior.CloudInit,
		SSHKeys:   prior.SSHKeys,

		// security_group_ids is mutable and returned by SHOW (json array); refresh.
		SecurityGroupIDs: stringSetFromAPI(obj, "security_group_ids", prior.SecurityGroupIDs),

		// paused mirrors the server status.
		Paused: types.BoolValue(status.ValueString() == "paused"),

		Status:       status,
		CurrentCount: int64FromAPI(obj, "current_count", prior.CurrentCount),

		Timeouts: prior.Timeouts,
	}
}

// stringSetValues flattens a types.Set of strings into a []string for a request
// body. A null/unknown set returns nil so the key is omitted; an explicit empty
// set returns a non-nil empty slice so the caller can send [] (clear).
func stringSetValues(s types.Set) []string {
	if s.IsNull() || s.IsUnknown() {
		return nil
	}
	elems := s.Elements()
	out := make([]string, 0, len(elems))
	for _, e := range elems {
		if v, ok := e.(types.String); ok && !v.IsNull() && !v.IsUnknown() {
			out = append(out, v.ValueString())
		}
	}
	return out
}

// stringSetFromAPI reads a JSON array of strings into a types.Set(String). An
// absent/null/non-array value falls back to the prior set. An empty array, when
// the prior was null/unknown (omitted config), stays null to avoid a perpetual
// diff; when the prior was an explicit set, an empty set is returned.
func stringSetFromAPI(obj map[string]any, key string, prior types.Set) types.Set {
	raw, ok := obj[key]
	if !ok || raw == nil {
		return prior
	}
	arr, ok := raw.([]any)
	if !ok {
		return prior
	}
	elems := make([]attr.Value, 0, len(arr))
	for _, v := range arr {
		if s, ok := v.(string); ok && s != "" {
			elems = append(elems, types.StringValue(s))
		}
	}
	if len(elems) == 0 {
		if prior.IsNull() || prior.IsUnknown() {
			return types.SetNull(types.StringType)
		}
		return mustSetValue(types.StringType, []attr.Value{})
	}
	return mustSetValue(types.StringType, elems)
}
