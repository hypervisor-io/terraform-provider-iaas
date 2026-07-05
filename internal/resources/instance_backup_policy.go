package resources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/iaas/terraform-provider-iaas/internal/client"
)

// Interface assertions.
var (
	_ resource.Resource                = &instanceBackupPolicyResource{}
	_ resource.ResourceWithConfigure   = &instanceBackupPolicyResource{}
	_ resource.ResourceWithImportState = &instanceBackupPolicyResource{}
)

// NewInstanceBackupPolicyResource is the resource constructor registered with
// the provider.
func NewInstanceBackupPolicyResource() resource.Resource {
	return &instanceBackupPolicyResource{}
}

// instanceBackupPolicyResource manages an iaas_instance_backup_policy - a
// named backup schedule/retention configuration that instances can be attached
// to (one instance per policy).
//
// The attached instance set is managed as an Optional SetAttribute of strings
// (instance UUIDs), diffed via one-at-a-time attach/detach calls on update,
// and rebuilt from the embedded "policy.instances" array on Read. This mirrors
// the security_group instance_ids set-diff pattern, adapted for the single-id
// attach/detach API (vs bulk array).
//
// Route summary (verified against UserApi\InstanceBackupPolicyController +
// InstanceBackupPolicyService + InstancePolicyStoreRequest/UpdateRequest/
// AttachRequest + routes/user_api.php):
//
//	INDEX   GET    /backup-policies              (plural)
//	CREATE  POST   /backup-policies              (plural)
//	                body {name,full_backup_frequency,full_backup_time,
//	                      full_backup_day?,max_incremental_chain,
//	                      retention_count,backup_device}
//	                → {success,message,policy:{id,...}}
//	SHOW    GET    /backup-policy/{id}           (singular)
//	                → {policy:{...,instances:[{id,...}]},available_instances:[...]}
//	UPDATE  PATCH  /backup-policy/{id}           (singular)
//	                body same as CREATE (all required)
//	                → {success,message,policy:{...}}
//	DELETE  DELETE /backup-policy/{id}           (singular)
//	                → {success,message}
//	ATTACH  POST   /backup-policy/{id}/attach    body {instance_id:"<uuid>"}
//	DETACH  POST   /backup-policy/{id}/detach    body {instance_id:"<uuid>"}
//
// All operations are SYNCHRONOUS (no task/waiter). No billing gate.
// testConnection and reset-failures are NOT modelled (operational).
type instanceBackupPolicyResource struct {
	client *client.Client
}

// instanceBackupPolicyModel maps the Terraform state/plan for
// iaas_instance_backup_policy.
type instanceBackupPolicyModel struct {
	ID                  types.String `tfsdk:"id"`
	Name                types.String `tfsdk:"name"`
	FullBackupFrequency types.String `tfsdk:"full_backup_frequency"`
	FullBackupTime      types.String `tfsdk:"full_backup_time"`
	FullBackupDay       types.Int64  `tfsdk:"full_backup_day"`
	MaxIncrementalChain types.Int64  `tfsdk:"max_incremental_chain"`
	RetentionCount      types.Int64  `tfsdk:"retention_count"`
	BackupDevice        types.String `tfsdk:"backup_device"`
	Status              types.String `tfsdk:"status"`
	InstanceIDs         types.Set    `tfsdk:"instance_ids"`
}

// Metadata sets the resource type name → "<provider>_instance_backup_policy".
func (r *instanceBackupPolicyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_instance_backup_policy"
}

// Schema describes the iaas_instance_backup_policy resource.
func (r *instanceBackupPolicyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages an instance backup policy - a named schedule and retention " +
			"configuration for KVM instance backups. Instances are attached to the policy " +
			"one at a time via the `instance_ids` set attribute. Changes to that set " +
			"attach or detach the corresponding instances in place. " +
			"All schedule fields are stored and returned in UTC.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the backup policy, assigned by the API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Display name for the backup policy. Maximum 255 characters. Updatable in place.",
			},
			"full_backup_frequency": schema.StringAttribute{
				Required:    true,
				Description: "How often a full backup is taken: \"daily\" or \"weekly\".",
			},
			"full_backup_time": schema.StringAttribute{
				Required: true,
				Description: "Time of day for the full backup in HH:MM format (UTC). " +
					"The server accepts the value in the user's local timezone and " +
					"stores it internally as UTC; the provider passes UTC directly.",
			},
			"full_backup_day": schema.Int64Attribute{
				Optional: true,
				Description: "Day of the week (0=Sunday … 6=Saturday) for weekly backups. " +
					"Required when full_backup_frequency is \"weekly\"; omit for \"daily\".",
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"max_incremental_chain": schema.Int64Attribute{
				Required: true,
				Description: "Maximum number of incremental backups to chain between full " +
					"backups (0-30). Zero disables incrementals.",
			},
			"retention_count": schema.Int64Attribute{
				Required:    true,
				Description: "Number of full backups to retain (1-365). Older backups are pruned.",
			},
			"backup_device": schema.StringAttribute{
				Required: true,
				Description: "Which disk device(s) to include in the backup: \"primary\" " +
					"(boot disk only) or \"all\" (all attached disks).",
			},
			"status": schema.StringAttribute{
				Computed: true,
				Description: "Current policy status reported by the server: \"active\", " +
					"\"error\", etc. Server-mutable; read on every refresh.",
			},
			"instance_ids": schema.SetAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "UUIDs of the instances attached to this backup policy, as an " +
					"order-independent set. Adding an id attaches that instance to the " +
					"policy; removing it detaches the instance. Each instance may be " +
					"attached to at most one backup policy.",
			},
		},
	}
}

