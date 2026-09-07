package resources

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
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

// lbFrontendResource manages an iaas_lb_frontend - a listener of a load balancer.
type lbFrontendResource struct {
	client *client.Client
}

// lbFrontendModel maps the Terraform state/plan for iaas_lb_frontend.
//
// load_balancer_id is in the path (Required + RequiresReplace). name/mode/port/
// protocol/ssl_certificate_id/certificate_ids/default_backend_id/enabled/
// idle_timeout/ssl_redirect are all updatable in place (the frontend has a
// PATCH route).
type lbFrontendModel struct {
	ID               types.String `tfsdk:"id"`
	LoadBalancerID   types.String `tfsdk:"load_balancer_id"`
	Name             types.String `tfsdk:"name"`
	Mode             types.String `tfsdk:"mode"`
	Port             types.Int64  `tfsdk:"port"`
	Protocol         types.String `tfsdk:"protocol"`
	SSLCertificateID types.String `tfsdk:"ssl_certificate_id"`
	CertificateIDs   types.List   `tfsdk:"certificate_ids"`
	DefaultBackendID types.String `tfsdk:"default_backend_id"`
	Enabled          types.Bool   `tfsdk:"enabled"`
	IdleTimeout      types.Int64  `tfsdk:"idle_timeout"`
	SslRedirect      types.Bool   `tfsdk:"ssl_redirect"`
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
			"default_backend_id and, for HTTPS, attach one or more certificates with " +
			"certificate_ids (or a single one with the legacy ssl_certificate_id). " +
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
				// Deliberately NOT RequiresReplace: changing a frontend port is a listener
				// rebind that the API handles in place via updateFrontend (PATCH). This is
				// asymmetric with iaas_lb_target's target_port, which IS RequiresReplace
				// because changing a target's IP/port denotes a different backend server and
				// the API does not support mutating the key fields in place.
				Description: "Port the listener binds to. Together with protocol it must be unique " +
					"per load balancer. Updatable in place.",
			},
			"protocol": schema.StringAttribute{
				Optional: true,
				Computed: true,
				// Deliberately NOT RequiresReplace: the API supports a protocol change (e.g.
				// http → https) as an in-place listener rebind via updateFrontend (PATCH).
				// Contrast with iaas_lb_target's target_ip/target_port, which are
				// RequiresReplace because they identify the physical backend server.
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
				Description: "Optional UUID of an iaas_certificate to terminate TLS with (for an " +
					"https listener). Legacy single-certificate form, kept for backward " +
					"compatibility - equivalent to certificate_ids with one element. Setting " +
					"both in the same apply sends certificate_ids; prefer certificate_ids for " +
					"new configurations, especially SNI (multiple certificates on one listener). " +
					"Updatable in place.",
			},
			"certificate_ids": schema.ListAttribute{
				ElementType: types.StringType,
				Optional:    true,
				Computed:    true,
				Description: "Ordered list of iaas_certificate UUIDs to attach to this listener for " +
					"SNI (the first entry is the default certificate served when the client sends " +
					"no SNI hostname or one that matches none of the attached certificates). " +
					"Superset of ssl_certificate_id - set this instead to attach more than one " +
					"certificate to a single https listener. Reflects the listener's attached " +
					"certificates even when only the legacy ssl_certificate_id was set. Updatable " +
					"in place.",
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
			"idle_timeout": schema.Int64Attribute{
				Optional: true,
				MarkdownDescription: "Idle connection timeout in seconds (30-86400). Omit for the load " +
					"balancer default: 50 s for http/https listeners, 3600 s for tcp. " +
					"Updatable in place.",
				Validators: []validator.Int64{
					int64validator.Between(30, 86400),
				},
			},
			"ssl_redirect": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Description: "Redirect HTTP to HTTPS with a 301. Only meaningful on an http-mode " +
					"listener on a port other than 443; ignored elsewhere. Defaults to false. " +
					"Updatable in place.",
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
// On UPDATE (forUpdate) an unset idle_timeout is sent as an explicit null so
// removing it from the config clears the stored value; on CREATE it is simply
// omitted and the load balancer default applies.
func frontendBody(plan lbFrontendModel, forUpdate bool) map[string]any {
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
	// certificate_ids[] takes precedence when explicitly set in the config
	// (even an empty list, which clears every attached certificate - mirrors
	// LoadBalancerService::storeFrontend/updateFrontend's `$request->has('certificate_ids')`
	// check). Otherwise fall back to the legacy single ssl_certificate_id.
	if !plan.CertificateIDs.IsNull() && !plan.CertificateIDs.IsUnknown() {
		ids := stringListValues(plan.CertificateIDs)
		if ids == nil {
			ids = []string{}
		}
		body["certificate_ids"] = ids
	} else if !plan.SSLCertificateID.IsNull() && !plan.SSLCertificateID.IsUnknown() && plan.SSLCertificateID.ValueString() != "" {
		body["ssl_certificate_id"] = plan.SSLCertificateID.ValueString()
	}
	if !plan.DefaultBackendID.IsNull() && !plan.DefaultBackendID.IsUnknown() && plan.DefaultBackendID.ValueString() != "" {
		body["default_backend_id"] = plan.DefaultBackendID.ValueString()
	}
	if !plan.Enabled.IsNull() && !plan.Enabled.IsUnknown() {
		body["enabled"] = plan.Enabled.ValueBool()
	}
	if !plan.IdleTimeout.IsNull() && !plan.IdleTimeout.IsUnknown() {
		body["idle_timeout"] = plan.IdleTimeout.ValueInt64()
	} else if forUpdate {
		body["idle_timeout"] = nil
	}
	if !plan.SslRedirect.IsNull() && !plan.SslRedirect.IsUnknown() {
		body["ssl_redirect"] = plan.SslRedirect.ValueBool()
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
	created, err := r.client.CreateLBFrontend(ctx, lbID, frontendBody(plan, false))
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
	state, diags := lbFrontendStateFromAPI(ctx, obj, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
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

	newState, diags := lbFrontendStateFromAPI(ctx, obj, state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

// Update patches the mutable frontend fields, then reads back by scan.
func (r *lbFrontendResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan lbFrontendModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	lbID := plan.LoadBalancerID.ValueString()
	if _, err := r.client.UpdateLBFrontend(ctx, lbID, plan.ID.ValueString(), frontendBody(plan, true)); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error updating load balancer frontend", err))
		return
	}

	obj, err := r.client.GetLBFrontend(ctx, lbID, plan.ID.ValueString())
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading load balancer frontend after update", err))
		return
	}
	newState, diags := lbFrontendStateFromAPI(ctx, obj, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
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
// certificate_ids is derived from the response's embedded "certificates"
// array (see frontendCertificateIDsFromAPI) so it reflects reality even when
// the caller only ever set the legacy ssl_certificate_id.
func lbFrontendStateFromAPI(ctx context.Context, obj map[string]any, prior lbFrontendModel) (lbFrontendModel, diag.Diagnostics) {
	certIDs, diags := frontendCertificateIDsFromAPI(ctx, obj, prior.CertificateIDs)

	return lbFrontendModel{
		ID:               stringFromAPI(obj, "id", prior.ID),
		LoadBalancerID:   prior.LoadBalancerID, // from the path
		Name:             stringOrPrior(obj, "name", prior.Name),
		Mode:             stringFromAPI(obj, "mode", prior.Mode),
		Port:             int64FromAPI(obj, "port", prior.Port),
		Protocol:         stringFromAPI(obj, "protocol", prior.Protocol),
		SSLCertificateID: optionalStringFromAPI(obj, "ssl_certificate_id", prior.SSLCertificateID),
		CertificateIDs:   certIDs,
		DefaultBackendID: optionalStringFromAPI(obj, "default_backend_id", prior.DefaultBackendID),
		Enabled:          boolFromIntAPI(obj, "enabled", prior.Enabled),
		IdleTimeout:      optionalInt64FromAPI(obj, "idle_timeout"),
		SslRedirect:      boolFromIntAPI(obj, "ssl_redirect", prior.SslRedirect),
	}, diags
}

// frontendCertificateIDsFromAPI extracts the ordered list of certificate
// UUIDs attached to a frontend from the API's embedded "certificates" array
// (each element an object with at least an "id" key) into a types.List(string)
// for iaas_lb_frontend's certificate_ids. A present array (possibly empty)
// becomes a known list; an absent/non-array key falls back to the prior value.
func frontendCertificateIDsFromAPI(ctx context.Context, obj map[string]any, fallback types.List) (types.List, diag.Diagnostics) {
	raw, ok := obj["certificates"]
	if !ok {
		return fallback, nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return fallback, nil
	}
	ids := make([]string, 0, len(arr))
	for _, v := range arr {
		cert, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if id, ok := cert["id"].(string); ok && id != "" {
			ids = append(ids, id)
		}
	}
	return types.ListValueFrom(ctx, types.StringType, ids)
}
