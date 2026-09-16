package resources

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
)

var (
	_ resource.Resource                   = &microvmImageResource{}
	_ resource.ResourceWithConfigure      = &microvmImageResource{}
	_ resource.ResourceWithImportState    = &microvmImageResource{}
	_ resource.ResourceWithValidateConfig = &microvmImageResource{}
)

func NewMicrovmImageResource() resource.Resource {
	return &microvmImageResource{}
}

type microvmImageResource struct {
	client *client.Client
}

type microvmImageModel struct {
	ID                 types.String `tfsdk:"id"`
	Name               types.String `tfsdk:"name"`
	Description        types.String `tfsdk:"description"`
	SourceKind         types.String `tfsdk:"source_kind"`
	Dockerfile         types.String `tfsdk:"dockerfile"`
	SourceImage        types.String `tfsdk:"source_image"`
	SourceRepo         types.String `tfsdk:"source_repo"`
	SourceBranch       types.String `tfsdk:"source_branch"`
	RegistryUsername   types.String `tfsdk:"registry_username"`
	RegistryPassword   types.String `tfsdk:"registry_password"`
	GitSourceID        types.String `tfsdk:"git_source_id"`
	BaseImageID        types.String `tfsdk:"base_image_id"`
	HypervisorGroupID  types.String `tfsdk:"hypervisor_group_id"`
	LocationID         types.String `tfsdk:"location_id"`
	Env                types.Map    `tfsdk:"env"`
	LifecycleHooks     types.String `tfsdk:"lifecycle_hooks"`
	BuildHooks         types.String `tfsdk:"build_hooks"`
	Status             types.String `tfsdk:"status"`
	TemplateName       types.String `tfsdk:"template_name"`
	CurrentVersionID   types.String `tfsdk:"current_version_id"`
	CurrentVersion     types.Int64  `tfsdk:"current_version"`
	CurrentBuildStatus types.String `tfsdk:"current_build_status"`
	ErrorMessage       types.String `tfsdk:"error_message"`
	OsID               types.String `tfsdk:"os_id"`
	OsFamily           types.String `tfsdk:"os_family"`
	OsName             types.String `tfsdk:"os_name"`
	OsVersion          types.String `tfsdk:"os_version"`
	OsCodename         types.String `tfsdk:"os_codename"`
	Arch               types.String `tfsdk:"arch"`
	Features           types.Map    `tfsdk:"features"`
}

func (r *microvmImageResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_microvm_image"
}

