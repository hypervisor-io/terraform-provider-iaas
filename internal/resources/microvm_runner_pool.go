package resources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
)

// Interface assertions. iaas_microvm_runner_pool manages a CI Runners pool
// (MV3-23 contract), the configuration row that maps GitHub Actions or GitLab
// CI jobs onto MicroVM capacity in a hypervisor group.
//
// ★ GITHUB POOLS ARE IMPORT-ONLY: they are linked out of band by the panel's
// Install GitHub App flow (MV3-7), so Create for provider_type = "github"
// returns an error directing the user to install the App and then
// `terraform import` the linked pool - mirroring how iaas_certificate
// documents its own Let's Encrypt exclusion. GitLab pools are fully managed
// here (Create/Update/Delete against the /microvm/runners/pools/gitlab*
// routes).
//
// ★ READ-PATH GAP (MV3-25): the merged MV3-23 routes have NO pool-show
// endpoint, so Read goes through client.GetRunnerPool, which pages the LIST
// endpoint and filters by id client-side (a missing id surfaces as a 404
// *APIError -> RemoveResource). A Master-side GET /microvm/runners/pools/{id}
// route is the clean fix and is recorded for the planner.
//
// ★ WRITE-ONLY SECRET: gitlab_token is $hidden on the Master model and is
// NEVER echoed by any response (same contract as iaas_certificate's PEM
// bodies and iaas_microvm_api_key's plaintext). It is sent on create, then
// preserved verbatim in state across every read; an imported gitlab pool
// therefore carries a null gitlab_token that cannot be recovered.
var (
	_ resource.Resource                = &microvmRunnerPoolResource{}
	_ resource.ResourceWithConfigure   = &microvmRunnerPoolResource{}
	_ resource.ResourceWithImportState = &microvmRunnerPoolResource{}
)

// NewMicrovmRunnerPoolResource is the resource constructor registered with the provider.
func NewMicrovmRunnerPoolResource() resource.Resource {
	return &microvmRunnerPoolResource{}
}

// microvmRunnerPoolResource manages an iaas_microvm_runner_pool.
//
// Route summary (MV3-23 contract):
//
//	INDEX   GET    /microvm/runners/pools                  -> bare Laravel paginator
//	CREATE  POST   /microvm/runners/pools/gitlab           -> bare pool object
//	UPDATE  PUT    /microvm/runners/pools/{provider}/{id}  -> bare pool object
//	DELETE  DELETE /microvm/runners/pools/{id}             -> {success,message}
type microvmRunnerPoolResource struct {
	client *client.Client
}

// microvmRunnerPoolModel maps the Terraform state/plan for
// iaas_microvm_runner_pool. provider_type is RequiresReplace (a pool cannot
// change CI provider). gitlab_token is the write-only secret preserved from
// plan/state; every other field is refreshed from the API pool object.
type microvmRunnerPoolModel struct {
	ID                types.String `tfsdk:"id"`
	ProviderType      types.String `tfsdk:"provider_type"`
	HypervisorGroupID types.String `tfsdk:"hypervisor_group_id"`
	PlanID            types.String `tfsdk:"plan_id"`
	Labels            types.List   `tfsdk:"labels"`
	MaxConcurrent     types.Int64  `tfsdk:"max_concurrent"`
	Enabled           types.Bool   `tfsdk:"enabled"`
	GitlabURL         types.String `tfsdk:"gitlab_url"`
	GitlabToken       types.String `tfsdk:"gitlab_token"`
	GitlabTagList     types.List   `tfsdk:"gitlab_tag_list"`
	GitlabRunUntagged types.Bool   `tfsdk:"gitlab_run_untagged"`
	WarmCount         types.Int64  `tfsdk:"warm_count"`
}

// Metadata sets the resource type name -> "iaas_microvm_runner_pool".
func (r *microvmRunnerPoolResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_microvm_runner_pool"
}

