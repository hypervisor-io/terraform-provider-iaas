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

// Interface assertions. iaas_lb_routing_rule is a 3-LEVEL CHILD resource: a rule
// belongs to a frontend, which belongs to a load balancer. BOTH parent ids
// (load_balancer_id, frontend_id) are in the API path → both Required +
// RequiresReplace, and import is a 3-part composite. Read scans the LB SHOW
// frontends[].routing_rules[]. All fields are updatable in place (PATCH route);
// writes are SYNCHRONOUS (no waiter).
var (
	_ resource.Resource                = &lbRoutingRuleResource{}
	_ resource.ResourceWithConfigure   = &lbRoutingRuleResource{}
	_ resource.ResourceWithImportState = &lbRoutingRuleResource{}
)

// NewLBRoutingRuleResource is the resource constructor registered with the provider.
func NewLBRoutingRuleResource() resource.Resource {
	return &lbRoutingRuleResource{}
}

// lbRoutingRuleResource manages an iaas_lb_routing_rule — an L7 routing rule of a
// load balancer frontend.
type lbRoutingRuleResource struct {
	client *client.Client
}

// lbRoutingRuleModel maps the Terraform state/plan for iaas_lb_routing_rule.
//
// load_balancer_id + frontend_id are in the path (Required + RequiresReplace).
// backend_id (the rule's target backend, API column lb_backend_id), match_type,
// match_value, match_host, match_header_name, priority and enabled are updatable
// in place.
type lbRoutingRuleModel struct {
	ID              types.String `tfsdk:"id"`
	LoadBalancerID  types.String `tfsdk:"load_balancer_id"`
	FrontendID      types.String `tfsdk:"frontend_id"`
	BackendID       types.String `tfsdk:"backend_id"`
	MatchType       types.String `tfsdk:"match_type"`
	MatchValue      types.String `tfsdk:"match_value"`
	MatchHost       types.String `tfsdk:"match_host"`
	MatchHeaderName types.String `tfsdk:"match_header_name"`
	Priority        types.Int64  `tfsdk:"priority"`
	Enabled         types.Bool   `tfsdk:"enabled"`
}

// Metadata sets the resource type name → "iaas_lb_routing_rule".
func (r *lbRoutingRuleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_lb_routing_rule"
}

// Schema describes the iaas_lb_routing_rule resource.
func (r *lbRoutingRuleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages an L7 routing rule of a load balancer frontend: it directs matching " +
			"traffic to a specific backend. A rule is a child of a frontend, which is a child of a " +
			"load balancer, so BOTH the load_balancer_id and frontend_id are part of the API path " +
			"and changing either forces a new resource. All other fields are updatable in place. " +
			"Import with a 3-part composite id: \"<load_balancer_id>/<frontend_id>/<rule_id>\".",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the routing rule, assigned by the API.",
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
			"frontend_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the parent frontend this rule belongs to. Part of the API path; " +
					"changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"backend_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the backend matching traffic is routed to. Updatable in place. " +
					"(API field: lb_backend_id.)",
			},
			"match_type": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "How match_value is compared: \"path_prefix\" (default), \"path_exact\", " +
					"\"host\", \"header\" or \"any\". Updatable in place.",
			},
			"match_value": schema.StringAttribute{
				Required:    true,
				Description: "The value to match (e.g. \"/api\" for a path_prefix). Updatable in place.",
			},
			"match_host": schema.StringAttribute{
				Optional:    true,
				Description: "Optional host to additionally match (Host header). Updatable in place.",
			},
			"match_header_name": schema.StringAttribute{
				Optional: true,
				Description: "Optional header name to match against (used when match_type is " +
					"\"header\"). Updatable in place.",
			},
			"priority": schema.Int64Attribute{
				Optional:    true,
				Computed:    true,
				Description: "Evaluation priority (lower wins; default 100). Updatable in place.",
			},
			"enabled": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Whether the rule is active. Defaults to true. Updatable in place.",
			},
		},
	}
}

