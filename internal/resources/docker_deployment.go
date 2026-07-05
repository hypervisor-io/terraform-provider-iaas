package resources

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/iaas/terraform-provider-iaas/internal/client"
	"github.com/iaas/terraform-provider-iaas/internal/waiter"
)

// Interface assertions - iaas_docker_deployment (Gap G2, the largest of the
// waves B/C gaps) manages a Docker app deployment on an instance:
//
//   - the parent instance_id lives in the URL path → Required + RequiresReplace;
//   - TWO create shapes share one resource, selected by `source`:
//     "app" (catalog app, POST .../docker, body app_slug) or "compose"
//     (POST .../docker/custom, body compose_url + app_name) - enforced by
//     ConfigValidators exactly like iaas_kubernetes_ssl_certificate's
//     source = "letsencrypt"|"custom" split;
//   - DOCKER-NOT-INSTALLED GATE: deploy/deployCustom 422 unless
//     instance.docker_enabled == 1. Create checks DockerEnabled first and,
//     if false, calls InstallDockerEngine then polls DockerEnabled to
//     converge (install is itself async and NOT idempotent - calling it
//     while already enabled 422s - hence the check-first, not call-always,
//     shape);
//   - ASYNC deploy: create returns the row synchronously (status
//     "deploying") and this resource waits for "running" (ready) or
//     "error"/"failed" (fail) via internal/waiter, mirroring
//     resources/instance.go;
//   - NO per-deployment SHOW route → Read lists within the instance and
//     matches by id (user_script.go pattern), synthesising a 404;
//   - NO update route beyond the control()/retry actions this v1
//     deliberately does not wire (see the Update doc comment) → every input
//     is RequiresReplace;
//   - env (env_variables) and port_mappings are WRITE-ONLY by policy: the
//     former because it is genuinely $hidden/encrypted server-side and never
//     returned, the latter by deliberate simplification (see docker.go);
//   - composite import "<instance_id>/<deployment_id>" (kubernetes_ssl_certificate.go
//     pattern).
var (
	_ resource.Resource                     = &dockerDeploymentResource{}
	_ resource.ResourceWithConfigure        = &dockerDeploymentResource{}
	_ resource.ResourceWithImportState      = &dockerDeploymentResource{}
	_ resource.ResourceWithConfigValidators = &dockerDeploymentResource{}
)

// NewDockerDeploymentResource is the resource constructor registered with the
// provider.
func NewDockerDeploymentResource() resource.Resource {
	return &dockerDeploymentResource{}
}

// dockerDeploymentResource manages an iaas_docker_deployment.
type dockerDeploymentResource struct {
	client *client.Client
}

// dockerPortMappingAttrTypes is the object schema for a single port mapping,
// reused for the types.List construction.
var dockerPortMappingAttrTypes = map[string]attr.Type{
	"container_port": types.Int64Type,
	"host_port":      types.Int64Type,
	"protocol":       types.StringType,
}

// dockerDeploymentModel maps the Terraform state/plan for
// iaas_docker_deployment.
//
//   - instance_id: parent, in the URL path → Required + RequiresReplace.
//   - source/slug/compose_url: create-shape selector + its two conditional
//     inputs, enforced by ConfigValidators. All Required-or-conditionally-
//     required + RequiresReplace (no update route).
//   - name: Optional+Computed - REQUIRED input for source = "compose" (sent
//     as app_name); FORBIDDEN for source = "app" (the server always derives
//     it from the catalog entry - allowing it there risks an
//     inconsistent-apply error the moment the server's resolved value
//     differs from a practitioner-supplied one, so ConfigValidators rejects
//     it outright rather than silently ignoring it).
//   - env/port_mappings: WRITE-ONLY (never refreshed from the API - see
//     docker.go) + RequiresReplace.
//   - status/project_name/error_message/deployed_at: server-managed computed.
type dockerDeploymentModel struct {
	ID         types.String `tfsdk:"id"`
	InstanceID types.String `tfsdk:"instance_id"`

	Source     types.String `tfsdk:"source"`
	Slug       types.String `tfsdk:"slug"`
	ComposeURL types.String `tfsdk:"compose_url"`
	Name       types.String `tfsdk:"name"`

	Env          types.Map  `tfsdk:"env"`
	PortMappings types.List `tfsdk:"port_mappings"`

	// Server-managed computed.
	Status       types.String `tfsdk:"status"`
	ProjectName  types.String `tfsdk:"project_name"`
	ErrorMessage types.String `tfsdk:"error_message"`
	DeployedAt   types.String `tfsdk:"deployed_at"`

	Timeouts timeouts.Value `tfsdk:"timeouts"`
}