// Schema describes the iaas_microvm_runner_pool resource.
func (r *microvmRunnerPoolResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a CI Runners pool (GitHub or GitLab). A GitHub pool must first be " +
			"linked via the panel's Install GitHub App flow and then imported by id - `provider_type " +
			"= \"github\"` does not support Create. A GitLab pool is fully managed here. " +
			"Import (`terraform import iaas_microvm_runner_pool.<name> <pool-id>`) is the only way " +
			"to manage a GitHub pool; gitlab_token is write-only and never returned by the API, so " +
			"an imported GitLab pool keeps a null gitlab_token.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the runner pool, assigned by the API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"provider_type": schema.StringAttribute{
				Required: true,
				Description: "CI provider for the pool: \"github\" or \"gitlab\". Immutable - a pool " +
					"cannot change provider, so changing this forces a new resource (GitHub pools " +
					"are import-only and cannot be recreated here).",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"hypervisor_group_id": schema.StringAttribute{
				Required:    true,
				Description: "UUID of the hypervisor group whose capacity runs this pool's CI jobs.",
			},
			"plan_id": schema.StringAttribute{
				Required:    true,
				Description: "UUID of the MicroVM plan each runner job MicroVM is created from.",
			},
			"labels": schema.ListAttribute{
				ElementType: types.StringType,
				Optional:    true,
				Description: "(GitHub only) Runner labels advertised to GitHub Actions. The panel " +
					"seeds the well-known self-hosted/linux/x64 labels at install time; keep the " +
					"configuration in sync with them.",
			},
			"max_concurrent": schema.Int64Attribute{
				Optional:    true,
				Computed:    true,
				Description: "Maximum number of runner jobs this pool executes concurrently. Server default applies when omitted.",
			},
			"enabled": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Description: "(GitHub only) Whether the pool accepts new jobs from GitHub. " +
					"Server-assigned at link time when omitted.",
			},
			"gitlab_url": schema.StringAttribute{
				Optional:    true,
				Description: "(GitLab only) Base URL of the GitLab instance the runners register with (e.g. https://gitlab.com).",
			},
			"gitlab_token": schema.StringAttribute{
				Optional:  true,
				Sensitive: true,
				Description: "(GitLab only) Runner authentication token. WRITE-ONLY: it is verified " +
					"against GitLab on create and never returned by any API response afterwards, so " +
					"it is preserved in state and cannot be recovered on import. Marked sensitive so " +
					"it is never shown in plan/CLI output.",
			},
			"gitlab_tag_list": schema.ListAttribute{
				ElementType: types.StringType,
				Optional:    true,
				Description: "(GitLab only) Tags assigned to the registered runners; jobs must request them to land on this pool.",
			},
			"gitlab_run_untagged": schema.BoolAttribute{
				Optional:    true,
				Description: "(GitLab only) Whether the registered runners also pick up untagged jobs.",
			},
			"warm_count": schema.Int64Attribute{
				Optional: true,
				Computed: true,
				Description: "(GitLab only) Number of pre-registered warm runner MicroVMs kept ready. " +
					"Server default applies when omitted.",
			},
		},
	}
}

