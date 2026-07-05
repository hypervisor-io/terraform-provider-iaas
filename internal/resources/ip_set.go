package resources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hypervisor-io/terraform-provider-iaas/internal/client"
)

// Interface assertions - ip_set establishes the NESTED-ENTRIES pattern: a set of
// child entry rows managed inside the parent resource (rather than as a separate
// child resource like vpc_subnet). The same diff-by-natural-key approach is what
// security_group(+rules) copies.
var (
	_ resource.Resource                = &ipSetResource{}
	_ resource.ResourceWithConfigure   = &ipSetResource{}
	_ resource.ResourceWithImportState = &ipSetResource{}
)

// NewIPSetResource is the resource constructor registered with the provider.
func NewIPSetResource() resource.Resource {
	return &ipSetResource{}
}

// ipSetResource manages an iaas_ip_set - a named, ip_version-scoped collection of
// CIDR entries used by security-group rules.
//
// The CHILD entries are managed inline as a SetNestedAttribute rather than as a
// separate resource: the API exposes per-entry add/remove endpoints (there is no
// entry-update endpoint) and SHOW embeds the entries, so the parent can fully own
// them. Create adds the configured entries; Update diffs the set (add new / delete
// removed by server id); Read rebuilds the set from the SHOW response.
//
// Route summary (verified against UserApi\IpSetController + routes/user_api.php):
//
//	INDEX   GET    /ip-sets              (plural)
//	CREATE  POST   /ip-sets              (plural)  body {name,description?,ip_version}
//	                                      → {success,message,ip_set}
//	SHOW    GET    /ip-set/{id}          (singular) → {success,ip_set:{...,entries:[...]}}
//	UPDATE  PATCH  /ip-set/{id}          (singular) body {name,description?,ip_version?}
//	                                      → {success,message}  (NO ip_set body → Read after)
//	DELETE  DELETE /ip-set/{id}          (singular) → {success,message}
//	ADD     POST   /ip-set/{id}/entries  body {cidr,description?} → {success,message,entry}
//	REMOVE  DELETE /ip-set/{id}/entry/{entryId}    → {success,message}
//
// All operations are synchronous (no task/waiter).
type ipSetResource struct {
	client *client.Client
}

// ipSetModel maps the Terraform state/plan for iaas_ip_set.
//
// Entries is a SET (order-independent) of nested entry objects. ip_version is
// immutable in practice - the controller rejects changing it once entries exist -
// so it is RequiresReplace. name/description are updatable in place.
type ipSetModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	IPVersion   types.String `tfsdk:"ip_version"`
	Entries     types.Set    `tfsdk:"entries"`
}

// ipSetEntryModel maps a single nested entry object inside the entries set.
//
// Cidr is the server-enforced unique key (the server normalises a bare IP to
// /32 or /128 and enforces uniqueness by cidr), but the in-provider diff key is
// cidr+comment, so a comment-only edit becomes a delete+add. Comment is the
// optional per-entry description (sent to the API as "description"). ID is the
// server-assigned entry UUID, needed to delete this exact entry on update.
type ipSetEntryModel struct {
	ID      types.String `tfsdk:"id"`
	Cidr    types.String `tfsdk:"cidr"`
	Comment types.String `tfsdk:"comment"`
}

// ipSetEntryAttrTypes is the attribute-type map for one entry object. It is the
// single source of truth for the nested object's shape, reused everywhere a
// types.Set of entries is (re)built so the schema and the runtime values never
// drift apart.
var ipSetEntryAttrTypes = map[string]attr.Type{
	"id":      types.StringType,
	"cidr":    types.StringType,
	"comment": types.StringType,
}

// Metadata sets the resource type name → "<provider>_ip_set" → "iaas_ip_set".
func (r *ipSetResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ip_set"
}

