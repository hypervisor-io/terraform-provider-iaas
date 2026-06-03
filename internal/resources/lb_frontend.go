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

// Interface assertions. iaas_lb_frontend is a CHILD resource of a load balancer
// (a listener: port + protocol). Read scans the LB SHOW frontends[]. All fields
// are updatable in place (the frontend has a PATCH route). Writes are
// SYNCHRONOUS (no waiter).
var (
	_ resource.Resource                = &lbFrontendResource{}
	_ resource.ResourceWithConfigure   = &lbFrontendResource{}
	_ resource.ResourceWithImportState = &lbFrontendResource{}
)

// NewLBFrontendResource is the resource constructor registered with the provider.
func NewLBFrontendResource() resource.Resource {
	return &lbFrontendResource{}
}

// lbFrontendResource manages an iaas_lb_frontend — a listener of a load balancer.
type lbFrontendResource struct {
	client *client.Client
}

// lbFrontendModel maps the Terraform state/plan for iaas_lb_frontend.
//
// load_balancer_id is in the path (Required + RequiresReplace). name/mode/port/
// protocol/ssl_certificate_id/default_backend_id/enabled are all updatable in
// place (the frontend has a PATCH route).
type lbFrontendModel struct {
	ID               types.String `tfsdk:"id"`
	LoadBalancerID   types.String `tfsdk:"load_balancer_id"`
	Name             types.String `tfsdk:"name"`
	Mode             types.String `tfsdk:"mode"`
	Port             types.Int64  `tfsdk:"port"`
	Protocol         types.String `tfsdk:"protocol"`
	SSLCertificateID types.String `tfsdk:"ssl_certificate_id"`
	DefaultBackendID types.String `tfsdk:"default_backend_id"`
	Enabled          types.Bool   `tfsdk:"enabled"`
}

// Metadata sets the resource type name → "iaas_lb_frontend".
func (r *lbFrontendResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_lb_frontend"
}

// Schema describes the iaas_lb_frontend resource.
func (r *lbFrontendResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a frontend listener of a load balancer (a port + protocol the load " +
			"balancer accepts traffic on). A frontend is a child of a load balancer: its parent " +
			"load_balancer_id is part of the API path, so changing it forces a new resource. The " +
			"listener is identified by (port, protocol), which must be unique per load balancer. " +
			"All other fields are updatable in place. Point a frontend at a default backend with " +
			"default_backend_id and, for HTTPS, attach a certificate with ssl_certificate_id. " +
			"Import with a composite id: \"<load_balancer_id>/<frontend_id>\".",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the frontend, assigned by the API.",
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
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Name for the frontend listener. Updatable in place.",
			},
			"port": schema.Int64Attribute{
				Required: true,
				Description: "Port the listener binds to. Together with protocol it must be unique " +
					"per load balancer. Updatable in place.",
			},
			"protocol": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "Listener protocol: \"http\" (default), \"https\", \"tcp\" or \"udp\". " +
					"Together with port it must be unique per load balancer. Updatable in place.",
			},
			"mode": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Proxy mode: \"http\" (default) or \"tcp\". Updatable in place.",
			},
			"ssl_certificate_id": schema.StringAttribute{
				Optional: true,
				Description: "Optional UUID of an iaas_lb_certificate to terminate TLS with (for an " +
					"https listener). Updatable in place.",
			},
			"default_backend_id": schema.StringAttribute{
				Optional: true,
				Description: "Optional UUID of the default backend traffic is sent to when no routing " +
					"rule matches. Updatable in place.",
			},
			"enabled": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Whether the listener is active. Defaults to true. Updatable in place.",
			},
		},
	}
}

