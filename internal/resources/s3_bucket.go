package resources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hypervisor-io/terraform-provider-iaas/internal/client"
)

// Interface assertions - iaas_s3_bucket is a SYNC resource. It models:
//
//   - an immutable bucket (name / s3_plan_id / s3_server_id → RequiresReplace,
//     because the user API has no rename / re-plan / migrate endpoint);
//   - an in-place ACL (default_access, set via the dedicated ACL PATCH endpoint);
//   - the bucket's OWN auto-generated access_key/secret_key (returned on every
//     SHOW - unlike a standalone access key, these are NOT shown-once, so the
//     secret is a plain Sensitive Computed re-read each time, no capture trick);
//   - a managed set of attached standalone access keys, each with a permission,
//     diffed via the per-key attach/update/detach endpoints. The permission is
//     UPDATABLE in place (PATCH .../update/{kid}), so a permission change is an
//     in-place update - NOT a delete+add (this is the key difference from the
//     security_group rule set-diff, which has no rule-update endpoint).
var (
	_ resource.Resource                = &s3BucketResource{}
	_ resource.ResourceWithConfigure   = &s3BucketResource{}
	_ resource.ResourceWithImportState = &s3BucketResource{}
)

// NewS3BucketResource is the resource constructor registered with the provider.
func NewS3BucketResource() resource.Resource {
	return &s3BucketResource{}
}

// s3BucketResource manages an iaas_s3_bucket.
//
// Route summary (verified against UserApi\S3BucketController + S3BucketService +
// the Store FormRequest + the S3Bucket model + routes/user_api.php):
//
//	INDEX   GET    /object-storage/buckets                    (PLURAL) paginator
//	CREATE  POST   /object-storage/buckets                    (PLURAL) body
//	                                                            {name,s3_plan_id,s3_server_id}
//	                                                            → {success,message} (NO id → C4)
//	SHOW    GET    /object-storage/bucket/{id}                (SINGULAR) → {bucket:{...},
//	                                                            endpoint,access_key,secret_key}
//	ACL     PATCH  /object-storage/bucket/{id}/acl/{action}   action ∈ public|private|upload|download
//	DELETE  DELETE /object-storage/bucket/{id}                (SINGULAR)
//	KEYS    GET    /object-storage/bucket/{id}/keys           attached keys (+pivot.permission)
//	ATTACH  POST   /object-storage/bucket/{id}/attach/{kid}   body {permission}
//	UPDATEK PATCH  /object-storage/bucket/{id}/update/{kid}   body {permission}  (in-place)
//	DETACH  POST   /object-storage/bucket/{id}/detach/{kid}
//
// All operations are synchronous (no task/waiter). The S3 routes are NOT
// billing-gated, so there is no 403 billing path here.
type s3BucketResource struct {
	client *client.Client
}

// s3BucketModel maps the Terraform state/plan for iaas_s3_bucket.
//
// Replace inputs: name, s3_plan_id, s3_server_id.
// In-place: default_access (ACL), attached_keys (set-diff with per-key permission update).
// Computed server-managed: access_key (stable), secret_key (stable, sensitive),
// endpoint (stable), suspended (mutable), quota/bandwidth (plan-derived, stable).
type s3BucketModel struct {
	ID            types.String `tfsdk:"id"`
	Name          types.String `tfsdk:"name"`
	S3PlanID      types.String `tfsdk:"s3_plan_id"`
	S3ServerID    types.String `tfsdk:"s3_server_id"`
	DefaultAccess types.String `tfsdk:"default_access"`
	AttachedKeys  types.Set    `tfsdk:"attached_keys"`

	// Computed read-only.
	AccessKey types.String `tfsdk:"access_key"`
	SecretKey types.String `tfsdk:"secret_key"`
	Endpoint  types.String `tfsdk:"endpoint"`
	Suspended types.Bool   `tfsdk:"suspended"`
	Quota     types.Int64  `tfsdk:"quota"`
	Bandwidth types.Int64  `tfsdk:"bandwidth"`
}

