package resources

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hypervisor-io/terraform-provider-iaas/internal/client"
	"github.com/hypervisor-io/terraform-provider-iaas/internal/waiter"
)

// Interface assertions - iaas_managed_database is an ASYNC resource backed by a
// REAL instance. It copies the established async-status-poll pattern from
// load_balancer / instance:
//
//   - ASYNC create: POST /databases records the row (status="deploying") and spins
//     up a backing instance + slave deploy task. There is NO task_id - the async
//     signal is the DB's own status, polled via SHOW until "active"
//     (StatePollerWithErrorTolerance). The id is persisted to state BEFORE the wait
//     so a failed wait still leaves a destroyable resource. A timeouts block is
//     exposed.
//   - PARTLY-MUTABLE: db_plan_id is resized IN PLACE (PATCH /database/{id}/resize).
//     engine_version is ALSO mutable in place (T9): changing it invokes the
//     upgrade action (POST /database/{id}/upgrade) - see Update and
//     upgradeEngineVersion. Everything else (name, engine, vpc_id, vpc_subnet_id,
//     hypervisor_group_id) remains immutable → RequiresReplace.
//   - ACTIONS: restart and reset-password are stateless actions (no in-place
//     attribute). reset-password is the ONLY endpoint that returns a cleartext
//     password, surfaced into the Sensitive computed `password` via the
//     reset_password trigger token. resync_replicas (T9) is a second trigger
//     token, using the same write-only-token pattern, that invokes
//     POST /database/{id}/resync-replicas.
//   - T9 also adds read-only `last_error` / `error_acknowledged` computed
//     attributes (surfacing the alert state the acknowledge-error action
//     clears) and implements - but deliberately does NOT auto-invoke -
//     RetryManagedDatabase / AcknowledgeManagedDatabaseError; see the doc
//     comments on those client methods and on Update for the rationale
//     (mirrors T7's kubernetes_cluster upgrade/retry precedent).
//
// The read replica is a SEPARATE resource (iaas_db_replica, the child); it is its
// own ManagedDatabase row and is NOT modelled here.
var (
	_ resource.Resource                = &managedDatabaseResource{}
	_ resource.ResourceWithConfigure   = &managedDatabaseResource{}
	_ resource.ResourceWithImportState = &managedDatabaseResource{}
)

// NewManagedDatabaseResource is the resource constructor registered with the provider.
func NewManagedDatabaseResource() resource.Resource {
	return &managedDatabaseResource{}
}

// managedDatabaseResource manages an iaas_managed_database - a managed DB engine
// (MySQL/MariaDB/PostgreSQL) backed by a dedicated Cloud Service instance.
type managedDatabaseResource struct {
	client *client.Client
}

// managedDatabaseModel maps the Terraform state/plan for iaas_managed_database.
//
// Field groups:
//   - REPLACE inputs (name, engine, engine_version, vpc_id, vpc_subnet_id,
//     hypervisor_group_id): immutable; changing any forces a new resource.
//   - RESIZE input (db_plan_id): mutable in place via the resize PATCH.
//   - ACTION trigger (reset_password): an optional token whose change invokes
//     reset-password during Update and captures the new password.
//   - server-managed computed (status, host, port, username, role, password).
type managedDatabaseModel struct {
	ID                types.String `tfsdk:"id"`
	Name              types.String `tfsdk:"name"`
	Engine            types.String `tfsdk:"engine"`
	EngineVersion     types.String `tfsdk:"engine_version"`
	DBPlanID          types.String `tfsdk:"db_plan_id"`
	VPCID             types.String `tfsdk:"vpc_id"`
	VPCSubnetID       types.String `tfsdk:"vpc_subnet_id"`
	HypervisorGroupID types.String `tfsdk:"hypervisor_group_id"`

	// Action triggers (write-only): changing either re-runs the corresponding
	// action. ResetPassword → reset-password; ResyncReplicas (T9) →
	// resync-replicas.
	ResetPassword  types.String `tfsdk:"reset_password"`
	ResyncReplicas types.String `tfsdk:"resync_replicas"`

	// Computed read-only.
	Status            types.String `tfsdk:"status"`
	Host              types.String `tfsdk:"host"`
	Port              types.Int64  `tfsdk:"port"`
	Username          types.String `tfsdk:"username"`
	Role              types.String `tfsdk:"role"`
	Password          types.String `tfsdk:"password"`
	LastError         types.String `tfsdk:"last_error"`
	ErrorAcknowledged types.Bool   `tfsdk:"error_acknowledged"`

	Timeouts timeouts.Value `tfsdk:"timeouts"`
}