// dockerPortMappingModel is the Go representation of a single port_mappings
// list element.
type dockerPortMappingModel struct {
	ContainerPort types.Int64  `tfsdk:"container_port"`
	HostPort      types.Int64  `tfsdk:"host_port"`
	Protocol      types.String `tfsdk:"protocol"`
}

// Metadata sets the resource type name → "<provider>_docker_deployment".
func (r *dockerDeploymentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_docker_deployment"
}

// Schema describes the iaas_docker_deployment resource.
func (r *dockerDeploymentResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a Docker app deployment on an instance - either a catalog app " +
			"(source = \"app\", via app_slug) or a custom Docker Compose deployment fetched from a " +
			"remote URL (source = \"compose\", via compose_url + name). If the instance does not yet " +
			"have the Docker engine installed, Create installs it automatically and waits for the " +
			"install to converge before deploying. Deployment itself is asynchronous: this resource " +
			"waits for the app to report \"running\" (or fails on \"error\"). There is no update route " +
			"beyond start/stop/restart/remove actions (not modelled in this version), so every input " +
			"forces a new resource. Import with a composite id: \"<instance_id>/<deployment_id>\".",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the deployment, assigned by the API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"instance_id": schema.StringAttribute{
				Required:    true,
				Description: "UUID of the instance to deploy onto. Changing this forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"source": schema.StringAttribute{
				Required: true,
				Description: "Deployment source: \"app\" (a catalog app, selected via `slug`) or " +
					"\"compose\" (a custom Compose file fetched from `compose_url`). Immutable; " +
					"changing it forces a new resource.",
				Validators: []validator.String{
					stringvalidator.OneOf("app", "compose"),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"slug": schema.StringAttribute{
				Optional: true,
				Description: "Catalog app slug (from the `iaas_docker_deployment` catalog). Required " +
					"when source = \"app\"; must be omitted for source = \"compose\". Immutable; " +
					"changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"compose_url": schema.StringAttribute{
				Optional: true,
				Description: "HTTPS URL of a docker-compose.yml to deploy. The compose file is FETCHED " +
					"SERVER-SIDE via an SSRF-guarded request - this is a URL, not literal compose " +
					"content. Required when source = \"compose\"; must be omitted for source = \"app\". " +
					"Immutable; changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"name": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "Display name of the deployment (sent as app_name). Required when source " +
					"= \"compose\"; for source = \"app\" the server always derives it from the catalog " +
					"entry and this attribute must be left unset (it becomes Computed once known). " +
					"Immutable; changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplaceIfConfigured(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"env": schema.MapAttribute{
				Optional:    true,
				Sensitive:   true,
				ElementType: types.StringType,
				Description: "Environment variables passed to the deployment. WRITE-ONLY: the API " +
					"stores this encrypted and never returns it on read, so it is always taken from " +
					"configuration and never refreshed. Immutable; changing it forces a new resource.",
				PlanModifiers: []planmodifier.Map{
					mapplanmodifier.RequiresReplace(),
				},
			},
			"port_mappings": schema.ListNestedAttribute{
				Optional: true,
				Description: "Host/container port overrides applied to the deployment's compose file. " +
					"WRITE-ONLY by policy (see the resource's package doc): always taken from " +
					"configuration and never refreshed from the API, even though the API technically " +
					"returns it. Immutable; changing it forces a new resource.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"container_port": schema.Int64Attribute{
							Required:    true,
							Description: "Container-side port (1-65535).",
						},
						"host_port": schema.Int64Attribute{
							Required:    true,
							Description: "Host-side port to map to (1-65535).",
						},
						"protocol": schema.StringAttribute{
							Optional:    true,
							Description: "\"tcp\" or \"udp\". Defaults server-side to \"tcp\" when omitted.",
						},
					},
				},
				PlanModifiers: []planmodifier.List{
					listplanmodifier.RequiresReplace(),
				},
			},

			// ── Server-managed computed ───────────────────────────────────────
			// status/error_message/deployed_at are SERVER-MUTABLE (the deployment
			// converges pending -> deploying -> running/error over time, and can
			// change again out-of-band via a control action). Per the golden
			// guardrail, do NOT attach UseStateForUnknown to any of them.
			"status": schema.StringAttribute{
				Computed: true,
				Description: "Deployment lifecycle status: \"pending\", \"deploying\", \"running\" " +
					"(ready), or \"error\"/\"failed\" (terminal failure). Server-mutable.",
			},
			"project_name": schema.StringAttribute{
				Computed:    true,
				Description: "Server-generated Docker Compose project name. Stable after create.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"error_message": schema.StringAttribute{
				Computed:    true,
				Description: "Error detail when status is \"error\"/\"failed\". Server-mutable.",
			},
			"deployed_at": schema.StringAttribute{
				Computed:    true,
				Description: "Timestamp the deployment last reported \"running\". Server-mutable.",
			},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{
				Create: true,
			}),
		},
	}
}