// s3BucketKeyModel maps a single attached-key object inside the attached_keys
// set. access_key_id is the standalone access key's UUID (the diff key);
// permission is read|write|readwrite and is updatable in place.
type s3BucketKeyModel struct {
	AccessKeyID types.String `tfsdk:"access_key_id"`
	Permission  types.String `tfsdk:"permission"`
}

// s3BucketKeyAttrTypes is the single source of truth for the attached-key object
// shape, reused everywhere a types.Set of attached keys is (re)built.
var s3BucketKeyAttrTypes = map[string]attr.Type{
	"access_key_id": types.StringType,
	"permission":    types.StringType,
}

// Metadata sets the resource type name → "<provider>_s3_bucket".
func (r *s3BucketResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_s3_bucket"
}

// Schema describes the iaas_s3_bucket resource.
func (r *s3BucketResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages an S3-compatible object storage bucket. The bucket name, plan and " +
			"server are fixed at creation (changing any forces a new resource). The default access " +
			"control (ACL) is set in place. A dedicated access_key/secret_key pair is generated for " +
			"the bucket at creation and returned on every read (the bucket secret is re-readable, " +
			"unlike a standalone access key whose secret is shown only once). Standalone access keys " +
			"can be attached to the bucket with a permission via the attached_keys set; the permission " +
			"is changed in place. Object-storage operations are not billing-gated at the route level, " +
			"but a Cloud Service billing record is created for the bucket.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the bucket, assigned by the API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required: true,
				Description: "Bucket name. Must be 3-63 characters, lowercase letters, digits, dots " +
					"or hyphens, starting and ending with a letter or digit, and globally unique. " +
					"Immutable; changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"s3_plan_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the S3 plan (quota / bandwidth sizing). Must be an enabled plan. " +
					"Immutable; changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"s3_server_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the S3 server (storage location) the bucket lives on. Must be an " +
					"enabled server. Immutable; changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"default_access": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "Default access control for the bucket: \"private\" (default), \"public\", " +
					"\"upload\", or \"download\". Set in place via the ACL endpoint.",
				PlanModifiers: []planmodifier.String{
					// Server defaults this to "private" when omitted; keep the
					// settled value stable across plans.
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"attached_keys": schema.SetNestedAttribute{
				Optional: true,
				Description: "Standalone access keys attached to this bucket, as an order-independent " +
					"set. Each entry grants the keyed access key a permission on the bucket. Adding or " +
					"removing an entry attaches or detaches the key; changing only the permission of an " +
					"existing entry updates it in place.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"access_key_id": schema.StringAttribute{
							Required: true,
							Description: "UUID of the standalone access key (iaas_s3_access_key) to " +
								"attach to this bucket.",
						},
						"permission": schema.StringAttribute{
							Required: true,
							Description: "Permission granted to this access key on the bucket: " +
								"\"read\", \"write\", or \"readwrite\". Updatable in place.",
						},
					},
				},
			},
			"access_key": schema.StringAttribute{
				Computed: true,
				Description: "The bucket's own auto-generated access key id. Assigned at creation and " +
					"stable thereafter.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"secret_key": schema.StringAttribute{
				Computed:  true,
				Sensitive: true,
				Description: "The bucket's own auto-generated secret key. Returned on every read (the " +
					"bucket SHOW endpoint re-exposes it), so it stays populated. Marked sensitive.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"endpoint": schema.StringAttribute{
				Computed:    true,
				Description: "S3 endpoint URL of the bucket's server. Server-derived; stable after create.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"suspended": schema.BoolAttribute{
				Computed: true,
				Description: "Whether the bucket is suspended (e.g. for exceeding its bandwidth quota). " +
					"Server-managed; reflected on every read.",
				// Server-MUTABLE: no UseStateForUnknown, so out-of-band suspension
				// is surfaced as drift rather than masked.
			},
			"quota": schema.Int64Attribute{
				Computed:    true,
				Description: "Storage quota in bytes, derived from the plan. Stable after create.",
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"bandwidth": schema.Int64Attribute{
				Computed:    true,
				Description: "Monthly bandwidth allowance in bytes, derived from the plan. Stable after create.",
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

// Configure pulls the shared *client.Client from the provider (nil-guarded).
func (r *s3BucketResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create provisions the bucket, then (because the create response carries no id)
// reads it back by its unique name to discover the id and hydrate computed
// fields, then applies the optional ACL and attaches the configured keys.
func (r *s3BucketResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan s3BucketModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := map[string]any{
		"name":         plan.Name.ValueString(),
		"s3_plan_id":   plan.S3PlanID.ValueString(),
		"s3_server_id": plan.S3ServerID.ValueString(),
	}
	if _, err := r.client.CreateS3Bucket(ctx, body); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating S3 bucket", err))
		return
	}

	// C4 readback: the create response has no id - find the bucket by its unique
	// name to learn the id.
	created, err := r.client.GetS3BucketByName(ctx, plan.Name.ValueString())
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading S3 bucket after creation", err))
		return
	}
	id, _ := created["id"].(string)
	if id == "" {
		resp.Diagnostics.AddError("Error creating S3 bucket", "the created bucket could not be located by name")
		return
	}

	// Persist the id immediately so a partial failure still leaves a destroyable
	// resource.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Apply the ACL if the user requested a non-default access control. The
	// server default is "private", so skip the no-op call when the plan asks
	// for "private" - it avoids a redundant ACL PATCH at create time.
	if !plan.DefaultAccess.IsNull() && !plan.DefaultAccess.IsUnknown() &&
		plan.DefaultAccess.ValueString() != "" && plan.DefaultAccess.ValueString() != "private" {
		if err := r.client.SetS3BucketACL(ctx, id, plan.DefaultAccess.ValueString()); err != nil {
			resp.Diagnostics.Append(diagFromErr("Error setting S3 bucket ACL", err))
			return
		}
	}

	// Attach the configured keys.
	plannedKeys, diags := bucketKeysFromSet(ctx, plan.AttachedKeys)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	for _, k := range plannedKeys {
		if err := r.client.AttachS3BucketKey(ctx, id, k.AccessKeyID.ValueString(), k.Permission.ValueString()); err != nil {
			resp.Diagnostics.Append(diagFromErr("Error attaching access key to S3 bucket", err))
			return
		}
	}

	// Read back so state carries the authoritative computed fields + attachments.
	state, notFound, diags := r.readState(ctx, id, plan)
	resp.Diagnostics.Append(diags...)
	if notFound {
		resp.Diagnostics.AddError("Error creating S3 bucket", "the bucket disappeared immediately after creation")
		return
	}
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Read refreshes state from the API. A 404 means the bucket was deleted out of
// band - remove it from state so Terraform plans a recreate.
func (r *s3BucketResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state s3BucketModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	newState, notFound, diags := r.readState(ctx, state.ID.ValueString(), state)
	if notFound {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

// Update applies the in-place changes: the ACL (default_access) and the
// attached-keys set-diff. name/s3_plan_id/s3_server_id are RequiresReplace, so
// the framework recreates the resource for those.
//
// The attached-keys diff keys by access_key_id ALONE (not the whole entry),
// because the permission is updatable in place: an entry present in both plan
// and state whose permission changed is a permission UPDATE; an entry only in
// the plan is an attach; an entry only in state is a detach.
func (r *s3BucketResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state s3BucketModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()

	// ACL change.
	if !plan.DefaultAccess.Equal(state.DefaultAccess) &&
		!plan.DefaultAccess.IsNull() && !plan.DefaultAccess.IsUnknown() && plan.DefaultAccess.ValueString() != "" {
		if err := r.client.SetS3BucketACL(ctx, id, plan.DefaultAccess.ValueString()); err != nil {
			resp.Diagnostics.Append(diagFromErr("Error setting S3 bucket ACL", err))
			return
		}
	}

	// Diff the attached-keys set.
	plannedKeys, diags := bucketKeysFromSet(ctx, plan.AttachedKeys)
	resp.Diagnostics.Append(diags...)
	stateKeys, diags := bucketKeysFromSet(ctx, state.AttachedKeys)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	plannedByID := make(map[string]s3BucketKeyModel, len(plannedKeys))
	for _, k := range plannedKeys {
		plannedByID[k.AccessKeyID.ValueString()] = k
	}
	stateByID := make(map[string]s3BucketKeyModel, len(stateKeys))
	for _, k := range stateKeys {
		stateByID[k.AccessKeyID.ValueString()] = k
	}

	// Detach keys removed from the plan.
	for keyID := range stateByID {
		if _, keep := plannedByID[keyID]; keep {
			continue
		}
		if err := r.client.DetachS3BucketKey(ctx, id, keyID); err != nil {
			resp.Diagnostics.Append(diagFromErr("Error detaching access key from S3 bucket", err))
			return
		}
	}

	// Attach new keys; update permission in place where it changed.
	for keyID, pk := range plannedByID {
		sk, exists := stateByID[keyID]
		if !exists {
			if err := r.client.AttachS3BucketKey(ctx, id, keyID, pk.Permission.ValueString()); err != nil {
				resp.Diagnostics.Append(diagFromErr("Error attaching access key to S3 bucket", err))
				return
			}
			continue
		}
		if !pk.Permission.Equal(sk.Permission) {
			if err := r.client.UpdateS3BucketKey(ctx, id, keyID, pk.Permission.ValueString()); err != nil {
				resp.Diagnostics.Append(diagFromErr("Error updating access key permission on S3 bucket", err))
				return
			}
		}
	}

	// Read back so state reflects the current attachments + ACL.
	newState, notFound, diags := r.readState(ctx, id, plan)
	resp.Diagnostics.Append(diags...)
	if notFound {
		resp.Diagnostics.AddError("Error updating S3 bucket", "the bucket disappeared during update")
		return
	}
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

// Delete removes the bucket (the service bills the final hours then dispatches an
// async delete job that tears down the bucket, its policies and users before
// soft-deleting the row). The key attachments are cascaded server-side.
func (r *s3BucketResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state s3BucketModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteS3Bucket(ctx, state.ID.ValueString()); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deleting S3 bucket", err))
		return
	}
}

// ImportState lets `terraform import iaas_s3_bucket.x <uuid>` adopt an existing
// bucket; the next Read hydrates everything (including rebuilding attached_keys).
func (r *s3BucketResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// readState GETs the bucket SHOW envelope + the attached-keys listing and builds
// a full model. The bool return is true when the bucket was not found (404).
func (r *s3BucketResource) readState(ctx context.Context, id string, prior s3BucketModel) (s3BucketModel, bool, diag.Diagnostics) {
	env, err := r.client.GetS3Bucket(ctx, id)
	if err != nil {
		if client.IsNotFound(err) {
			return s3BucketModel{}, true, nil
		}
		var diags diag.Diagnostics
		diags.Append(diagFromErr("Error reading S3 bucket", err))
		return s3BucketModel{}, false, diags
	}

	keys, err := r.client.ListS3BucketKeys(ctx, id)
	if err != nil {
		var diags diag.Diagnostics
		diags.Append(diagFromErr("Error reading S3 bucket access keys", err))
		return s3BucketModel{}, false, diags
	}

	m, diags := s3BucketStateFromAPI(env, keys, prior)
	return m, false, diags
}

// s3BucketStateFromAPI builds the model from the SHOW envelope (bucket nested
// under "bucket"; access_key/secret_key/endpoint top-level) and the attached-key
// listing.
func s3BucketStateFromAPI(env map[string]any, keys []map[string]any, prior s3BucketModel) (s3BucketModel, diag.Diagnostics) {
	bucket, _ := env["bucket"].(map[string]any)
	if bucket == nil {
		// Tolerate a flat shape just in case.
		bucket = env
	}

	m := s3BucketModel{
		ID:            stringFromAPI(bucket, "id", prior.ID),
		Name:          stringFromAPI(bucket, "name", prior.Name),
		S3PlanID:      stringFromAPI(bucket, "s3_plan_id", prior.S3PlanID),
		S3ServerID:    stringFromAPI(bucket, "s3_server_id", prior.S3ServerID),
		DefaultAccess: stringFromAPI(bucket, "default_access", prior.DefaultAccess),

		// access_key / secret_key / endpoint are TOP-LEVEL envelope keys, not
		// inside the bucket object. The bucket SHOW re-exposes the secret every
		// time, so it is simply re-read (no shown-once capture needed here).
		AccessKey: computedStringFromAPI(env, "access_key", prior.AccessKey),
		SecretKey: computedStringFromAPI(env, "secret_key", prior.SecretKey),
		Endpoint:  computedStringFromAPI(env, "endpoint", prior.Endpoint),

		Suspended: boolFromIntAPI(bucket, "suspended", prior.Suspended),
		Quota:     int64FromAPI(bucket, "quota", prior.Quota),
		Bandwidth: int64FromAPI(bucket, "bandwidth", prior.Bandwidth),
	}

	keySet, diags := s3BucketKeySetFromAPI(keys, prior.AttachedKeys)
	m.AttachedKeys = keySet
	return m, diags
}

// s3BucketKeySetFromAPI converts the attached-key listing into a types.Set of
// {access_key_id, permission}. Each listing element is a key object carrying its
// id and a "pivot" sub-object with the "permission". When the listing is
// absent/empty AND the prior config had a null attached_keys set, the result
// stays null so an unmanaged-attachments config does not show drift.
func s3BucketKeySetFromAPI(keys []map[string]any, prior types.Set) (types.Set, diag.Diagnostics) {
	objType := types.ObjectType{AttrTypes: s3BucketKeyAttrTypes}

	if len(keys) == 0 {
		if prior.IsNull() || prior.IsUnknown() {
			return types.SetNull(objType), nil
		}
		return types.SetValue(objType, []attr.Value{})
	}

	elems := make([]attr.Value, 0, len(keys))
	for _, k := range keys {
		idv, _ := k["id"].(string)
		if idv == "" {
			continue
		}
		permission := ""
		if pivot, ok := k["pivot"].(map[string]any); ok {
			if p, ok := pivot["permission"].(string); ok {
				permission = p
			}
		}
		obj, d := types.ObjectValue(s3BucketKeyAttrTypes, map[string]attr.Value{
			"access_key_id": types.StringValue(idv),
			"permission":    types.StringValue(permission),
		})
		if d.HasError() {
			return types.SetNull(objType), d
		}
		elems = append(elems, obj)
	}
	if len(elems) == 0 {
		if prior.IsNull() || prior.IsUnknown() {
			return types.SetNull(objType), nil
		}
		return types.SetValue(objType, []attr.Value{})
	}
	return types.SetValue(objType, elems)
}

// bucketKeysFromSet decodes a types.Set of attached-key objects into a Go slice.
// A null or unknown set yields an empty slice (no keys managed).
func bucketKeysFromSet(ctx context.Context, set types.Set) ([]s3BucketKeyModel, diag.Diagnostics) {
	if set.IsNull() || set.IsUnknown() {
		return nil, nil
	}
	var keys []s3BucketKeyModel
	d := set.ElementsAs(ctx, &keys, false)
	return keys, d
}
