package resources

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/iaas/terraform-provider-iaas/internal/client"
	"github.com/iaas/terraform-provider-iaas/internal/waiter"
)

// Interface assertions - iaas_db_replica is the CHILD of iaas_managed_database: a
// read replica created via POST /database/{primaryID}/replica. A replica is its
// OWN managed_databases row (with primary_database_id set + role="replica"), so it
// reuses GetManagedDatabase / DeleteManagedDatabase / ResizeManagedDatabase by the
// replica's own id. It is an ASYNC resource (status deploying → active), copying
// the managed_database async-status-poll pattern, with:
//   - the PRIMARY id in the CREATE path (RequiresReplace - a replica cannot move
//     primaries),
//   - a composite import id "primary_id/replica_id" (vpc_subnet pattern),
//   - db_plan_id resizable IN PLACE (the replica is its own DB row), everything
//     else immutable.
var (
	_ resource.Resource                = &dbReplicaResource{}
	_ resource.ResourceWithConfigure   = &dbReplicaResource{}
	_ resource.ResourceWithImportState = &dbReplicaResource{}
)

// NewDBReplicaResource is the resource constructor registered with the provider.
func NewDBReplicaResource() resource.Resource {
	return &dbReplicaResource{}
}

// dbReplicaResource manages an iaas_db_replica - a read replica of a managed
// database.
type dbReplicaResource struct {
	client *client.Client
}

// dbReplicaModel maps the Terraform state/plan for iaas_db_replica.
//
//   - REPLACE inputs: primary_id (create path), name (no rename), vpc_subnet_id.
//   - RESIZE input: db_plan_id (mutable in place via the resize PATCH).
//   - server-managed computed: status, host, port, username, engine,
//     engine_version, replication_status.
type dbReplicaModel struct {
	ID          types.String `tfsdk:"id"`
	PrimaryID   types.String `tfsdk:"primary_id"`
	Name        types.String `tfsdk:"name"`
	DBPlanID    types.String `tfsdk:"db_plan_id"`
	VPCSubnetID types.String `tfsdk:"vpc_subnet_id"`

	// Computed read-only.
	Status            types.String `tfsdk:"status"`
	Host              types.String `tfsdk:"host"`
	Port              types.Int64  `tfsdk:"port"`
	Username          types.String `tfsdk:"username"`
	Engine            types.String `tfsdk:"engine"`
	EngineVersion     types.String `tfsdk:"engine_version"`
	ReplicationStatus types.String `tfsdk:"replication_status"`

	Timeouts timeouts.Value `tfsdk:"timeouts"`
}

// Metadata sets the resource type name → "<provider>_db_replica".
func (r *dbReplicaResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_db_replica"
}

// Schema describes the iaas_db_replica resource.
func (r *dbReplicaResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a read replica of an iaas_managed_database. The replica is its own " +
			"database row attached to a primary; the engine and version are inherited from the primary. " +
			"Creation is ASYNCHRONOUS: the replica record and its backing instance are created, then this " +
			"resource waits for status to become \"active\". The primary must be active, a primary, and in " +
			"a VPC; the replica plan's storage must be >= the primary plan's. The primary, name, and " +
			"subnet are immutable (changing any forces a new replica); the plan can be changed in place " +
			"(a resize). To turn a replica into a standalone primary, promote it out-of-band (not modelled " +
			"as an attribute).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the replica database, assigned by the API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"primary_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the primary managed database this replica attaches to. Immutable; " +
					"changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"name": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "Name of the replica. The server generates one (\"<primary>-replica-N\") when " +
					"omitted. Immutable; changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplaceIfConfigured(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"db_plan_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the database plan for the replica (its storage must be >= the " +
					"primary plan's). Changeable IN PLACE via a resize.",
			},
			"vpc_subnet_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the VPC subnet (in the primary's VPC) to place the replica in. " +
					"Immutable; changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			// status / replication_status are SERVER-MUTABLE → no UseStateForUnknown.
			"status": schema.StringAttribute{
				Computed: true,
				Description: "Lifecycle status of the replica: \"deploying\", \"active\", \"suspended\", " +
					"\"error\", \"destroying\". Server-mutable.",
			},
			"replication_status": schema.StringAttribute{
				Computed: true,
				Description: "Replication state: \"syncing\", \"active\", \"stopped\". Server-mutable " +
					"(the slave reports replication health).",
			},
			"host": schema.StringAttribute{
				Computed: true,
				Description: "Connection host - the replica's public IPv4 address (for public-subnet " +
					"replicas), extracted from the nested public_ip object. Empty for private-subnet " +
					"replicas. Stable after create.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"port": schema.Int64Attribute{
				Computed:    true,
				Description: "Connection port (inherited from the primary). Stable after create.",
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"username": schema.StringAttribute{
				Computed:    true,
				Description: "Admin username (\"dbadmin\"). Stable after create.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"engine": schema.StringAttribute{
				Computed:    true,
				Description: "Database engine, inherited from the primary. Stable after create.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"engine_version": schema.StringAttribute{
				Computed:    true,
				Description: "Engine version, inherited from the primary. Stable after create.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{
				Create: true,
				Update: true,
				Delete: true,
			}),
		},
	}
}

