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

	"github.com/hypervisor-io/terraform-provider-iaas/client"
)

// iaas_lb_waf_rule is a CHILD resource: a custom WAF rule of a load balancer.
// load_balancer_id is in the path (Required + RequiresReplace). rule_id, name,
// enabled, raw_seclang, target, operator, match_value, action and priority are
// updatable in place. Read scans the rules list (no single SHOW route). Import
// is a 2-part composite "<load_balancer_id>/<rule_uuid>". Writes are SYNCHRONOUS.
//
// Attribute names mirror the API-facing wire contract (client/lb_waf.go),
// which is intentionally NOT the raw `lb_waf_rules` DB columns
// (rule_id/name/seclang/enabled/sort_order only, see LB-WAF-COORDINATION.md
// S6 Task 2 row) and NOT the S5 human panel's beginner-friendly
// phase/source/builder{} wizard shape either: target/operator/match_value/
// action/priority are a flatter, SecLang-native guided-builder convenience
// for automation/IaC callers, rendered into `seclang` server-side alongside
// (or instead of) a directly-supplied raw_seclang. "priority" maps to the
// real `sort_order` column.
var (
	_ resource.Resource                = &lbWafRuleResource{}
	_ resource.ResourceWithConfigure   = &lbWafRuleResource{}
	_ resource.ResourceWithImportState = &lbWafRuleResource{}
)

// NewLBWafRuleResource is the resource constructor registered with the provider.
func NewLBWafRuleResource() resource.Resource {
	return &lbWafRuleResource{}
}

type lbWafRuleResource struct {
	client *client.Client
}

type lbWafRuleModel struct {
	ID             types.String `tfsdk:"id"`
	LoadBalancerID types.String `tfsdk:"load_balancer_id"`
	RuleID         types.Int64  `tfsdk:"rule_id"`
	Name           types.String `tfsdk:"name"`
	Enabled        types.Bool   `tfsdk:"enabled"`
	RawSeclang     types.String `tfsdk:"raw_seclang"`
	Target         types.String `tfsdk:"target"`
	Operator       types.String `tfsdk:"operator"`
	MatchValue     types.String `tfsdk:"match_value"`
	Action         types.String `tfsdk:"action"`
	Priority       types.Int64  `tfsdk:"priority"`
	Seclang        types.String `tfsdk:"seclang"`
}

func (r *lbWafRuleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_lb_waf_rule"
}

func (r *lbWafRuleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a custom WAF rule of a load balancer. The rule is a child of a load balancer, " +
			"so load_balancer_id is part of the API path and changing it forces a new resource. Custom rule " +
			"ids must be in 190000-199999 (the API rejects out-of-range ids). A rule is authored either " +
			"directly (raw_seclang) or via the guided target/operator/match_value/action fields, which render " +
			"into seclang server-side when raw_seclang is omitted. Import with a 2-part composite " +
			"id: \"<load_balancer_id>/<rule_uuid>\".",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "UUID of the rule, assigned by the API.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"load_balancer_id": schema.StringAttribute{
				Required:      true,
				Description:   "UUID of the parent load balancer. Part of the API path; changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"rule_id":     schema.Int64Attribute{Optional: true, Computed: true, Description: "SecLang rule id, must be 190000-199999. Auto-assigned in range if omitted (or extracted from an id: token in raw_seclang)."},
			"name":        schema.StringAttribute{Required: true, Description: "Human-readable rule name."},
			"enabled":     schema.BoolAttribute{Optional: true, Computed: true, Description: "Whether the rule is active (default true)."},
			"raw_seclang": schema.StringAttribute{Optional: true, Description: "Raw SecLang directive (advanced). Any embedded id: must be 190000-199999. Takes precedence over the guided fields below."},
			"target":      schema.StringAttribute{Optional: true, Description: "Guided-builder SecLang variable (e.g. ARGS). Used to render seclang when raw_seclang is omitted."},
			"operator":    schema.StringAttribute{Optional: true, Description: "Guided-builder SecLang operator token (e.g. @rx). Used to render seclang when raw_seclang is omitted."},
			"match_value": schema.StringAttribute{Optional: true, Description: "Guided-builder match value."},
			"action":      schema.StringAttribute{Optional: true, Description: "deny, log, pass or drop (default deny)."},
			"priority":    schema.Int64Attribute{Optional: true, Computed: true, Description: "Evaluation priority; maps to the real sort_order column."},
			"seclang": schema.StringAttribute{
				Computed:      true,
				Description:   "The rendered/stored SecLang directive, as returned by the API.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *lbWafRuleResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected Provider Data Type",
			fmt.Sprintf("Expected *client.Client, got: %T. This is a provider bug; please report it.", req.ProviderData))
		return
	}
	r.client = c
}

