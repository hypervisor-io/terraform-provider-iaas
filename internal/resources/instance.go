package resources

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/iaas/terraform-provider-iaas/internal/client"
	"github.com/iaas/terraform-provider-iaas/internal/waiter"
)

// Interface assertions — instance is the GOLDEN ASYNC resource. It establishes
// the pattern every later async resource (load_balancer, managed_database,
// kubernetes_cluster) copies:
//   - TWO-PHASE create (record row → deploy OS) with a task-poll waiter,
//   - WRITE-ONLY deploy fields the SHOW endpoint cannot return,
//   - a Sensitive computed field (vnc_password),
//   - a timeouts nested block (create/update/delete),
//   - DELETE convergence by polling SHOW until 404.
var (
	_ resource.Resource                = &instanceResource{}
	_ resource.ResourceWithConfigure   = &instanceResource{}
	_ resource.ResourceWithImportState = &instanceResource{}
)

// Default timeouts. Create/delete are async (deploy task convergence, then slave
// finalization), so they get the longer default; update is a synchronous PATCH.
const (
	defaultCreateTimeout = 30 * time.Minute
	defaultUpdateTimeout = 10 * time.Minute
	defaultDeleteTimeout = 30 * time.Minute

	// defaultPollInterval is the base poll interval for both the create
	// (task→completed) and delete (SHOW→404) waiters. It is overridable via the
	// IAAS_INSTANCE_POLL_INTERVAL env var purely as a TEST-ONLY knob so the
	// mock-backed lifecycle test does not sleep between polls (see pollInterval).
	defaultPollInterval = 5 * time.Second
)