func (r *microvmImageResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replaceString := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Description: "Builds and manages a reusable MicroVM image from a Dockerfile, OCI image, or Git repository. Image inputs are immutable because the API creates new versions through an imperative build action.",
		Attributes: map[string]schema.Attribute{
			"id":          schema.StringAttribute{Computed: true, Description: "UUID assigned to the image.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"name":        schema.StringAttribute{Required: true, Description: "Account-unique image name.", PlanModifiers: replaceString},
			"description": schema.StringAttribute{Optional: true, Description: "Optional image description.", PlanModifiers: replaceString},
			"source_kind": schema.StringAttribute{
				Required:      true,
				Description:   "Image source: dockerfile, oci, or git.",
				Validators:    []validator.String{stringvalidator.OneOf("dockerfile", "oci", "git")},
				PlanModifiers: replaceString,
			},
			"dockerfile":        schema.StringAttribute{Optional: true, Description: "Dockerfile contents. Required for source_kind = dockerfile.", PlanModifiers: replaceString},
			"source_image":      schema.StringAttribute{Optional: true, Description: "OCI image reference. Required for source_kind = oci.", PlanModifiers: replaceString},
			"source_repo":       schema.StringAttribute{Optional: true, Description: "Git repository URL. Required for source_kind = git.", PlanModifiers: replaceString},
			"source_branch":     schema.StringAttribute{Optional: true, Description: "Git branch. The server default applies when omitted.", PlanModifiers: replaceString},
			"registry_username": schema.StringAttribute{Optional: true, Description: "Registry username used while building an OCI image.", PlanModifiers: replaceString},
			"registry_password": schema.StringAttribute{Optional: true, Sensitive: true, Description: "Write-only registry password used while building an OCI image.", PlanModifiers: replaceString},
			"git_source_id":     schema.StringAttribute{Optional: true, Description: "Owned Git source UUID used to authenticate a Git build.", PlanModifiers: replaceString},
			"base_image_id": schema.StringAttribute{
				Optional:      true,
				Description:   "Platform or account image used as the build base (C6). Required when source_kind = dockerfile; the base must be ready and carry features.envd or features.vcagent.",
				PlanModifiers: replaceString,
			},
			"hypervisor_group_id": schema.StringAttribute{
				Optional: true,
				Computed: true,
				DeprecationMessage: "Use location_id instead. hypervisor_group_id is deprecated " +
					"and will be removed in the next release.",
				Description:   "Location UUID where the image build runs. Changing this forces a new resource.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplaceIfConfigured()},
			},
			"location_id": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "Location UUID where the image build runs. Canonical replacement for " +
					"hypervisor_group_id; exactly one of the two must be set. Changing this forces a new resource.",
				Validators: []validator.String{
					stringvalidator.ExactlyOneOf(path.MatchRoot("hypervisor_group_id")),
				},
				PlanModifiers: []planmodifier.String{
					locationIDFromAliasModifier{},
					stringplanmodifier.RequiresReplaceIfConfigured(),
				},
			},
			"env": schema.MapAttribute{
				Optional:      true,
				Sensitive:     true,
				ElementType:   types.StringType,
				Description:   "Write-only image-level environment defaults.",
				PlanModifiers: []planmodifier.Map{mapplanmodifier.RequiresReplace()},
			},
			"lifecycle_hooks":      schema.StringAttribute{Optional: true, Description: "Lifecycle hook defaults as a JSON object.", PlanModifiers: replaceString},
			"build_hooks":          schema.StringAttribute{Optional: true, Description: "Build ready and validation hooks as a JSON object.", PlanModifiers: replaceString},
			"status":               schema.StringAttribute{Computed: true, Description: "Current image status: pending, building, ready, or error."},
			"template_name":        schema.StringAttribute{Computed: true, Description: "Daemon-side template name.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"current_version_id":   schema.StringAttribute{Computed: true, Description: "UUID of the current image version."},
			"current_version":      schema.Int64Attribute{Computed: true, Description: "Current image version number."},
			"current_build_status": schema.StringAttribute{Computed: true, Description: "Status of the current image version."},
			"error_message":        schema.StringAttribute{Computed: true, Description: "Most recent build error, when present."},
			"os_id":                schema.StringAttribute{Computed: true, Description: "Catalog OS id, e.g. \"debian-13\" (C5). Empty for an image without OS metadata."},
			"os_family":            schema.StringAttribute{Computed: true, Description: "OS family: debian, rhel, or amazon (C5)."},
			"os_name":              schema.StringAttribute{Computed: true, Description: "Human OS name, e.g. \"Debian\" (C5)."},
			"os_version":           schema.StringAttribute{Computed: true, Description: "OS version, e.g. \"13\" (C5)."},
			"os_codename":          schema.StringAttribute{Computed: true, Description: "OS codename, e.g. \"trixie\" (C5)."},
			"arch":                 schema.StringAttribute{Computed: true, Description: "CPU architecture, e.g. \"x86_64\" (C5)."},
			"features": schema.MapAttribute{
				Computed:    true,
				ElementType: types.BoolType,
				Description: "Baked-in capability flags: sshd, envd, vcagent (C5).",
			},
		},
	}
}

// ValidateConfig mirrors CreateImageRequest's base_image_id required_if
// (source_kind = dockerfile) rule (Master's UX-layer guard) so a Dockerfile
// build without a base fails fast in `tofu plan`, before the authoritative
// ImageService::create() 422s. This is a plan-time convenience only; the
// service remains authoritative for every caller off the FormRequest path.
func (r *microvmImageResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config microvmImageModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if microvmImageMissingRequiredBase(config) {
		resp.Diagnostics.AddAttributeError(
			path.Root("base_image_id"),
			"Missing Required Base Image",
			"base_image_id is required when source_kind is \"dockerfile\" (C6: every custom build runs on top of a catalog base).",
		)
	}
}

