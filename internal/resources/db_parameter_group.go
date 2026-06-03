package resources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/iaas/terraform-provider-iaas/internal/client"
)

// Interface assertions — iaas_db_parameter_group follows the golden ssh_key
// resource pattern (simple sync CRUD).
var (
	_ resource.Resource                = &dbParameterGroupResource{}
	_ resource.ResourceWithConfigure   = &dbParameterGroupResource{}
	_ resource.ResourceWithImportState = &dbParameterGroupResource{}
)

// NewDBParameterGroupResource is the resource constructor registered with the
// provider.
func NewDBParameterGroupResource() resource.Resource {
	return &dbParameterGroupResource{}
}

// dbParameterGroupResource manages an iaas_db_parameter_group — a named,
// engine-scoped collection of key→value database configuration parameters that
// can be applied to a managed database.
//
// All CRUD operations are SYNCHRONOUS (no async task/waiter). The parameters
// map is a full-replacement update: PATCH sends the entire new map.
//
// Route summary (verified against UserApi\DbParameterGroupController +
// routes/user_api.php; all routes are wrapped in billing.enabled):
//
//	LIST    GET    /db/parameter-groups         (PLURAL)  → {success,parameter_groups:[...]}
//	CREATE  POST   /db/parameter-groups         (PLURAL)
//	                                              body {name (req), engine (req),
//	                                                parameters (req: map[string]any)}
//	                                              → {success,parameter_group:{id,...}}
//	UPDATE  PATCH  /db/parameter-group/{id}     (SINGULAR)
//	                                              body {name?, parameters?}
//	                                              → {success,parameter_group:{id,...}}
//	DELETE  DELETE /db/parameter-group/{id}     (SINGULAR) → {success,message}
//
// DEVIATION: There is NO SHOW endpoint in user_api.php. Read uses
// GetDBParameterGroup which lists all groups and finds by id (list-and-match).
//
// Parameters are modelled as MapAttribute(String):
//   - The API accepts a name→value map; the controller appends unit suffixes
//     from the catalog (e.g. 512 → "512M") before storing.
//   - Values in the LIST response may be strings with suffixes.
//   - Users should provide string values in the form the API returns them.
//   - This is a simple flat map (no per-param add/remove endpoints, no nested
//     set) — update sends the full replacement map.
//   - The map is Required on create and updatable in place.
//
// Applying a parameter group to a database is done via the separate PATCH
// /database/{id}/parameter-group endpoint (a db-resource action, not modelled
// here; noted as a future iaas_managed_database attribute or action).
type dbParameterGroupResource struct {
	client *client.Client
}

// dbParameterGroupModel maps the Terraform state/plan for iaas_db_parameter_group.
//
// engine is immutable (RequiresReplace): the parameter catalog is engine-scoped
// and the server does not allow changing the engine of an existing group.
// name and parameters are updatable in place.
type dbParameterGroupModel struct {
	ID         types.String `tfsdk:"id"`
	Name       types.String `tfsdk:"name"`
	Engine     types.String `tfsdk:"engine"`
	Parameters types.Map    `tfsdk:"parameters"`
}

// Metadata sets the resource type name → "<provider>_db_parameter_group".
func (r *dbParameterGroupResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_db_parameter_group"
}