// Configure pulls the shared *client.Client from the provider.
func (r *instanceBackupPolicyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create provisions the backup policy and then attaches the configured
// instance ids one by one, then reads back to get authoritative state.
func (r *instanceBackupPolicyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan instanceBackupPolicyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := ibpCreateBody(plan)

	obj, err := r.client.CreateInstanceBackupPolicy(ctx, body)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating instance backup policy", err))
		return
	}

	id, _ := obj["id"].(string)
	if id == "" {
		resp.Diagnostics.AddError("Error creating instance backup policy",
			"the create response did not contain an id")
		return
	}

	// Persist a minimal destroyable state before side-effects so a partial
	// failure leaves a resource Terraform can clean up.
	persistMinimal := func() {
		_ = resp.State.Set(ctx, instanceBackupPolicyModel{
			ID:                  types.StringValue(id),
			Name:                plan.Name,
			FullBackupFrequency: plan.FullBackupFrequency,
			FullBackupTime:      plan.FullBackupTime,
			FullBackupDay:       plan.FullBackupDay,
			MaxIncrementalChain: plan.MaxIncrementalChain,
			RetentionCount:      plan.RetentionCount,
			BackupDevice:        plan.BackupDevice,
			Status:              types.StringValue("active"),
			InstanceIDs:         types.SetNull(types.StringType),
		})
	}

	// Attach the configured instances one at a time.
	attachIDs, diags := stringsFromSet(ctx, plan.InstanceIDs)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	for _, instID := range attachIDs {
		if err := r.client.AttachInstanceToBackupPolicy(ctx, id, instID); err != nil {
			resp.Diagnostics.Append(diagFromErr("Error attaching instance to backup policy", err))
			persistMinimal()
			return
		}
	}

	// Read back so state reflects the server-assigned values.
	state, notFound, diags := r.readState(ctx, id, plan)
	resp.Diagnostics.Append(diags...)
	if notFound {
		resp.Diagnostics.AddError("Error creating instance backup policy",
			"the policy disappeared immediately after creation")
		return
	}
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Read refreshes state from the API. A 404 means the policy was deleted
// out of band - remove it from state so Terraform plans a recreate.
func (r *instanceBackupPolicyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state instanceBackupPolicyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	newState, notFound, diags := r.readState(ctx, state.ID.ValueString(), state)
	if notFound {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

// Update applies the planned changes: patches all schedule/retention fields,
// then diffs the instance_ids set (attach added / detach removed).
func (r *instanceBackupPolicyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state instanceBackupPolicyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()

	// Always PATCH the scalar fields (all required by the controller).
	body := ibpCreateBody(plan)
	if _, err := r.client.UpdateInstanceBackupPolicy(ctx, id, body); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error updating instance backup policy", err))
		return
	}

	// Diff the instance_ids set: attach added, detach removed.
	plannedIDs, diags := stringsFromSet(ctx, plan.InstanceIDs)
	resp.Diagnostics.Append(diags...)
	stateIDs, diags := stringsFromSet(ctx, state.InstanceIDs)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	toAttach, toDetach := ibpDiffIDs(plannedIDs, stateIDs)

	for _, instID := range toAttach {
		if err := r.client.AttachInstanceToBackupPolicy(ctx, id, instID); err != nil {
			resp.Diagnostics.Append(diagFromErr("Error attaching instance to backup policy", err))
			return
		}
	}
	for _, instID := range toDetach {
		if err := r.client.DetachInstanceFromBackupPolicy(ctx, id, instID); err != nil {
			resp.Diagnostics.Append(diagFromErr("Error detaching instance from backup policy", err))
			return
		}
	}

	// Read back so state is authoritative.
	newState, notFound, diags := r.readState(ctx, id, plan)
	resp.Diagnostics.Append(diags...)
	if notFound {
		resp.Diagnostics.AddError("Error updating instance backup policy",
			"the policy disappeared during update")
		return
	}
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

// Delete removes the backup policy (the service detaches all instances first).
func (r *instanceBackupPolicyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state instanceBackupPolicyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteInstanceBackupPolicy(ctx, state.ID.ValueString()); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deleting instance backup policy", err))
		return
	}
}

// ImportState lets `terraform import iaas_instance_backup_policy.x <uuid>`.
func (r *instanceBackupPolicyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// ibpCreateBody builds the body map for CREATE and UPDATE (same shape).
func ibpCreateBody(plan instanceBackupPolicyModel) map[string]any {
	body := map[string]any{
		"name":                  plan.Name.ValueString(),
		"full_backup_frequency": plan.FullBackupFrequency.ValueString(),
		"full_backup_time":      plan.FullBackupTime.ValueString(),
		"max_incremental_chain": plan.MaxIncrementalChain.ValueInt64(),
		"retention_count":       plan.RetentionCount.ValueInt64(),
		"backup_device":         plan.BackupDevice.ValueString(),
	}
	if !plan.FullBackupDay.IsNull() && !plan.FullBackupDay.IsUnknown() {
		body["full_backup_day"] = plan.FullBackupDay.ValueInt64()
	}
	return body
}

// readState GETs the policy and builds a full model from it, rebuilding the
// instance_ids set from the embedded "instances" array. prior supplies
// fallbacks for any field the response omits.
func (r *instanceBackupPolicyResource) readState(ctx context.Context, id string, prior instanceBackupPolicyModel) (instanceBackupPolicyModel, bool, diag.Diagnostics) {
	obj, err := r.client.GetInstanceBackupPolicy(ctx, id)
	if err != nil {
		if client.IsNotFound(err) {
			return instanceBackupPolicyModel{}, true, nil
		}
		var diags diag.Diagnostics
		diags.Append(diagFromErr("Error reading instance backup policy", err))
		return instanceBackupPolicyModel{}, false, diags
	}
	m, diags := ibpStateFromAPI(obj, prior)
	return m, false, diags
}

// ibpStateFromAPI builds the model from the SHOW "policy" object.
func ibpStateFromAPI(obj map[string]any, prior instanceBackupPolicyModel) (instanceBackupPolicyModel, diag.Diagnostics) {
	var diags diag.Diagnostics

	m := instanceBackupPolicyModel{
		ID:                  stringFromAPI(obj, "id", prior.ID),
		Name:                stringFromAPI(obj, "name", prior.Name),
		FullBackupFrequency: stringFromAPI(obj, "full_backup_frequency", prior.FullBackupFrequency),
		FullBackupTime:      stringFromAPI(obj, "full_backup_time", prior.FullBackupTime),
		FullBackupDay:       optionalInt64FromAPI(obj, "full_backup_day"),
		MaxIncrementalChain: requiredInt64FromAPI(obj, "max_incremental_chain", prior.MaxIncrementalChain),
		RetentionCount:      requiredInt64FromAPI(obj, "retention_count", prior.RetentionCount),
		BackupDevice:        stringFromAPI(obj, "backup_device", prior.BackupDevice),
		Status:              stringFromAPI(obj, "status", prior.Status),
	}

	// Rebuild instance_ids from the embedded "instances" array (each element
	// has an "id" field), the same way security_group rebuilds from
	// "attached_instances".
	instSet, d := instanceIDSetFromAPI(obj["instances"], prior.InstanceIDs)
	diags.Append(d...)
	m.InstanceIDs = instSet

	return m, diags
}

// ibpDiffIDs computes the set of ids to attach (in plan but not in state) and
// to detach (in state but not in plan).
func ibpDiffIDs(plannedIDs, stateIDs []string) (toAttach, toDetach []string) {
	plannedSet := make(map[string]struct{}, len(plannedIDs))
	for _, id := range plannedIDs {
		plannedSet[id] = struct{}{}
	}
	stateSet := make(map[string]struct{}, len(stateIDs))
	for _, id := range stateIDs {
		stateSet[id] = struct{}{}
	}
	for _, id := range plannedIDs {
		if _, exists := stateSet[id]; !exists {
			toAttach = append(toAttach, id)
		}
	}
	for _, id := range stateIDs {
		if _, exists := plannedSet[id]; !exists {
			toDetach = append(toDetach, id)
		}
	}
	return
}
