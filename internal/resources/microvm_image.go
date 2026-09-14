package resources

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
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
	_ resource.Resource                = &microvmImageResource{}
	_ resource.ResourceWithConfigure   = &microvmImageResource{}
	_ resource.ResourceWithImportState = &microvmImageResource{}
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
	Env                types.Map    `tfsdk:"env"`
	LifecycleHooks     types.String `tfsdk:"lifecycle_hooks"`
	BuildHooks         types.String `tfsdk:"build_hooks"`
	Status             types.String `tfsdk:"status"`
	TemplateName       types.String `tfsdk:"template_name"`
	CurrentVersionID   types.String `tfsdk:"current_version_id"`
	CurrentVersion     types.Int64  `tfsdk:"current_version"`
	CurrentBuildStatus types.String `tfsdk:"current_build_status"`
	ErrorMessage       types.String `tfsdk:"error_message"`
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
			"dockerfile":          schema.StringAttribute{Optional: true, Description: "Dockerfile contents. Required for source_kind = dockerfile.", PlanModifiers: replaceString},
			"source_image":        schema.StringAttribute{Optional: true, Description: "OCI image reference. Required for source_kind = oci.", PlanModifiers: replaceString},
			"source_repo":         schema.StringAttribute{Optional: true, Description: "Git repository URL. Required for source_kind = git.", PlanModifiers: replaceString},
			"source_branch":       schema.StringAttribute{Optional: true, Description: "Git branch. The server default applies when omitted.", PlanModifiers: replaceString},
			"registry_username":   schema.StringAttribute{Optional: true, Description: "Registry username used while building an OCI image.", PlanModifiers: replaceString},
			"registry_password":   schema.StringAttribute{Optional: true, Sensitive: true, Description: "Write-only registry password used while building an OCI image.", PlanModifiers: replaceString},
			"git_source_id":       schema.StringAttribute{Optional: true, Description: "Owned Git source UUID used to authenticate a Git build.", PlanModifiers: replaceString},
			"base_image_id":       schema.StringAttribute{Optional: true, Description: "Optional platform or account image used as the build base.", PlanModifiers: replaceString},
			"hypervisor_group_id": schema.StringAttribute{Required: true, Description: "Location UUID where the image build runs.", PlanModifiers: replaceString},
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
		},
	}
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
	obj, err := r.client.GetMicrovmImage(ctx, state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("Error reading MicroVM image", err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, microvmImageStateFromAPI(obj, state))...)
}

func (r *microvmImageResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan microvmImageModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	obj, err := r.client.GetMicrovmImage(ctx, plan.ID.ValueString())
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading MicroVM image", err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, microvmImageStateFromAPI(obj, plan))...)
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
		"name":                plan.Name.ValueString(),
		"source_kind":         plan.SourceKind.ValueString(),
		"source":              source,
		"hypervisor_group_id": plan.HypervisorGroupID.ValueString(),
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

func microvmImageStateFromAPI(obj map[string]any, prior microvmImageModel) microvmImageModel {
	state := prior
	state.ID = stringFromAPI(obj, "id", prior.ID)
	state.Name = stringFromAPI(obj, "name", prior.Name)
	state.Description = optionalStringFromAPI(obj, "description", prior.Description)
	state.SourceKind = stringFromAPI(obj, "source_kind", prior.SourceKind)
	state.BaseImageID = optionalStringFromAPI(obj, "base_image_id", prior.BaseImageID)
	state.Status = stringFromAPI(obj, "status", prior.Status)
	state.TemplateName = stringFromAPI(obj, "template_name", prior.TemplateName)
	state.CurrentVersionID = optionalStringFromAPI(obj, "current_version_id", prior.CurrentVersionID)
	state.ErrorMessage = optionalStringFromAPI(obj, "error_message", prior.ErrorMessage)
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

func putOptionalString(body map[string]any, key string, value types.String) {
	if !value.IsNull() && !value.IsUnknown() {
		body[key] = value.ValueString()
	}
}