// Schema describes the iaas_db_parameter_group resource.
func (r *dbParameterGroupResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a database parameter group — a named, engine-scoped collection of " +
			"key→value configuration parameters that can be applied to a managed database. " +
			"The engine is immutable (changing it forces a new resource); the name and " +
			"parameters map can be updated in place. Updating parameters sends the full " +
			"replacement map to the API.\n\n" +
			"Parameter groups are a billed add-on: if billing is disabled, all operations " +
			"fail with HTTP 403. To apply a parameter group to a database, use the " +
			"dedicated `PATCH /database/{id}/parameter-group` API action (not yet modelled " +
			"as a Terraform attribute; planned for a future iaas_managed_database update).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the parameter group, assigned by the API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Display name for the parameter group. Maximum 255 characters. Updatable in place.",
			},
			"engine": schema.StringAttribute{
				Required: true,
				Description: "Database engine this group targets: \"mysql\", \"mariadb\", or " +
					"\"postgresql\". Parameter keys are validated against the engine's catalog. " +
					"Immutable; changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"parameters": schema.MapAttribute{
				Required:    true,
				ElementType: types.StringType,
				Description: "Map of database configuration parameter names to their string values. " +
					"Parameter keys are validated against the engine's catalog on the server. " +
					"The server may append unit suffixes (e.g. \"M\" for memory values) so " +
					"values should be provided in the form the API returns them. Updating this " +
					"attribute sends the full replacement map; use an empty map ({}) to clear " +
					"all custom parameters.",
				PlanModifiers: []planmodifier.Map{
					mapplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

// Configure pulls the shared *client.Client from the provider. Tolerates a nil
// ProviderData (the framework calls Configure once with nil data before the
// provider's own Configure has run).
func (r *dbParameterGroupResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create provisions a new parameter group, then reads it back via list so state
// reflects the server-assigned id and any parameter transformations (e.g. suffix
// appending by the controller).
func (r *dbParameterGroupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan dbParameterGroupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	paramsMap, diags := parametersToAPIMap(ctx, plan.Parameters)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := map[string]any{
		"name":       plan.Name.ValueString(),
		"engine":     plan.Engine.ValueString(),
		"parameters": paramsMap,
	}

	obj, err := r.client.CreateDBParameterGroup(ctx, body)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating DB parameter group", err))
		return
	}

	id, _ := obj["id"].(string)
	if id == "" {
		resp.Diagnostics.AddError("Error creating DB parameter group",
			"the create response did not contain an id")
		return
	}

	// Read back so state reflects the server-stored form (e.g. suffix-transformed
	// parameter values). Use GetDBParameterGroup (list-and-match) since there is no
	// SHOW endpoint.
	state, notFound, readDiags := r.readState(ctx, id)
	resp.Diagnostics.Append(readDiags...)
	if notFound {
		resp.Diagnostics.AddError("Error creating DB parameter group",
			"the group disappeared immediately after creation")
		return
	}
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Read refreshes state from the API. A 404 (group deleted out of band) removes
// the resource from state so Terraform plans a recreate.
func (r *dbParameterGroupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state dbParameterGroupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	newState, notFound, readDiags := r.readState(ctx, state.ID.ValueString())
	if notFound {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(readDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

// Update applies the planned changes: patches name and/or parameters if either
// changed. The engine is immutable (RequiresReplace handles it). Reads back
// after the patch so state reflects the server-stored form.
func (r *dbParameterGroupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state dbParameterGroupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()

	// Build the PATCH body with only the changed fields.
	patchBody := map[string]any{}

	if !plan.Name.Equal(state.Name) {
		patchBody["name"] = plan.Name.ValueString()
	}

	if !plan.Parameters.Equal(state.Parameters) {
		paramsMap, diags := parametersToAPIMap(ctx, plan.Parameters)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		patchBody["parameters"] = paramsMap
	}

	if len(patchBody) > 0 {
		if _, err := r.client.UpdateDBParameterGroup(ctx, id, patchBody); err != nil {
			resp.Diagnostics.Append(diagFromErr("Error updating DB parameter group", err))
			return
		}
	}

	// Read back so state reflects the current server form.
	newState, notFound, readDiags := r.readState(ctx, id)
	resp.Diagnostics.Append(readDiags...)
	if notFound {
		resp.Diagnostics.AddError("Error updating DB parameter group",
			"the group disappeared during update")
		return
	}
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

// Delete removes the parameter group. Any managed databases referencing it are
// automatically detached (parameter_group_id set to null) server-side.
func (r *dbParameterGroupResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state dbParameterGroupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteDBParameterGroup(ctx, state.ID.ValueString()); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deleting DB parameter group", err))
		return
	}
}

// ImportState lets `terraform import iaas_db_parameter_group.x <uuid>` adopt
// an existing parameter group by its id; the next Read hydrates name/engine/
// parameters from the list.
func (r *dbParameterGroupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// readState fetches the parameter group via list-and-match and builds a model.
// notFound is true when the group was not found (404), in which case diagnostics
// are empty (the caller should RemoveResource or report the group disappeared).
func (r *dbParameterGroupResource) readState(ctx context.Context, id string) (dbParameterGroupModel, bool, diag.Diagnostics) {
	obj, err := r.client.GetDBParameterGroup(ctx, id)
	if err != nil {
		if client.IsNotFound(err) {
			return dbParameterGroupModel{}, true, nil
		}
		var d diag.Diagnostics
		d.Append(diagFromErr("Error reading DB parameter group", err))
		return dbParameterGroupModel{}, false, d
	}

	m, buildErr := dbParameterGroupStateFromAPI(ctx, obj)
	if buildErr != nil {
		var d diag.Diagnostics
		d.Append(diagFromErr("Error reading DB parameter group", buildErr))
		return dbParameterGroupModel{}, false, d
	}
	return m, false, nil
}

// dbParameterGroupStateFromAPI builds the model from an API parameter_group
// object. The parameters map is converted from map[string]any to types.Map(string).
func dbParameterGroupStateFromAPI(ctx context.Context, obj map[string]any) (dbParameterGroupModel, error) {
	m := dbParameterGroupModel{
		ID:     stringFromAPI(obj, "id", types.StringNull()),
		Name:   stringFromAPI(obj, "name", types.StringNull()),
		Engine: stringFromAPI(obj, "engine", types.StringNull()),
	}

	paramsMap, err := apiMapToParameters(ctx, obj["parameters"])
	if err != nil {
		return m, err
	}
	m.Parameters = paramsMap
	return m, nil
}

// parametersToAPIMap converts a types.Map(string) to map[string]any for the
// API request body. An empty or null map yields an empty map (not null) so the
// API receives {} rather than omitting the field.
func parametersToAPIMap(ctx context.Context, m types.Map) (map[string]any, diag.Diagnostics) {
	if m.IsNull() || m.IsUnknown() {
		return map[string]any{}, nil
	}
	var goMap map[string]string
	var d diag.Diagnostics
	d = m.ElementsAs(ctx, &goMap, false)
	if d.HasError() {
		return nil, d
	}
	out := make(map[string]any, len(goMap))
	for k, v := range goMap {
		out[k] = v
	}
	return out, nil
}

// apiMapToParameters converts the API "parameters" field (map[string]any) to a
// types.Map(string). Values are stringified so that numeric or bool API values
// (returned by JSON unmarshal as float64 or bool) round-trip consistently.
func apiMapToParameters(ctx context.Context, raw any) (types.Map, error) {
	objType := types.StringType

	if raw == nil {
		m, _ := types.MapValue(objType, map[string]attr.Value{})
		return m, nil
	}

	apiMap, ok := raw.(map[string]any)
	if !ok {
		// Unexpected type — return empty map rather than failing.
		m, _ := types.MapValue(objType, map[string]attr.Value{})
		return m, nil
	}

	elems := make(map[string]attr.Value, len(apiMap))
	for k, v := range apiMap {
		var s string
		switch tv := v.(type) {
		case string:
			s = tv
		case nil:
			s = ""
		default:
			s = fmt.Sprintf("%v", tv)
		}
		elems[k] = types.StringValue(s)
	}

	result, diags := types.MapValue(objType, elems)
	if diags.HasError() {
		return types.MapNull(objType), fmt.Errorf("building parameters map: %v", diags)
	}
	return result, nil
}
