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

// Interface assertions - ssh_key is the golden resource and implements the
// full set of optional resource behaviours later resources also implement.
var (
	_ resource.Resource                = &sshKeyResource{}
	_ resource.ResourceWithConfigure   = &sshKeyResource{}
	_ resource.ResourceWithImportState = &sshKeyResource{}
)

// NewSSHKeyResource is the resource constructor registered with the provider.
func NewSSHKeyResource() resource.Resource {
	return &sshKeyResource{}
}

// sshKeyResource manages an iaas_ssh_key.
type sshKeyResource struct {
	client *client.Client
}

// sshKeyModel maps the Terraform state/plan for iaas_ssh_key.
//
// Fingerprint and Comments are server-computed: the API derives the fingerprint
// from the key and derives the comment from the key's natural trailing comment
// (and a controller bug stores "" for any client-supplied comment on create),
// so both are modelled Computed and never sent on create.
type sshKeyModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	PublicKey   types.String `tfsdk:"public_key"`
	Fingerprint types.String `tfsdk:"fingerprint"`
	Comments    types.String `tfsdk:"comments"`
}

// Metadata sets the resource type name → "<provider>_ssh_key" → "iaas_ssh_key".
func (r *sshKeyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ssh_key"
}

// Schema describes the iaas_ssh_key resource.
func (r *sshKeyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages an SSH public key in your IaaS account. SSH keys can be " +
			"injected into instances at provision time.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the SSH key, assigned by the API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required: true,
				Description: "Display name for the SSH key. Max 32 characters; only lowercase " +
					"letters, digits, spaces, dots, and hyphens are allowed (case-insensitive). " +
					"Validated server-side.",
			},
			"public_key": schema.StringAttribute{
				Required: true,
				Description: "The SSH public key material (e.g. an ssh-ed25519 or ssh-rsa line). " +
					"Cannot be changed after creation; modifying it forces replacement.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"fingerprint": schema.StringAttribute{
				Computed:    true,
				Description: "Fingerprint of the public key, computed by the API.",
				// UseStateForUnknown is safe here because this computed value is
				// stable after create. Only apply UseStateForUnknown to computed
				// fields that the server does NOT mutate post-create; for
				// server-mutable computed fields, omit it so the plan shows the
				// refreshed value (otherwise drift is masked).
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"comments": schema.StringAttribute{
				Computed: true,
				Description: "Comment associated with the key, derived by the API from the " +
					"key's natural trailing comment. Server-managed.",
				// UseStateForUnknown is safe here because this computed value is
				// stable after create. Only apply UseStateForUnknown to computed
				// fields that the server does NOT mutate post-create; for
				// server-mutable computed fields, omit it so the plan shows the
				// refreshed value (otherwise drift is masked).
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
func (r *sshKeyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create provisions the SSH key. Only name + public_key are sent; the API
// returns id, fingerprint, and the derived comments, which we persist.
func (r *sshKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan sshKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	obj, err := r.client.CreateSSHKey(ctx, plan.Name.ValueString(), plan.PublicKey.ValueString())
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating SSH key", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, sshKeyStateFromAPI(obj, plan))...)
}

// Read refreshes state from the API. A 404 means the key was deleted out of
// band - remove it from state so Terraform plans a recreate (drift handling).
func (r *sshKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state sshKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	obj, err := r.client.GetSSHKey(ctx, state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("Error reading SSH key", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, sshKeyStateFromAPI(obj, state))...)
}

// Update changes mutable fields. Only name is user-settable (public_key forces
// replacement; comments is computed), so only name is sent.
//
// This relies on the UPDATE (PATCH) response being a FULL resource object
// (same shape as SHOW), which lets us rehydrate all computed fields from it.
// When copying this template: if a resource's UPDATE response is partial/thinner
// than SHOW, call Read after a successful update instead of mapping the PATCH
// response directly - otherwise computed fields get dropped from state.
func (r *sshKeyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan sshKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	fields := map[string]any{
		"name": plan.Name.ValueString(),
	}

	obj, err := r.client.UpdateSSHKey(ctx, plan.ID.ValueString(), fields)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error updating SSH key", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, sshKeyStateFromAPI(obj, plan))...)
}

// Delete removes the SSH key.
func (r *sshKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state sshKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteSSHKey(ctx, state.ID.ValueString()); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deleting SSH key", err))
		return
	}
}

// ImportState lets `terraform import iaas_ssh_key.x <uuid>` adopt an existing
// key; the next Read populates the rest of the attributes.
func (r *sshKeyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// sshKeyStateFromAPI builds the model from an API ssh_key object, falling back
// to the prior model's value for any field the response omits (e.g. public_key,
// which SHOW returns but which we keep stable regardless).
func sshKeyStateFromAPI(obj map[string]any, prior sshKeyModel) sshKeyModel {
	m := sshKeyModel{
		ID:          stringFromAPI(obj, "id", prior.ID),
		Name:        stringFromAPI(obj, "name", prior.Name),
		PublicKey:   stringFromAPI(obj, "public_key", prior.PublicKey),
		Fingerprint: stringFromAPI(obj, "fingerprint", prior.Fingerprint),
		Comments:    stringFromAPI(obj, "comments", prior.Comments),
	}
	return m
}

// stringFromAPI reads a string field from an API object map. A present string
// (including "") wins; a present null collapses to "" (the field is non-null in
// state once known); an absent key falls back to the prior value.
func stringFromAPI(obj map[string]any, key string, fallback types.String) types.String {
	raw, ok := obj[key]
	if !ok {
		return fallback
	}
	if raw == nil {
		return types.StringValue("")
	}
	if s, ok := raw.(string); ok {
		return types.StringValue(s)
	}
	// Non-string JSON value (number/bool) - coerce defensively.
	return types.StringValue(fmt.Sprintf("%v", raw))
}