// microvmImageMissingRequiredBase mirrors CreateImageRequest's
// base_image_id `required_if:source_kind,dockerfile` rule (Master's
// authoritative UX-layer guard - ImageService::create() remains the real
// enforcement point for every caller off the FormRequest path, tenancy.md).
// An unknown value (computed-from-elsewhere in a more complex config) never
// triggers the plan-time error; the service 422s if it turns out empty.
func microvmImageMissingRequiredBase(config microvmImageModel) bool {
	if config.SourceKind.IsUnknown() || config.SourceKind.ValueString() != "dockerfile" {
		return false
	}
	if config.BaseImageID.IsUnknown() {
		return false
	}
	return config.BaseImageID.IsNull() || config.BaseImageID.ValueString() == ""
}

func (r *microvmImageResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected Provider Data Type", fmt.Sprintf("Expected *client.Client, got: %T. This is a provider bug; please report it.", req.ProviderData))
		return
	}
	r.client = c
}

func (r *microvmImageResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan microvmImageModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, err := microvmImageCreateBody(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Invalid MicroVM image configuration", err.Error())
		return
	}
	obj, err := r.client.CreateMicrovmImage(ctx, body)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating MicroVM image", err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, microvmImageStateFromAPI(obj, plan))...)
}

func (r *microvmImageResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state microvmImageModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	envelope, err := r.client.GetMicrovmImage(ctx, state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("Error reading MicroVM image", err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, microvmImageStateFromAPI(microvmImageEnvelopeParts(envelope), state))...)
}

func (r *microvmImageResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan microvmImageModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	envelope, err := r.client.GetMicrovmImage(ctx, plan.ID.ValueString())
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading MicroVM image", err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, microvmImageStateFromAPI(microvmImageEnvelopeParts(envelope), plan))...)
}

func (r *microvmImageResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state microvmImageModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteMicrovmImage(ctx, state.ID.ValueString()); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deleting MicroVM image", err))
	}
}

