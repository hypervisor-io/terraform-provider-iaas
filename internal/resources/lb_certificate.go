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

// Interface assertions. iaas_lb_certificate is a CHILD resource of a load
// balancer. Unlike the other LB children it has NO update/PATCH route, so every
// field is immutable (RequiresReplace) - rotate by replacing. Read scans the LB
// SHOW certificates[]. Writes are SYNCHRONOUS (no waiter).
//
// SENSITIVE / WRITE-ONLY: private_key is $hidden server-side and never returned by
// the SHOW, so it is echoed from the plan on Create and PRESERVED across reads
// (and added to ImportStateVerifyIgnore in the lifecycle test). It is also marked
// Sensitive so it is redacted in plan/state output.
var (
	_ resource.Resource                = &lbCertificateResource{}
	_ resource.ResourceWithConfigure   = &lbCertificateResource{}
	_ resource.ResourceWithImportState = &lbCertificateResource{}
)

// NewLBCertificateResource is the resource constructor registered with the provider.
func NewLBCertificateResource() resource.Resource {
	return &lbCertificateResource{}
}

// lbCertificateResource manages an iaas_lb_certificate - an SSL certificate of a
// load balancer (manual PEM upload).
type lbCertificateResource struct {
	client *client.Client
}

// lbCertificateModel maps the Terraform state/plan for iaas_lb_certificate.
//
// load_balancer_id is in the path (Required + RequiresReplace). There is no
// certificate update route, so name/certificate/private_key/chain are all
// immutable (RequiresReplace). private_key is Sensitive + write-only.
type lbCertificateModel struct {
	ID             types.String `tfsdk:"id"`
	LoadBalancerID types.String `tfsdk:"load_balancer_id"`
	Name           types.String `tfsdk:"name"`
	Certificate    types.String `tfsdk:"certificate"`
	PrivateKey     types.String `tfsdk:"private_key"`
	Chain          types.String `tfsdk:"chain"`
}

// Metadata sets the resource type name → "iaas_lb_certificate".
func (r *lbCertificateResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_lb_certificate"
}

// Schema describes the iaas_lb_certificate resource.
func (r *lbCertificateResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a manually-uploaded SSL/TLS certificate on a load balancer (PEM " +
			"certificate + private key, optional chain). A certificate is a child of a load " +
			"balancer: its parent load_balancer_id is part of the API path, so changing it forces " +
			"a new resource. Certificates are immutable - there is no update endpoint - so " +
			"changing any field rotates (replaces) the certificate. The private_key is write-only " +
			"and sensitive: it is never returned by the API, so it is taken from configuration and " +
			"never refreshed. Attach a certificate to an https frontend via its ssl_certificate_id. " +
			"(Let's Encrypt issuance is not managed by this resource.) Import with a composite id: " +
			"\"<load_balancer_id>/<certificate_id>\".",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the certificate, assigned by the API.",
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
				Required: true,
				Description: "Name for the certificate. Immutable (no certificate update endpoint); " +
					"changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"certificate": schema.StringAttribute{
				Required: true,
				Description: "PEM-encoded certificate. Immutable; changing it forces a new resource " +
					"(rotation).",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"private_key": schema.StringAttribute{
				Required:  true,
				Sensitive: true,
				Description: "PEM-encoded private key. WRITE-ONLY and SENSITIVE: it is never returned " +
					"by the API (stored encrypted, hidden from reads), so it is taken from " +
					"configuration and never refreshed from the server. Immutable; changing it forces " +
					"a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"chain": schema.StringAttribute{
				Optional: true,
				Description: "Optional PEM-encoded intermediate certificate chain. Immutable; changing " +
					"it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
		},
	}
}

// Configure pulls the shared *client.Client from the provider (nil-guarded).
func (r *lbCertificateResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create uploads the certificate to its parent load balancer (synchronous), then
// reads back by scan. The private_key is sent in the body but echoed from the
// plan into state (the SHOW never returns it).
func (r *lbCertificateResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan lbCertificateModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := map[string]any{
		"name":        plan.Name.ValueString(),
		"certificate": plan.Certificate.ValueString(),
		"private_key": plan.PrivateKey.ValueString(),
	}
	if !plan.Chain.IsNull() && !plan.Chain.IsUnknown() && plan.Chain.ValueString() != "" {
		body["chain"] = plan.Chain.ValueString()
	}

	lbID := plan.LoadBalancerID.ValueString()
	created, err := r.client.CreateLBCertificate(ctx, lbID, body)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating load balancer certificate", err))
		return
	}
	id, _ := created["id"].(string)
	if id == "" {
		resp.Diagnostics.AddError("Error creating load balancer certificate", "the create response did not include a certificate id")
		return
	}

	obj, err := r.client.GetLBCertificate(ctx, lbID, id)
	if err != nil {
		obj = created
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, lbCertificateStateFromAPI(obj, plan))...)
}

// Read refreshes state by scanning the LB SHOW certificates[]. A 404 removes it.
// The write-only private_key is preserved from prior state (SHOW never returns it).
func (r *lbCertificateResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state lbCertificateModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	obj, err := r.client.GetLBCertificate(ctx, state.LoadBalancerID.ValueString(), state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("Error reading load balancer certificate", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, lbCertificateStateFromAPI(obj, state))...)
}

// Update is unreachable: every field is RequiresReplace (there is no certificate
// update endpoint), so the framework recreates rather than updating. Implemented
// as a no-op refresh only to satisfy the resource.Resource interface.
func (r *lbCertificateResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan lbCertificateModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

// Delete removes the certificate (and clears it from any frontend that used it).
func (r *lbCertificateResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state lbCertificateModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteLBCertificate(ctx, state.LoadBalancerID.ValueString(), state.ID.ValueString()); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deleting load balancer certificate", err))
		return
	}
}

// ImportState implements COMPOSITE import: "<load_balancer_id>/<certificate_id>".
// The write-only private_key cannot be read back, so it must be added to the
// lifecycle test's ImportStateVerifyIgnore.
func (r *lbCertificateResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	lbID, certID, ok := strings.Cut(req.ID, "/")
	if !ok || lbID == "" || certID == "" {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Expected import ID in the format \"load_balancer_id/certificate_id\", got: %q. "+
				"Load balancer certificates are child resources, so both the parent load balancer id "+
				"and the certificate id are required to import.", req.ID),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("load_balancer_id"), lbID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), certID)...)
}

// lbCertificateStateFromAPI builds the model from an embedded certificate object.
// private_key is NEVER in the SHOW ($hidden) → preserved verbatim from the prior
// model. certificate/chain ARE returned (decrypted) so they refresh from the API
// (falling back to prior when absent - e.g. on import before the first apply).
func lbCertificateStateFromAPI(obj map[string]any, prior lbCertificateModel) lbCertificateModel {
	return lbCertificateModel{
		ID:             stringFromAPI(obj, "id", prior.ID),
		LoadBalancerID: prior.LoadBalancerID, // from the path
		Name:           stringOrPrior(obj, "name", prior.Name),
		Certificate:    stringOrPrior(obj, "certificate", prior.Certificate),

		// WRITE-ONLY - never in the SHOW; preserve the plan/state value verbatim.
		PrivateKey: prior.PrivateKey,

		Chain: optionalStringFromAPI(obj, "chain", prior.Chain),
	}
}