// ConfigValidators enforces, at plan time, the create-shape split the two
// deploy endpoints impose (mirrors kubernetes_ssl_certificate.go's
// source-conditional validator):
//   - source = "app": slug required; compose_url and name forbidden (name is
//     server-derived from the catalog and setting it risks an
//     inconsistent-apply error the moment the server's value differs).
//   - source = "compose": compose_url and name required; slug forbidden (the
//     custom-compose endpoint has no app_slug input).
func (r *dockerDeploymentResource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		&dockerDeploymentSourceValidator{},
	}
}

// dockerDeploymentSourceValidator implements resource.ConfigValidator.
type dockerDeploymentSourceValidator struct{}

func (v *dockerDeploymentSourceValidator) Description(_ context.Context) string {
	return "Enforces the source-conditional required/forbidden fields for iaas_docker_deployment " +
		"(mirrors the Master API's two distinct deploy endpoints)."
}

func (v *dockerDeploymentSourceValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v *dockerDeploymentSourceValidator) ValidateResource(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg dockerDeploymentModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if cfg.Source.IsUnknown() || cfg.Source.IsNull() {
		return
	}

	// Don't evaluate presence checks against an unknown value (e.g. derived
	// from another resource) - defer to a later validation pass.
	if cfg.Slug.IsUnknown() || cfg.ComposeURL.IsUnknown() || cfg.Name.IsUnknown() {
		return
	}

	hasSlug := !cfg.Slug.IsNull() && cfg.Slug.ValueString() != ""
	hasComposeURL := !cfg.ComposeURL.IsNull() && cfg.ComposeURL.ValueString() != ""
	hasName := !cfg.Name.IsNull() && cfg.Name.ValueString() != ""

	switch cfg.Source.ValueString() {
	case "app":
		if !hasSlug {
			resp.Diagnostics.AddAttributeError(path.Root("slug"), "Missing Required Field",
				`slug is required when source = "app".`)
		}
		if hasComposeURL {
			resp.Diagnostics.AddAttributeError(path.Root("compose_url"), "Invalid Field for source = \"app\"",
				`compose_url is only used when source = "compose"; the catalog-app deploy endpoint has no compose_url input.`)
		}
		if hasName {
			resp.Diagnostics.AddAttributeError(path.Root("name"), "Invalid Field for source = \"app\"",
				`name is server-derived from the catalog entry for source = "app" and cannot be set; omit it, or use source = "compose" to set a custom name.`)
		}
	case "compose":
		if !hasComposeURL {
			resp.Diagnostics.AddAttributeError(path.Root("compose_url"), "Missing Required Field",
				`compose_url is required when source = "compose".`)
		}
		if !hasName {
			resp.Diagnostics.AddAttributeError(path.Root("name"), "Missing Required Field",
				`name is required when source = "compose" (sent as app_name).`)
		}
		if hasSlug {
			resp.Diagnostics.AddAttributeError(path.Root("slug"), "Invalid Field for source = \"compose\"",
				`slug is only used when source = "app"; the custom-compose deploy endpoint has no app_slug input.`)
		}
	}
}