func (r *microvmImageResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func microvmImageCreateBody(ctx context.Context, plan microvmImageModel) (map[string]any, error) {
	source := map[string]any{}
	switch plan.SourceKind.ValueString() {
	case "dockerfile":
		if plan.Dockerfile.IsNull() || plan.Dockerfile.ValueString() == "" {
			return nil, fmt.Errorf("dockerfile is required when source_kind is dockerfile")
		}
		source["dockerfile"] = plan.Dockerfile.ValueString()
	case "oci":
		if plan.SourceImage.IsNull() || plan.SourceImage.ValueString() == "" {
			return nil, fmt.Errorf("source_image is required when source_kind is oci")
		}
		source["image"] = plan.SourceImage.ValueString()
	case "git":
		if plan.SourceRepo.IsNull() || plan.SourceRepo.ValueString() == "" {
			return nil, fmt.Errorf("source_repo is required when source_kind is git")
		}
		source["repo"] = plan.SourceRepo.ValueString()
		if !plan.SourceBranch.IsNull() && !plan.SourceBranch.IsUnknown() {
			source["branch"] = plan.SourceBranch.ValueString()
		}
	default:
		return nil, fmt.Errorf("unsupported source_kind %q", plan.SourceKind.ValueString())
	}

	body := map[string]any{
		"name":        plan.Name.ValueString(),
		"source_kind": plan.SourceKind.ValueString(),
		"source":      source,
		"location_id": effectiveLocationID(plan.LocationID, plan.HypervisorGroupID),
	}
	putOptionalString(body, "description", plan.Description)
	putOptionalString(body, "base_image_id", plan.BaseImageID)
	if !plan.RegistryUsername.IsNull() || !plan.RegistryPassword.IsNull() {
		body["auth"] = map[string]any{"kind": "registry", "username": plan.RegistryUsername.ValueString(), "password": plan.RegistryPassword.ValueString()}
	} else if !plan.GitSourceID.IsNull() {
		body["auth"] = map[string]any{"kind": "git_source", "git_source_id": plan.GitSourceID.ValueString()}
	}
	if !plan.Env.IsNull() && !plan.Env.IsUnknown() {
		var env map[string]string
		if diags := plan.Env.ElementsAs(ctx, &env, false); diags.HasError() {
			return nil, fmt.Errorf("decoding env: %v", diags)
		}
		raw, err := json.Marshal(env)
		if err != nil {
			return nil, fmt.Errorf("encoding env: %w", err)
		}
		body["env"] = string(raw)
	}
	for key, value := range map[string]types.String{"lifecycle_hooks": plan.LifecycleHooks, "build_hooks": plan.BuildHooks} {
		if value.IsNull() || value.IsUnknown() {
			continue
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(value.ValueString()), &decoded); err != nil {
			return nil, fmt.Errorf("%s must contain a JSON object: %w", key, err)
		}
		body[key] = decoded
	}
	return body, nil
}

// microvmImageEnvelopeParts unwraps the SHOW envelope
// ({success,image:{...},versions:[...],microvms_count}) down to the
// presented image object. GetMicrovmImage deliberately returns the bare
// envelope (versions/microvms_count are useful to a future data source), so
// every resource-layer reader of the image itself must unwrap here - a
// direct pass of the envelope to microvmImageStateFromAPI leaves the "id"/
// "name"/... lookups miss and every field falls back to `prior` (Read/Update
// silently never refresh anything).
func microvmImageEnvelopeParts(envelope map[string]any) map[string]any {
	if obj, ok := envelope["image"].(map[string]any); ok {
		return obj
	}
	return envelope
}

func microvmImageStateFromAPI(obj map[string]any, prior microvmImageModel) microvmImageModel {
	state := prior
	state.ID = stringFromAPI(obj, "id", prior.ID)
	state.Name = stringFromAPI(obj, "name", prior.Name)
	state.Description = optionalStringFromAPI(obj, "description", prior.Description)
	state.SourceKind = stringFromAPI(obj, "source_kind", prior.SourceKind)
	state.BaseImageID = optionalStringFromAPI(obj, "base_image_id", prior.BaseImageID)
	state.HypervisorGroupID = hypervisorGroupIDFromAPI(obj, prior.HypervisorGroupID)
	state.LocationID = locationIDFromAPI(obj, prior.LocationID)
	state.Status = stringFromAPI(obj, "status", prior.Status)
	state.TemplateName = stringFromAPI(obj, "template_name", prior.TemplateName)
	state.CurrentVersionID = optionalStringFromAPI(obj, "current_version_id", prior.CurrentVersionID)
	state.ErrorMessage = optionalStringFromAPI(obj, "error_message", prior.ErrorMessage)
	state.OsID = optionalStringFromAPI(obj, "os_id", prior.OsID)
	state.OsFamily = optionalStringFromAPI(obj, "os_family", prior.OsFamily)
	state.OsName = optionalStringFromAPI(obj, "os_name", prior.OsName)
	state.OsVersion = optionalStringFromAPI(obj, "os_version", prior.OsVersion)
	state.OsCodename = optionalStringFromAPI(obj, "os_codename", prior.OsCodename)
	state.Arch = optionalStringFromAPI(obj, "arch", prior.Arch)
	state.Features = microvmImageFeaturesFromAPI(obj["features"], prior.Features)
	if source, ok := obj["source"].(map[string]any); ok {
		state.Dockerfile = optionalStringFromAPI(source, "dockerfile", prior.Dockerfile)
		state.SourceImage = optionalStringFromAPI(source, "image", prior.SourceImage)
		state.SourceRepo = optionalStringFromAPI(source, "repo", prior.SourceRepo)
		state.SourceBranch = optionalStringFromAPI(source, "branch", prior.SourceBranch)
	}
	if version, ok := obj["current_version"].(map[string]any); ok {
		state.CurrentVersion = int64FromAPI(version, "version", prior.CurrentVersion)
		state.CurrentBuildStatus = stringFromAPI(version, "status", prior.CurrentBuildStatus)
	}
	return state
}

// microvmImageFeaturesFromAPI reads the C5 capability map ({sshd, envd,
// vcagent}: bool). A missing/non-object "features" key falls back to prior
// (an image created before C5 shipped, or one the API hasn't stamped yet),
// never to an empty map that would read as "every capability off".
func microvmImageFeaturesFromAPI(raw any, prior types.Map) types.Map {
	obj, ok := raw.(map[string]any)
	if !ok {
		return prior
	}
	values := map[string]attr.Value{}
	for key, v := range obj {
		if b, ok := v.(bool); ok {
			values[key] = types.BoolValue(b)
		}
	}
	m, diags := types.MapValue(types.BoolType, values)
	if diags.HasError() {
		return prior
	}
	return m
}

func putOptionalString(body map[string]any, key string, value types.String) {
	if !value.IsNull() && !value.IsUnknown() {
		body[key] = value.ValueString()
	}
}
