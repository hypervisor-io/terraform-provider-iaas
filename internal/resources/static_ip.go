package resources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/iaas/terraform-provider-iaas/internal/client"
)

// Interface assertions — static_ip mirrors the golden vpc resource pattern
// (synchronous create-only, no update endpoint → all configurable attrs RequiresReplace).
var (
	_ resource.Resource                = &staticIPResource{}
	_ resource.ResourceWithConfigure   = &staticIPResource{}
	_ resource.ResourceWithImportState = &staticIPResource{}
)

// NewStaticIPResource is the resource constructor registered with the provider.
func NewStaticIPResource() resource.Resource {
	return &staticIPResource{}
}

// staticIPResource manages an iaas_static_ip.
//
// The Static IP API has:
//   - NO individual SHOW route — Read uses GET /static-ips (paginator) + scan-by-id.
//   - NO UPDATE route — every configurable attribute is RequiresReplace.
//   - Create (allocate) is SYNCHRONOUS — the allocate response carries the id directly.
//   - All routes are gated behind billing.enabled middleware; 403 is surfaced as an error.
//
// Route summary (verified against the real controller):
//
//	INDEX    GET    /static-ips             (plural) → Laravel paginator {data:[...]}
//	ALLOCATE POST   /static-ips/allocate    (plural) body {ip_id,hypervisor_group_id}
//	                                        → 200 {success,message,static_ip:{id,status,ip:{ip,...},...}}
//	DELETE   DELETE /static-ip/{id}         (singular) → 200 {success,message}
type staticIPResource struct {
	client *client.Client
}

// staticIPModel maps the Terraform state/plan for iaas_static_ip.
//
// Allocate inputs (ip_id, hypervisor_group_id) are Required+RequiresReplace
// because there is no update endpoint and changing them would mean a different IP.
//
// address (the actual IP string, nested at .ip.ip in the response) and
// hypervisor_group_name are server-assigned stable computed fields:
//   - address: assigned at allocation, never changes → UseStateForUnknown is safe.
//   - hypervisor_group_name: derived from the group, stable → UseStateForUnknown.
//
// status is server-MUTABLE: transitions between "allocated" and "attached" when
// the IP is bound/unbound from an instance. UseStateForUnknown is intentionally
// NOT used here so that Terraform always shows the refreshed value (otherwise
// drift from out-of-band attach/detach operations would be masked).
type staticIPModel struct {
	ID                  types.String `tfsdk:"id"`
	IpID                types.String `tfsdk:"ip_id"`
	HypervisorGroupID   types.String `tfsdk:"hypervisor_group_id"`
	Address             types.String `tfsdk:"address"`
	Status              types.String `tfsdk:"status"`
	HypervisorGroupName types.String `tfsdk:"hypervisor_group_name"`
}

// Metadata sets the resource type name → "<provider>_static_ip" → "iaas_static_ip".
func (r *staticIPResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_static_ip"
}

