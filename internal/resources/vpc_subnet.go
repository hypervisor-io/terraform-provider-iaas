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

	"github.com/hypervisor-io/terraform-provider-iaas/internal/client"
)

// Interface assertions - vpc_subnet is the golden CHILD resource and implements
// the full set of optional resource behaviours (Configure + ImportState). It
// establishes the child-resource pattern later child resources copy:
//   - the parent UUID lives in the URL path (vpc_id, RequiresReplace),
//   - import takes a COMPOSITE id "<vpc_id>/<subnet_id>",
//   - server-mutable computed fields OMIT UseStateForUnknown (the guardrail).
var (
	_ resource.Resource                = &vpcSubnetResource{}
	_ resource.ResourceWithConfigure   = &vpcSubnetResource{}
	_ resource.ResourceWithImportState = &vpcSubnetResource{}
)

// NewVPCSubnetResource is the resource constructor registered with the provider.
func NewVPCSubnetResource() resource.Resource {
	return &vpcSubnetResource{}
}

// vpcSubnetResource manages an iaas_vpc_subnet - a subnet inside a parent VPC.
//
// The parent VPC id (vpc_id) is part of every request path, so it is Required +
// RequiresReplace. Only name is mutable in place; cidr/type/gateway/netmask are
// immutable (cidr/type RequiresReplace; gateway/netmask are derived computed).
// Create is synchronous: the row is returned immediately with its id and the
// server-derived gateway/netmask. IP generation (used/free) is async on a queue
// with NO status field, so there is no waiter.
type vpcSubnetResource struct {
	client *client.Client
}

// vpcSubnetModel maps the Terraform state/plan for iaas_vpc_subnet.
//
// Netmask/Gateway are server-DERIVED from cidr at create and stable thereafter
// (Computed + UseStateForUnknown). Used/Free/UsedPercentage are server-MUTABLE
// computed values (they change as IPs are allocated) - see the schema comment
// for why they deliberately omit UseStateForUnknown.
type vpcSubnetModel struct {
	ID             types.String  `tfsdk:"id"`
	VPCID          types.String  `tfsdk:"vpc_id"`
	Cidr           types.String  `tfsdk:"cidr"`
	Type           types.String  `tfsdk:"type"`
	Name           types.String  `tfsdk:"name"`
	Netmask        types.String  `tfsdk:"netmask"`
	Gateway        types.String  `tfsdk:"gateway"`
	Used           types.Int64   `tfsdk:"used"`
	Free           types.Int64   `tfsdk:"free"`
	UsedPercentage types.Float64 `tfsdk:"used_percentage"`
}

// Metadata sets the resource type name → "<provider>_vpc_subnet" → "iaas_vpc_subnet".
func (r *vpcSubnetResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vpc_subnet"
}

// Schema describes the iaas_vpc_subnet resource.
func (r *vpcSubnetResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a subnet inside a VPC (virtual private cloud network). " +
			"A subnet is a child of a VPC: its parent vpc_id is part of the API path, " +
			"so changing it forces a new resource. The gateway and netmask are derived " +
			"server-side from the CIDR. Only the name can be changed in place; changing " +
			"the CIDR or type forces the subnet to be replaced.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the subnet, assigned by the API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"vpc_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the parent VPC this subnet belongs to. This value is part " +
					"of the API request path, so changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"cidr": schema.StringAttribute{
				Required: true,
				Description: "IPv4 CIDR block for the subnet (e.g. 192.168.10.0/24). The gateway " +
					"and netmask are derived from it server-side. Changing this forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"type": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "Subnet type: \"public\" or \"private\". Defaults to \"public\" when " +
					"omitted. Immutable; changing it forces a new resource. Modelled " +
					"Optional+Computed so that an omitted type round-trips against the " +
					"server default without showing spurious drift.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"name": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "Display name for the subnet. Defaults to a server-assigned " +
					"\"Subnet N\" when omitted. This is the ONLY field that can be changed " +
					"in place (it is NOT RequiresReplace).",
				// Intentionally no RequiresReplace: name is the single in-place
				// updatable attribute (Update → PATCH name).
			},
			"netmask": schema.StringAttribute{
				Computed: true,
				Description: "Subnet mask, derived server-side from the CIDR at create time. " +
					"Stable after creation.",
				// UseStateForUnknown is safe here because this computed value is
				// stable after create (derived once from cidr). Only apply
				// UseStateForUnknown to computed fields the server does NOT mutate
				// post-create.
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"gateway": schema.StringAttribute{
				Computed: true,
				Description: "Gateway IP, derived server-side from the CIDR at create time. " +
					"Stable after creation.",
				// UseStateForUnknown is safe here because this computed value is
				// stable after create (derived once from cidr).
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			// used / free / used_percentage are SERVER-MUTABLE computed fields:
			// they change over the life of the subnet as IPs are allocated and
			// released (IP generation itself runs async on a queue after create).
			//
			// The golden guardrail: do NOT attach UseStateForUnknown to
			// server-mutable computed fields. UseStateForUnknown copies the prior
			// state value into the plan, which would MASK real drift - the plan
			// would keep showing the stale used/free instead of the refreshed
			// values Read pulled from the API. Omitting it lets the plan reflect
			// the server's current values (the field re-plans as (known after
			// apply) when it may change). Only stable-after-create computed fields
			// (id, netmask, gateway) use UseStateForUnknown.
			"used": schema.Int64Attribute{
				Computed: true,
				Description: "Number of IPs currently allocated in the subnet. Server-mutable; " +
					"populated asynchronously after IP generation completes.",
			},
			"free": schema.Int64Attribute{
				Computed: true,
				Description: "Number of IPs currently free in the subnet. Server-mutable; " +
					"populated asynchronously after IP generation completes.",
			},
			"used_percentage": schema.Float64Attribute{
				Computed: true,
				Description: "Percentage of the subnet's IPs currently in use, derived from " +
					"used/free. Server-mutable.",
			},
		},
	}
}

