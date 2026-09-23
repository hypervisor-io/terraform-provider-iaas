package resources

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
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
// name/certificate/private_key/chain update IN PLACE via PUT /certificate/{id}
// (NUI-V-R17-ACM-ROTATE1/2) - rotating a manually-uploaded certificate no
// longer destroys and recreates the resource (unlike iaas_lb_certificate /
// iaas_kubernetes_ssl_certificate, which are still RequiresReplace-only).
// Keeping the id stable across a rotation matters because it is referenced by
// iaas_lb_frontend's certificate_ids/ssl_certificate_id. A certificate that
// was issued via Let's Encrypt cannot be replaced this way (it renews
// automatically) - Update maps that 422 to a dedicated diagnostic.
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
//	UPDATE PUT    /certificate/{id}         body {certificate,private_key,chain?,name?}
//	                                          → {success,certificate:{...},resync:[...]}
//	DELETE DELETE /certificate/{id}          → {success,message}
type certificateResource struct {
	client *client.Client
}

// certificateModel maps the Terraform state/plan for iaas_certificate.
//
// name/certificate/private_key/chain are all plain Optional/Required
// attributes with NO RequiresReplace plan modifier: Update() calls
// PUT /certificate/{id} to rotate the material in place, keeping the id
// stable. private_key (and chain, following the iaas_lb_certificate
// precedent) are Sensitive; the API never returns certificate/private_key/chain
// in any response (create OR update), so they are echoed from the
// plan/prior state rather than refreshed - true write-only fields.
//
// domain/san_domains/expires_at/fingerprint_sha256/status are Computed but
// deliberately have NO UseStateForUnknown plan modifier (same reasoning as
// dns_record.go's is_healthy): they are server-mutable via Update - rotating
// the certificate material genuinely changes them - so the plan must show
// them as unknown ("(known after apply)") rather than asserting the prior
// value will still hold. Only id keeps UseStateForUnknown; it never changes.
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
			"frontends via ssl_certificate_id. Changing name, certificate, private_key or chain " +
			"updates the certificate material IN PLACE (PUT /certificate/{id}) rather than " +
			"replacing the resource, so its id - and every frontend's reference to it - is " +
			"preserved. private_key and chain are write-only and sensitive: the API never returns " +
			"them (on create OR update), so they are taken from configuration and never refreshed. " +
			"A certificate issued via Let's Encrypt cannot be updated this way (it renews " +
			"automatically); attempting to change one fails with a clear error. " +
			"Let's Encrypt issuance is asynchronous (ACME) and is NOT modelled as a resource in " +
			"v1 - use the panel or the MCP server's user.certificate.request_letsencrypt tool.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
				Description: "UUID of the certificate, assigned by the API. Stable across an update " +
					"(rotation does not change it).",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required: true,
				Description: "Display name for the certificate. Updating it sends a PUT to rename/rotate " +
					"in place - it does not force a new resource.",
			},
			"certificate": schema.StringAttribute{
				Required: true,
				Description: "PEM-encoded certificate (\"-----BEGIN CERTIFICATE-----...\"). " +
					"Updating it rotates the certificate material in place via PUT /certificate/{id} - " +
					"it does not force a new resource. Rejected with a clear error for a Let's " +
					"Encrypt-issued certificate.",
			},
			"private_key": schema.StringAttribute{
				Required:  true,
				Sensitive: true,
				Description: "PEM-encoded private key. WRITE-ONLY and SENSITIVE: it is never " +
					"returned by the API, so it is taken from configuration and never refreshed from " +
					"the server. Updating it rotates the certificate material in place - it does not " +
					"force a new resource.",
			},
			"chain": schema.StringAttribute{
				Optional:  true,
				Sensitive: true,
				Description: "Optional PEM-encoded intermediate certificate chain. WRITE-ONLY: not " +
					"returned by the API. Updating it rotates the certificate material in place - it " +
					"does not force a new resource.",
			},
			"domain": schema.StringAttribute{
				Computed: true,
				Description: "Primary common-name domain extracted from the uploaded certificate. " +
					"Server-computed; re-derived whenever the certificate material is updated.",
			},
			"san_domains": schema.ListAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "Subject Alternative Name domains covered by the certificate, in " +
					"addition to domain. Server-computed; re-derived whenever the certificate material " +
					"is updated.",
			},
			"expires_at": schema.StringAttribute{
				Computed: true,
				Description: "Certificate expiry timestamp (ISO 8601), parsed from the uploaded PEM. " +
					"Server-computed; re-derived whenever the certificate material is updated.",
			},
			"fingerprint_sha256": schema.StringAttribute{
				Computed: true,
				Description: "SHA-256 fingerprint of the certificate, for out-of-band verification. " +
					"Server-computed; re-derived whenever the certificate material is updated.",
			},
			"status": schema.StringAttribute{
				Computed: true,
				Description: "Certificate status. One of: `active`, `pending`, `failed`, " +
					"`expiring`, `expired`. Server-computed; may change on update.",
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

// Update rotates the certificate material in place via
// PUT /certificate/{id} (NUI-V-R17-ACM-ROTATE1/2): certificate, private_key
// and chain are always sent (chain omitted when unset, mirroring Create);
// name is always sent too, so a plain rename goes through the same call.
// The id never changes.
//
// A Let's Encrypt-issued certificate rejects with a 422 whose body is plain
// {success:false,message} - there is NO machine-readable "code" field on
// this endpoint (verified against the landed Master implementation,
// 5167436db), so the dedicated diagnostic below matches a case-insensitive
// "let's encrypt" substring of apiErr.Message rather than a stable code.
// This is mapped to a dedicated, actionable diagnostic instead of the
// generic error passthrough, since a bare "422: ..." message would not tell
// the operator that Let's Encrypt certificates renew themselves and cannot
// be manually rotated. Every OTHER 422 (key/certificate mismatch, expired
// certificate, mismatched chain) falls through to the generic passthrough,
// which already surfaces the server's own clear message text.
//
// The response's "resync" list reports, per load balancer that references
// this certificate, whether the server-side config re-sync succeeded. The
// certificate itself is already updated at this point (server-side, in one
// transaction) regardless of resync outcome, so a failed entry is surfaced
// as a warning - never an error - matching the server's own best-effort
// contract (the load balancer's reconciler catches up later).
func (r *certificateResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
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

	obj, resync, err := r.client.UpdateCertificate(ctx, plan.ID.ValueString(), body)
	if err != nil {
		var apiErr *client.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusUnprocessableEntity &&
			strings.Contains(strings.ToLower(apiErr.Message), "let's encrypt") {
			// The endpoint has NO machine-readable "code" field (confirmed
			// against the landed Master implementation, 5167436db) - every
			// 422 reason, including this one, is message-only. Matching on a
			// case-insensitive substring of the server's own wording is the
			// only available discriminator; CertificateService::replace()
			// throws "Only a manually uploaded certificate can be replaced —
			// Let's Encrypt certificates renew automatically." for this case.
			resp.Diagnostics.AddError(
				"Cannot Replace a Let's Encrypt Certificate",
				"This certificate was issued via Let's Encrypt and renews automatically - it cannot be "+
					"manually replaced. Remove it from this resource's configuration (and, if needed, "+
					"`terraform state rm` it) rather than changing certificate, private_key, chain or "+
					"name; upload a new manual certificate as a separate iaas_certificate resource instead."+
					"\n\nServer message: "+apiErr.Message,
			)
			return
		}
		// Every other 422 (key/certificate mismatch, expired certificate,
		// chain that doesn't match the leaf) has no dedicated mapping - the
		// server's own message is already a clear, specific sentence, so the
		// generic passthrough surfaces it verbatim.
		resp.Diagnostics.Append(diagFromErr("Error updating certificate", err))
		return
	}

	for _, entry := range resync {
		ok, _ := entry["success"].(bool)
		if ok {
			continue
		}
		lbName, _ := entry["name"].(string)
		lbErr, _ := entry["error"].(string)
		resp.Diagnostics.AddWarning(
			"Load Balancer Re-sync Failed",
			fmt.Sprintf("The certificate was updated, but re-syncing load balancer %q with the new "+
				"material failed: %s. The load balancer's own reconciler will retry; no action is "+
				"required unless this persists.", lbName, lbErr),
		)
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
