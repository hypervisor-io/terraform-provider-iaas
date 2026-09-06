package resources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
)

// Interface assertions. iaas_microvm_api_key manages a MicroVM Sandboxes API
// key (the X-API-Key credential for the E2B-compatible microvm surface). There
// is NO rename/rotate endpoint - name is RequiresReplace and Update is never
// called.
//
// ★ SHOWN-ONCE SECRET (the central design point, same shape as
// iaas_s3_access_key's secret_key): the CREATE response is the ONLY place the
// plaintext key is ever returned ({success,key:{id,name,prefix},plaintext}).
// The LIST readback carries only id/name/prefix. key is therefore a Sensitive
// Computed attribute CAPTURED from the create response and then PRESERVED in
// state forever (UseStateForUnknown so a plan never re-marks it unknown; Read
// never touches it). There is deliberately no ImportState: the plaintext
// cannot be recovered after creation, so an imported resource would be
// missing its key material.
var (
	_ resource.Resource              = &microvmApiKeyResource{}
	_ resource.ResourceWithConfigure = &microvmApiKeyResource{}
)

// NewMicrovmApiKeyResource is the resource constructor registered with the provider.
func NewMicrovmApiKeyResource() resource.Resource {
	return &microvmApiKeyResource{}
}

// microvmApiKeyResource manages an iaas_microvm_api_key.
//
// Route summary (MV1-26 contract):
//
//	INDEX  GET    /microvm/api-keys                     -> {success,keys:[...]}
//	CREATE POST   /microvm/api-keys        body {name}  -> {success,key:{...},plaintext}
//	DELETE DELETE /microvm/api-key/{id}                 -> {success,message}
type microvmApiKeyResource struct {
	client *client.Client
}

// microvmApiKeyModel maps the Terraform state/plan for iaas_microvm_api_key.
//
// name is RequiresReplace (no rename endpoint). id/prefix are Computed with
// UseStateForUnknown. key is Sensitive + Computed + UseStateForUnknown: it is
// populated ONLY on Create from the shown-once plaintext and never refreshed
// (no endpoint returns it again).
type microvmApiKeyModel struct {
	ID     types.String `tfsdk:"id"`
	Name   types.String `tfsdk:"name"`
	Prefix types.String `tfsdk:"prefix"`
	Key    types.String `tfsdk:"key"`
}

// Metadata sets the resource type name -> "iaas_microvm_api_key".
func (r *microvmApiKeyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_microvm_api_key"
}

// Schema describes the iaas_microvm_api_key resource.
func (r *microvmApiKeyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a MicroVM Sandboxes API key (X-API-Key for the E2B-compatible surface). " +
			"The plaintext key is only ever returned at create time; there is no rotate/rename endpoint, " +
			"so every field is RequiresReplace and changing the name destroys and recreates the key. " +
			"Import is not supported: the plaintext key cannot be recovered after creation.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the API key, assigned by the API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required: true,
				Description: "Display name for the API key. Immutable (no rename endpoint); " +
					"changing it forces a new resource (a fresh key).",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"prefix": schema.StringAttribute{
				Computed: true,
				Description: "The non-secret key prefix (e.g. vc_sb_ab12), safe to display; " +
					"identifies the key in listings. Server-assigned at creation.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"key": schema.StringAttribute{
				Computed:  true,
				Sensitive: true,
				Description: "The plaintext API key. Returned by the API ONLY once, at creation, " +
					"and never again; it is captured here on create and preserved in state - a " +
					"read cannot refresh it. Marked sensitive so it is never shown in plan/CLI output.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

// Configure pulls the shared *client.Client from the provider (nil-guarded).
func (r *microvmApiKeyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create issues the API key and CAPTURES the shown-once plaintext from the
// create response into the sensitive key attribute - this is the only
// opportunity to ever read it. id/prefix come from the same response's key
// object, so no post-create readback is needed.
func (r *microvmApiKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan microvmApiKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	result, err := r.client.CreateMicrovmApiKey(ctx, plan.Name.ValueString())
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating microvm api key", err))
		return
	}

	keyObj, _ := result["key"].(map[string]any)
	id, _ := keyObj["id"].(string)
	prefix, _ := keyObj["prefix"].(string)
	plaintext, _ := result["plaintext"].(string)
	if id == "" || plaintext == "" {
		resp.Diagnostics.AddError(
			"Error creating microvm api key",
			"the create response did not include the key id and the shown-once plaintext key",
		)
		return
	}

	plan.ID = types.StringValue(id)
	plan.Prefix = types.StringValue(prefix)
	plan.Key = types.StringValue(plaintext)

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes name/prefix via the list endpoint (there is no SHOW route).
// An id absent from the listing means the key was deleted out of band -
// remove it from state. The plaintext key is NOT in the listing, so the
// captured key attribute is preserved from prior state and never overwritten.
func (r *microvmApiKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state microvmApiKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	keys, err := r.client.ListMicrovmApiKeys(ctx)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading microvm api keys", err))
		return
	}

	found := false
	for _, k := range keys {
		if k["id"] == state.ID.ValueString() {
			found = true
			if name, ok := k["name"].(string); ok {
				state.Name = types.StringValue(name)
			}
			if prefix, ok := k["prefix"].(string); ok {
				state.Prefix = types.StringValue(prefix)
			}
			break
		}
	}
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is unreachable: name is RequiresReplace (there is no rename/rotate
// endpoint), so the framework recreates rather than updating. It exists only
// to satisfy the resource.Resource interface and fails loudly if ever invoked.
func (r *microvmApiKeyResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError(
		"Update not supported",
		"iaas_microvm_api_key has no update path; every attribute forces replacement.",
	)
}

// Delete revokes the API key server-side.
func (r *microvmApiKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state microvmApiKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteMicrovmApiKey(ctx, state.ID.ValueString()); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deleting microvm api key", err))
		return
	}
}
