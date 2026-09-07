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

var (
	_ resource.Resource              = &microvmAppResource{}
	_ resource.ResourceWithConfigure = &microvmAppResource{}
)

// NewMicrovmAppResource is the resource constructor registered with the provider.
func NewMicrovmAppResource() resource.Resource {
	return &microvmAppResource{}
}

// microvmAppResource manages a Serverless App: container-image-or-git-repo
// scale-to-zero microVM app. Redeploying (a changed `source`) goes through
// the create-a-revision-then-deploy flow server-side; Update() here re-calls
// CreateApp's create-revision-and-deploy path implicitly by issuing a new
// deploy against the freshly built revision id the API returns.
//
// Route summary (MV2-25 contract):
//
//	INDEX    GET    /microvm/apps                -> {success,apps:{data:[...]}}
//	SHOW     GET    /microvm/app/{id}             -> {success,app:{...},revisions:[...]}
//	CREATE   POST   /microvm/apps                 -> {success,app:{...}}
//	DEPLOY   POST   /microvm/app/{id}/deploy      body {revision_id}
//	ROLLBACK POST   /microvm/app/{id}/rollback    body {revision_id}
//	STOP     POST   /microvm/app/{id}/stop
//	START    POST   /microvm/app/{id}/start
//	SET ENV  PUT    /microvm/app/{id}/env         body {env: {K: V, ...}}
//	DELETE   DELETE /microvm/app/{id}
type microvmAppResource struct {
	client *client.Client
}

// microvmAppModel maps the Terraform state/plan for iaas_microvm_app.
//
// hypervisor_group_id/slug/source_kind are RequiresReplace (no rename/re-home
// endpoint exists). The remaining configured fields flow through the
// single-path Update (see Update). fqdn/state are Computed and refreshed from
// every app response.
type microvmAppModel struct {
	ID                 types.String `tfsdk:"id"`
	HypervisorGroupID  types.String `tfsdk:"hypervisor_group_id"`
	Slug               types.String `tfsdk:"slug"`
	SourceKind         types.String `tfsdk:"source_kind"`
	SourceImage        types.String `tfsdk:"source_image"`
	SourceRepo         types.String `tfsdk:"source_repo"`
	SourceBranch       types.String `tfsdk:"source_branch"`
	Port               types.Int64  `tfsdk:"port"`
	MinInstances       types.Int64  `tfsdk:"min_instances"`
	IdleTimeoutSeconds types.Int64  `tfsdk:"idle_timeout_seconds"`
	HealthPath         types.String `tfsdk:"health_path"`
	Fqdn               types.String `tfsdk:"fqdn"`
	State              types.String `tfsdk:"state"`
}

// Metadata sets the resource type name -> "iaas_microvm_app".
func (r *microvmAppResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_microvm_app"
}

// Schema describes the iaas_microvm_app resource.
func (r *microvmAppResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a Serverless App: a scale-to-zero microVM app deployed from a container " +
			"image or a git repository, reachable at an instant <slug>.apps.<domain> URL.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"hypervisor_group_id": schema.StringAttribute{
				Required:      true,
				Description:   "Location (hypervisor group) to place the app in. Immutable.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"slug": schema.StringAttribute{
				Required:      true,
				Description:   "App name, used in its default URL. Immutable.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"source_kind": schema.StringAttribute{
				Required:      true,
				Description:   "\"oci\" or \"git\". Immutable.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"source_image": schema.StringAttribute{
				Optional:    true,
				Description: "Container image reference, required when source_kind is \"oci\".",
			},
			"source_repo": schema.StringAttribute{
				Optional:    true,
				Description: "Git repository URL, required when source_kind is \"git\".",
			},
			"source_branch": schema.StringAttribute{
				Optional:    true,
				Description: "Git branch to build from, defaults to \"main\".",
			},
			"port": schema.Int64Attribute{
				Optional:    true,
				Description: "Container port the app listens on. Defaults to 8080.",
			},
			"min_instances": schema.Int64Attribute{
				Optional:    true,
				Description: "0 (scale to zero, default) or 1 (never pause).",
			},
			"idle_timeout_seconds": schema.Int64Attribute{
				Optional:    true,
				Description: "Seconds of inactivity before pausing. Defaults to 300.",
			},
			"health_path": schema.StringAttribute{
				Optional:    true,
				Description: "HTTP path used for the deploy health check. Defaults to \"/\".",
			},
			"fqdn": schema.StringAttribute{
				Computed: true,
			},
			"state": schema.StringAttribute{
				Computed: true,
			},
		},
	}
}

