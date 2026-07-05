package resources

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/iaas/terraform-provider-iaas/internal/client"
)

// Interface assertions - security_group copies the NESTED-ENTRIES pattern
// established by ip_set (a set of child rule rows managed inside the parent via
// per-child add/remove endpoints, embedded in the parent SHOW) and ADDS a second
// managed set: the attached instance ids, diffed via bulk attach/detach.
var (
	_ resource.Resource                = &securityGroupResource{}
	_ resource.ResourceWithConfigure   = &securityGroupResource{}
	_ resource.ResourceWithImportState = &securityGroupResource{}
)

// NewSecurityGroupResource is the resource constructor registered with the
// provider.
func NewSecurityGroupResource() resource.Resource {
	return &securityGroupResource{}
}

// securityGroupResource manages an iaas_security_group - a named collection of
// firewall rules, with a set of attached instances.
//
// TWO child sets are owned inline:
//
//   - rules: a SetNestedAttribute. The API exposes per-rule add/remove endpoints
//     (no rule-update endpoint) and SHOW embeds the rules, so the parent fully
//     owns them. Create adds each configured rule; Update diffs the set (add new /
//     delete removed by server rule id); Read rebuilds it from the SHOW response.
//     Because there is no rule-update endpoint, ANY field change to a rule is a
//     delete+add, so the diff key is the WHOLE rule (all its fields).
//
//   - instance_ids: an Optional SetAttribute(String). Instances are attached and
//     detached in bulk by id. Create attaches the configured ids; Update diffs
//     the set (attach added / detach removed); Read rebuilds it from the
//     top-level "attached_instances" array in the SHOW envelope.
//
// Route summary (verified against UserApi\SecurityGroupController +
// SecurityGroupService + routes/user_api.php):
//
//	INDEX   GET    /security-groups          (plural)
//	CREATE  POST   /security-groups          (plural)   body {name,description?}
//	                                          → {success,message,security_group}
//	SHOW    GET    /security-group/{id}       (singular) → {success,
//	                                          security_group:{...,rules:[...]},
//	                                          attached_instances:[...]}
//	UPDATE  PATCH  /security-group/{id}       (singular) body {name,description?}
//	                                          → {success,message} (NO body → Read after)
//	DELETE  DELETE /security-group/{id}       (singular) → {success,message}
//	ADD     POST   /security-group/{id}/rules body {direction,protocol,ip_version,...}
//	                                          → {success,message,rule}
//	REMOVE  DELETE /security-group/{id}/rule/{ruleId}      → {success,message}
//	ATTACH  POST   /security-group/{id}/attach-instances   body {instance_ids:[...]}
//	DETACH  POST   /security-group/{id}/detach-instances   body {instance_ids:[...]}
//
// All operations are synchronous (no task/waiter). The slave firewall sync is
// pulled by the hypervisor agent, so no user-API "apply"/sync call is needed
// after rule or attachment changes.
type securityGroupResource struct {
	client *client.Client
}

// securityGroupModel maps the Terraform state/plan for iaas_security_group.
//
// Rules is a SET (order-independent) of nested rule objects. InstanceIDs is an
// order-independent set of attached instance UUID strings. name/description are
// updatable in place.
type securityGroupModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	Rules       types.Set    `tfsdk:"rules"`
	InstanceIDs types.Set    `tfsdk:"instance_ids"`
}

// securityGroupRuleModel maps a single nested rule object inside the rules set.
//
// Because the API has no rule-update endpoint, every rule field is part of the
// rule's identity for diffing: a change to ANY field becomes a delete of the old
// rule plus an add of the new one. ID is the server-assigned rule UUID, needed to
// delete this exact rule on update.
type securityGroupRuleModel struct {
	ID            types.String `tfsdk:"id"`
	Direction     types.String `tfsdk:"direction"`
	Protocol      types.String `tfsdk:"protocol"`
	PortRangeMin  types.Int64  `tfsdk:"port_range_min"`
	PortRangeMax  types.Int64  `tfsdk:"port_range_max"`
	IPVersion     types.String `tfsdk:"ip_version"`
	Cidr          types.String `tfsdk:"cidr"`
	RemoteGroupID types.String `tfsdk:"remote_group_id"`
	IPSetID       types.String `tfsdk:"ip_set_id"`
	Description   types.String `tfsdk:"description"`
}