// Configure pulls the shared *client.Client from the provider (nil-guarded).
func (r *dockerDeploymentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create deploys the app in up to two phases and waits for convergence:
//
//  1. DockerEnabled checks whether the Docker engine is already installed on
//     the instance. If not, InstallDockerEngine kicks off the (asynchronous,
//     non-idempotent) install and this resource polls DockerEnabled until it
//     converges to true.
//  2. DeployDockerApp/DeployDockerCompose (chosen by `source`) creates the
//     deployment row synchronously (status "deploying") and returns its id.
//  3. The id is persisted to state BEFORE the deploy-convergence wait, so a
//     failure/timeout still tracks the (possibly still-deploying) row for a
//     subsequent destroy.
//  4. WaitFor polls GetDockerDeployment's status until "running" (fail on
//     "error"/"failed").
//  5. A final GetDockerDeployment hydrates the computed fields.
//
// The install wait (step 1) and the deploy wait (step 4) are BOTH bounded by
// a single createTimeout window overall, not createTimeout EACH: ctx is
// wrapped with an absolute deadline (now + createTimeout) up front, so the
// deploy wait's own waiter.WaitFor - which otherwise restarts a fresh
// createTimeout-long relative timer from whenever it is called, see
// internal/waiter.WaitFor - can never run past what the install wait already
// spent. Without this, an install-then-deploy Create could take up to ~2x
// createTimeout in the worst case (Docker not installed AND a slow deploy).
func (r *dockerDeploymentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan dockerDeploymentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	createTimeout, diags := plan.Timeouts.Create(ctx, defaultCreateTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Bound the ENTIRE Create (install wait + deploy wait combined) to
	// createTimeout via an absolute deadline on ctx. context.WithDeadline
	// composes correctly with each waiter's own context.WithTimeout: the
	// effective deadline any downstream call observes is always the EARLIER
	// of the two, so this caps total wall time without needing to compute a
	// remaining-time budget by hand for the second wait.
	ctx, cancel := context.WithDeadline(ctx, time.Now().Add(createTimeout))
	defer cancel()

	instanceID := plan.InstanceID.ValueString()

	// ── PHASE 0: ensure the Docker engine is installed ───────────────────────
	enabled, err := r.client.DockerEnabled(ctx, instanceID)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error checking Docker engine status", err))
		return
	}
	if !enabled {
		if _, err := r.client.InstallDockerEngine(ctx, instanceID); err != nil {
			resp.Diagnostics.AddError(
				"Error installing Docker engine",
				fmt.Sprintf("Docker is not installed on instance %s and the automatic install request "+
					"failed: %s. The instance must be a RUNNING Linux instance for the install to "+
					"succeed.", instanceID, err.Error()),
			)
			return
		}

		// The install itself completes asynchronously via an out-of-band
		// hypervisor/QGA callback; poll until instance.docker_enabled flips.
		installErr := waiter.WaitFor(ctx, waiter.Options{
			Interval: pollInterval(),
			Timeout:  createTimeout,
			Refresh: func() (string, bool, error) {
				ok, err := r.client.DockerEnabled(ctx, instanceID)
				if err != nil {
					return "", false, err
				}
				if ok {
					return "installed", true, nil
				}
				return "installing", false, nil
			},
		})
		if installErr != nil {
			resp.Diagnostics.AddError(
				"Error waiting for Docker engine install",
				fmt.Sprintf("instance %s did not report docker_enabled after installing Docker: %s",
					instanceID, installErr.Error()),
			)
			return
		}
	}

	// ── PHASE 1: deploy (app or compose) ─────────────────────────────────────
	body, diags := dockerDeployBody(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	var created map[string]any
	if plan.Source.ValueString() == "compose" {
		created, err = r.client.DeployDockerCompose(ctx, instanceID, body)
	} else {
		created, err = r.client.DeployDockerApp(ctx, instanceID, body)
	}
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deploying Docker app", err))
		return
	}
	id, _ := created["id"].(string)
	if id == "" {
		resp.Diagnostics.AddError(
			"Error deploying Docker app",
			"the deploy response did not include a deployment id",
		)
		return
	}

	// Persist the id immediately so a failed/timed-out convergence still
	// tracks the resource for cleanup on the next destroy.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// ── PHASE 2: wait for the deployment to converge ─────────────────────────
	// Tolerance=3: up to 3 consecutive transport blips are silently skipped so
	// a brief network hiccup during a long deploy does not abort the create.
	waitErr := waiter.WaitFor(ctx, waiter.Options{
		Interval: pollInterval(),
		Timeout:  createTimeout,
		Refresh: waiter.StatePollerWithErrorTolerance(
			func() (map[string]any, error) { return r.client.GetDockerDeployment(ctx, instanceID, id) },
			"status",
			[]string{"running"},
			[]string{"error", "failed"},
			3,
		),
	})
	if waitErr != nil {
		resp.Diagnostics.AddError(
			"Error waiting for Docker deployment",
			fmt.Sprintf("deployment %s on instance %s did not become running: %s", id, instanceID, waitErr.Error()),
		)
		return
	}

	obj, err := r.client.GetDockerDeployment(ctx, instanceID, id)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading Docker deployment after deploy", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, dockerDeploymentStateFromAPI(obj, plan))...)
}

// Read refreshes state via LIST-and-match (no per-deployment SHOW). A
// not-found (deployment absent from the instance's list) removes the resource
// from state so Terraform plans a recreate.
func (r *dockerDeploymentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state dockerDeploymentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	obj, err := r.client.GetDockerDeployment(ctx, state.InstanceID.ValueString(), state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("Error reading Docker deployment", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, dockerDeploymentStateFromAPI(obj, state))...)
}

// Update is unreachable in practice: every attribute is RequiresReplace, so
// Terraform Core forces a replacement instead of calling Update for any
// configuration change. Implemented only to satisfy resource.Resource.
//
// DELIBERATE v1 SCOPE DECISION: the API's only other mutation surface is the
// control(start|stop|restart|remove) action and retry - these are
// operational actions, not declarative attribute state, and wiring a
// `desired_state` attribute to drive them adds meaningful complexity (the
// one-active-deployment-at-a-time guard on the Master interacts awkwardly
// with a stop/start cycle). Kept out of v1; a future iteration could add a
// `desired_state` (running|stopped) attribute that calls ControlDockerDeployment
// on change, without touching the RequiresReplace inputs here.
func (r *dockerDeploymentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan dockerDeploymentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

// Delete removes the deployment. DELETE .../docker/{depID} is synchronous
// server-side (the row is hard-deleted right after the fire-and-forget
// hypervisor removal command is enqueued), so no delete-side waiter is needed.
func (r *dockerDeploymentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state dockerDeploymentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteDockerDeployment(ctx, state.InstanceID.ValueString(), state.ID.ValueString()); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deleting Docker deployment", err))
		return
	}
}

// ImportState implements COMPOSITE import for this child resource:
//
//	terraform import iaas_docker_deployment.x <instance_id>/<deployment_id>
//
// source/slug/compose_url/name are recovered from the read-back (see
// dockerDeploymentStateFromAPI's derivation helpers). env/port_mappings
// cannot be recovered (write-only/policy-preserved - see docker.go) and land
// null; set them in configuration after importing if they matter (they have
// no update path, so leaving them unset is otherwise harmless post-import).
func (r *dockerDeploymentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	instanceID, depID, ok := strings.Cut(req.ID, "/")
	if !ok || instanceID == "" || depID == "" {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Expected import ID in the format \"instance_id/deployment_id\", got: %q. "+
				"Docker deployments are child resources, so both the parent instance id and the "+
				"deployment id are required to import.", req.ID),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("instance_id"), instanceID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), depID)...)
}

