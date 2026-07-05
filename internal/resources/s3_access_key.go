package resources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
)

// Interface assertions - iaas_s3_access_key is a SYNC resource modelling a
// standalone S3 access key.
//
// ★ SHOWN-ONCE SECRET (the central design point):
//
//   - The CREATE response is the ONLY place the secret_key is ever returned
//     ({success,message,data:{access_key,secret_key}}). The model marks
//     secret_key as $hidden, so it never appears in the LIST (the only readback
//     path - there is no SHOW route). The access_key (public) and id ARE in the
//     LIST.
//   - secret_key is therefore a **Sensitive Computed** attribute that is CAPTURED
//     from the create response and then PRESERVED in state forever
//     (UseStateForUnknown so a plan never marks it unknown; Read explicitly copies
//     the prior value because the LIST cannot supply it; ImportStateVerifyIgnore
//     because an imported key has no secret to recover).
//   - The CREATE response carries no record id, so the id is discovered by a
//     list-and-match on the just-issued access_key (C4 readback).
//
// There is NO user-API delete route for access keys (only index/store/update),
// so Delete is a state-only removal that warns the operator to delete the key in
// the panel - Terraform stops tracking it but cannot tear it down server-side.
var (
	_ resource.Resource                = &s3AccessKeyResource{}
	_ resource.ResourceWithConfigure   = &s3AccessKeyResource{}
	_ resource.ResourceWithImportState = &s3AccessKeyResource{}
)

// NewS3AccessKeyResource is the resource constructor registered with the provider.
func NewS3AccessKeyResource() resource.Resource {
	return &s3AccessKeyResource{}
}

// s3AccessKeyResource manages an iaas_s3_access_key.
//
// Route summary (verified against UserApi\S3AccessKeyController +
// S3AccessKeyService + the Store/Update FormRequests + the UserS3AccessKey model
// + routes/user_api.php):
//
//	INDEX  GET   /object-storage/access-keys           (PLURAL) paginator
//	                                                     {data:[{id,name,access_key,active}]}
//	                                                     (secret_key $hidden - never listed)
//	CREATE POST  /object-storage/access-keys           (PLURAL) body {name}
//	                                                     → {success,message,
//	                                                     data:{access_key,secret_key}}
//	                                                     ★ secret shown ONCE; NO id → C4 readback
//	UPDATE PATCH /object-storage/access-key/{id}        (SINGULAR) body {name?,active?}
//	                                                     → {success,message} (Read after)
//
// (No SHOW, no DELETE route.) All writes are synchronous; toggling active
// dispatches an async suspend/resume job. The routes are NOT billing-gated.
type s3AccessKeyResource struct {
	client *client.Client
}

// s3AccessKeyModel maps the Terraform state/plan for iaas_s3_access_key.
//
//   - name: Required, updatable in place (rename).
//   - active: Optional+Computed, defaults to true; toggled via update.
//   - access_key: Computed stable (server-issued public id).
//   - secret_key: Sensitive Computed, captured from CREATE, preserved thereafter.
type s3AccessKeyModel struct {
	ID        types.String `tfsdk:"id"`
	Name      types.String `tfsdk:"name"`
	Active    types.Bool   `tfsdk:"active"`
	AccessKey types.String `tfsdk:"access_key"`
	SecretKey types.String `tfsdk:"secret_key"`
}

// Metadata sets the resource type name → "<provider>_s3_access_key".
func (r *s3AccessKeyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_s3_access_key"
}

