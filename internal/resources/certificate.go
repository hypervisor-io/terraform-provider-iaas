package resources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
)

// Interface assertions. iaas_certificate is an ACCOUNT-level resource - unlike
// iaas_lb_certificate it is NOT a child of a load balancer, and it manages
// only manual PEM uploads: Let's Encrypt issuance is asynchronous (ACME) and
// is deliberately NOT modelled as a Terraform resource in v1 (use the panel
// or the MCP server's user.certificate.request_letsencrypt tool instead - see
// docs/resources/certificate.md).
//
// There is NO update route for a certificate - every input is RequiresReplace
// (rotate by replacing, same shape as iaas_lb_certificate / iaas_kubernetes_ssl_certificate).
var (
	_ resource.Resource                = &certificateResource{}
	_ resource.ResourceWithConfigure   = &certificateResource{}
	_ resource.ResourceWithImportState = &certificateResource{}
)

// NewCertificateResource is the resource constructor registered with the provider.
func NewCertificateResource() resource.Resource {
	return &certificateResource{}
}

// certificateResource manages an iaas_certificate - an account-level SSL/TLS
// certificate (manual PEM upload) that can be attached to any of the
// account's load balancer frontends via ssl_certificate_id.
//
// Route summary (Task 7 contract):
//
//	INDEX  GET    /certificates             → {success,certificates:[...]}
//	SHOW   GET    /certificate/{id}          → {success,certificate:{...,usages:[...]}}
//	CREATE POST   /certificates              body {name,certificate,private_key,chain?}
//	DELETE DELETE /certificate/{id}          → {success,message}
type certificateResource struct {
	client *client.Client
}

// certificateModel maps the Terraform state/plan for iaas_certificate.
//
// name/certificate/private_key/chain are all RequiresReplace: there is no
// update endpoint, so rotating a certificate means replacing the resource.
// private_key (and chain, following the iaas_lb_certificate precedent) are
// Sensitive; the API never returns certificate/private_key/chain in any
// response, so they are echoed from the plan/prior state rather than
// refreshed - true write-only fields.
//
// domain/san_domains/expires_at/fingerprint_sha256/status are Computed and
// use UseStateForUnknown: because every configurable input is
// RequiresReplace, a fresh value is only ever produced by a genuine
// create/replace, so keeping the prior known value across an unrelated plan
// is safe and avoids spurious "(known after apply)" noise.
type certificateModel struct {
	ID                types.String `tfsdk:"id"`
	Name              types.String `tfsdk:"name"`
	Certificate       types.String `tfsdk:"certificate"`
	PrivateKey        types.String `tfsdk:"private_key"`
	Chain             types.String `tfsdk:"chain"`
	Domain            types.String `tfsdk:"domain"`
	SanDomains        types.List   `tfsdk:"san_domains"`
	ExpiresAt         types.String `tfsdk:"expires_at"`
	FingerprintSha256 types.String `tfsdk:"fingerprint_sha256"`
	Status            types.String `tfsdk:"status"`
}

// Metadata sets the resource type name → "iaas_certificate".
func (r *certificateResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_certificate"
}

