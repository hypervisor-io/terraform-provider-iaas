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

// Interface assertions. iaas_lb_backend is a CHILD resource of a load balancer.
// It follows the vpc_subnet child pattern (parent id in path + composite import)
// but reads by SCAN: there is no individual backend SHOW route, so Read calls the
// LB SHOW and scans the embedded backends[] array (like volume_snapshot's read).
// Backend writes are SYNCHRONOUS (the service runs syncConfig internally and
// returns the fresh backend), so there is NO waiter / timeouts block.
var (
	_ resource.Resource                = &lbBackendResource{}
	_ resource.ResourceWithConfigure   = &lbBackendResource{}
	_ resource.ResourceWithImportState = &lbBackendResource{}
)

// NewLBBackendResource is the resource constructor registered with the provider.
func NewLBBackendResource() resource.Resource {
	return &lbBackendResource{}
}

// lbBackendResource manages an iaas_lb_backend - a backend pool of a load balancer.
type lbBackendResource struct {
	client *client.Client
}

// lbBackendModel maps the Terraform state/plan for iaas_lb_backend.
//
// load_balancer_id is part of the API path (Required + RequiresReplace). name,
// algorithm and mode are all updatable in place (the backend has a PATCH route).
type lbBackendModel struct {
	ID             types.String `tfsdk:"id"`
	LoadBalancerID types.String `tfsdk:"load_balancer_id"`
	Name           types.String `tfsdk:"name"`
	Algorithm      types.String `tfsdk:"algorithm"`
	Mode           types.String `tfsdk:"mode"`
}

// Metadata sets the resource type name → "iaas_lb_backend".
func (r *lbBackendResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_lb_backend"
}

// Schema describes the iaas_lb_backend resource.
func (r *lbBackendResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a backend pool of a load balancer. A backend is a child of a load " +
			"balancer: its parent load_balancer_id is part of the API path, so changing it " +
			"forces a new resource. The name, algorithm (balancing method) and mode can be " +
			"changed in place. Add target servers to a backend with iaas_lb_target. Import " +
			"with a composite id: \"<load_balancer_id>/<backend_id>\".",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the backend, assigned by the API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"load_balancer_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the parent load balancer this backend belongs to. This value is " +
					"part of the API request path, so changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Name for the backend pool. Updatable in place.",
			},
			"algorithm": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "Load-balancing algorithm: \"roundrobin\" (default), \"leastconn\" or " +
					"\"source\". Updatable in place. (The API wire field is \"algorithm\".)",
				// Optional+Computed: an omitted value adopts the server default
				// (roundrobin) and round-trips without spurious drift.
			},
			"mode": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Proxy mode: \"http\" (default) or \"tcp\". Updatable in place.",
			},
		},
	}
}

// Configure pulls the shared *client.Client from the provider (nil-guarded).
func (r *lbBackendResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create adds the backend to its parent load balancer. The create is synchronous;
// the response carries the new backend object with its id. We then read-back by
// scanning the LB SHOW so state reflects the server-applied defaults.
func (r *lbBackendResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan lbBackendModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := map[string]any{
		"name": plan.Name.ValueString(),
	}
	if !plan.Algorithm.IsNull() && !plan.Algorithm.IsUnknown() {
		body["algorithm"] = plan.Algorithm.ValueString()
	}
	if !plan.Mode.IsNull() && !plan.Mode.IsUnknown() {
		body["mode"] = plan.Mode.ValueString()
	}

	lbID := plan.LoadBalancerID.ValueString()
	created, err := r.client.CreateLBBackend(ctx, lbID, body)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating load balancer backend", err))
		return
	}
	id, _ := created["id"].(string)
	if id == "" {
		resp.Diagnostics.AddError("Error creating load balancer backend", "the create response did not include a backend id")
		return
	}

	// Read-back by scanning the LB SHOW so the server defaults (algorithm/mode)
	// are reflected. Fall back to the create response if the scan can't find it
	// yet (defensive - syncConfig is synchronous so it should be present).
	obj, err := r.client.GetLBBackend(ctx, lbID, id)
	if err != nil {
		obj = created
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, lbBackendStateFromAPI(obj, plan))...)
}

// Read refreshes state by scanning the parent LB SHOW embedded backends[]. A 404
// (backend or its LB gone) removes the resource from state.
func (r *lbBackendResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state lbBackendModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	obj, err := r.client.GetLBBackend(ctx, state.LoadBalancerID.ValueString(), state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("Error reading load balancer backend", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, lbBackendStateFromAPI(obj, state))...)
}

// Update patches the mutable backend fields (name, algorithm, mode). The PATCH
// returns the fresh backend; we read-back by scan for a consistent view.
func (r *lbBackendResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan lbBackendModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := map[string]any{
		"name": plan.Name.ValueString(),
	}
	if !plan.Algorithm.IsNull() && !plan.Algorithm.IsUnknown() {
		body["algorithm"] = plan.Algorithm.ValueString()
	}
	if !plan.Mode.IsNull() && !plan.Mode.IsUnknown() {
		body["mode"] = plan.Mode.ValueString()
	}

	lbID := plan.LoadBalancerID.ValueString()
	if _, err := r.client.UpdateLBBackend(ctx, lbID, plan.ID.ValueString(), body); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error updating load balancer backend", err))
		return
	}

	obj, err := r.client.GetLBBackend(ctx, lbID, plan.ID.ValueString())
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading load balancer backend after update", err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, lbBackendStateFromAPI(obj, plan))...)
}

// Delete removes the backend (and all its targets) from the load balancer.
func (r *lbBackendResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state lbBackendModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteLBBackend(ctx, state.LoadBalancerID.ValueString(), state.ID.ValueString()); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deleting load balancer backend", err))
		return
	}
}

// ImportState implements COMPOSITE import: "<load_balancer_id>/<backend_id>".
func (r *lbBackendResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	lbID, backendID, ok := strings.Cut(req.ID, "/")
	if !ok || lbID == "" || backendID == "" {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Expected import ID in the format \"load_balancer_id/backend_id\", got: %q. "+
				"Load balancer backends are child resources, so both the parent load balancer id "+
				"and the backend id are required to import.", req.ID),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("load_balancer_id"), lbID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), backendID)...)
}

// lbBackendStateFromAPI builds the model from an embedded backend object, falling
// back to the prior model's value for fields the response omits. load_balancer_id
// is authoritative from the path (the embedded object may carry it as
// load_balancer_id, but the path value is canonical).
func lbBackendStateFromAPI(obj map[string]any, prior lbBackendModel) lbBackendModel {
	return lbBackendModel{
		ID:             stringFromAPI(obj, "id", prior.ID),
		LoadBalancerID: prior.LoadBalancerID, // from the path
		Name:           stringOrPrior(obj, "name", prior.Name),
		Algorithm:      stringFromAPI(obj, "algorithm", prior.Algorithm),
		Mode:           stringFromAPI(obj, "mode", prior.Mode),
	}
}