// Configure pulls the shared *client.Client from the provider. It tolerates a
// nil ProviderData (the framework calls Configure once with nil data before the
// provider's own Configure has run).
func (r *vpcSubnetResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create provisions the subnet under its parent VPC. cidr is always sent; name
// and type are sent only when the user set them (omit, don't send null) so the
// server applies its own defaults. The create is synchronous - the response
// carries id plus the derived gateway/netmask, which we persist directly.
func (r *vpcSubnetResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan vpcSubnetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := map[string]any{
		"cidr": plan.Cidr.ValueString(),
	}
	// Only send name/type when the user set them (omit, don't send null). type
	// is Optional+Computed, so an unset value is null (not unknown after the
	// plan); guard both for safety.
	if !plan.Name.IsNull() && !plan.Name.IsUnknown() {
		body["name"] = plan.Name.ValueString()
	}
	if !plan.Type.IsNull() && !plan.Type.IsUnknown() {
		body["type"] = plan.Type.ValueString()
	}

	obj, err := r.client.CreateVPCSubnet(ctx, plan.VPCID.ValueString(), body)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating VPC subnet", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, vpcSubnetStateFromAPI(obj, plan))...)
}

// Read refreshes state from the API. The parent vpc_id is read from prior state
// to build the request path. A 404 means the subnet (or its VPC) was deleted
// out of band - remove it from state so Terraform plans a recreate.
func (r *vpcSubnetResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state vpcSubnetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	obj, err := r.client.GetVPCSubnet(ctx, state.VPCID.ValueString(), state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("Error reading VPC subnet", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, vpcSubnetStateFromAPI(obj, state))...)
}

// Update changes the only mutable field - name. cidr/type/vpc_id all force
// replacement, so only name ever reaches here. The PATCH response is a full
// fresh subnet object, so we rehydrate all computed fields from it.
func (r *vpcSubnetResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan vpcSubnetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	fields := map[string]any{
		"name": plan.Name.ValueString(),
	}

	obj, err := r.client.UpdateVPCSubnet(ctx, plan.VPCID.ValueString(), plan.ID.ValueString(), fields)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error updating VPC subnet", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, vpcSubnetStateFromAPI(obj, plan))...)
}

// Delete removes the subnet from its parent VPC.
func (r *vpcSubnetResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state vpcSubnetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteVPCSubnet(ctx, state.VPCID.ValueString(), state.ID.ValueString()); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deleting VPC subnet", err))
		return
	}
}

// ImportState implements COMPOSITE import for this child resource. Because the
// parent vpc_id is required to build the API path (and is not derivable from the
// subnet id alone), `terraform import` must supply BOTH ids joined by a slash:
//
//	terraform import iaas_vpc_subnet.x <vpc_id>/<subnet_id>
//
// We split req.ID on the FIRST "/" into vpc_id and subnet_id, set both into
// state via path.Root, and let the subsequent Read hydrate the remaining
// attributes. This composite-import is THE pattern every child resource copies.
func (r *vpcSubnetResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	vpcID, subnetID, ok := strings.Cut(req.ID, "/")
	if !ok || vpcID == "" || subnetID == "" {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Expected import ID in the format \"vpc_id/subnet_id\", got: %q. "+
				"VPC subnets are child resources, so both the parent VPC id and the "+
				"subnet id are required to import.", req.ID),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("vpc_id"), vpcID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), subnetID)...)
}

// vpcSubnetStateFromAPI builds the model from an API subnet object, falling back
// to the prior model's value for any field the response omits. vpc_id is never
// returned in the subnet object (it lives in the URL), so it always falls back
// to the prior plan/state value. The nested ips array (returned by SHOW) is
// intentionally ignored.
func vpcSubnetStateFromAPI(obj map[string]any, prior vpcSubnetModel) vpcSubnetModel {
	return vpcSubnetModel{
		ID:             stringFromAPI(obj, "id", prior.ID),
		VPCID:          prior.VPCID, // never in the response body; from the path
		Cidr:           stringFromAPI(obj, "cidr", prior.Cidr),
		Type:           stringFromAPI(obj, "type", prior.Type),
		Name:           stringFromAPI(obj, "name", prior.Name),
		Netmask:        stringFromAPI(obj, "netmask", prior.Netmask),
		Gateway:        stringFromAPI(obj, "gateway", prior.Gateway),
		Used:           int64FromAPI(obj, "used", prior.Used),
		Free:           int64FromAPI(obj, "free", prior.Free),
		UsedPercentage: float64FromAPI(obj, "used_percentage", prior.UsedPercentage),
	}
}

// float64FromAPI reads a floating-point field from an API object map. JSON
// numbers decode to float64; an absent key or non-numeric value falls back to
// the prior value (or null when there is none). It sits alongside
// stringFromAPI/int64FromAPI as the third typed *FromAPI helper.
func float64FromAPI(obj map[string]any, key string, fallback types.Float64) types.Float64 {
	raw, ok := obj[key]
	if !ok || raw == nil {
		return fallback
	}
	switch v := raw.(type) {
	case float64:
		return types.Float64Value(v)
	case float32:
		return types.Float64Value(float64(v))
	case int64:
		return types.Float64Value(float64(v))
	case int:
		return types.Float64Value(float64(v))
	default:
		return fallback
	}
}