// securityGroupRuleAttrTypes is the attribute-type map for one rule object. It is
// the single source of truth for the nested object's shape, reused everywhere a
// types.Set of rules is (re)built so the schema and runtime values never drift.
var securityGroupRuleAttrTypes = map[string]attr.Type{
	"id":              types.StringType,
	"direction":       types.StringType,
	"protocol":        types.StringType,
	"port_range_min":  types.Int64Type,
	"port_range_max":  types.Int64Type,
	"ip_version":      types.StringType,
	"cidr":            types.StringType,
	"remote_group_id": types.StringType,
	"ip_set_id":       types.StringType,
	"description":     types.StringType,
}

// Metadata sets the resource type name → "<provider>_security_group".
func (r *securityGroupResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_security_group"
}

// Schema describes the iaas_security_group resource.
func (r *securityGroupResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a security group - a named collection of stateful firewall rules " +
			"that can be attached to instances. The rules are managed inline as an " +
			"order-independent set: adding or removing a rule from the config adds or removes it " +
			"on the server in place, without replacing the security group. Because the API has no " +
			"rule-update endpoint, changing any field of a rule removes and re-adds it. The set of " +
			"attached instances is managed via the `instance_ids` attribute (attach/detach in " +
			"place).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the security group, assigned by the API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required: true,
				Description: "Display name for the security group. Maximum 255 characters. " +
					"Updatable in place.",
			},
			"description": schema.StringAttribute{
				Optional: true,
				Description: "Optional free-text description of the security group. " +
					"Set to null to clear. Updatable in place.",
			},
			"instance_ids": schema.SetAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "UUIDs of the instances this security group is attached to, as an " +
					"order-independent set. Adding or removing an id attaches or detaches the group " +
					"on that instance in place. An instance may have at most 10 security groups.",
			},
			"rules": schema.SetNestedAttribute{
				Optional: true,
				Description: "The firewall rules in this security group, as an order-independent " +
					"set. Each rule is added or removed on the server individually when it appears " +
					"or disappears from this set. Because the API has no rule-update endpoint, " +
					"changing any field of an existing rule removes and re-adds that rule.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Computed: true,
							Description: "UUID of the rule, assigned by the API. Used internally to " +
								"delete this specific rule when it is removed from the set.",
							// No UseStateForUnknown: set elements have no stable
							// position, so the framework cannot reliably copy a prior
							// element's id onto a planned element. The id is resolved
							// during Create/Update (from the add response) and Read.
						},
						"direction": schema.StringAttribute{
							Required: true,
							Description: "Traffic direction this rule applies to: \"ingress\" " +
								"(inbound) or \"egress\" (outbound).",
						},
						"protocol": schema.StringAttribute{
							Required: true,
							Description: "Protocol the rule matches: \"tcp\", \"udp\", \"icmp\", " +
								"\"icmpv6\", or \"all\". For \"tcp\" and \"udp\", port_range_min and " +
								"port_range_max are required.",
						},
						"port_range_min": schema.Int64Attribute{
							Optional: true,
							Description: "Lowest port in the matched range (1-65535). Required for " +
								"tcp/udp; omit for icmp/icmpv6/all.",
						},
						"port_range_max": schema.Int64Attribute{
							Optional: true,
							Description: "Highest port in the matched range (1-65535). Required for " +
								"tcp/udp; omit for icmp/icmpv6/all.",
						},
						"ip_version": schema.StringAttribute{
							Required:    true,
							Description: "IP version the rule applies to: \"ipv4\" or \"ipv6\".",
						},
						"cidr": schema.StringAttribute{
							Optional: true,
							Description: "Source (ingress) or destination (egress) CIDR, e.g. " +
								"\"0.0.0.0/0\". Mutually exclusive with remote_group_id and ip_set_id.",
						},
						"remote_group_id": schema.StringAttribute{
							Optional: true,
							Description: "UUID of another security group to use as the rule's source/" +
								"destination. Mutually exclusive with cidr and ip_set_id.",
						},
						"ip_set_id": schema.StringAttribute{
							Optional: true,
							Description: "UUID of an IP set to use as the rule's source/destination. " +
								"The IP set's ip_version must match the rule's ip_version. Mutually " +
								"exclusive with cidr and remote_group_id.",
						},
						"description": schema.StringAttribute{
							Optional:    true,
							Description: "Optional per-rule description.",
						},
					},
				},
			},
		},
	}
}

