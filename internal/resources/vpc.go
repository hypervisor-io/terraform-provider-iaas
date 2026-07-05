package resources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/iaas/terraform-provider-iaas/internal/client"
)

// Interface assertions - vpc mirrors the golden ssh_key resource's full set of
// optional behaviours (Configure + ImportState).
var (
	_ resource.Resource                = &vpcResource{}
	_ resource.ResourceWithConfigure   = &vpcResource{}
	_ resource.ResourceWithImportState = &vpcResource{}
)

// NewVPCResource is the resource constructor registered with the provider.
func NewVPCResource() resource.Resource {
	return &vpcResource{}
}

// vpcResource manages an iaas_vpc.
//
// The VPC API has NO update endpoint (VpcService::update exists server-side but
// is unwired), so every configurable attribute is RequiresReplace and the
// resource is effectively immutable in place. Create is synchronous: the create
// response already carries the new id and the appended vni_number, so no
// task/waiter and no list-and-match read-back are needed.
type vpcResource struct {
	client *client.Client
}

// vpcModel maps the Terraform state/plan for iaas_vpc.
//
// VniNumber is server-computed (the API appends it on create and it is stable
// afterwards), so it is modelled Computed and never sent on create.
type vpcModel struct {
	ID                types.String `tfsdk:"id"`
	Name              types.String `tfsdk:"name"`
	Cidr              types.String `tfsdk:"cidr"`
	HypervisorGroupID types.String `tfsdk:"hypervisor_group_id"`
	Description       types.String `tfsdk:"description"`
	VniNumber         types.Int64  `tfsdk:"vni_number"`
}

// Metadata sets the resource type name → "<provider>_vpc" → "iaas_vpc".
func (r *vpcResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vpc"
}

// Schema describes the iaas_vpc resource.
//
// Because there is NO update endpoint, EVERY configurable attribute is
// RequiresReplace: any change must be applied by destroying and recreating.
func (r *vpcResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a VPC (virtual private cloud network) in your IaaS account. " +
			"A VPC is an isolated layer-2 network, identified by a server-assigned VNI, " +
			"into which private subnets and instances can be placed. The VPC API has no " +
			"update endpoint, so changing any attribute forces the VPC to be replaced.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the VPC, assigned by the API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required: true,
				Description: "Name of the VPC. Maximum 16 characters; only lowercase letters " +
					"and digits are allowed (regex ^[a-z0-9]+$ - no spaces, dots, or dashes). " +
					"Validated server-side. Changing this forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"cidr": schema.StringAttribute{
				Required: true,
				Description: "CIDR block for the VPC (e.g. 10.0.0.0/24). Must fall within an " +
					"RFC1918 private range, enforced server-side. Changing this forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"hypervisor_group_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the hypervisor group (VPC-enabled location) the VPC is " +
					"created in. Discover valid ids via the panel's VPC locations endpoint. " +
					"Changing this forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"description": schema.StringAttribute{
				Optional: true,
				Description: "Optional free-text description of the VPC. The API has no update " +
					"endpoint, so changing this forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"vni_number": schema.Int64Attribute{
				Computed: true,
				Description: "VXLAN Network Identifier (VNI) assigned to the VPC by the API. " +
					"Server-managed and stable after creation.",
				// UseStateForUnknown is safe here because this computed value is
				// stable after create. Only apply UseStateForUnknown to computed
				// fields that the server does NOT mutate post-create; for
				// server-mutable computed fields, omit it so the plan shows the
				// refreshed value (otherwise drift is masked).
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

// Configure pulls the shared *client.Client from the provider. It tolerates a
// nil ProviderData (the framework calls Configure once with nil data before the
// provider's own Configure has run).
func (r *vpcResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create provisions the VPC. name/cidr/hypervisor_group_id are always sent;
// description is sent only when set so the server stores null rather than "".
// The create is synchronous - the response carries id and vni_number, which we
// persist directly (no read-back / waiter).
func (r *vpcResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan vpcModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := map[string]any{
		"name":                plan.Name.ValueString(),
		"cidr":                plan.Cidr.ValueString(),
		"hypervisor_group_id": plan.HypervisorGroupID.ValueString(),
	}
	// Only send description when the user set it (omit, don't send null).
	if !plan.Description.IsNull() && !plan.Description.IsUnknown() {
		body["description"] = plan.Description.ValueString()
	}

	obj, err := r.client.CreateVPC(ctx, body)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating VPC", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, vpcStateFromAPI(obj, plan))...)
}

// Read refreshes state from the API. A 404 means the VPC was deleted out of
// band - remove it from state so Terraform plans a recreate (drift handling).
func (r *vpcResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state vpcModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	obj, err := r.client.GetVPC(ctx, state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("Error reading VPC", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, vpcStateFromAPI(obj, state))...)
}

// Update is a defensive no-op. The VPC API has NO update endpoint, and every
// configurable attribute is RequiresReplace, so the framework recreates the
// resource instead of ever calling Update. We re-read via GetVPC and write the
// refreshed state so that, in the impossible event Update is invoked, state
// stays consistent rather than silently drifting. We never call a PATCH.
func (r *vpcResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan vpcModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	obj, err := r.client.GetVPC(ctx, plan.ID.ValueString())
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading VPC", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, vpcStateFromAPI(obj, plan))...)
}

// Delete removes the VPC.
func (r *vpcResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state vpcModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteVPC(ctx, state.ID.ValueString()); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deleting VPC", err))
		return
	}
}

// ImportState lets `terraform import iaas_vpc.x <uuid>` adopt an existing VPC;
// the next Read populates the rest of the attributes.
func (r *vpcResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// vpcStateFromAPI builds the model from an API vpc object, falling back to the
// prior model's value for any field the response omits. The nested subnets
// array (returned by SHOW) is intentionally ignored.
func vpcStateFromAPI(obj map[string]any, prior vpcModel) vpcModel {
	return vpcModel{
		ID:                stringFromAPI(obj, "id", prior.ID),
		Name:              stringFromAPI(obj, "name", prior.Name),
		Cidr:              stringFromAPI(obj, "cidr", prior.Cidr),
		HypervisorGroupID: stringFromAPI(obj, "hypervisor_group_id", prior.HypervisorGroupID),
		Description:       optionalStringFromAPI(obj, "description", prior.Description),
		VniNumber:         int64FromAPI(obj, "vni_number", prior.VniNumber),
	}
}

// optionalStringFromAPI reads an Optional (non-Computed) string field. Unlike
// stringFromAPI, a present null collapses to a null types.String (not ""), so
// an unset optional attribute round-trips as null and does not show spurious
// drift against config that omits it.
func optionalStringFromAPI(obj map[string]any, key string, fallback types.String) types.String {
	raw, ok := obj[key]
	if !ok {
		return fallback
	}
	if raw == nil {
		return types.StringNull()
	}
	if s, ok := raw.(string); ok {
		return types.StringValue(s)
	}
	return types.StringValue(fmt.Sprintf("%v", raw))
}

// int64FromAPI reads an integer field from an API object map. JSON numbers
// decode to float64; an absent key or non-numeric value falls back to the prior
// value (or null when there is none).
func int64FromAPI(obj map[string]any, key string, fallback types.Int64) types.Int64 {
	raw, ok := obj[key]
	if !ok || raw == nil {
		return fallback
	}
	switch v := raw.(type) {
	case float64:
		return types.Int64Value(int64(v))
	case int64:
		return types.Int64Value(v)
	case int:
		return types.Int64Value(int64(v))
	default:
		return fallback
	}
}