// Schema describes the iaas_certificate resource.
func (r *certificateResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages an account-level SSL/TLS certificate (manual PEM upload). " +
			"Unlike iaas_lb_certificate, an iaas_certificate is NOT a child of a load balancer - " +
			"it belongs to the account and can be attached to any of the account's load balancer " +
			"frontends via ssl_certificate_id. There is no update endpoint, so changing name, " +
			"certificate, private_key or chain forces the resource to be replaced (rotation). " +
			"private_key and chain are write-only and sensitive: the API never returns them, so " +
			"they are taken from configuration and never refreshed. " +
			"Let's Encrypt issuance is asynchronous (ACME) and is NOT modelled as a resource in " +
			"v1 - use the panel or the MCP server's user.certificate.request_letsencrypt tool.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the certificate, assigned by the API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required: true,
				Description: "Display name for the certificate. Immutable (no certificate update " +
					"endpoint); changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"certificate": schema.StringAttribute{
				Required: true,
				Description: "PEM-encoded certificate (\"-----BEGIN CERTIFICATE-----...\"). " +
					"Immutable; changing it forces a new resource (rotation).",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"private_key": schema.StringAttribute{
				Required:  true,
				Sensitive: true,
				Description: "PEM-encoded private key. WRITE-ONLY and SENSITIVE: it is never " +
					"returned by the API, so it is taken from configuration and never refreshed from " +
					"the server. Immutable; changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"chain": schema.StringAttribute{
				Optional:  true,
				Sensitive: true,
				Description: "Optional PEM-encoded intermediate certificate chain. WRITE-ONLY: not " +
					"returned by the API. Immutable; changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"domain": schema.StringAttribute{
				Computed: true,
				Description: "Primary common-name domain extracted from the uploaded certificate. " +
					"Server-assigned at upload time.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"san_domains": schema.ListAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "Subject Alternative Name domains covered by the certificate, in " +
					"addition to domain. Server-assigned at upload time.",
				PlanModifiers: []planmodifier.List{
					listplanmodifier.UseStateForUnknown(),
				},
			},
			"expires_at": schema.StringAttribute{
				Computed:    true,
				Description: "Certificate expiry timestamp (ISO 8601), parsed from the uploaded PEM.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"fingerprint_sha256": schema.StringAttribute{
				Computed:    true,
				Description: "SHA-256 fingerprint of the certificate, for out-of-band verification.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"status": schema.StringAttribute{
				Computed: true,
				Description: "Certificate status. One of: `active`, `pending`, `failed`, " +
					"`expiring`, `expired`.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

// Configure pulls the shared *client.Client from the provider (nil-guarded).
func (r *certificateResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create uploads the certificate. certificate/private_key are always sent;
// chain only when configured. The API never echoes the PEM bodies back, so
// they are preserved from the plan into state.
func (r *certificateResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan certificateModel
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

	obj, err := r.client.CreateCertificate(ctx, body)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating certificate", err))
		return
	}

	state, diags := certificateStateFromAPI(ctx, obj, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Read refreshes state via GetCertificate. A 404 (deleted out of band)
// removes it from state so Terraform plans a re-create. The write-only
// certificate/private_key/chain fields are preserved from prior state - the
// API never returns them.
func (r *certificateResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state certificateModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	obj, err := r.client.GetCertificate(ctx, state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("Error reading certificate", err))
		return
	}

	newState, diags := certificateStateFromAPI(ctx, obj, state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

// Update is unreachable: every input is RequiresReplace (there is no
// certificate update endpoint), so the framework recreates rather than
// updating. Implemented as a defensive re-read only to satisfy the
// resource.Resource interface and keep state consistent in the impossible
// event Update is invoked.
func (r *certificateResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan certificateModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	obj, err := r.client.GetCertificate(ctx, plan.ID.ValueString())
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading certificate", err))
		return
	}

	state, diags := certificateStateFromAPI(ctx, obj, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Delete removes the certificate. The API refuses with success:false (surfaced
// as an error by DeleteCertificate) when the certificate is still in use by a
// load balancer frontend - detach it first.
func (r *certificateResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state certificateModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteCertificate(ctx, state.ID.ValueString()); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deleting certificate", err))
		return
	}
}

// ImportState lets `terraform import iaas_certificate.x <uuid>` adopt an
// existing certificate; the next Read populates the computed attributes. The
// write-only certificate/private_key/chain fields cannot be recovered on
// import (the API never returns them) - callers must add them to
// ImportStateVerifyIgnore.
func (r *certificateResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// certificateStateFromAPI builds the model from an API certificate object.
// certificate/private_key/chain are NEVER in the response - preserved
// verbatim from the prior model (write-only). san_domains is converted via
// sanDomainsFromAPI, falling back to the prior list value when the response
// omits the field (e.g. an error mid-decode never reaches this point, but a
// partial object still degrades gracefully).
func certificateStateFromAPI(ctx context.Context, obj map[string]any, prior certificateModel) (certificateModel, diag.Diagnostics) {
	sanDomains, diags := certSanDomainsFromAPI(ctx, obj, prior.SanDomains)

	return certificateModel{
		ID:   stringFromAPI(obj, "id", prior.ID),
		Name: stringFromAPI(obj, "name", prior.Name),

		// WRITE-ONLY - never in the response; preserve the plan/state value verbatim.
		Certificate: prior.Certificate,
		PrivateKey:  prior.PrivateKey,
		Chain:       prior.Chain,

		Domain:            stringFromAPI(obj, "domain", prior.Domain),
		SanDomains:        sanDomains,
		ExpiresAt:         stringFromAPI(obj, "expires_at", prior.ExpiresAt),
		FingerprintSha256: stringFromAPI(obj, "fingerprint_sha256", prior.FingerprintSha256),
		Status:            stringFromAPI(obj, "status", prior.Status),
	}, diags
}

// certSanDomainsFromAPI converts the API "san_domains" field (a real JSON
// array, possibly empty) to a types.List(string) for iaas_certificate. Unlike
// iaas_kubernetes_ssl_certificate's sanDomainsFromAPI (which models the field
// as a comma-joined String to tolerate a legacy string-cast shape), the
// account certificate endpoint always returns a genuine JSON array, so this
// helper is a plain list conversion. A present-but-empty array becomes a
// known empty list (not null) so plan/state comparisons are stable; an
// absent key falls back to the prior value.
func certSanDomainsFromAPI(ctx context.Context, obj map[string]any, fallback types.List) (types.List, diag.Diagnostics) {
	raw, ok := obj["san_domains"]
	if !ok {
		return fallback, nil
	}
	if raw == nil {
		return types.ListValueFrom(ctx, types.StringType, []string{})
	}
	arr, ok := raw.([]any)
	if !ok {
		return fallback, nil
	}
	items := make([]string, 0, len(arr))
	for _, v := range arr {
		if s, ok := v.(string); ok {
			items = append(items, s)
		}
	}
	return types.ListValueFrom(ctx, types.StringType, items)
}