// Metadata sets the resource type name → "<provider>_managed_database".
func (r *managedDatabaseResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_managed_database"
}

// Schema describes the iaas_managed_database resource.
func (r *managedDatabaseResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a managed database (MySQL, MariaDB, or PostgreSQL) backed by a dedicated " +
			"instance, deployed into a VPC. Creation is ASYNCHRONOUS: the database record and its " +
			"backing instance are created, then this resource waits for the status to become " +
			"\"active\" (the lifecycle is deploying → active). The engine, name, and network placement " +
			"are immutable (changing any forces a new resource); the plan can be changed in place (a " +
			"resize), and the engine_version can be changed in place (an in-place major-version upgrade, " +
			"POST .../upgrade - see the engine_version attribute for the async-timing caveat). The " +
			"connection password is never returned by the API on create or read (it is encrypted and " +
			"hidden server-side); set/rotate it by changing reset_password, which invokes the " +
			"reset-password action and exposes the new cleartext password in the (sensitive) password " +
			"attribute. resync_replicas is a similar write-only trigger that resyncs this primary's " +
			"replicas. Managed databases are a billed add-on: if billing is disabled the create " +
			"fails with HTTP 403; feature/quota limits (plan disabled, engine unsupported, quota reached, " +
			"location not database-enabled, no free IP, NAT gateway required for a private subnet) fail " +
			"with a clear message.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the managed database, assigned by the API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required: true,
				Description: "Name of the managed database. Immutable (there is no rename endpoint); " +
					"changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"engine": schema.StringAttribute{
				Required: true,
				Description: "Database engine: \"mysql\", \"mariadb\", or \"postgresql\". Must be supported " +
					"by db_plan_id. Immutable; changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"engine_version": schema.StringAttribute{
				Required: true,
				Description: "Engine version (e.g. \"8.0\" for MySQL, \"16\" for PostgreSQL). Must be a " +
					"version offered for the engine. Changeable IN PLACE (T9): raising it invokes the " +
					"upgrade action (POST /database/{id}/upgrade), which requires the target to be a " +
					"version offered for the engine AND strictly higher than the current one (no downgrade, " +
					"no re-apply), takes a pre-upgrade backup first, then upgrades the engine on the " +
					"backing instance. CAVEAT: the API updates this field's value SYNCHRONOUSLY once the " +
					"hypervisor accepts the upgrade command - not once the upgrade actually finishes running " +
					"on the box - and exposes no independent completion signal, so a slave-side failure only " +
					"surfaces later via last_error/error_acknowledged on a subsequent read/plan, not as a " +
					"blocking apply-time error.",
			},
			"db_plan_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the database plan (CPU/RAM/storage sizing). Changeable IN PLACE via " +
					"a resize - the new plan's storage must be >= the current plan's, and it must still " +
					"support the engine.",
			},
			"vpc_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the VPC to deploy the database into. Immutable; changing it forces a " +
					"new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"vpc_subnet_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the VPC subnet to place the database in. A public subnet gives the " +
					"database a public IP; a private subnet requires a NAT gateway. Immutable; changing it " +
					"forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"hypervisor_group_id": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "Optional UUID of the location (hypervisor group). When omitted it is derived " +
					"from the VPC and returned by the API. Immutable; changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					// RequiresReplaceIfConfigured: a user-supplied change forces a replace,
					// but the server-derived value settling into this Computed field does
					// not. UseStateForUnknown keeps the derived value stable across plans.
					stringplanmodifier.RequiresReplaceIfConfigured(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"reset_password": schema.StringAttribute{
				Optional: true,
				Description: "Write-only trigger token for rotating the admin password. On create it is " +
					"echoed into state and (if set) the password is reset once after deploy; on update, " +
					"changing this value re-runs the reset-password action and refreshes the (sensitive) " +
					"password attribute. Its actual value is arbitrary - use a timestamp or version string " +
					"to force a rotation. Not returned by the API.",
			},
			"resync_replicas": schema.StringAttribute{
				Optional: true,
				Description: "Write-only trigger token (T9): changing this value invokes the " +
					"resync-replicas action (POST /database/{id}/resync-replicas), which resyncs every " +
					"eligible replica of this PRIMARY database from the current primary snapshot. Only " +
					"meaningful on a primary that already has one or more iaas_db_replica children and is " +
					"\"active\" - a rejection (not a primary, not active, no eligible replicas) surfaces as " +
					"an error. Its actual value is arbitrary - use a timestamp or version string to force a " +
					"resync. Not returned by the API.",
			},
			// status is SERVER-MUTABLE (deploying → active → suspended/error/destroying):
			// per the golden guardrail, do NOT attach UseStateForUnknown - that would copy
			// the stale prior value into the plan and MASK real drift.
			"status": schema.StringAttribute{
				Computed: true,
				Description: "Lifecycle status of the managed database: \"deploying\", \"active\", " +
					"\"suspended\", \"error\", \"destroying\". Server-mutable.",
			},
			"host": schema.StringAttribute{
				Computed: true,
				Description: "Connection host - the database's public IPv4 address (for public-subnet " +
					"databases), extracted from the nested public_ip object. Empty for private-subnet " +
					"databases (reachable only inside the VPC). Stable after create.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"port": schema.Int64Attribute{
				Computed: true,
				Description: "Connection port (3306 for MySQL/MariaDB, 5432 for PostgreSQL). Stable after " +
					"create.",
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"username": schema.StringAttribute{
				Computed:    true,
				Description: "Admin username (the server-created \"dbadmin\" account). Stable after create.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"role": schema.StringAttribute{
				Computed: true,
				Description: "Replication role: \"primary\" for a standalone/primary database. " +
					"Server-mutable (a replica promotion can change it).",
			},
			// last_error / error_acknowledged (T9) are SERVER-MUTABLE alert state: like
			// status, do NOT attach UseStateForUnknown, so a newly-appeared or newly-cleared
			// error is not masked by a stale prior value.
			"last_error": schema.StringAttribute{
				Computed: true,
				Description: "The most recent action failure recorded for this database (deploy, backup, " +
					"upgrade, resync, health check, ...), or empty when none is outstanding. Cleared by a " +
					"successful subsequent action, or explicitly via the acknowledge-error action " +
					"(AcknowledgeManagedDatabaseError in the client - implemented but not invoked by this " +
					"resource; see error_acknowledged). Server-mutable.",
			},
			"error_acknowledged": schema.BoolAttribute{
				Computed: true,
				Description: "Whether the last_error above has been acknowledged/dismissed. false while an " +
					"unacknowledged error is outstanding. Server-mutable.",
			},
			"password": schema.StringAttribute{
				Computed:  true,
				Sensitive: true,
				Description: "Cleartext admin password. The API NEVER returns the password on create or " +
					"read (it is encrypted and hidden server-side), so this is empty until you rotate it " +
					"by changing reset_password - the reset-password action returns the new password, which " +
					"is captured here. Marked sensitive so it is never shown in plan/CLI output.",
				// Server-only secret captured from the reset-password action - keep the
				// prior value stable when no rotation occurs (it is otherwise unreadable).
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
func (r *managedDatabaseResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create deploys the managed database and waits for it to become active:
//
//  1. CreateManagedDatabase records the row + backing instance and returns the
//     object WITH its id (status="deploying"). There is NO task_id - the async
//     signal is the DB's own status, polled via SHOW.
//  2. The id is saved into state BEFORE the wait, so a provisioning failure or
//     timeout still tracks the database for a subsequent destroy.
//  3. WaitFor polls GetManagedDatabase until status=="active" (fail on "error").
//  4. If reset_password is set, the password is rotated once so the password
//     attribute is populated on create.
//  5. GetManagedDatabase hydrates the computed fields; the immutable inputs are
//     echoed from the PLAN.
func (r *managedDatabaseResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan managedDatabaseModel
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
		"name":           plan.Name.ValueString(),
		"engine":         plan.Engine.ValueString(),
		"engine_version": plan.EngineVersion.ValueString(),
		"db_plan_id":     plan.DBPlanID.ValueString(),
		"vpc_id":         plan.VPCID.ValueString(),
		"vpc_subnet_id":  plan.VPCSubnetID.ValueString(),
	}
	if !plan.HypervisorGroupID.IsNull() && !plan.HypervisorGroupID.IsUnknown() && plan.HypervisorGroupID.ValueString() != "" {
		body["hypervisor_group_id"] = plan.HypervisorGroupID.ValueString()
	}

	created, err := r.client.CreateManagedDatabase(ctx, body)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating managed database", err))
		return
	}
	id, _ := created["id"].(string)
	if id == "" {
		resp.Diagnostics.AddError("Error creating managed database", "the create response did not include a managed database id")
		return
	}

	// Persist the id immediately so a failed provisioning/wait still tracks the
	// resource for cleanup on the next destroy.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// ── ASYNC convergence: poll SHOW until status="active" ───────────────────
	// Lifecycle deploying → active; "error" is the terminal deploy failure.
	// Tolerance=3 absorbs transient transport blips that bypass the 429/5xx retry.
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
			"Error waiting for managed database provisioning",
			fmt.Sprintf("managed database %s did not become active: %s", id, waitErr.Error()),
		)
		return
	}

	// Optionally rotate the password on create so the password attribute is set.
	password := types.StringValue("")
	if !plan.ResetPassword.IsNull() && !plan.ResetPassword.IsUnknown() && plan.ResetPassword.ValueString() != "" {
		pw, perr := r.rotatePassword(ctx, id)
		if perr != nil {
			resp.Diagnostics.Append(diagFromErr("Error setting managed database password", perr))
			return
		}
		password = pw
	}

	obj, err := r.client.GetManagedDatabase(ctx, id)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading managed database after provisioning", err))
		return
	}
	state := managedDatabaseStateFromAPI(obj, plan)
	state.Password = password
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Read refreshes state from the API. A 404 means the database was deleted out of
// band - remove it from state so Terraform plans a recreate. The reset_password
// trigger and the captured password are write-only/server-only and are preserved
// from prior state (SHOW never returns them).
func (r *managedDatabaseResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state managedDatabaseModel
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
		resp.Diagnostics.Append(diagFromErr("Error reading managed database", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, managedDatabaseStateFromAPI(obj, state))...)
}