// Configure pulls the shared *client.Client from the provider (nil-guarded,
// type-checked).
func (r *microvmRunnerPoolResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create supports GitLab pools only. GitHub pools are linked by the panel's
// Install GitHub App flow, so Create for provider_type = "github" fails with
// the install-then-import guidance instead of touching the API. The create
// response is the bare pool object; the state is built from it (resolving the
// Optional+Computed attributes the server defaulted) with the write-only
// gitlab_token preserved from the plan.
func (r *microvmRunnerPoolResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan microvmRunnerPoolModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if plan.ProviderType.ValueString() == "github" {
		resp.Diagnostics.AddError(
			"github pools cannot be created via Terraform",
			"Install the GitHub App from the panel's CI Runners page, then `terraform import iaas_microvm_runner_pool.<name> <pool-id>`.",
		)
		return
	}

	body := map[string]any{
		"hypervisor_group_id": plan.HypervisorGroupID.ValueString(),
		"plan_id":             plan.PlanID.ValueString(),
		"gitlab_url":          plan.GitlabURL.ValueString(),
		"gitlab_token":        plan.GitlabToken.ValueString(),
		"warm_count":          plan.WarmCount.ValueInt64(),
	}

	pool, err := r.client.CreateGitlabRunnerPool(ctx, body)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating microvm runner pool", err))
		return
	}

	id, _ := pool["id"].(string)
	if id == "" {
		resp.Diagnostics.AddError(
			"Error creating microvm runner pool",
			"the create response did not include the pool id",
		)
		return
	}

	state, diags := microvmRunnerPoolStateFromAPI(ctx, pool, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Read refreshes the pool through the LIST endpoint (READ-PATH GAP: there is
// no pool-show route - client.GetRunnerPool pages the listing and filters by
// id). A pool absent from the listing was deleted out of band - remove it
// from state. gitlab_token is NEVER in the response ($hidden), so the
// captured value is preserved from prior state and never overwritten.
func (r *microvmRunnerPoolResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state microvmRunnerPoolModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	pool, err := r.client.GetRunnerPool(ctx, state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("Error reading microvm runner pool", err))
		return
	}

	newState, diags := microvmRunnerPoolStateFromAPI(ctx, pool, state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

// Update routes through the provider-specific path
// PUT /microvm/runners/pools/{provider}/{id} with the provider-correct body:
// the shared fields plus warm_count for gitlab, enabled for github. The
// response (the bare updated pool) is discarded - the plan is authoritative
// for state, and the write-only gitlab_token rides along from the plan.
func (r *microvmRunnerPoolResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan microvmRunnerPoolModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := map[string]any{
		"hypervisor_group_id": plan.HypervisorGroupID.ValueString(),
		"plan_id":             plan.PlanID.ValueString(),
		"max_concurrent":      plan.MaxConcurrent.ValueInt64(),
	}
	if plan.ProviderType.ValueString() == "gitlab" {
		body["warm_count"] = plan.WarmCount.ValueInt64()
	} else {
		body["enabled"] = plan.Enabled.ValueBool()
	}

	if _, err := r.client.UpdateRunnerPool(ctx, plan.ProviderType.ValueString(), plan.ID.ValueString(), body); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error updating microvm runner pool", err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete destroys the pool (either provider) via DELETE
// /microvm/runners/pools/{id}. The Master refuses to delete a pool with an
// active job; the refusal surfaces as an error diagnostic.
func (r *microvmRunnerPoolResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state microvmRunnerPoolModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteRunnerPool(ctx, state.ID.ValueString()); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deleting microvm runner pool", err))
		return
	}
}

// ImportState lets `terraform import iaas_microvm_runner_pool.<name> <uuid>`
// adopt an existing pool - the ONLY way to manage a GitHub pool (linked by
// the panel's Install GitHub App flow). The next Read populates the tracked
// attributes from the listing; the write-only gitlab_token cannot be
// recovered on import (the API never returns it) - callers must add it to
// ImportStateVerifyIgnore, exactly like iaas_certificate's PEM bodies.
func (r *microvmRunnerPoolResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// microvmRunnerPoolStateFromAPI builds the model from an API pool object.
// gitlab_token is NEVER in the response ($hidden on the Master model) -
// preserved verbatim from the prior model (write-only). The API's provider
// field maps to provider_type. Nullable provider-specific fields fall back
// to the prior value rather than collapsing to ""/empty so an Optional-only
// attribute omitted from configuration never shows a perpetual diff.
func microvmRunnerPoolStateFromAPI(ctx context.Context, obj map[string]any, prior microvmRunnerPoolModel) (microvmRunnerPoolModel, diag.Diagnostics) {
	labels, diags := runnerPoolListFromAPI(ctx, obj, "labels", prior.Labels)
	tagList, tagDiags := runnerPoolListFromAPI(ctx, obj, "gitlab_tag_list", prior.GitlabTagList)
	diags.Append(tagDiags...)

	return microvmRunnerPoolModel{
		ID:                stringFromAPI(obj, "id", prior.ID),
		ProviderType:      stringFromAPI(obj, "provider", prior.ProviderType),
		HypervisorGroupID: stringFromAPI(obj, "hypervisor_group_id", prior.HypervisorGroupID),
		PlanID:            stringFromAPI(obj, "plan_id", prior.PlanID),
		Labels:            labels,
		MaxConcurrent:     int64FromAPI(obj, "max_concurrent", prior.MaxConcurrent),
		Enabled:           boolFromIntAPI(obj, "enabled", prior.Enabled),
		GitlabURL:         runnerPoolNullableStringFromAPI(obj, "gitlab_url", prior.GitlabURL),

		// WRITE-ONLY - $hidden on the model, never in any response; preserve
		// the plan/state value verbatim.
		GitlabToken: prior.GitlabToken,

		GitlabTagList:     tagList,
		GitlabRunUntagged: boolFromIntAPI(obj, "gitlab_run_untagged", prior.GitlabRunUntagged),
		WarmCount:         int64FromAPI(obj, "warm_count", prior.WarmCount),
	}, diags
}

// runnerPoolNullableStringFromAPI reads a nullable string field (gitlab_url
// on a github pool is null). A present string wins; null or absent falls
// back to the prior value. Unlike stringFromAPI it does NOT collapse null to
// "": these fields are Optional-only in the schema, so a "" state against a
// configuration that omits them would show a perpetual "" -> null diff.
func runnerPoolNullableStringFromAPI(obj map[string]any, key string, fallback types.String) types.String {
	raw, ok := obj[key]
	if !ok || raw == nil {
		return fallback
	}
	if s, ok := raw.(string); ok {
		return types.StringValue(s)
	}
	// Non-string JSON value (number/bool) - coerce defensively.
	return types.StringValue(fmt.Sprintf("%v", raw))
}

// runnerPoolListFromAPI converts a JSON array field (labels /
// gitlab_tag_list, both `array`-cast on the Master model) to a
// types.List(string). A present array (even empty) becomes a known list;
// null or absent falls back to the prior value - same perpetual-diff
// reasoning as runnerPoolNullableStringFromAPI.
func runnerPoolListFromAPI(ctx context.Context, obj map[string]any, key string, fallback types.List) (types.List, diag.Diagnostics) {
	raw, ok := obj[key]
	if !ok || raw == nil {
		return fallback, nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return fallback, nil
	}
	items := make([]string, 0, len(arr))
	for _, v := range arr {
		if s, ok := v.(string); ok {
			items = append(items, s)
		}
	}
	return types.ListValueFrom(ctx, types.StringType, items)
}