// Schema describes the iaas_ip_set resource.
func (r *ipSetResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages an IP set - a named, version-scoped collection of CIDR entries " +
			"that can be referenced from security group rules. The CIDR entries are managed " +
			"inline as an order-independent set: adding or removing an entry from the config " +
			"adds or removes it on the server in place, without replacing the IP set. The " +
			"`ip_version` cannot be changed once the set has entries, so changing it forces a " +
			"new resource.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the IP set, assigned by the API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required: true,
				Description: "Display name for the IP set. Maximum 255 characters. " +
					"Updatable in place.",
			},
			"description": schema.StringAttribute{
				Optional: true,
				Description: "Optional free-text description of the IP set. " +
					"Set to null to clear. Updatable in place.",
			},
			"ip_version": schema.StringAttribute{
				Required: true,
				Description: "IP version of the entries in this set: \"ipv4\" or \"ipv6\". " +
					"All entries must match this version. The server rejects changing the " +
					"version once entries exist, so changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"entries": schema.SetNestedAttribute{
				Optional: true,
				Description: "The CIDR entries in this IP set, as an order-independent set. " +
					"Each entry is added or removed on the server individually when it appears " +
					"or disappears from this set. Changing an entry's comment removes and " +
					"re-adds that entry (the API has no entry-update endpoint).",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Computed: true,
							Description: "UUID of the entry, assigned by the API. Used internally " +
								"to delete this specific entry when it is removed from the set.",
							// No UseStateForUnknown: set elements have no stable
							// position, so the framework cannot reliably copy a prior
							// element's id onto a planned element. The id is resolved
							// during Create/Update (from the add response) and Read.
						},
						"cidr": schema.StringAttribute{
							Required: true,
							Description: "CIDR or IP for this entry (e.g. \"192.168.1.0/24\" or " +
								"\"10.0.0.5\"). A bare IP is normalised to /32 (IPv4) or /128 (IPv6) " +
								"server-side. Must match the set's ip_version.",
						},
						"comment": schema.StringAttribute{
							Optional: true,
							Description: "Optional per-entry description. Changing it removes and " +
								"re-adds the entry, since the API has no entry-update endpoint.",
						},
					},
				},
			},
		},
	}
}

