package resources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
)

// iaas_lb_waf_policy is a SINGLETON CHILD resource: one WAF policy per load
// balancer. load_balancer_id is in the API path (Required + RequiresReplace)
// and is also the import id. Create and Update both PUT the full policy;
// Delete disables the WAF (removes the policy row). Writes are SYNCHRONOUS
// (no waiter).
//
// Attribute names match the wire contract's customer-facing field names
// (client/lb_waf.go), not the raw `lb_waf_policies` DB columns: "sensitivity"
// (not "paranoia_level") and "exclusions" (not "crs_exclusions") -
// product-identity lock, see LB-WAF-COORDINATION.md.
var (
	_ resource.Resource                = &lbWafPolicyResource{}
	_ resource.ResourceWithConfigure   = &lbWafPolicyResource{}
	_ resource.ResourceWithImportState = &lbWafPolicyResource{}
)

// NewLBWafPolicyResource is the resource constructor registered with the provider.
func NewLBWafPolicyResource() resource.Resource {
	return &lbWafPolicyResource{}
}

type lbWafPolicyResource struct {
	client *client.Client
}

type lbWafPolicyModel struct {
	ID                 types.String `tfsdk:"id"`
	LoadBalancerID     types.String `tfsdk:"load_balancer_id"`
	Enabled            types.Bool   `tfsdk:"enabled"`
	Mode               types.String `tfsdk:"mode"`
	FailMode           types.String `tfsdk:"fail_mode"`
	Sensitivity        types.Int64  `tfsdk:"sensitivity"`
	ResponseInspection types.Bool   `tfsdk:"response_inspection"`
	CrsEnabled         types.Bool   `tfsdk:"crs_enabled"`
	FullAudit          types.Bool   `tfsdk:"full_audit"`
	Exclusions         types.List   `tfsdk:"exclusions"`
}

func (r *lbWafPolicyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_lb_waf_policy"
}

func (r *lbWafPolicyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages the Web Application Firewall policy of a load balancer (a singleton child). " +
			"load_balancer_id is part of the API path; changing it forces a new resource. Import with the load balancer id. " +
			"Requires a load balancer plan with the waf capability enabled.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "UUID of the WAF policy, assigned by the API.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"load_balancer_id": schema.StringAttribute{
				Required:      true,
				Description:   "UUID of the parent load balancer. Part of the API path; changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"enabled": schema.BoolAttribute{Required: true, Description: "Whether the WAF is active."},
			"mode":    schema.StringAttribute{Required: true, Description: "Inspection mode: \"off\", \"detect\" (log, pass) or \"block\" (deny matched)."},
			"fail_mode": schema.StringAttribute{
				Optional: true, Computed: true,
				Description: "Agent-fault behavior: \"open\" (default) or \"close\".",
			},
			"sensitivity": schema.Int64Attribute{
				Optional: true, Computed: true,
				Description: "Rule sensitivity level 1-4 (default 1). Higher catches more but risks more false positives.",
			},
			"response_inspection": schema.BoolAttribute{
				Optional: true, Computed: true,
				Description: "Inspect response bodies (default false).",
			},
			"crs_enabled": schema.BoolAttribute{
				Optional: true, Computed: true,
				Description: "Enable the managed core rule set (default true).",
			},
			"full_audit": schema.BoolAttribute{
				Optional: true, Computed: true,
				Description: "Write heavy per-transaction audit detail (default false).",
			},
			"exclusions": schema.ListAttribute{
				ElementType: types.Int64Type,
				Optional:    true, Computed: true,
				Description: "Managed rule ids to exclude from evaluation.",
			},
		},
	}
}