// dockerDeployBody builds the deploy/deployCustom request body from the plan.
// Only the fields relevant to the selected `source` are populated, matching
// DeployRequest/DeployCustomRequest's own field sets exactly.
func dockerDeployBody(ctx context.Context, plan dockerDeploymentModel) (map[string]any, diag.Diagnostics) {
	var diags diag.Diagnostics
	body := map[string]any{}

	switch plan.Source.ValueString() {
	case "compose":
		body["compose_url"] = plan.ComposeURL.ValueString()
		body["app_name"] = plan.Name.ValueString()
	default: // "app"
		body["app_slug"] = plan.Slug.ValueString()
	}

	if !plan.Env.IsNull() && !plan.Env.IsUnknown() {
		envMap, d := parametersToAPIMap(ctx, plan.Env)
		diags.Append(d...)
		if len(envMap) > 0 {
			body["env_variables"] = envMap
		}
	}

	pm, d := portMappingsToAPI(ctx, plan.PortMappings)
	diags.Append(d...)
	if len(pm) > 0 {
		body["port_mappings"] = pm
	}

	return body, diags
}

// portMappingsToAPI converts the port_mappings types.List to a
// []map[string]any for the API. A null/unknown/empty list yields nil so the
// key is omitted from the request body entirely.
func portMappingsToAPI(ctx context.Context, l types.List) ([]map[string]any, diag.Diagnostics) {
	if l.IsNull() || l.IsUnknown() {
		return nil, nil
	}
	var items []dockerPortMappingModel
	d := l.ElementsAs(ctx, &items, false)
	if d.HasError() {
		return nil, d
	}
	if len(items) == 0 {
		return nil, nil
	}
	out := make([]map[string]any, 0, len(items))
	for _, p := range items {
		entry := map[string]any{
			"container_port": p.ContainerPort.ValueInt64(),
			"host_port":      p.HostPort.ValueInt64(),
		}
		if !p.Protocol.IsNull() && !p.Protocol.IsUnknown() && p.Protocol.ValueString() != "" {
			entry["protocol"] = p.Protocol.ValueString()
		}
		out = append(out, entry)
	}
	return out, nil
}