func wafRuleBody(plan lbWafRuleModel) map[string]any {
	body := map[string]any{"name": plan.Name.ValueString()}
	if !plan.RuleID.IsNull() && !plan.RuleID.IsUnknown() {
		body["rule_id"] = plan.RuleID.ValueInt64()
	}
	if !plan.Enabled.IsNull() && !plan.Enabled.IsUnknown() {
		body["enabled"] = plan.Enabled.ValueBool()
	}
	if !plan.RawSeclang.IsNull() && !plan.RawSeclang.IsUnknown() && plan.RawSeclang.ValueString() != "" {
		body["raw_seclang"] = plan.RawSeclang.ValueString()
	}
	if !plan.Target.IsNull() && !plan.Target.IsUnknown() {
		body["target"] = plan.Target.ValueString()
	}
	if !plan.Operator.IsNull() && !plan.Operator.IsUnknown() {
		body["operator"] = plan.Operator.ValueString()
	}
	if !plan.MatchValue.IsNull() && !plan.MatchValue.IsUnknown() && plan.MatchValue.ValueString() != "" {
		body["match_value"] = plan.MatchValue.ValueString()
	}
	if !plan.Action.IsNull() && !plan.Action.IsUnknown() {
		body["action"] = plan.Action.ValueString()
	}
	if !plan.Priority.IsNull() && !plan.Priority.IsUnknown() {
		body["priority"] = plan.Priority.ValueInt64()
	}
	return body
}

func (r *lbWafRuleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan lbWafRuleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	lbID := plan.LoadBalancerID.ValueString()
	created, err := r.client.CreateLBWafRule(ctx, lbID, wafRuleBody(plan))
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating load balancer WAF rule", err))
		return
	}
	id, _ := created["id"].(string)
	if id == "" {
		resp.Diagnostics.AddError("Error creating load balancer WAF rule", "the create response did not include a rule id")
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, lbWafRuleStateFromAPI(created, plan))...)
}

func (r *lbWafRuleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state lbWafRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	obj, err := r.client.GetLBWafRule(ctx, state.LoadBalancerID.ValueString(), state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("Error reading load balancer WAF rule", err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, lbWafRuleStateFromAPI(obj, state))...)
}

func (r *lbWafRuleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan lbWafRuleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	lbID := plan.LoadBalancerID.ValueString()
	if _, err := r.client.UpdateLBWafRule(ctx, lbID, plan.ID.ValueString(), wafRuleBody(plan)); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error updating load balancer WAF rule", err))
		return
	}
	obj, err := r.client.GetLBWafRule(ctx, lbID, plan.ID.ValueString())
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading load balancer WAF rule after update", err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, lbWafRuleStateFromAPI(obj, plan))...)
}

func (r *lbWafRuleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state lbWafRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteLBWafRule(ctx, state.LoadBalancerID.ValueString(), state.ID.ValueString()); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deleting load balancer WAF rule", err))
	}
}

// ImportState implements 2-PART COMPOSITE import "<load_balancer_id>/<rule_uuid>".
func (r *lbWafRuleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		resp.Diagnostics.AddError("Invalid Import ID",
			fmt.Sprintf("Expected import ID in the format \"load_balancer_id/rule_uuid\", got: %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("load_balancer_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[1])...)
}

func lbWafRuleStateFromAPI(obj map[string]any, prior lbWafRuleModel) lbWafRuleModel {
	return lbWafRuleModel{
		ID:             stringFromAPI(obj, "id", prior.ID),
		LoadBalancerID: prior.LoadBalancerID, // from the path
		RuleID:         int64FromAPI(obj, "rule_id", prior.RuleID),
		Name:           stringOrPrior(obj, "name", prior.Name),
		Enabled:        boolFromIntAPI(obj, "enabled", prior.Enabled),
		RawSeclang:     prior.RawSeclang, // input-only convenience field, not echoed by the API
		Target:         prior.Target,     // input-only convenience field, not echoed by the API
		Operator:       prior.Operator,   // input-only convenience field, not echoed by the API
		MatchValue:     prior.MatchValue, // input-only convenience field, not echoed by the API
		Action:         prior.Action,     // input-only convenience field, not echoed by the API
		Priority:       int64FromAPI(obj, "sort_order", prior.Priority),
		Seclang:        optionalStringFromAPI(obj, "seclang", prior.Seclang),
	}
}