// Update applies the in-place mutations: a resize (db_plan_id change) via the
// resize PATCH, an engine version upgrade (T9) when engine_version changes, a
// password rotation when reset_password changes, and a replica resync (T9) when
// resync_replicas changes. Every other input is RequiresReplace, so the
// framework recreates the resource for those.
func (r *managedDatabaseResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state managedDatabaseModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()

	// Resize in place when the plan changed.
	if !plan.DBPlanID.Equal(state.DBPlanID) {
		if _, err := r.client.ResizeManagedDatabase(ctx, id, map[string]any{"db_plan_id": plan.DBPlanID.ValueString()}); err != nil {
			resp.Diagnostics.Append(diagFromErr("Error resizing managed database", err))
			return
		}
	}

	// ── Engine version upgrade (T9) ──────────────────────────────────────────
	if !plan.EngineVersion.Equal(state.EngineVersion) {
		updateTimeout, diags := plan.Timeouts.Update(ctx, defaultUpdateTimeout)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		if err := r.upgradeEngineVersion(ctx, id, plan.EngineVersion.ValueString(), updateTimeout); err != nil {
			resp.Diagnostics.AddError("Error upgrading managed database version", err.Error())
			return
		}
	}

	// Rotate the password when the trigger token changed.
	password := state.Password
	if !plan.ResetPassword.Equal(state.ResetPassword) &&
		!plan.ResetPassword.IsNull() && !plan.ResetPassword.IsUnknown() && plan.ResetPassword.ValueString() != "" {
		pw, perr := r.rotatePassword(ctx, id)
		if perr != nil {
			resp.Diagnostics.Append(diagFromErr("Error resetting managed database password", perr))
			return
		}
		password = pw
	}

	// Resync replicas when the trigger token changed (T9). Only meaningful on a
	// primary that already has replicas; a rejection (no eligible replicas, not
	// active, ...) surfaces as a plain Diagnostics error like the other actions.
	if !plan.ResyncReplicas.Equal(state.ResyncReplicas) &&
		!plan.ResyncReplicas.IsNull() && !plan.ResyncReplicas.IsUnknown() && plan.ResyncReplicas.ValueString() != "" {
		if err := r.client.ResyncManagedDatabaseReplicas(ctx, id); err != nil {
			resp.Diagnostics.Append(diagFromErr("Error resyncing managed database replicas", err))
			return
		}
	}

	obj, err := r.client.GetManagedDatabase(ctx, id)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading managed database after update", err))
		return
	}
	newState := managedDatabaseStateFromAPI(obj, plan)
	newState.Password = password
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