// dockerDeploymentStateFromAPI builds the model from an API deployment object
// (the create response or the LIST scan). instance_id/env/port_mappings are
// never reliably re-derivable from the response (either genuinely absent -
// env/port_mappings are policy-preserved, see docker.go - or a RequiresReplace
// input whose authoritative value is the plan/prior state), so they always
// fall back to the prior model. name IS read back (app_name is a plain,
// always-populated column for both source shapes) so the server-derived
// value for source = "app" surfaces correctly. source/slug/compose_url are
// DERIVED from the persisted app_slug/metadata columns rather than preserved
// blindly, so `terraform import` recovers them (see the three helpers below).
func dockerDeploymentStateFromAPI(obj map[string]any, prior dockerDeploymentModel) dockerDeploymentModel {
	return dockerDeploymentModel{
		ID:         stringFromAPI(obj, "id", prior.ID),
		InstanceID: stringOrPrior(obj, "instance_id", prior.InstanceID),

		Source:     dockerSourceFromAPI(obj, prior.Source),
		Slug:       dockerSlugFromAPI(obj, prior.Slug),
		ComposeURL: dockerComposeURLFromAPI(obj, prior.ComposeURL),
		Name:       stringFromAPI(obj, "app_name", prior.Name),

		// WRITE-ONLY by policy - never in the response, or deliberately never
		// round-tripped; preserve prior/plan verbatim.
		Env:          prior.Env,
		PortMappings: prior.PortMappings,

		Status:       stringFromAPI(obj, "status", prior.Status),
		ProjectName:  stringFromAPI(obj, "project_name", prior.ProjectName),
		ErrorMessage: optionalStringFromAPI(obj, "error_message", prior.ErrorMessage),
		DeployedAt:   optionalStringFromAPI(obj, "deployed_at", prior.DeployedAt),

		Timeouts: prior.Timeouts,
	}
}

// dockerSourceFromAPI derives the write-input `source` from the persisted
// `app_slug` column, since the API never echoes a "source" field back:
// deployCustom() hardcodes app_slug = "custom" for every compose deployment,
// while deploy() always stores the real catalog slug. An absent/null/empty
// app_slug falls back to the prior value (defensive; the column is NOT NULL
// in practice). EDGE CASE: a catalog app whose own slug happens to be
// "custom" would misclassify as source = "compose" on import/read - accepted
// as a narrow, documented limitation (catalog slugs are operator-controlled).
func dockerSourceFromAPI(obj map[string]any, fallback types.String) types.String {
	raw, ok := obj["app_slug"]
	if !ok || raw == nil {
		return fallback
	}
	slug, ok := raw.(string)
	if !ok || slug == "" {
		return fallback
	}
	if slug == "custom" {
		return types.StringValue("compose")
	}
	return types.StringValue("app")
}

// dockerSlugFromAPI reads back the catalog slug for a source = "app"
// deployment. For a source = "compose" deployment app_slug is always the
// literal string "custom" (not a real user input), so that sentinel - like an
// absent/null/empty value - falls back to the prior value (null, since
// compose deployments never set slug).
func dockerSlugFromAPI(obj map[string]any, fallback types.String) types.String {
	raw, ok := obj["app_slug"]
	if !ok || raw == nil {
		return fallback
	}
	slug, ok := raw.(string)
	if !ok || slug == "" || slug == "custom" {
		return fallback
	}
	return types.StringValue(slug)
}

// dockerComposeURLFromAPI reads back compose_url for a source = "compose"
// deployment from the persisted `metadata.compose_url` field (deployCustom is
// the only path that ever sets it). Absent for source = "app" deployments
// (metadata is null there), so it falls back to the prior value (null, since
// app deployments never set compose_url).
func dockerComposeURLFromAPI(obj map[string]any, fallback types.String) types.String {
	meta, ok := obj["metadata"].(map[string]any)
	if !ok {
		return fallback
	}
	url, ok := meta["compose_url"].(string)
	if !ok || url == "" {
		return fallback
	}
	return types.StringValue(url)
}