// Configure pulls the shared *client.Client from the provider (nil-guarded).
func (r *microvmAppResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create creates the app: the server stores it, creates the first revision
// and dispatches its build in the same call. Unconfigured optional attributes
// are OMITTED from the body so the server-side defaults apply (the create
// validation rejects a zeroed port/idle_timeout_seconds instead of treating
// it as "unset"; same write-only-optionals guard shape as iaas_certificate's
// chain).
func (r *microvmAppResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan microvmAppModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	source := map[string]any{}
	if plan.SourceKind.ValueString() == "oci" {
		if !plan.SourceImage.IsNull() && !plan.SourceImage.IsUnknown() && plan.SourceImage.ValueString() != "" {
			source["image"] = plan.SourceImage.ValueString()
		}
	} else {
		if !plan.SourceRepo.IsNull() && !plan.SourceRepo.IsUnknown() && plan.SourceRepo.ValueString() != "" {
			source["repo"] = plan.SourceRepo.ValueString()
		}
		if !plan.SourceBranch.IsNull() && !plan.SourceBranch.IsUnknown() && plan.SourceBranch.ValueString() != "" {
			source["branch"] = plan.SourceBranch.ValueString()
		}
	}

	body := map[string]any{
		"hypervisor_group_id": plan.HypervisorGroupID.ValueString(),
		"slug":                plan.Slug.ValueString(),
		"source_kind":         plan.SourceKind.ValueString(),
		"source":              source,
	}
	if !plan.Port.IsNull() && !plan.Port.IsUnknown() {
		body["port"] = plan.Port.ValueInt64()
	}
	if !plan.MinInstances.IsNull() && !plan.MinInstances.IsUnknown() {
		body["min_instances"] = plan.MinInstances.ValueInt64()
	}
	if !plan.IdleTimeoutSeconds.IsNull() && !plan.IdleTimeoutSeconds.IsUnknown() {
		body["idle_timeout_seconds"] = plan.IdleTimeoutSeconds.ValueInt64()
	}
	if !plan.HealthPath.IsNull() && !plan.HealthPath.IsUnknown() && plan.HealthPath.ValueString() != "" {
		body["health_path"] = plan.HealthPath.ValueString()
	}

	app, err := r.client.CreateApp(ctx, body)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating microvm app", err))
		return
	}

	applyAppToModel(app, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes state via GetApp. A 404 (deleted out of band) removes the
// resource from state so Terraform plans a re-create.
func (r *microvmAppResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state microvmAppModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	app, err := r.client.GetApp(ctx, state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("Error reading microvm app", err))
		return
	}

	applyAppToModel(app, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update re-runs the create-and-deploy flow. A source change (image tag/git
// branch) is the only field that needs a new build+deploy - v1 has no PATCH
// for the other scalar fields (min_instances/idle_timeout_seconds/
// health_path/port) either, so this resource intentionally re-runs the
// create-and-deploy flow whenever ANY tracked field differs, keeping Update()
// a single code path instead of N per-field branches that would each need
// their own API round trip.
func (r *microvmAppResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state microvmAppModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	plan.ID = state.ID
	r.Create(ctx, resource.CreateRequest{Plan: req.Plan}, (*resource.CreateResponse)(resp))
}

// Delete permanently destroys the app. A failure is surfaced as an error
// diagnostic so Delete never reports success for an app that still exists.
func (r *microvmAppResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state microvmAppModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteApp(ctx, state.ID.ValueString()); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deleting microvm app", err))
		return
	}
}

// applyAppToModel copies the server-owned fields of an app response object
// into the model; the configured fields are preserved from the plan/prior
// state.
func applyAppToModel(app map[string]any, m *microvmAppModel) {
	if v, ok := app["id"].(string); ok {
		m.ID = types.StringValue(v)
	}
	if v, ok := app["fqdn"].(string); ok {
		m.Fqdn = types.StringValue(v)
	}
	if v, ok := app["state"].(string); ok {
		m.State = types.StringValue(v)
	}
}