// Schema describes the iaas_s3_access_key resource.
func (r *s3AccessKeyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a standalone S3 access key that can be attached to buckets (via the " +
			"iaas_s3_bucket attached_keys set) with a per-bucket permission. The secret key is " +
			"returned ONLY once, at creation, and is captured into the (sensitive) secret_key " +
			"attribute and preserved in state thereafter - no read or import can recover it, so a " +
			"key imported into Terraform will have an empty secret_key. The key can be renamed and " +
			"activated/deactivated in place. The platform's user API exposes no delete endpoint for " +
			"access keys, so destroying this resource only removes it from Terraform state (a warning " +
			"is emitted); delete the key in the control panel to remove it server-side.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the access key, assigned by the API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required: true,
				Description: "Display name for the access key. Maximum 255 characters and globally " +
					"unique. Updatable in place.",
			},
			"active": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(true),
				Description: "Whether the access key is active. Defaults to true. Setting it to false " +
					"suspends the key (dispatches a suspend job); setting it back to true resumes it.",
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
			"access_key": schema.StringAttribute{
				Computed: true,
				Description: "The public access key id (e.g. ak_…), issued by the server at creation. " +
					"Stable thereafter.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"secret_key": schema.StringAttribute{
				Computed:  true,
				Sensitive: true,
				Description: "The secret key (e.g. sk_…). Returned by the API ONLY once, at creation, " +
					"and never again (it is hidden on every other endpoint). It is captured here on " +
					"create and preserved in state; a read cannot refresh it and an imported key will " +
					"have it empty. Marked sensitive so it is never shown in plan/CLI output.",
				// Captured on create, preserved forever - the LIST (the only
				// readback) never returns it, so UseStateForUnknown keeps the
				// prior value stable and a plan never re-marks it unknown.
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

// Configure pulls the shared *client.Client from the provider (nil-guarded).
func (r *s3AccessKeyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create issues the access key, CAPTURES the shown-once secret from the create
// response, then reads the key back by its public access_key (the create
// response has no id) to learn the id and the rest of the fields. If the user
// requested active=false, the key is deactivated after creation.
func (r *s3AccessKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan s3AccessKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	created, err := r.client.CreateS3AccessKey(ctx, map[string]any{"name": plan.Name.ValueString()})
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating S3 access key", err))
		return
	}

	// CAPTURE the shown-once secret + the public access key from the create
	// response. This is the only opportunity to read the secret.
	accessKey, _ := created["access_key"].(string)
	secretKey, _ := created["secret_key"].(string)
	if accessKey == "" {
		resp.Diagnostics.AddError("Error creating S3 access key", "the create response did not include an access_key")
		return
	}

	// C4 readback: the create response has no record id - find the key by its
	// just-issued public access_key to learn the id (and confirm name/active).
	obj, err := r.client.GetS3AccessKeyByAccessKey(ctx, accessKey)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading S3 access key after creation", err))
		return
	}
	id, _ := obj["id"].(string)
	if id == "" {
		resp.Diagnostics.AddError("Error creating S3 access key", "the created access key could not be located")
		return
	}

	// Persist the id + captured secret immediately so a partial failure still
	// leaves a tracked resource with its (unrecoverable) secret.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("secret_key"), secretKey)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// The server creates keys active=1. If the user wants it inactive, toggle now.
	wantActive := true
	if !plan.Active.IsNull() && !plan.Active.IsUnknown() {
		wantActive = plan.Active.ValueBool()
	}
	if !wantActive {
		if err := r.client.UpdateS3AccessKey(ctx, id, map[string]any{"active": false}); err != nil {
			resp.Diagnostics.Append(diagFromErr("Error deactivating S3 access key", err))
			return
		}
		// The listing won't reflect the just-dispatched suspend immediately, so
		// trust the plan for the active flag below.
	}

	// Read back to hydrate name/active/access_key; re-apply the captured secret.
	obj, err = r.client.GetS3AccessKey(ctx, id)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading S3 access key after creation", err))
		return
	}
	state := s3AccessKeyStateFromAPI(obj, plan)
	state.ID = types.StringValue(id)
	state.SecretKey = types.StringValue(secretKey)
	// active is a fire-and-forget async toggle; trust the plan value so create
	// does not churn on the not-yet-applied suspend.
	state.Active = types.BoolValue(wantActive)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Read refreshes state from the API (list-and-match by id - there is no SHOW
// route). A 404 (id absent from the listing) means the key was deleted out of
// band - remove it from state. The shown-once secret_key is NOT returned by the
// listing, so it is PRESERVED from prior state (never overwritten).
func (r *s3AccessKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state s3AccessKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	obj, err := r.client.GetS3AccessKey(ctx, state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("Error reading S3 access key", err))
		return
	}

	newState := s3AccessKeyStateFromAPI(obj, state)
	// PRESERVE the shown-once secret - the listing never returns it.
	newState.SecretKey = state.SecretKey
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

// Update renames and/or toggles the active flag in place. The PATCH response
// carries no key body, so it reads back afterwards. The secret_key is preserved
// (never returned by the listing).
func (r *s3AccessKeyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state s3AccessKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()

	fields := map[string]any{}
	if !plan.Name.Equal(state.Name) {
		fields["name"] = plan.Name.ValueString()
	}
	wantActive := plan.Active.ValueBool()
	activeChanged := !plan.Active.Equal(state.Active)
	if activeChanged {
		fields["active"] = wantActive
	}
	if len(fields) > 0 {
		if err := r.client.UpdateS3AccessKey(ctx, id, fields); err != nil {
			resp.Diagnostics.Append(diagFromErr("Error updating S3 access key", err))
			return
		}
	}

	obj, err := r.client.GetS3AccessKey(ctx, id)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading S3 access key after update", err))
		return
	}
	newState := s3AccessKeyStateFromAPI(obj, plan)
	newState.ID = types.StringValue(id)
	// PRESERVE the shown-once secret.
	newState.SecretKey = state.SecretKey
	// active is a fire-and-forget async toggle the listing may not reflect yet -
	// trust the plan so update converges without churn.
	if activeChanged {
		newState.Active = types.BoolValue(wantActive)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

// Delete removes the access key from Terraform state only. The user API exposes
// no delete endpoint for access keys, so the key continues to exist server-side
// until removed in the control panel; a warning makes this explicit.
func (r *s3AccessKeyResource) Delete(_ context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.AddWarning(
		"S3 access key not deleted server-side",
		"The platform's user API does not expose a delete endpoint for S3 access keys, so this "+
			"key has only been removed from Terraform state and still exists on the server. Delete "+
			"it from the control panel to remove it completely.",
	)
}

// ImportState lets `terraform import iaas_s3_access_key.x <uuid>` adopt an
// existing key; the next Read hydrates name/active/access_key. The shown-once
// secret_key cannot be recovered, so it is added to the lifecycle test's
// ImportStateVerifyIgnore and will be empty for an imported key.
func (r *s3AccessKeyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// s3AccessKeyStateFromAPI builds the model from a LIST access-key object,
// preserving prior values for fields the listing omits. secret_key is NEVER in
// the listing, so it is left to the caller to set/preserve.
func s3AccessKeyStateFromAPI(obj map[string]any, prior s3AccessKeyModel) s3AccessKeyModel {
	return s3AccessKeyModel{
		ID:        stringFromAPI(obj, "id", prior.ID),
		Name:      stringFromAPI(obj, "name", prior.Name),
		Active:    boolFromIntAPI(obj, "active", prior.Active),
		AccessKey: computedStringFromAPI(obj, "access_key", prior.AccessKey),
		// secret_key intentionally preserved by the caller (shown-once).
		SecretKey: prior.SecretKey,
	}
}