// Configure pulls the shared *client.Client from the provider (nil-guarded).
func (r *dbReplicaResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create deploys the replica and waits for it to become active:
//
//  1. CreateDatabaseReplica records the replica row (its own managed_databases
//     row) + backing instance and returns the object WITH its id (status=
//     "deploying"). There is NO task_id - the async signal is the replica's own
//     status, polled via SHOW (GetManagedDatabase by the replica id).
//  2. The id is saved into state BEFORE the wait.
//  3. WaitFor polls until status=="active" (fail on "error").
//  4. GetManagedDatabase hydrates the computed fields.
func (r *dbReplicaResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan dbReplicaModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	createTimeout, diags := plan.Timeouts.Create(ctx, defaultCreateTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := map[string]any{
		"db_plan_id":    plan.DBPlanID.ValueString(),
		"vpc_subnet_id": plan.VPCSubnetID.ValueString(),
	}
	if !plan.Name.IsNull() && !plan.Name.IsUnknown() && plan.Name.ValueString() != "" {
		body["name"] = plan.Name.ValueString()
	}

	created, err := r.client.CreateDatabaseReplica(ctx, plan.PrimaryID.ValueString(), body)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating database replica", err))
		return
	}
	id, _ := created["id"].(string)
	if id == "" {
		resp.Diagnostics.AddError("Error creating database replica", "the create response did not include a replica id")
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	if resp.Diagnostics.HasError() {
		return
	}

	waitErr := waiter.WaitFor(ctx, waiter.Options{
		Interval: pollInterval(),
		Timeout:  createTimeout,
		Refresh: waiter.StatePollerWithErrorTolerance(
			func() (map[string]any, error) { return r.client.GetManagedDatabase(ctx, id) },
			"status",
			[]string{"active"},
			[]string{"error"},
			3,
		),
	})
	if waitErr != nil {
		resp.Diagnostics.AddError(
			"Error waiting for database replica provisioning",
			fmt.Sprintf("database replica %s did not become active: %s", id, waitErr.Error()),
		)
		return
	}

	obj, err := r.client.GetManagedDatabase(ctx, id)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading database replica after provisioning", err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, dbReplicaStateFromAPI(obj, plan))...)
}

// Read refreshes state from the API (a replica is its own DB row). A 404 means it
// was deleted out of band → remove it from state.
func (r *dbReplicaResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state dbReplicaModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	obj, err := r.client.GetManagedDatabase(ctx, state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("Error reading database replica", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, dbReplicaStateFromAPI(obj, state))...)
}

// Update applies the only in-place mutation - a resize (db_plan_id change) via the
// resize PATCH on the replica's own id. Every other input is RequiresReplace.
func (r *dbReplicaResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state dbReplicaModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()
	if !plan.DBPlanID.Equal(state.DBPlanID) {
		if _, err := r.client.ResizeManagedDatabase(ctx, id, map[string]any{"db_plan_id": plan.DBPlanID.ValueString()}); err != nil {
			resp.Diagnostics.Append(diagFromErr("Error resizing database replica", err))
			return
		}
	}

	obj, err := r.client.GetManagedDatabase(ctx, id)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading database replica after update", err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, dbReplicaStateFromAPI(obj, plan))...)
}

// Delete removes the replica (its own DB row) and polls SHOW until 404.
func (r *dbReplicaResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state dbReplicaModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	deleteTimeout, diags := state.Timeouts.Delete(ctx, defaultDeleteTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()
	if err := r.client.DeleteManagedDatabase(ctx, id); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deleting database replica", err))
		return
	}

	waitErr := waiter.WaitFor(ctx, waiter.Options{
		Interval: pollInterval(),
		Timeout:  deleteTimeout,
		Refresh: func() (string, bool, error) {
			_, err := r.client.GetManagedDatabase(ctx, id)
			if err != nil {
				if client.IsNotFound(err) {
					return "deleted", true, nil
				}
				return "", false, err
			}
			return "destroying", false, nil
		},
	})
	if waitErr != nil {
		resp.Diagnostics.AddError(
			"Error waiting for database replica deletion",
			fmt.Sprintf("database replica %s was not removed: %s", id, waitErr.Error()),
		)
		return
	}
}

// ImportState supports `terraform import iaas_db_replica.x <primary_id>/<replica_id>`.
// The primary id is in the create path (RequiresReplace) and is not returned by the
// replica SHOW under that name, so the composite import seeds both parts (the
// vpc_subnet child pattern).
func (r *dbReplicaResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	primaryID, replicaID, found := strings.Cut(req.ID, "/")
	if !found || primaryID == "" || replicaID == "" {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Expected import ID in the form \"primary_id/replica_id\", got %q.", req.ID),
		)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("primary_id"), primaryID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), replicaID)...)
}

// dbReplicaStateFromAPI builds the model from a SHOW managed_database object (the
// replica's own row). primary_id, name, vpc_subnet_id, and db_plan_id are the
// authoritative inputs; the rest are computed. The SHOW returns primary_database_id
// for a replica, which we map onto primary_id when present.
func dbReplicaStateFromAPI(obj map[string]any, prior dbReplicaModel) dbReplicaModel {
	return dbReplicaModel{
		ID:          stringFromAPI(obj, "id", prior.ID),
		PrimaryID:   stringOrPrior(obj, "primary_database_id", prior.PrimaryID),
		Name:        stringFromAPI(obj, "name", prior.Name),
		DBPlanID:    stringOrPrior(obj, "db_plan_id", prior.DBPlanID),
		VPCSubnetID: optionalStringFromAPI(obj, "vpc_subnet_id", prior.VPCSubnetID),

		Status:            stringFromAPI(obj, "status", prior.Status),
		ReplicationStatus: computedStringFromAPI(obj, "replication_status", prior.ReplicationStatus),
		Host:              nestedStringFromAPI(obj, "public_ip", "ip", prior.Host),
		Port:              int64FromAPI(obj, "port", prior.Port),
		Username:          computedStringFromAPI(obj, "admin_user", prior.Username),
		Engine:            computedStringFromAPI(obj, "engine", prior.Engine),
		EngineVersion:     computedStringFromAPI(obj, "engine_version", prior.EngineVersion),

		Timeouts: prior.Timeouts,
	}
}