// Configure pulls the shared *client.Client from the provider (nil-guarded).
func (r *lbRoutingRuleResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// routingRuleBody builds the wire body from the plan, mapping backend_id →
// lb_backend_id and omitting unset optionals.
func routingRuleBody(plan lbRoutingRuleModel) map[string]any {
	body := map[string]any{
		"lb_backend_id": plan.BackendID.ValueString(),
		"match_value":   plan.MatchValue.ValueString(),
	}
	if !plan.MatchType.IsNull() && !plan.MatchType.IsUnknown() {
		body["match_type"] = plan.MatchType.ValueString()
	}
	if !plan.MatchHost.IsNull() && !plan.MatchHost.IsUnknown() && plan.MatchHost.ValueString() != "" {
		body["match_host"] = plan.MatchHost.ValueString()
	}
	if !plan.MatchHeaderName.IsNull() && !plan.MatchHeaderName.IsUnknown() && plan.MatchHeaderName.ValueString() != "" {
		body["match_header_name"] = plan.MatchHeaderName.ValueString()
	}
	if !plan.Priority.IsNull() && !plan.Priority.IsUnknown() {
		body["priority"] = plan.Priority.ValueInt64()
	}
	if !plan.Enabled.IsNull() && !plan.Enabled.IsUnknown() {
		body["enabled"] = plan.Enabled.ValueBool()
	}
	return body
}

// Create adds the routing rule to its frontend (synchronous), then reads back by scan.
func (r *lbRoutingRuleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan lbRoutingRuleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	lbID := plan.LoadBalancerID.ValueString()
	frontendID := plan.FrontendID.ValueString()
	created, err := r.client.CreateLBRoutingRule(ctx, lbID, frontendID, routingRuleBody(plan))
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating load balancer routing rule", err))
		return
	}
	id, _ := created["id"].(string)
	if id == "" {
		resp.Diagnostics.AddError("Error creating load balancer routing rule", "the create response did not include a routing rule id")
		return
	}

	obj, err := r.client.GetLBRoutingRule(ctx, lbID, frontendID, id)
	if err != nil {
		obj = created
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, lbRoutingRuleStateFromAPI(obj, plan))...)
}

// Read refreshes state by scanning the LB SHOW frontends[].routing_rules[]. A 404 removes it.
func (r *lbRoutingRuleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state lbRoutingRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	obj, err := r.client.GetLBRoutingRule(ctx, state.LoadBalancerID.ValueString(), state.FrontendID.ValueString(), state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("Error reading load balancer routing rule", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, lbRoutingRuleStateFromAPI(obj, state))...)
}

// Update patches the mutable rule fields, then reads back by scan.
func (r *lbRoutingRuleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan lbRoutingRuleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	lbID := plan.LoadBalancerID.ValueString()
	frontendID := plan.FrontendID.ValueString()
	if _, err := r.client.UpdateLBRoutingRule(ctx, lbID, frontendID, plan.ID.ValueString(), routingRuleBody(plan)); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error updating load balancer routing rule", err))
		return
	}

	obj, err := r.client.GetLBRoutingRule(ctx, lbID, frontendID, plan.ID.ValueString())
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading load balancer routing rule after update", err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, lbRoutingRuleStateFromAPI(obj, plan))...)
}

// Delete removes the routing rule from its frontend.
func (r *lbRoutingRuleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state lbRoutingRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteLBRoutingRule(ctx, state.LoadBalancerID.ValueString(), state.FrontendID.ValueString(), state.ID.ValueString()); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deleting load balancer routing rule", err))
		return
	}
}

// ImportState implements 3-PART COMPOSITE import:
// "<load_balancer_id>/<frontend_id>/<rule_id>".
func (r *lbRoutingRuleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Expected import ID in the format \"load_balancer_id/frontend_id/rule_id\", got: %q. "+
				"Load balancer routing rules are nested child resources, so all three ids are required to import.", req.ID),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("load_balancer_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("frontend_id"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[2])...)
}

// lbRoutingRuleStateFromAPI builds the model from an embedded routing rule object.
func lbRoutingRuleStateFromAPI(obj map[string]any, prior lbRoutingRuleModel) lbRoutingRuleModel {
	return lbRoutingRuleModel{
		ID:              stringFromAPI(obj, "id", prior.ID),
		LoadBalancerID:  prior.LoadBalancerID, // from the path
		FrontendID:      prior.FrontendID,     // from the path
		BackendID:       stringOrPrior(obj, "lb_backend_id", prior.BackendID),
		MatchType:       stringFromAPI(obj, "match_type", prior.MatchType),
		MatchValue:      stringOrPrior(obj, "match_value", prior.MatchValue),
		MatchHost:       optionalStringFromAPI(obj, "match_host", prior.MatchHost),
		MatchHeaderName: optionalStringFromAPI(obj, "match_header_name", prior.MatchHeaderName),
		Priority:        int64FromAPI(obj, "priority", prior.Priority),
		Enabled:         boolFromIntAPI(obj, "enabled", prior.Enabled),
	}
}
