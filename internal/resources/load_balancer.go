package resources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
	"github.com/hypervisor-io/terraform-provider-iaas/waiter"
)

// Interface assertions - iaas_load_balancer is an ASYNC resource backed by a
// REAL instance. It combines two established patterns:
//
//   - ASYNC (from instance/volume): CREATE records the LB row (status="deploying")
//     and spins up a backing instance + slave deploy task, then this resource
//     polls the SHOW status until "active" via a StatePollerWithErrorTolerance
//     waiter; the id is persisted to state BEFORE the wait so a failed wait still
//     leaves a destroyable resource; a timeouts block is exposed.
//   - NO-UPDATE (from vpc): there is NO update/PATCH route for the load balancer
//     ITSELF (only its children - frontends/backends/etc. - have PATCH routes), so
//     every create input is immutable and changing any of them forces a new
//     resource (RequiresReplace). The Update method is therefore a no-op read-back.
//
// This is the CORE load balancer only. The LB's children (frontends, backends,
// targets, certificates, routing rules) are a SEPARATE resource family (id21) and
// are NOT modelled here.
var (
	_ resource.Resource                = &loadBalancerResource{}
	_ resource.ResourceWithConfigure   = &loadBalancerResource{}
	_ resource.ResourceWithImportState = &loadBalancerResource{}
)

// NewLoadBalancerResource is the resource constructor registered with the provider.
func NewLoadBalancerResource() resource.Resource {
	return &loadBalancerResource{}
}

// loadBalancerResource manages an iaas_load_balancer - an HAProxy load balancer
// backed by a dedicated Cloud Service instance.
type loadBalancerResource struct {
	client *client.Client
}

// loadBalancerModel maps the Terraform state/plan for iaas_load_balancer.
//
// Field groups:
//   - REPLACE inputs (name, lb_plan_id, vpc_id, hypervisor_group_id): immutable;
//     there is no LB update endpoint, so changing any forces a new resource.
//   - WRITE-ONLY create input (vpc_subnet_id): consumed at deploy time but NOT
//     returned by SHOW; echoed from the plan into state and preserved on read.
//   - server-managed computed (status, public_ip, instance_id).
type loadBalancerModel struct {
	ID                types.String `tfsdk:"id"`
	Name              types.String `tfsdk:"name"`
	LbPlanID          types.String `tfsdk:"lb_plan_id"`
	VPCID             types.String `tfsdk:"vpc_id"`
	VPCSubnetID       types.String `tfsdk:"vpc_subnet_id"`
	HypervisorGroupID types.String `tfsdk:"hypervisor_group_id"`

	// Computed read-only.
	Status     types.String `tfsdk:"status"`
	PublicIP   types.String `tfsdk:"public_ip"`
	InstanceID types.String `tfsdk:"instance_id"`

	Timeouts timeouts.Value `tfsdk:"timeouts"`
}

// Metadata sets the resource type name → "<provider>_load_balancer".
func (r *loadBalancerResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_load_balancer"
}