// Configure pulls the shared *client.Client from the provider (nil-guarded).
func (r *lbFrontendResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// frontendBody builds the wire body from the plan, omitting unset optionals.
func frontendBody(plan lbFrontendModel) map[string]any {
	body := map[string]any{
		"name": plan.Name.ValueString(),
		"port": plan.Port.ValueInt64(),
	}
	if !plan.Protocol.IsNull() && !plan.Protocol.IsUnknown() {
		body["protocol"] = plan.Protocol.ValueString()
	}
	if !plan.Mode.IsNull() && !plan.Mode.IsUnknown() {
		body["mode"] = plan.Mode.ValueString()
	}
	if !plan.SSLCertificateID.IsNull() && !plan.SSLCertificateID.IsUnknown() && plan.SSLCertificateID.ValueString() != "" {
		body["ssl_certificate_id"] = plan.SSLCertificateID.ValueString()
	}
	if !plan.DefaultBackendID.IsNull() && !plan.DefaultBackendID.IsUnknown() && plan.DefaultBackendID.ValueString() != "" {
		body["default_backend_id"] = plan.DefaultBackendID.ValueString()
	}
	if !plan.Enabled.IsNull() && !plan.Enabled.IsUnknown() {
		body["enabled"] = plan.Enabled.ValueBool()
	}
	return body
}

// Create adds the frontend to its parent load balancer (synchronous), then reads back.
func (r *lbFrontendResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan lbFrontendModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	lbID := plan.LoadBalancerID.ValueString()
	created, err := r.client.CreateLBFrontend(ctx, lbID, frontendBody(plan))
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating load balancer frontend", err))
		return
	}
	id, _ := created["id"].(string)
	if id == "" {
		resp.Diagnostics.AddError("Error creating load balancer frontend", "the create response did not include a frontend id")
		return
	}

	obj, err := r.client.GetLBFrontend(ctx, lbID, id)
	if err != nil {
		obj = created
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, lbFrontendStateFromAPI(obj, plan))...)
}

// Read refreshes state by scanning the LB SHOW frontends[]. A 404 removes it.
func (r *lbFrontendResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state lbFrontendModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	obj, err := r.client.GetLBFrontend(ctx, state.LoadBalancerID.ValueString(), state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("Error reading load balancer frontend", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, lbFrontendStateFromAPI(obj, state))...)
}

// Update patches the mutable frontend fields, then reads back by scan.
func (r *lbFrontendResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan lbFrontendModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	lbID := plan.LoadBalancerID.ValueString()
	if _, err := r.client.UpdateLBFrontend(ctx, lbID, plan.ID.ValueString(), frontendBody(plan)); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error updating load balancer frontend", err))
		return
	}

	obj, err := r.client.GetLBFrontend(ctx, lbID, plan.ID.ValueString())
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading load balancer frontend after update", err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, lbFrontendStateFromAPI(obj, plan))...)
}

// Delete removes the frontend (and its routing rules).
func (r *lbFrontendResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state lbFrontendModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteLBFrontend(ctx, state.LoadBalancerID.ValueString(), state.ID.ValueString()); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deleting load balancer frontend", err))
		return
	}
}

// ImportState implements COMPOSITE import: "<load_balancer_id>/<frontend_id>".
func (r *lbFrontendResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	lbID, frontendID, ok := strings.Cut(req.ID, "/")
	if !ok || lbID == "" || frontendID == "" {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Expected import ID in the format \"load_balancer_id/frontend_id\", got: %q. "+
				"Load balancer frontends are child resources, so both the parent load balancer id "+
				"and the frontend id are required to import.", req.ID),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("load_balancer_id"), lbID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), frontendID)...)
}

// lbFrontendStateFromAPI builds the model from an embedded frontend object.
func lbFrontendStateFromAPI(obj map[string]any, prior lbFrontendModel) lbFrontendModel {
	return lbFrontendModel{
		ID:               stringFromAPI(obj, "id", prior.ID),
		LoadBalancerID:   prior.LoadBalancerID, // from the path
		Name:             stringOrPrior(obj, "name", prior.Name),
		Mode:             stringFromAPI(obj, "mode", prior.Mode),
		Port:             int64FromAPI(obj, "port", prior.Port),
		Protocol:         stringFromAPI(obj, "protocol", prior.Protocol),
		SSLCertificateID: optionalStringFromAPI(obj, "ssl_certificate_id", prior.SSLCertificateID),
		DefaultBackendID: optionalStringFromAPI(obj, "default_backend_id", prior.DefaultBackendID),
		Enabled:          boolFromIntAPI(obj, "enabled", prior.Enabled),
	}
}