// upgradeEngineVersion drives a user-initiated engine_version change: calls the
// upgrade action, then waits via the SAME async-status-poll primitive Create
// uses (StatePollerWithErrorTolerance on "status", ready="active",
// fail="error").
//
// IMPORTANT (documented concern, mirrors T7's k8s-upgrade caveat): unlike
// iaas_kubernetes_cluster, the managed-database UserApi SHOW does not
// eager-load a tasks[] relation, and ManagedDatabaseService::upgradeVersion
// returns no task_id - so there is no per-operation completion signal to poll
// for. The controller writes the target engine_version onto the row
// SYNCHRONOUSLY, the moment the hypervisor ACCEPTS the upgrade command, so by
// the time UpgradeManagedDatabase returns, GetManagedDatabase already reports
// the target version and status is essentially always still "active" (the
// upgrade call itself does not flip it). This wait therefore converges
// immediately in the overwhelming common case; it is kept (rather than
// skipped outright) so that (a) the resource still behaves correctly if a
// future Master release DOES flip status during the upgrade, and (b) a fast
// synchronous failure (hypervisor rejects the command, pre-upgrade backup
// fails) - which UpgradeManagedDatabase already surfaces as an error before
// this wait even starts - is not the only failure path. True completion of
// the upgrade running on the box is NOT independently observable through this
// API; a slave-side failure surfaces later as last_error/error_acknowledged on
// a subsequent read, not as a blocking error here.
func (r *managedDatabaseResource) upgradeEngineVersion(ctx context.Context, id, targetVersion string, timeout time.Duration) error {
	if err := r.client.UpgradeManagedDatabase(ctx, id, targetVersion); err != nil {
		return fmt.Errorf("starting version upgrade: %w", err)
	}

	return waiter.WaitFor(ctx, waiter.Options{
		Interval: pollInterval(),
		Timeout:  timeout,
		Refresh: waiter.StatePollerWithErrorTolerance(
			func() (map[string]any, error) { return r.client.GetManagedDatabase(ctx, id) },
			"status",
			[]string{"active"},
			[]string{"error"},
			3,
		),
	})
}