// Schema describes the iaas_load_balancer resource.
func (r *loadBalancerResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a load balancer (HAProxy) backed by a dedicated instance. Creation is " +
			"ASYNCHRONOUS: the LB record and its backing instance are created, then this resource " +
			"waits for the LB status to become \"active\" (the lifecycle is deploying → configuring " +
			"→ active). Deploy in PUBLIC mode by setting hypervisor_group_id (the location), or in " +
			"VPC mode by setting vpc_id + vpc_subnet_id. There is NO update endpoint for the load " +
			"balancer itself, so every input is immutable - changing any forces a new resource. The " +
			"load balancer's frontends, backends, targets, certificates, and routing rules are " +
			"managed by separate resources. The feature must be enabled for the chosen location; if " +
			"it is not (or the per-account load balancer quota is reached, or no public IP is " +
			"available), the create fails with a clear message.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the load balancer, assigned by the API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required: true,
				Description: "Name for the load balancer. Immutable (there is no LB update endpoint); " +
					"changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"lb_plan_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the load balancer plan (sizing of the backing instance). " +
					"Immutable; changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"vpc_id": schema.StringAttribute{
				Optional: true,
				Description: "Optional UUID of a VPC to deploy the load balancer into (VPC mode). When " +
					"set, vpc_subnet_id is required and hypervisor_group_id is derived from the VPC. " +
					"Omit for public mode (supply hypervisor_group_id instead). Changing it forces a " +
					"new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"vpc_subnet_id": schema.StringAttribute{
				Optional: true,
				Description: "UUID of the VPC subnet to place the load balancer in (required when vpc_id " +
					"is set). WRITE-ONLY: not returned by the API on read, so this value is echoed from " +
					"configuration and never refreshed. Changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"hypervisor_group_id": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "UUID of the location (hypervisor group) to deploy into. Required for public " +
					"mode (no VPC); in VPC mode it is derived from the VPC and returned by the API. " +
					"Immutable; changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					// RequiresReplaceIfConfigured: a user-supplied change forces a
					// replace, but the server-derived value (VPC mode) settling into
					// this Computed field does not. UseStateForUnknown keeps the
					// derived value stable across plans.
					stringplanmodifier.RequiresReplaceIfConfigured(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			// status is a SERVER-MUTABLE computed field: it changes over the LB's
			// life (deploying → configuring → active, suspended, error, deleting).
			// Per the golden guardrail, do NOT attach UseStateForUnknown to a
			// server-mutable computed field - that would copy the stale prior value
			// into the plan and MASK real drift.
			"status": schema.StringAttribute{
				Computed: true,
				Description: "Lifecycle status of the load balancer: \"deploying\" (provisioning), " +
					"\"configuring\" (instance up, applying HAProxy config), \"active\" (ready), " +
					"\"suspended\", \"error\", \"deleting\". Server-mutable.",
			},
			"public_ip": schema.StringAttribute{
				Computed: true,
				Description: "Public IPv4 address of the load balancer, auto-assigned at deploy and " +
					"extracted from the nested public_ip object. A public IP is present for public-mode " +
					"LBs and for public-subnet VPC LBs. Stable after create. Not sensitive (a public " +
					"address is not a secret).",
				// Stable after create (the public IP allocation is fixed at deploy).
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"instance_id": schema.StringAttribute{
				Computed: true,
				Description: "UUID of the backing instance that runs HAProxy. Assigned at create and " +
					"stable thereafter.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
		Blocks: map[string]schema.Block{
			// Only create is async (waits for status="active"); delete soft-deletes
			// and the next SHOW 404s. The block still exposes all three for
			// consistency with the async-resource pattern.
			"timeouts": timeouts.Block(ctx, timeouts.Opts{
				Create: true,
				Update: true,
				Delete: true,
			}),
		},
	}
}

// Configure pulls the shared *client.Client from the provider (nil-guarded).
func (r *loadBalancerResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create deploys the load balancer and waits for it to become active:
//
//  1. CreateLoadBalancer records the LB row + backing instance and returns the
//     object WITH its id (status="deploying"). There is NO task_id - the async
//     signal is the LB's own status, polled via SHOW.
//  2. The id is saved into state BEFORE the wait, so a provisioning failure or
//     timeout still tracks the LB for a subsequent destroy.
//  3. WaitFor polls GetLoadBalancer until status=="active" (fail on "error").
//  4. GetLoadBalancer hydrates the computed fields; the immutable inputs and the
//     write-only vpc_subnet_id are echoed from the PLAN (SHOW cannot return the
//     subnet id).
func (r *loadBalancerResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan loadBalancerModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	createTimeout, diags := plan.Timeouts.Create(ctx, defaultCreateTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := map[string]any{
		"name":       plan.Name.ValueString(),
		"lb_plan_id": plan.LbPlanID.ValueString(),
	}
	if !plan.VPCID.IsNull() && !plan.VPCID.IsUnknown() && plan.VPCID.ValueString() != "" {
		body["vpc_id"] = plan.VPCID.ValueString()
	}
	if !plan.VPCSubnetID.IsNull() && !plan.VPCSubnetID.IsUnknown() && plan.VPCSubnetID.ValueString() != "" {
		body["vpc_subnet_id"] = plan.VPCSubnetID.ValueString()
	}
	// hypervisor_group_id is Optional+Computed: only send it when the user
	// configured it (it is known/non-empty in the plan). In VPC mode it is
	// derived by the server, so the plan value is unknown and must be omitted.
	if !plan.HypervisorGroupID.IsNull() && !plan.HypervisorGroupID.IsUnknown() && plan.HypervisorGroupID.ValueString() != "" {
		body["hypervisor_group_id"] = plan.HypervisorGroupID.ValueString()
	}

	created, err := r.client.CreateLoadBalancer(ctx, body)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating load balancer", err))
		return
	}
	id, _ := created["id"].(string)
	if id == "" {
		resp.Diagnostics.AddError("Error creating load balancer", "the create response did not include a load balancer id")
		return
	}

	// Persist the id immediately so a failed provisioning/wait still tracks the
	// resource for cleanup on the next destroy.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// ── ASYNC convergence: poll the LB SHOW until status="active" ─────────────
	// The lifecycle is deploying → configuring → active; "error" is the terminal
	// failure (a config-apply failure). Tolerance=3 absorbs transient transport
	// blips during provisioning that bypass the client's 429/5xx retry.
	waitErr := waiter.WaitFor(ctx, waiter.Options{
		Interval: pollInterval(),
		Timeout:  createTimeout,
		Refresh: waiter.StatePollerWithErrorTolerance(
			func() (map[string]any, error) { return r.client.GetLoadBalancer(ctx, id) },
			"status",
			[]string{"active"},
			[]string{"error"},
			3,
		),
	})
	if waitErr != nil {
		resp.Diagnostics.AddError(
			"Error waiting for load balancer provisioning",
			fmt.Sprintf("load balancer %s did not become active: %s", id, waitErr.Error()),
		)
		return
	}

	// Read back so state reflects the public IP, backing instance, and derived
	// hypervisor_group_id.
	obj, err := r.client.GetLoadBalancer(ctx, id)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading load balancer after provisioning", err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, loadBalancerStateFromAPI(obj, plan))...)
}

// Read refreshes state from the API. A 404 means the LB was deleted out of band -
// remove it from state so Terraform plans a recreate. The write-only
// vpc_subnet_id is not in the SHOW payload, so it is preserved from prior state.
func (r *loadBalancerResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state loadBalancerModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	obj, err := r.client.GetLoadBalancer(ctx, state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("Error reading load balancer", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, loadBalancerStateFromAPI(obj, state))...)
}

// Update is effectively a no-op: every input is RequiresReplace (there is no LB
// update endpoint), so the framework recreates the resource rather than calling
// Update for any input change. Only the timeouts block can change without a
// replace; we re-read to keep computed fields fresh and carry the new timeouts.
func (r *loadBalancerResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan loadBalancerModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	obj, err := r.client.GetLoadBalancer(ctx, plan.ID.ValueString())
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading load balancer after update", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, loadBalancerStateFromAPI(obj, plan))...)
}

// Delete removes the load balancer. DELETE flips status→"deleting", destroys the
// backing instance (releasing its public IP), bills the final hours, and
// soft-deletes the row, so a subsequent SHOW 404s. We poll GetLoadBalancer until
// it reports 404 (IsNotFound) as the convergence signal. A failure (e.g. the
// backing instance destroy threw) surfaces as success:false from
// DeleteLoadBalancer.
func (r *loadBalancerResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state loadBalancerModel
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
	if err := r.client.DeleteLoadBalancer(ctx, id); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deleting load balancer", err))
		return
	}

	// Converge by polling SHOW until it 404s. The Refresh closure treats an
	// IsNotFound error as "done", and any other error as terminal.
	waitErr := waiter.WaitFor(ctx, waiter.Options{
		Interval: pollInterval(),
		Timeout:  deleteTimeout,
		Refresh: func() (string, bool, error) {
			_, err := r.client.GetLoadBalancer(ctx, id)
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
			"Error waiting for load balancer deletion",
			fmt.Sprintf("load balancer %s was not removed: %s", id, waitErr.Error()),
		)
		return
	}
}

// ImportState lets `terraform import iaas_load_balancer.x <uuid>` adopt an
// existing load balancer; the next Read populates the readable attributes. The
// write-only vpc_subnet_id cannot be read back, so it is added to the lifecycle
// test's ImportStateVerifyIgnore.
func (r *loadBalancerResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// loadBalancerStateFromAPI builds the model from a SHOW load_balancer object. The
// immutable inputs (name, lb_plan_id, vpc_id, hypervisor_group_id) authoritative
// value is the plan/state; the computed fields come from the API. The write-only
// vpc_subnet_id is preserved verbatim from the prior model (SHOW never returns
// it). public_ip is extracted from the nested public_ip{ip} object.
func loadBalancerStateFromAPI(obj map[string]any, prior loadBalancerModel) loadBalancerModel {
	return loadBalancerModel{
		ID:       stringFromAPI(obj, "id", prior.ID),
		Name:     stringOrPrior(obj, "name", prior.Name),
		LbPlanID: stringOrPrior(obj, "lb_plan_id", prior.LbPlanID),
		VPCID:    optionalStringFromAPI(obj, "vpc_id", prior.VPCID),

		// WRITE-ONLY create input - never in SHOW; preserve prior verbatim.
		VPCSubnetID: prior.VPCSubnetID,

		HypervisorGroupID: stringFromAPI(obj, "hypervisor_group_id", prior.HypervisorGroupID),

		// Computed read-only.
		Status:     stringFromAPI(obj, "status", prior.Status),
		PublicIP:   nestedStringFromAPI(obj, "public_ip", "ip", prior.PublicIP),
		InstanceID: computedStringFromAPI(obj, "instance_id", prior.InstanceID),

		Timeouts: prior.Timeouts,
	}
}