// Schema describes the iaas_static_ip resource.
//
// Because there is NO update endpoint, EVERY configurable attribute is
// RequiresReplace: any change must be applied by deallocating and re-allocating.
//
// This resource requires billing to be enabled on the platform (Cloud Service
// billing). If billing is disabled, all operations return a 403 which is
// surfaced as an informative diagnostic.
func (r *staticIPResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reserves (allocates) a static public IPv4 address from the chosen " +
			"location's available pool. Static IPs are long-lived addresses that persist " +
			"independent of any single instance and can be attached to or detached from " +
			"instances. Allocation pre-bills the first hour via Cloud Service credits. " +
			"This resource requires billing to be enabled on the platform; if billing is " +
			"disabled the API returns a 403 error.\n\n" +
			"The Static IP API has no update endpoint, so changing any configurable " +
			"attribute forces the resource to be replaced (deallocated and re-allocated). " +
			"Deallocating an IP that is currently attached to an instance will fail — " +
			"detach it from the instance first.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the static IP record, assigned by the API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"ip_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the underlying IP address to reserve. Must be a free, " +
					"non-reserved public IPv4 from the chosen location's available pool. " +
					"Obtain eligible ids from the /static-ips/available endpoint or the " +
					"panel UI. Changing this forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"hypervisor_group_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the hypervisor group (location) the IP belongs to. " +
					"Static IPs must be enabled for this location (static_ip_enabled = true). " +
					"Changing this forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"address": schema.StringAttribute{
				Computed: true,
				Description: "The allocated IPv4 address (e.g. 203.0.113.10). " +
					"Server-assigned at allocation time; stable once allocated.",
				// UseStateForUnknown is safe: the address is assigned at create
				// and never changes for the lifetime of this static IP record.
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"status": schema.StringAttribute{
				Computed: true,
				Description: "Current status of the static IP. One of: `allocated` " +
					"(reserved but not attached to any instance) or `attached` " +
					"(currently bound to an instance). This field is server-managed and " +
					"changes when the IP is attached to or detached from an instance out " +
					"of band. Terraform will show the current value on every plan.",
				// Do NOT add UseStateForUnknown: status is server-mutable and
				// Terraform must always reflect the refreshed value so that
				// out-of-band attach/detach operations do not silently drift.
			},
			"hypervisor_group_name": schema.StringAttribute{
				Computed: true,
				Description: "Display name of the hypervisor group (location) the IP " +
					"belongs to. Server-assigned and stable after allocation.",
				// UseStateForUnknown is safe: group membership is stable post-create.
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

// Configure pulls the shared *client.Client from the provider. It tolerates a
// nil ProviderData (the framework calls Configure once with nil data before the
// provider's own Configure has run).
func (r *staticIPResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create allocates the static IP. ip_id and hypervisor_group_id are always
// sent (both are required by the server). The allocate is synchronous — the
// response carries id and the nested ip object with the address directly, so
// no task/waiter and no list-and-match read-back are needed.
func (r *staticIPResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan staticIPModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := map[string]any{
		"ip_id":               plan.IpID.ValueString(),
		"hypervisor_group_id": plan.HypervisorGroupID.ValueString(),
	}

	obj, err := r.client.AllocateStaticIP(ctx, body)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error allocating static IP", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, staticIPStateFromAPI(obj, plan))...)
}

// Read refreshes state from the API. Because there is no individual SHOW route
// for static IPs, Read uses GetStaticIP (which lists all IPs and scans for the
// matching id). A 404 (id absent from list) means the IP was deallocated out
// of band — remove it from state so Terraform plans a re-allocation.
func (r *staticIPResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state staticIPModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	obj, err := r.client.GetStaticIP(ctx, state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("Error reading static IP", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, staticIPStateFromAPI(obj, state))...)
}

// Update is a defensive no-op. The Static IP API has NO update endpoint, and
// every configurable attribute is RequiresReplace, so the framework replaces
// the resource instead of ever calling Update. We re-read via GetStaticIP and
// write the refreshed state so that, in the impossible event Update is invoked,
// state stays consistent rather than silently drifting.
func (r *staticIPResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan staticIPModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	obj, err := r.client.GetStaticIP(ctx, plan.ID.ValueString())
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading static IP", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, staticIPStateFromAPI(obj, plan))...)
}

// Delete deallocates the static IP. The IP must not be currently attached to
// an instance (the API returns success:false with a clear error message if it
// is); detach it from the instance first.
func (r *staticIPResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state staticIPModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteStaticIP(ctx, state.ID.ValueString()); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deallocating static IP", err))
		return
	}
}

// ImportState lets `terraform import iaas_static_ip.x <uuid>` adopt an existing
// static IP; the next Read populates the rest of the attributes.
func (r *staticIPResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// staticIPStateFromAPI builds the model from an API static_ip object, falling
// back to the prior model's value for any field the response omits.
//
// address is extracted from the nested ip object (obj["ip"]["ip"]) using
// nestedStringFromAPI. This matches the API shape:
//
//	{"id":"…","status":"allocated","ip":{"ip":"203.0.113.10","subnet_id":"…"},"hypervisor_group":{"id":"…","name":"US East"}}
//
// hypervisor_group_name is extracted from the nested hypervisor_group object.
// status is read directly (server-mutable, no UseStateForUnknown).
func staticIPStateFromAPI(obj map[string]any, prior staticIPModel) staticIPModel {
	return staticIPModel{
		ID:                  stringFromAPI(obj, "id", prior.ID),
		IpID:                stringFromAPI(obj, "ip_id", prior.IpID),
		HypervisorGroupID:   stringFromAPI(obj, "hypervisor_group_id", prior.HypervisorGroupID),
		Address:             nestedStringFromAPI(obj, "ip", "ip", prior.Address),
		Status:              stringFromAPI(obj, "status", prior.Status),
		HypervisorGroupName: nestedStringFromAPI(obj, "hypervisor_group", "name", prior.HypervisorGroupName),
	}
}