func (r *lbWafPolicyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func wafExclusionsFromPlan(l types.List) []int64 {
	if l.IsNull() || l.IsUnknown() {
		return nil
	}
	out := make([]int64, 0, len(l.Elements()))
	for _, v := range l.Elements() {
		if iv, ok := v.(types.Int64); ok && !iv.IsNull() && !iv.IsUnknown() {
			out = append(out, iv.ValueInt64())
		}
	}
	return out
}

func wafPolicyBody(plan lbWafPolicyModel) map[string]any {
	body := map[string]any{
		"enabled": plan.Enabled.ValueBool(),
		"mode":    plan.Mode.ValueString(),
	}
	if !plan.FailMode.IsNull() && !plan.FailMode.IsUnknown() {
		body["fail_mode"] = plan.FailMode.ValueString()
	}
	if !plan.Sensitivity.IsNull() && !plan.Sensitivity.IsUnknown() {
		body["sensitivity"] = plan.Sensitivity.ValueInt64()
	}
	if !plan.ResponseInspection.IsNull() && !plan.ResponseInspection.IsUnknown() {
		body["response_inspection"] = plan.ResponseInspection.ValueBool()
	}
	if !plan.CrsEnabled.IsNull() && !plan.CrsEnabled.IsUnknown() {
		body["crs_enabled"] = plan.CrsEnabled.ValueBool()
	}
	if !plan.FullAudit.IsNull() && !plan.FullAudit.IsUnknown() {
		body["full_audit"] = plan.FullAudit.ValueBool()
	}
	if !plan.Exclusions.IsNull() && !plan.Exclusions.IsUnknown() {
		body["exclusions"] = wafExclusionsFromPlan(plan.Exclusions)
	}
	return body
}

func (r *lbWafPolicyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan lbWafPolicyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	obj, err := r.client.PutLBWafPolicy(ctx, plan.LoadBalancerID.ValueString(), wafPolicyBody(plan))
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating load balancer WAF policy", err))
		return
	}
	state, diags := lbWafPolicyStateFromAPI(obj, plan)
	resp.Diagnostics.Append(diags...)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *lbWafPolicyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state lbWafPolicyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	obj, err := r.client.GetLBWafPolicy(ctx, state.LoadBalancerID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("Error reading load balancer WAF policy", err))
		return
	}
	newState, diags := lbWafPolicyStateFromAPI(obj, state)
	resp.Diagnostics.Append(diags...)
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

func (r *lbWafPolicyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan lbWafPolicyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	obj, err := r.client.PutLBWafPolicy(ctx, plan.LoadBalancerID.ValueString(), wafPolicyBody(plan))
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error updating load balancer WAF policy", err))
		return
	}
	state, diags := lbWafPolicyStateFromAPI(obj, plan)
	resp.Diagnostics.Append(diags...)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *lbWafPolicyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state lbWafPolicyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteLBWafPolicy(ctx, state.LoadBalancerID.ValueString()); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error disabling load balancer WAF", err))
	}
}

// ImportState: the load balancer id is the singleton policy's identity.
func (r *lbWafPolicyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("load_balancer_id"), req.ID)...)
}

// wafExclusionsListFromAPI reads the "exclusions" int array from the API
// object, falling back to the prior list when absent/null/malformed.
func wafExclusionsListFromAPI(obj map[string]any, fallback types.List) (types.List, diag.Diagnostics) {
	raw, ok := obj["exclusions"]
	if !ok || raw == nil {
		return fallback, nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return fallback, nil
	}
	vals := make([]attr.Value, 0, len(arr))
	for _, item := range arr {
		switch n := item.(type) {
		case float64:
			vals = append(vals, types.Int64Value(int64(n)))
		case int64:
			vals = append(vals, types.Int64Value(n))
		case int:
			vals = append(vals, types.Int64Value(int64(n)))
		}
	}
	return types.ListValue(types.Int64Type, vals)
}

func lbWafPolicyStateFromAPI(obj map[string]any, prior lbWafPolicyModel) (lbWafPolicyModel, diag.Diagnostics) {
	exclusions, diags := wafExclusionsListFromAPI(obj, prior.Exclusions)

	return lbWafPolicyModel{
		ID:                 stringFromAPI(obj, "id", prior.ID),
		LoadBalancerID:     prior.LoadBalancerID, // from the path
		Enabled:            boolFromIntAPI(obj, "enabled", prior.Enabled),
		Mode:               stringOrPrior(obj, "mode", prior.Mode),
		FailMode:           stringFromAPI(obj, "fail_mode", prior.FailMode),
		Sensitivity:        int64FromAPI(obj, "sensitivity", prior.Sensitivity),
		ResponseInspection: boolFromIntAPI(obj, "response_inspection", prior.ResponseInspection),
		CrsEnabled:         boolFromIntAPI(obj, "crs_enabled", prior.CrsEnabled),
		FullAudit:          boolFromIntAPI(obj, "full_audit", prior.FullAudit),
		Exclusions:         exclusions,
	}, diags
}