// Configure pulls the shared *client.Client from the provider. It tolerates a
// nil ProviderData (the framework calls Configure once with nil data before the
// provider's own Configure has run).
func (r *securityGroupResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create provisions the security group, then adds the configured rules one-by-one,
// then attaches the configured instance ids, then reads the group back so state
// reflects the server-assigned rule ids and the authoritative attached-instance
// set.
func (r *securityGroupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan securityGroupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := map[string]any{"name": plan.Name.ValueString()}
	if !plan.Description.IsNull() && !plan.Description.IsUnknown() {
		body["description"] = plan.Description.ValueString()
	}

	obj, err := r.client.CreateSecurityGroup(ctx, body)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating security group", err))
		return
	}

	id, _ := obj["id"].(string)
	if id == "" {
		resp.Diagnostics.AddError("Error creating security group", "the create response did not contain an id")
		return
	}

	// Helper that persists a minimal destroyable state (id only) so a partial
	// failure after the group exists still leaves a resource Terraform can clean
	// up on the next apply.
	persistMinimal := func() {
		_ = resp.State.Set(ctx, securityGroupModel{
			ID:          types.StringValue(id),
			Name:        plan.Name,
			Description: plan.Description,
			Rules:       types.SetNull(types.ObjectType{AttrTypes: securityGroupRuleAttrTypes}),
			InstanceIDs: types.SetNull(types.StringType),
		})
	}

	// Add the configured rules.
	plannedRules, diags := rulesFromSet(ctx, plan.Rules)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	for _, rl := range plannedRules {
		if err := r.addRule(ctx, id, rl); err != nil {
			resp.Diagnostics.Append(diagFromErr("Error adding security group rule", err))
			persistMinimal()
			return
		}
	}

	// Attach the configured instances.
	attachIDs, diags := stringsFromSet(ctx, plan.InstanceIDs)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if len(attachIDs) > 0 {
		if err := r.client.AttachSecurityGroupInstances(ctx, id, attachIDs); err != nil {
			resp.Diagnostics.Append(diagFromErr("Error attaching instances to security group", err))
			persistMinimal()
			return
		}
	}

	// Read back so state carries the server rule ids + authoritative attachments.
	state, notFound, diags := r.readState(ctx, id, plan)
	resp.Diagnostics.Append(diags...)
	if notFound {
		resp.Diagnostics.AddError("Error creating security group",
			"the security group disappeared immediately after creation")
		return
	}
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Read refreshes state from the API. A 404 means the group was deleted out of
// band - remove it from state so Terraform plans a recreate.
func (r *securityGroupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state securityGroupModel
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

// Update applies the planned changes:
//   - patches name/description if either changed;
//   - diffs the rules set (planned vs state): adds rules that are new, deletes
//     rules that were removed (by their server id). A rule is "the same" only
//     when ALL of its fields match (there is no rule-update endpoint), so any
//     field change becomes a delete+add;
//   - diffs the instance_ids set: attaches ids added to the plan, detaches ids
//     removed from it.
//
// After mutating, it reads back so state reflects the server rule ids and the
// authoritative attachment set.
func (r *securityGroupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state securityGroupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()

	// Patch the scalar fields when either changed. name is always sent (required);
	// description is sent as null when cleared.
	if !plan.Name.Equal(state.Name) || !plan.Description.Equal(state.Description) {
		fields := map[string]any{"name": plan.Name.ValueString()}
		if plan.Description.IsNull() {
			fields["description"] = nil
		} else if !plan.Description.IsUnknown() {
			fields["description"] = plan.Description.ValueString()
		}
		if _, err := r.client.UpdateSecurityGroup(ctx, id, fields); err != nil {
			resp.Diagnostics.Append(diagFromErr("Error updating security group", err))
			return
		}
	}

	// Diff the rules set.
	plannedRules, diags := rulesFromSet(ctx, plan.Rules)
	resp.Diagnostics.Append(diags...)
	stateRules, diags := rulesFromSet(ctx, state.Rules)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Key by the WHOLE rule (every field) so any change is a delete+add. The
	// state rules carry the server id needed for deletion.
	plannedByKey := make(map[string]securityGroupRuleModel, len(plannedRules))
	for _, rl := range plannedRules {
		plannedByKey[ruleKey(rl)] = rl
	}
	stateByKey := make(map[string]securityGroupRuleModel, len(stateRules))
	for _, rl := range stateRules {
		stateByKey[ruleKey(rl)] = rl
	}

	// Delete rules that are in state but not in the plan (by server id).
	for key, rl := range stateByKey {
		if _, keep := plannedByKey[key]; keep {
			continue
		}
		if rl.ID.IsNull() || rl.ID.IsUnknown() || rl.ID.ValueString() == "" {
			resp.Diagnostics.AddWarning(
				"Security group rule could not be deleted: server id unknown",
				fmt.Sprintf(
					"A rule (direction=%q protocol=%q) was removed from config but its "+
						"server-assigned id is not present in state, so the provider cannot issue a "+
						"delete request. The rule may still exist on the server. Manual cleanup or "+
						"`terraform import` may be needed to reconcile.",
					rl.Direction.ValueString(), rl.Protocol.ValueString(),
				),
			)
			continue // nothing to delete server-side
		}
		if err := r.client.DeleteSecurityGroupRule(ctx, id, rl.ID.ValueString()); err != nil {
			resp.Diagnostics.Append(diagFromErr("Error removing security group rule", err))
			return
		}
	}

	// Add rules that are in the plan but not in state.
	for key, rl := range plannedByKey {
		if _, exists := stateByKey[key]; exists {
			continue
		}
		if err := r.addRule(ctx, id, rl); err != nil {
			resp.Diagnostics.Append(diagFromErr("Error adding security group rule", err))
			return
		}
	}

	// Diff the instance_ids set: attach added, detach removed.
	plannedIDs, diags := stringsFromSet(ctx, plan.InstanceIDs)
	resp.Diagnostics.Append(diags...)
	stateIDs, diags := stringsFromSet(ctx, state.InstanceIDs)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	plannedIDset := make(map[string]struct{}, len(plannedIDs))
	for _, idv := range plannedIDs {
		plannedIDset[idv] = struct{}{}
	}
	stateIDset := make(map[string]struct{}, len(stateIDs))
	for _, idv := range stateIDs {
		stateIDset[idv] = struct{}{}
	}

	var toAttach, toDetach []string
	for _, idv := range plannedIDs {
		if _, exists := stateIDset[idv]; !exists {
			toAttach = append(toAttach, idv)
		}
	}
	for _, idv := range stateIDs {
		if _, exists := plannedIDset[idv]; !exists {
			toDetach = append(toDetach, idv)
		}
	}
	if len(toAttach) > 0 {
		if err := r.client.AttachSecurityGroupInstances(ctx, id, toAttach); err != nil {
			resp.Diagnostics.Append(diagFromErr("Error attaching instances to security group", err))
			return
		}
	}
	if len(toDetach) > 0 {
		if err := r.client.DetachSecurityGroupInstances(ctx, id, toDetach); err != nil {
			resp.Diagnostics.Append(diagFromErr("Error detaching instances from security group", err))
			return
		}
	}

	// Read back so state reflects the current server rule ids + attachments.
	newState, notFound, diags := r.readState(ctx, id, plan)
	resp.Diagnostics.Append(diags...)
	if notFound {
		resp.Diagnostics.AddError("Error updating security group",
			"the security group disappeared during update")
		return
	}
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

// Delete removes the security group (its rules cascade and instance attachments
// are detached server-side).
func (r *securityGroupResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state securityGroupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteSecurityGroup(ctx, state.ID.ValueString()); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deleting security group", err))
		return
	}
}

// ImportState lets `terraform import iaas_security_group.x <uuid>` adopt an
// existing security group by its id; the next Read hydrates name/description and
// rebuilds both the rules set and the instance_ids set from the SHOW response.
func (r *securityGroupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// addRule POSTs a single rule, omitting any unset optional field so the server
// stores null rather than "" / 0.
func (r *securityGroupResource) addRule(ctx context.Context, sgID string, rl securityGroupRuleModel) error {
	body := map[string]any{
		"direction":  rl.Direction.ValueString(),
		"protocol":   rl.Protocol.ValueString(),
		"ip_version": rl.IPVersion.ValueString(),
	}
	if !rl.PortRangeMin.IsNull() && !rl.PortRangeMin.IsUnknown() {
		body["port_range_min"] = rl.PortRangeMin.ValueInt64()
	}
	if !rl.PortRangeMax.IsNull() && !rl.PortRangeMax.IsUnknown() {
		body["port_range_max"] = rl.PortRangeMax.ValueInt64()
	}
	if !rl.Cidr.IsNull() && !rl.Cidr.IsUnknown() {
		body["cidr"] = rl.Cidr.ValueString()
	}
	if !rl.RemoteGroupID.IsNull() && !rl.RemoteGroupID.IsUnknown() {
		body["remote_group_id"] = rl.RemoteGroupID.ValueString()
	}
	if !rl.IPSetID.IsNull() && !rl.IPSetID.IsUnknown() {
		body["ip_set_id"] = rl.IPSetID.ValueString()
	}
	if !rl.Description.IsNull() && !rl.Description.IsUnknown() {
		body["description"] = rl.Description.ValueString()
	}
	_, err := r.client.AddSecurityGroupRule(ctx, sgID, body)
	return err
}

// readState GETs the security group envelope and builds a full model from it,
// rebuilding the rules set from the embedded "security_group.rules" array and the
// instance_ids set from the top-level "attached_instances" array. prior supplies
// fallbacks for any field the response omits. The bool return is true when the
// group was not found (404), so the caller can RemoveResource; in that case the
// returned diagnostics are empty.
func (r *securityGroupResource) readState(ctx context.Context, id string, prior securityGroupModel) (securityGroupModel, bool, diag.Diagnostics) {
	env, err := r.client.GetSecurityGroupEnvelope(ctx, id)
	if err != nil {
		if client.IsNotFound(err) {
			return securityGroupModel{}, true, nil
		}
		var diags diag.Diagnostics
		diags.Append(diagFromErr("Error reading security group", err))
		return securityGroupModel{}, false, diags
	}
	m, diags := securityGroupStateFromAPI(env, prior)
	return m, false, diags
}

// securityGroupStateFromAPI builds the model from the SHOW envelope. The
// security group scalar fields + embedded rules come from env["security_group"];
// the attached-instance ids come from the top-level env["attached_instances"].
func securityGroupStateFromAPI(env map[string]any, prior securityGroupModel) (securityGroupModel, diag.Diagnostics) {
	var diags diag.Diagnostics

	sg, _ := env["security_group"].(map[string]any)
	if sg == nil {
		// Tolerate an unexpectedly flat shape (just in case the SHOW ever returns
		// the group at the top level) so the resource degrades gracefully.
		sg = env
	}

	m := securityGroupModel{
		ID:          stringFromAPI(sg, "id", prior.ID),
		Name:        stringFromAPI(sg, "name", prior.Name),
		Description: optionalStringFromAPI(sg, "description", prior.Description),
	}

	ruleSet, d := ruleSetFromAPI(sg["rules"], prior.Rules)
	diags.Append(d...)
	m.Rules = ruleSet

	instSet, d := instanceIDSetFromAPI(env["attached_instances"], prior.InstanceIDs)
	diags.Append(d...)
	m.InstanceIDs = instSet

	return m, diags
}

// ruleSetFromAPI converts the embedded "rules" JSON array into a types.Set of
// rule objects. When the array is absent/empty AND the prior config had a null
// rules set, the result stays null so an unmanaged-rules config does not show
// drift; otherwise an empty managed set becomes an empty (non-null) set.
func ruleSetFromAPI(raw any, prior types.Set) (types.Set, diag.Diagnostics) {
	objType := types.ObjectType{AttrTypes: securityGroupRuleAttrTypes}

	arr, _ := raw.([]any)
	if len(arr) == 0 {
		if prior.IsNull() || prior.IsUnknown() {
			return types.SetNull(objType), nil
		}
		return types.SetValue(objType, []attr.Value{})
	}

	elems := make([]attr.Value, 0, len(arr))
	for _, item := range arr {
		ro, ok := item.(map[string]any)
		if !ok {
			continue
		}
		obj, d := types.ObjectValue(securityGroupRuleAttrTypes, map[string]attr.Value{
			"id":              optionalStringFromAPI(ro, "id", types.StringNull()),
			"direction":       stringFromAPI(ro, "direction", types.StringNull()),
			"protocol":        stringFromAPI(ro, "protocol", types.StringNull()),
			"port_range_min":  optionalInt64FromAPI(ro, "port_range_min"),
			"port_range_max":  optionalInt64FromAPI(ro, "port_range_max"),
			"ip_version":      stringFromAPI(ro, "ip_version", types.StringNull()),
			"cidr":            optionalStringFromAPI(ro, "cidr", types.StringNull()),
			"remote_group_id": optionalStringFromAPI(ro, "remote_group_id", types.StringNull()),
			"ip_set_id":       optionalStringFromAPI(ro, "ip_set_id", types.StringNull()),
			"description":     optionalStringFromAPI(ro, "description", types.StringNull()),
		})
		if d.HasError() {
			return types.SetNull(objType), d
		}
		elems = append(elems, obj)
	}

	return types.SetValue(objType, elems)
}

// instanceIDSetFromAPI converts the top-level "attached_instances" JSON array
// (each element an object with an "id") into a types.Set of id strings. When the
// array is absent/empty AND the prior config had a null instance_ids set, the
// result stays null so an unmanaged-attachments config does not show drift;
// otherwise an empty managed set becomes an empty (non-null) set.
func instanceIDSetFromAPI(raw any, prior types.Set) (types.Set, diag.Diagnostics) {
	arr, _ := raw.([]any)
	if len(arr) == 0 {
		if prior.IsNull() || prior.IsUnknown() {
			return types.SetNull(types.StringType), nil
		}
		return types.SetValue(types.StringType, []attr.Value{})
	}

	elems := make([]attr.Value, 0, len(arr))
	for _, item := range arr {
		io, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if idv, ok := io["id"].(string); ok && idv != "" {
			elems = append(elems, types.StringValue(idv))
		}
	}
	if len(elems) == 0 {
		if prior.IsNull() || prior.IsUnknown() {
			return types.SetNull(types.StringType), nil
		}
		return types.SetValue(types.StringType, []attr.Value{})
	}
	return types.SetValue(types.StringType, elems)
}

// rulesFromSet decodes a types.Set of rule objects into a Go slice. A null or
// unknown set yields an empty slice (no rules managed).
func rulesFromSet(ctx context.Context, set types.Set) ([]securityGroupRuleModel, diag.Diagnostics) {
	if set.IsNull() || set.IsUnknown() {
		return nil, nil
	}
	var rules []securityGroupRuleModel
	d := set.ElementsAs(ctx, &rules, false)
	return rules, d
}

// stringsFromSet decodes a types.Set of strings into a Go slice. A null or
// unknown set yields an empty slice.
func stringsFromSet(ctx context.Context, set types.Set) ([]string, diag.Diagnostics) {
	if set.IsNull() || set.IsUnknown() {
		return nil, nil
	}
	var out []string
	d := set.ElementsAs(ctx, &out, false)
	return out, d
}

// ruleKey is the natural identity of a rule for diffing: every field except the
// server-assigned id. Because the API has no rule-update endpoint, two rules that
// differ in ANY field are distinct, so a change becomes a delete-then-add.
func ruleKey(rl securityGroupRuleModel) string {
	var b strings.Builder
	writeStr := func(s types.String) {
		if s.IsNull() || s.IsUnknown() {
			b.WriteString("\x00")
		} else {
			b.WriteString(s.ValueString())
			b.WriteString("\x00")
		}
	}
	writeInt := func(n types.Int64) {
		if n.IsNull() || n.IsUnknown() {
			b.WriteString("\x00")
		} else {
			b.WriteString(fmt.Sprintf("%d", n.ValueInt64()))
			b.WriteString("\x00")
		}
	}
	writeStr(rl.Direction)
	writeStr(rl.Protocol)
	writeInt(rl.PortRangeMin)
	writeInt(rl.PortRangeMax)
	writeStr(rl.IPVersion)
	writeStr(rl.Cidr)
	writeStr(rl.RemoteGroupID)
	writeStr(rl.IPSetID)
	writeStr(rl.Description)
	return b.String()
}

// optionalInt64FromAPI reads a nullable integer field from an API object map.
// JSON numbers decode to float64. A present null (e.g. port_range_min on an icmp
// rule) collapses to a null types.Int64 (not 0), so an unset optional port
// round-trips as null and does not show spurious drift against config that omits
// it. An absent key also yields null.
func optionalInt64FromAPI(obj map[string]any, key string) types.Int64 {
	raw, ok := obj[key]
	if !ok || raw == nil {
		return types.Int64Null()
	}
	switch v := raw.(type) {
	case float64:
		return types.Int64Value(int64(v))
	case int64:
		return types.Int64Value(v)
	case int:
		return types.Int64Value(int64(v))
	default:
		return types.Int64Null()
	}
}

// requiredInt64FromAPI reads a REQUIRED integer field from an API object map.
// Unlike optionalInt64FromAPI, an absent or null key falls back to prior so
// that a Required schema attribute never ends up null in state (which would
// cause an "inconsistent result after apply" error from the framework).
func requiredInt64FromAPI(obj map[string]any, key string, prior types.Int64) types.Int64 {
	raw, ok := obj[key]
	if !ok || raw == nil {
		return prior
	}
	switch v := raw.(type) {
	case float64:
		return types.Int64Value(int64(v))
	case int64:
		return types.Int64Value(v)
	case int:
		return types.Int64Value(int64(v))
	default:
		return prior
	}
}