// Delete removes the managed database. DELETE flips status→"destroying", bills the
// final hours, destroys the backing instance (releasing its public IP), and
// soft-deletes the row, so a subsequent SHOW 404s. We poll GetManagedDatabase
// until it reports 404 (IsNotFound) as the convergence signal. A failure (e.g. a
// primary that still has replicas) surfaces as success:false from
// DeleteManagedDatabase.
func (r *managedDatabaseResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state managedDatabaseModel
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
		resp.Diagnostics.Append(diagFromErr("Error deleting managed database", err))
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
			"Error waiting for managed database deletion",
			fmt.Sprintf("managed database %s was not removed: %s", id, waitErr.Error()),
		)
		return
	}
}

// ImportState lets `terraform import iaas_managed_database.x <uuid>` adopt an
// existing database; the next Read populates the readable attributes. The
// write-only reset_password trigger and the captured password cannot be read
// back, so they are added to the lifecycle test's ImportStateVerifyIgnore.
func (r *managedDatabaseResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// rotatePassword invokes the reset-password action and returns the new cleartext
// password as a Sensitive types.String. The reset-password endpoint is the only
// one that returns a password.
func (r *managedDatabaseResource) rotatePassword(ctx context.Context, id string) (types.String, error) {
	res, err := r.client.ResetManagedDatabasePassword(ctx, id)
	if err != nil {
		return types.StringNull(), err
	}
	if pw, ok := res["password"].(string); ok && pw != "" {
		return types.StringValue(pw), nil
	}
	// Action succeeded but no password echoed - leave it empty rather than fail.
	return types.StringValue(""), nil
}

// managedDatabaseStateFromAPI builds the model from a SHOW managed_database object.
// The immutable inputs' authoritative value is the plan/state; the computed
// connection fields come from the API. host is extracted from the nested
// public_ip{ip} object; username maps from admin_user. The write-only
// reset_password trigger and the captured password are preserved verbatim from the
// prior model (SHOW never returns them).
func managedDatabaseStateFromAPI(obj map[string]any, prior managedDatabaseModel) managedDatabaseModel {
	return managedDatabaseModel{
		ID:            stringFromAPI(obj, "id", prior.ID),
		Name:          stringOrPrior(obj, "name", prior.Name),
		Engine:        stringOrPrior(obj, "engine", prior.Engine),
		EngineVersion: stringOrPrior(obj, "engine_version", prior.EngineVersion),
		DBPlanID:      stringOrPrior(obj, "db_plan_id", prior.DBPlanID),
		VPCID:         stringOrPrior(obj, "vpc_id", prior.VPCID),

		// vpc_subnet_id IS returned by SHOW; preserve prior when absent.
		VPCSubnetID:       optionalStringFromAPI(obj, "vpc_subnet_id", prior.VPCSubnetID),
		HypervisorGroupID: optionalStringFromAPI(obj, "hypervisor_group_id", prior.HypervisorGroupID),

		// Write-only triggers - never in SHOW; preserve prior verbatim.
		ResetPassword:  prior.ResetPassword,
		ResyncReplicas: prior.ResyncReplicas,

		// Computed read-only.
		Status:   stringFromAPI(obj, "status", prior.Status),
		Host:     nestedStringFromAPI(obj, "public_ip", "ip", prior.Host),
		Port:     int64FromAPI(obj, "port", prior.Port),
		Username: computedStringFromAPI(obj, "admin_user", prior.Username),
		Role:     dbRoleFromAPI(obj, prior.Role),

		// last_error/error_acknowledged (T9) are plain server-mutable columns
		// returned by SHOW like status - no write-only preservation needed.
		LastError:         optionalStringFromAPI(obj, "last_error", prior.LastError),
		ErrorAcknowledged: boolFromIntAPI(obj, "error_acknowledged", prior.ErrorAcknowledged),

		// password is captured from the reset-password action, never from SHOW -
		// preserve prior (the caller overrides it after a rotation).
		Password: prior.Password,

		Timeouts: prior.Timeouts,
	}
}

// dbRoleFromAPI reads the role field, defaulting an absent/empty value to
// "primary" (the server leaves role null for a standalone primary until a replica
// is attached). Kept known-after-apply for the Computed attribute.
func dbRoleFromAPI(obj map[string]any, fallback types.String) types.String {
	raw, ok := obj["role"]
	if ok && raw != nil {
		if s, ok := raw.(string); ok && s != "" {
			return types.StringValue(s)
		}
	}
	if !fallback.IsNull() && !fallback.IsUnknown() && fallback.ValueString() != "" {
		return fallback
	}
	return types.StringValue("primary")
}