// Configure pulls the shared *client.Client from the provider. It tolerates a
// nil ProviderData (the framework calls Configure once with nil data before the
// provider's own Configure has run).
func (r *ipSetResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create provisions the IP set, then adds the configured entries one-by-one, then
// reads the set back so state reflects the server-assigned entry ids and any
// CIDR normalisation. Entries are added individually (not via the bulk-add
// endpoint) because bulk-add drops per-entry comments.
func (r *ipSetResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ipSetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := map[string]any{
		"name":       plan.Name.ValueString(),
		"ip_version": plan.IPVersion.ValueString(),
	}
	if !plan.Description.IsNull() && !plan.Description.IsUnknown() {
		body["description"] = plan.Description.ValueString()
	}

	obj, err := r.client.CreateIPSet(ctx, body)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating IP set", err))
		return
	}

	id, _ := obj["id"].(string)
	if id == "" {
		resp.Diagnostics.AddError("Error creating IP set", "the create response did not contain an id")
		return
	}

	// Add the configured entries. Decode the planned set into Go structs first.
	plannedEntries, diags := entriesFromSet(ctx, plan.Entries)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	for _, e := range plannedEntries {
		if err := r.addEntry(ctx, id, e); err != nil {
			// The set was created but adding an entry failed. Persist the id so
			// the resource is destroyable, then surface the error.
			resp.Diagnostics.Append(diagFromErr("Error adding IP set entry", err))
			_ = resp.State.Set(ctx, ipSetModel{
				ID:          types.StringValue(id),
				Name:        plan.Name,
				Description: plan.Description,
				IPVersion:   plan.IPVersion,
				Entries:     types.SetNull(types.ObjectType{AttrTypes: ipSetEntryAttrTypes}),
			})
			return
		}
	}

	// Read back so state carries the server entry ids + normalised cidrs.
	state, notFound, diags := r.readState(ctx, id, plan)
	resp.Diagnostics.Append(diags...)
	if notFound {
		resp.Diagnostics.AddError("Error creating IP set",
			"the IP set disappeared immediately after creation")
		return
	}
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Read refreshes state from the API. A 404 means the set was deleted out of band
// - remove it from state so Terraform plans a recreate.
func (r *ipSetResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ipSetModel
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

// Update applies the planned changes:
//   - patches name/description/ip_version if the scalar fields changed;
//   - diffs the entries set (planned vs state): adds entries that are new,
//     deletes entries that were removed (by their server id).
//
// An entry is "the same" only when BOTH its cidr and comment match - so changing
// a comment on an existing cidr deletes the old entry and adds a fresh one (the
// API has no entry-update endpoint). After mutating, it reads back so state
// reflects the server entry ids.
func (r *ipSetResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state ipSetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()

	// Patch the scalar fields when any changed. name is always sent (required);
	// description is sent as null when cleared.
	if !plan.Name.Equal(state.Name) || !plan.Description.Equal(state.Description) {
		fields := map[string]any{"name": plan.Name.ValueString()}
		if plan.Description.IsNull() {
			fields["description"] = nil
		} else if !plan.Description.IsUnknown() {
			fields["description"] = plan.Description.ValueString()
		}
		if _, err := r.client.UpdateIPSet(ctx, id, fields); err != nil {
			resp.Diagnostics.Append(diagFromErr("Error updating IP set", err))
			return
		}
	}

	// Diff the entries set.
	plannedEntries, diags := entriesFromSet(ctx, plan.Entries)
	resp.Diagnostics.Append(diags...)
	stateEntries, diags := entriesFromSet(ctx, state.Entries)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Key by cidr+comment so a comment-only change is a delete+add. The state
	// entries carry the server id needed for deletion.
	plannedByKey := make(map[string]ipSetEntryModel, len(plannedEntries))
	for _, e := range plannedEntries {
		plannedByKey[entryKey(e)] = e
	}
	stateByKey := make(map[string]ipSetEntryModel, len(stateEntries))
	for _, e := range stateEntries {
		stateByKey[entryKey(e)] = e
	}

	// Delete entries that are in state but not in the plan.
	for key, e := range stateByKey {
		if _, keep := plannedByKey[key]; keep {
			continue
		}
		if e.ID.IsNull() || e.ID.IsUnknown() || e.ID.ValueString() == "" {
			resp.Diagnostics.AddWarning(
				"IP set entry could not be deleted: server id unknown",
				fmt.Sprintf(
					"Entry with cidr %q was removed from config but its server-assigned id "+
						"is not present in state, so the provider cannot issue a delete request. "+
						"The entry may still exist on the server. Manual cleanup or "+
						"`terraform import` may be needed to reconcile.",
					e.Cidr.ValueString(),
				),
			)
			continue // nothing to delete server-side
		}
		if err := r.client.DeleteIPSetEntry(ctx, id, e.ID.ValueString()); err != nil {
			resp.Diagnostics.Append(diagFromErr("Error removing IP set entry", err))
			return
		}
	}

	// Add entries that are in the plan but not in state.
	for key, e := range plannedByKey {
		if _, exists := stateByKey[key]; exists {
			continue
		}
		if err := r.addEntry(ctx, id, e); err != nil {
			resp.Diagnostics.Append(diagFromErr("Error adding IP set entry", err))
			return
		}
	}

	// Read back so state reflects the current server entry ids + cidrs.
	newState, notFound, diags := r.readState(ctx, id, plan)
	resp.Diagnostics.Append(diags...)
	if notFound {
		resp.Diagnostics.AddError("Error updating IP set",
			"the IP set disappeared during update")
		return
	}
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

// Delete removes the IP set (its entries cascade server-side).
func (r *ipSetResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state ipSetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteIPSet(ctx, state.ID.ValueString()); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deleting IP set", err))
		return
	}
}

// ImportState lets `terraform import iaas_ip_set.x <uuid>` adopt an existing IP
// set by its id; the next Read hydrates name/description/ip_version and rebuilds
// the entries set from the SHOW response.
func (r *ipSetResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// addEntry POSTs a single entry, sending the comment as the API's "description"
// field (omitted when null so the server stores null rather than "").
func (r *ipSetResource) addEntry(ctx context.Context, setID string, e ipSetEntryModel) error {
	body := map[string]any{"cidr": e.Cidr.ValueString()}
	if !e.Comment.IsNull() && !e.Comment.IsUnknown() {
		body["description"] = e.Comment.ValueString()
	}
	_, err := r.client.AddIPSetEntry(ctx, setID, body)
	return err
}

// readState GETs the IP set and builds a full model from it, rebuilding the
// entries set from the embedded entries array. prior supplies fallbacks for any
// field the response omits. The bool return is true when the set was not found
// (404), so the caller can RemoveResource; in that case the returned diagnostics
// are empty.
func (r *ipSetResource) readState(ctx context.Context, id string, prior ipSetModel) (ipSetModel, bool, diag.Diagnostics) {
	obj, err := r.client.GetIPSet(ctx, id)
	if err != nil {
		if client.IsNotFound(err) {
			return ipSetModel{}, true, nil
		}
		var diags diag.Diagnostics
		diags.Append(diagFromErr("Error reading IP set", err))
		return ipSetModel{}, false, diags
	}
	m, diags := ipSetStateFromAPI(ctx, obj, prior)
	return m, false, diags
}

// ipSetStateFromAPI builds the model from an API ip_set object, rebuilding the
// entries set from the embedded "entries" array. Each entry maps id+cidr+
// description→comment. The entries set is null when the prior config had no
// entries attribute and the server reports none (so an omitted entries block
// round-trips without spurious drift).
func ipSetStateFromAPI(ctx context.Context, obj map[string]any, prior ipSetModel) (ipSetModel, diag.Diagnostics) {
	m := ipSetModel{
		ID:          stringFromAPI(obj, "id", prior.ID),
		Name:        stringFromAPI(obj, "name", prior.Name),
		Description: optionalStringFromAPI(obj, "description", prior.Description),
		IPVersion:   stringFromAPI(obj, "ip_version", prior.IPVersion),
	}

	entrySet, diags := entrySetFromAPI(obj["entries"], prior.Entries)
	m.Entries = entrySet
	return m, diags
}

// entrySetFromAPI converts the embedded "entries" JSON array into a types.Set of
// entry objects. When the array is absent/empty AND the prior config had a null
// entries set, the result stays null so an unmanaged-entries config does not show
// drift; otherwise an empty managed set becomes an empty (non-null) set.
func entrySetFromAPI(raw any, prior types.Set) (types.Set, diag.Diagnostics) {
	objType := types.ObjectType{AttrTypes: ipSetEntryAttrTypes}

	arr, _ := raw.([]any)
	if len(arr) == 0 {
		// No entries on the server. Preserve a null vs empty distinction:
		// if the prior set was null (entries unmanaged), keep it null.
		if prior.IsNull() || prior.IsUnknown() {
			return types.SetNull(objType), nil
		}
		return types.SetValue(objType, []attr.Value{})
	}

	elems := make([]attr.Value, 0, len(arr))
	for _, item := range arr {
		eo, ok := item.(map[string]any)
		if !ok {
			continue
		}
		idVal := optionalStringFromAPI(eo, "id", types.StringNull())
		cidrVal := stringFromAPI(eo, "cidr", types.StringNull())
		commentVal := optionalStringFromAPI(eo, "description", types.StringNull())

		obj, d := types.ObjectValue(ipSetEntryAttrTypes, map[string]attr.Value{
			"id":      idVal,
			"cidr":    cidrVal,
			"comment": commentVal,
		})
		if d.HasError() {
			return types.SetNull(objType), d
		}
		elems = append(elems, obj)
	}

	return types.SetValue(objType, elems)
}

// entriesFromSet decodes a types.Set of entry objects into a Go slice. A null or
// unknown set yields an empty slice (no entries managed).
func entriesFromSet(ctx context.Context, set types.Set) ([]ipSetEntryModel, diag.Diagnostics) {
	if set.IsNull() || set.IsUnknown() {
		return nil, nil
	}
	var entries []ipSetEntryModel
	d := set.ElementsAs(ctx, &entries, false)
	return entries, d
}

// entryKey is the natural identity of an entry for diffing: cidr + comment. Two
// entries that differ only in comment are treated as distinct, so a comment edit
// becomes a delete-then-add (the API has no entry-update endpoint).
func entryKey(e ipSetEntryModel) string {
	comment := ""
	if !e.Comment.IsNull() && !e.Comment.IsUnknown() {
		comment = e.Comment.ValueString()
	}
	return e.Cidr.ValueString() + "\x00" + comment
}