// pollInterval returns the waiter poll interval. It reads IAAS_INSTANCE_POLL_INTERVAL
// (a Go duration string such as "1ms") when set, falling back to
// defaultPollInterval. This is a TEST-ONLY seam: the mock lifecycle test sets it
// tiny so convergence is instant. An unset/invalid value yields the 5s default,
// so production behaviour is unchanged.
func pollInterval() time.Duration {
	if v := os.Getenv("IAAS_INSTANCE_POLL_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return defaultPollInterval
}

// NewInstanceResource is the resource constructor registered with the provider.
func NewInstanceResource() resource.Resource {
	return &instanceResource{}
}

// instanceResource manages an iaas_instance (a Cloud Service VM).
type instanceResource struct {
	client *client.Client
}

// instanceModel maps the Terraform state/plan for iaas_instance.
//
// Three groups of fields:
//   - REPLACE inputs (location_id, plan_id, image_id, vpc_id, vpc_subnet_id):
//     immutable; changing any forces a new instance.
//   - WRITE-ONLY deploy inputs (SSHKeys, Timezone, Cloudcfg): consumed by the
//     phase-2 deploy call and NEVER returned by SHOW. Create echoes the plan into
//     state; Read deliberately preserves the prior values so they don't drift.
//   - server-managed metadata/computed (everything else).
type instanceModel struct {
	ID          types.String `tfsdk:"id"`
	LocationID  types.String `tfsdk:"location_id"`
	PlanID      types.String `tfsdk:"plan_id"`
	ImageID     types.String `tfsdk:"image_id"`
	VPCID       types.String `tfsdk:"vpc_id"`
	VPCSubnetID types.String `tfsdk:"vpc_subnet_id"`

	// Write-only deploy fields (not returned by SHOW).
	SSHKeys  types.List   `tfsdk:"ssh_keys"`
	Timezone types.String `tfsdk:"timezone"`
	Cloudcfg types.String `tfsdk:"cloudcfg"`

	// Metadata (mutable via PATCH; Computed because the server may auto-assign).
	Hostname    types.String `tfsdk:"hostname"`
	DisplayName types.String `tfsdk:"display_name"`

	// Computed read-only.
	CPUCores         types.Int64  `tfsdk:"cpu_cores"`
	RAM              types.Int64  `tfsdk:"ram"`
	Deployed         types.Bool   `tfsdk:"deployed"`
	Status           types.Int64  `tfsdk:"status"`
	PrimaryPublicIP  types.String `tfsdk:"primary_public_ip"`
	PrimaryPrivateIP types.String `tfsdk:"primary_private_ip"`
	VNCPassword      types.String `tfsdk:"vnc_password"`

	Timeouts timeouts.Value `tfsdk:"timeouts"`
}

// Metadata sets the resource type name → "<provider>_instance" → "iaas_instance".
func (r *instanceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_instance"
}

// Schema describes the iaas_instance resource.
func (r *instanceResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a Cloud Service virtual machine instance. Creation is two-phase: " +
			"the instance record is created synchronously, then the OS is deployed " +
			"asynchronously via a platform task that this resource waits on. The plan, " +
			"location, image, and network placement are immutable (changing any forces a " +
			"new instance); only the hostname and display_name can be changed in place.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the instance, assigned by the API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"location_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the deploy location (hypervisor group). The plan_id must be " +
					"offered at this location. Changing this forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"plan_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the instance plan (CPU/RAM/storage template). Must be offered " +
					"at location_id. Changing this forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"image_id": schema.StringAttribute{
				Required: true,
				Description: "UUID of the OS image deployed onto the instance. Changing the image is " +
					"a destructive reinstall, so changing this forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"vpc_id": schema.StringAttribute{
				Optional: true,
				Description: "Optional UUID of a VPC to attach the instance to at create time. " +
					"Changing this forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"vpc_subnet_id": schema.StringAttribute{
				Optional: true,
				Description: "Optional UUID of a VPC subnet to place the instance in. Changing this " +
					"forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"ssh_keys": schema.ListAttribute{
				ElementType: types.StringType,
				Optional:    true,
				Description: "Optional list of SSH key UUIDs injected at deploy time. WRITE-ONLY: " +
					"the API does not return the keys on read, so this value is echoed from " +
					"configuration and never refreshed. Changing it forces a new resource.",
				PlanModifiers: []planmodifier.List{
					listplanmodifier.RequiresReplace(),
				},
			},
			"timezone": schema.StringAttribute{
				Optional: true,
				Description: "Optional timezone applied at deploy time (e.g. \"UTC\"). WRITE-ONLY: " +
					"not returned by the API on read. Changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"cloudcfg": schema.StringAttribute{
				Optional: true,
				Description: "Optional cloud-init user-data (YAML) applied at deploy time. WRITE-ONLY: " +
					"not returned by the API on read. Changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"hostname": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "Hostname of the instance. The server auto-generates one when omitted. " +
					"This field is updatable in place (PATCH) and is NOT RequiresReplace.",
			},
			"display_name": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "Display name of the instance. Server-assigned when omitted; updatable " +
					"in place (PATCH).",
			},
			"cpu_cores": schema.Int64Attribute{
				Computed:    true,
				Description: "Number of vCPU cores, derived from the plan. Stable after create.",
				// Stable-after-create computed → UseStateForUnknown is safe.
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"ram": schema.Int64Attribute{
				Computed:    true,
				Description: "RAM in megabytes, derived from the plan. Stable after create.",
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			// deployed / status are SERVER-MUTABLE computed fields: a stop, suspend,
			// or admin action changes them over the instance's life. Per the golden
			// guardrail, do NOT attach UseStateForUnknown — that would copy the stale
			// prior value into the plan and MASK real drift. Omitting it lets the plan
			// reflect the server's refreshed value.
			"deployed": schema.BoolAttribute{
				Computed: true,
				Description: "Whether the OS has been deployed (the API's int 0/1 mapped to a bool). " +
					"Server-mutable.",
			},
			"status": schema.Int64Attribute{
				Computed: true,
				Description: "Power/lifecycle status code (0 stopped, 1 running, 2 suspended). " +
					"Server-mutable.",
			},
			"primary_public_ip": schema.StringAttribute{
				Computed:    true,
				Description: "Primary public IPv4 address, extracted from the appended IP object.",
				// Stable after create (the primary IP allocation is fixed at deploy).
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"primary_private_ip": schema.StringAttribute{
				Computed:    true,
				Description: "Primary private IPv4 address, extracted from the appended IP object.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"vnc_password": schema.StringAttribute{
				Computed:  true,
				Sensitive: true,
				Description: "Cleartext VNC console password (server force-generated). Marked " +
					"sensitive so it is never shown in plan/CLI output.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
		Blocks: map[string]schema.Block{
			// The timeouts nested block (create/update/delete) is the async-resource
			// timeouts pattern later resources copy. Defaults are applied in the
			// CRUD methods (Create/Update/Delete) via the typed default constants.
			"timeouts": timeouts.Block(ctx, timeouts.Opts{
				Create: true,
				Update: true,
				Delete: true,
			}),
		},
	}
}

// Configure pulls the shared *client.Client from the provider. It tolerates a
// nil ProviderData (the framework calls Configure once with nil data before the
// provider's own Configure has run).
func (r *instanceResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create provisions the instance in TWO PHASES and waits for the deploy task to
// converge:
//
//  1. CreateCSInstance records the row synchronously and returns the id.
//  2. DeployInstance deploys the OS and returns a task_id.
//  3. The id is saved into state BEFORE the wait, so a deploy-task failure or
//     timeout still tracks the half-created instance and a subsequent destroy can
//     clean it up.
//  4. WaitFor polls the task until status=="completed" (fail on "failed").
//  5. GetInstance hydrates all computed fields; the write-only deploy fields and
//     the RequiresReplace inputs are echoed from the PLAN (SHOW cannot return them).
func (r *instanceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan instanceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	createTimeout, diags := plan.Timeouts.Create(ctx, defaultCreateTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// ── PHASE 1: record the instance row (sync, returns id) ──────────────────
	phase1 := map[string]any{
		"location_id": plan.LocationID.ValueString(),
		"plan_id":     plan.PlanID.ValueString(),
	}
	if !plan.VPCID.IsNull() && !plan.VPCID.IsUnknown() {
		phase1["vpc_id"] = plan.VPCID.ValueString()
	}
	if !plan.VPCSubnetID.IsNull() && !plan.VPCSubnetID.IsUnknown() {
		phase1["vpc_subnet_id"] = plan.VPCSubnetID.ValueString()
	}
	if !plan.Hostname.IsNull() && !plan.Hostname.IsUnknown() {
		phase1["hostname"] = plan.Hostname.ValueString()
	}

	created, err := r.client.CreateCSInstance(ctx, phase1)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating instance", err))
		return
	}
	id, _ := created["id"].(string)
	if id == "" {
		resp.Diagnostics.AddError(
			"Error creating instance",
			"phase 1 create response did not include an instance id",
		)
		return
	}

	// Persist the id immediately so a failed deploy/wait still tracks the
	// resource for cleanup on the next destroy.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// ── PHASE 2: deploy the OS (async, returns task_id) ──────────────────────
	phase2 := map[string]any{
		"image_id": plan.ImageID.ValueString(),
	}
	if keys := stringListValues(plan.SSHKeys); keys != nil {
		phase2["ssh_keys"] = keys
	}
	if !plan.Hostname.IsNull() && !plan.Hostname.IsUnknown() {
		phase2["hostname"] = plan.Hostname.ValueString()
	}
	if !plan.Timezone.IsNull() && !plan.Timezone.IsUnknown() {
		phase2["timezone"] = plan.Timezone.ValueString()
	}
	if !plan.Cloudcfg.IsNull() && !plan.Cloudcfg.IsUnknown() {
		phase2["cloudcfg"] = plan.Cloudcfg.ValueString()
	}

	deployResp, err := r.client.DeployInstance(ctx, id, phase2)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deploying instance", err))
		return
	}
	taskID, _ := deployResp["task_id"].(string)
	if taskID == "" {
		resp.Diagnostics.AddError(
			"Error deploying instance",
			"deploy response did not include a task_id",
		)
		return
	}

	// ── ASYNC convergence: poll the deploy task until completed ──────────────
	// Tolerance=3: up to 3 consecutive transport blips (connection reset, i/o
	// timeout, etc.) are silently skipped so a brief network hiccup during a
	// long deploy does not abort the whole create. The HTTP client only retries
	// 429/5xx; raw transport errors reach here directly, hence the extra guard.
	waitErr := waiter.WaitFor(ctx, waiter.Options{
		Interval: pollInterval(),
		Timeout:  createTimeout,
		Refresh: waiter.StatePollerWithErrorTolerance(
			func() (map[string]any, error) { return r.client.GetInstanceTask(ctx, id, taskID) },
			"status",
			[]string{"completed"},
			[]string{"failed"},
			3,
		),
	})
	if waitErr != nil {
		// The id is already in state, so the user can `terraform destroy` to
		// clean up the half-created instance.
		resp.Diagnostics.AddError(
			"Error waiting for instance deploy",
			fmt.Sprintf("instance %s deploy task %s did not complete: %s", id, taskID, waitErr.Error()),
		)
		return
	}

	// ── hydrate state from the now-deployed instance ─────────────────────────
	obj, err := r.client.GetInstance(ctx, id)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading instance after deploy", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, instanceStateFromAPI(obj, plan))...)
}

// Read refreshes state from the API. A 404 means the instance was deleted out of
// band — remove it from state so Terraform plans a recreate (drift handling).
//
// The write-only deploy fields (ssh_keys/timezone/cloudcfg) are NOT in the SHOW
// payload, so instanceStateFromAPI preserves their prior state values to avoid
// perpetual drift.
func (r *instanceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state instanceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	obj, err := r.client.GetInstance(ctx, state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("Error reading instance", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, instanceStateFromAPI(obj, state))...)
}

// Update changes the only mutable fields — display_name and hostname. Everything
// else is RequiresReplace, so only those two ever reach here. We PATCH the
// changed fields then GetInstance to refresh, since the PATCH response is a
// thinner envelope than SHOW.
func (r *instanceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state instanceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	fields := map[string]any{}
	if !plan.DisplayName.Equal(state.DisplayName) {
		fields["display_name"] = plan.DisplayName.ValueString()
	}
	if !plan.Hostname.Equal(state.Hostname) {
		fields["hostname"] = plan.Hostname.ValueString()
	}

	if len(fields) > 0 {
		if _, err := r.client.UpdateInstance(ctx, state.ID.ValueString(), fields); err != nil {
			resp.Diagnostics.Append(diagFromErr("Error updating instance", err))
			return
		}
	}

	// Refresh via SHOW (full model) to rehydrate computed fields.
	obj, err := r.client.GetInstance(ctx, state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading instance after update", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, instanceStateFromAPI(obj, plan))...)
}

// Delete removes the instance. DELETE is asynchronous (the slave finalizes and
// the row soft-deletes later), so after the enqueue we poll GetInstance until it
// reports 404 (IsNotFound) — that is the convergence signal. A protection_enabled
// failure surfaces as an error from DeleteCSInstance (success:false at 200).
func (r *instanceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state instanceModel
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
	if err := r.client.DeleteCSInstance(ctx, id); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deleting instance", err))
		return
	}

	// Converge by polling SHOW until it 404s. The Refresh closure treats an
	// IsNotFound error as "done", and any other error as terminal.
	waitErr := waiter.WaitFor(ctx, waiter.Options{
		Interval: pollInterval(),
		Timeout:  deleteTimeout,
		Refresh: func() (string, bool, error) {
			_, err := r.client.GetInstance(ctx, id)
			if err != nil {
				if client.IsNotFound(err) {
					return "deleted", true, nil
				}
				return "", false, err
			}
			return "deleting", false, nil
		},
	})
	if waitErr != nil {
		resp.Diagnostics.AddError(
			"Error waiting for instance deletion",
			fmt.Sprintf("instance %s was not removed: %s", id, waitErr.Error()),
		)
		return
	}
}

// ImportState lets `terraform import iaas_instance.x <uuid>` adopt an existing
// instance; the next Read populates the readable attributes. The write-only
// deploy fields (ssh_keys/timezone/cloudcfg) cannot be read back, so they are
// added to the lifecycle test's ImportStateVerifyIgnore.
func (r *instanceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// instanceStateFromAPI builds the model from a SHOW instance object. Computed and
// metadata fields come from the API; the RequiresReplace inputs and WRITE-ONLY
// deploy fields are preserved from the prior model (plan on create/update, state
// on read) because SHOW does not return them — this is what keeps ssh_keys /
// timezone / cloudcfg drift-free.
func instanceStateFromAPI(obj map[string]any, prior instanceModel) instanceModel {
	return instanceModel{
		ID: stringFromAPI(obj, "id", prior.ID),

		// RequiresReplace inputs — SHOW may or may not echo them; preserve plan.
		LocationID:  stringOrPrior(obj, "location_id", prior.LocationID),
		PlanID:      stringOrPrior(obj, "plan_id", prior.PlanID),
		ImageID:     stringOrPrior(obj, "image_id", prior.ImageID),
		VPCID:       optionalStringFromAPI(obj, "vpc_id", prior.VPCID),
		VPCSubnetID: optionalStringFromAPI(obj, "vpc_subnet_id", prior.VPCSubnetID),

		// WRITE-ONLY deploy fields — never in SHOW; preserve prior verbatim.
		SSHKeys:  prior.SSHKeys,
		Timezone: prior.Timezone,
		Cloudcfg: prior.Cloudcfg,

		// Metadata.
		Hostname:    stringFromAPI(obj, "hostname", prior.Hostname),
		DisplayName: stringFromAPI(obj, "display_name", prior.DisplayName),

		// Computed read-only.
		CPUCores:         int64FromAPI(obj, "cpu_cores", prior.CPUCores),
		RAM:              int64FromAPI(obj, "ram", prior.RAM),
		Deployed:         boolFromIntAPI(obj, "deployed", prior.Deployed),
		Status:           int64FromAPI(obj, "status", prior.Status),
		PrimaryPublicIP:  nestedStringFromAPI(obj, "primary_public_ip", "ip", prior.PrimaryPublicIP),
		PrimaryPrivateIP: nestedStringFromAPI(obj, "primary_private_ip", "ip", prior.PrimaryPrivateIP),
		VNCPassword:      stringFromAPI(obj, "vnc_password", prior.VNCPassword),

		Timeouts: prior.Timeouts,
	}
}

// stringOrPrior reads a string field but, unlike stringFromAPI, falls back to the
// prior value when the key is present-but-null (not just absent). Used for the
// RequiresReplace inputs whose authoritative value is the plan, not SHOW.
func stringOrPrior(obj map[string]any, key string, fallback types.String) types.String {
	raw, ok := obj[key]
	if !ok || raw == nil {
		return fallback
	}
	if s, ok := raw.(string); ok && s != "" {
		return types.StringValue(s)
	}
	return fallback
}

// boolFromIntAPI maps an API integer flag (0/1) — or a native bool — to a
// types.Bool. An absent/unrecognised value falls back to the prior value.
func boolFromIntAPI(obj map[string]any, key string, fallback types.Bool) types.Bool {
	raw, ok := obj[key]
	if !ok || raw == nil {
		return fallback
	}
	switch v := raw.(type) {
	case bool:
		return types.BoolValue(v)
	case float64:
		return types.BoolValue(v != 0)
	case int:
		return types.BoolValue(v != 0)
	case int64:
		return types.BoolValue(v != 0)
	default:
		return fallback
	}
}

// nestedStringFromAPI extracts a string sub-field (e.g. "ip") from an appended
// nested object (e.g. primary_public_ip{ip:"…"}). A missing parent, null, or
// missing sub-field falls back to the prior value, so the appended IP objects —
// which are absent before deploy completes — never crash mapping.
//
// Because these map to COMPUTED attributes, the value must be KNOWN after apply:
// if the field is absent and the prior value is null/unknown (the first-create
// case), it resolves to "" rather than leaking an unknown into state.
func nestedStringFromAPI(obj map[string]any, parent, sub string, fallback types.String) types.String {
	// "" means "absent or empty by design" for these Computed string fields —
	// required for known-after-apply. Later async resources copying this pattern
	// should note that an empty string is indistinguishable from a genuinely-absent
	// value; use a sentinel (e.g. "none") if the distinction matters.
	settle := func(v types.String) types.String {
		if v.IsNull() || v.IsUnknown() {
			return types.StringValue("")
		}
		return v
	}
	raw, ok := obj[parent]
	if !ok || raw == nil {
		return settle(fallback)
	}
	nested, ok := raw.(map[string]any)
	if !ok {
		return settle(fallback)
	}
	v, ok := nested[sub]
	if !ok || v == nil {
		return settle(fallback)
	}
	if s, ok := v.(string); ok {
		return types.StringValue(s)
	}
	return types.StringValue(fmt.Sprintf("%v", v))
}

// stringListValues flattens a types.List of strings into a []string suitable for
// the deploy body. A null/unknown/empty list returns nil so the key is omitted
// (the server then injects no keys) rather than sent as an empty array.
func stringListValues(l types.List) []string {
	if l.IsNull() || l.IsUnknown() {
		return nil
	}
	elems := l.Elements()
	if len(elems) == 0 {
		return nil
	}
	out := make([]string, 0, len(elems))
	for _, e := range elems {
		if s, ok := e.(types.String); ok && !s.IsNull() && !s.IsUnknown() {
			out = append(out, s.ValueString())
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
