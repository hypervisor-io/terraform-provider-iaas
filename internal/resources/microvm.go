package resources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
)

var (
	_ resource.Resource                   = &microvmResource{}
	_ resource.ResourceWithConfigure      = &microvmResource{}
	_ resource.ResourceWithValidateConfig = &microvmResource{}
	_ resource.ResourceWithImportState    = &microvmResource{}
)

func NewMicrovmResource() resource.Resource {
	return &microvmResource{}
}

type microvmResource struct {
	client *client.Client
}

type microvmModel struct {
	ID                  types.String `tfsdk:"id"`
	HypervisorGroupID   types.String `tfsdk:"hypervisor_group_id"`
	LocationID          types.String `tfsdk:"location_id"`
	ImageID             types.String `tfsdk:"image_id"`
	SnapshotID          types.String `tfsdk:"snapshot_id"`
	InheritEnv          types.Bool   `tfsdk:"inherit_env"`
	FastStart           types.String `tfsdk:"fast_start"`
	ImageVersionID      types.String `tfsdk:"image_version_id"`
	PlanID              types.String `tfsdk:"plan_id"`
	Name                types.String `tfsdk:"name"`
	SshKeyIDs           types.List   `tfsdk:"ssh_key_ids"`
	SecurityGroupIDs    types.Set    `tfsdk:"security_group_ids"`
	Ingress             types.Object `tfsdk:"ingress"`
	Network             types.List   `tfsdk:"network"`
	MaxLifetimeSeconds  types.Int64  `tfsdk:"max_lifetime_seconds"`
	OnTimeout           types.String `tfsdk:"on_timeout"`
	IdleTimeoutSeconds  types.Int64  `tfsdk:"idle_timeout_seconds"`
	AlwaysOn            types.Bool   `tfsdk:"always_on"`
	Env                 types.Map    `tfsdk:"env"`
	Metadata            types.Map    `tfsdk:"metadata"`
	LifecycleHooks      types.String `tfsdk:"lifecycle_hooks"`
	Secure              types.Bool   `tfsdk:"secure"`
	Domains             types.Set    `tfsdk:"domains"`
	State               types.String `tfsdk:"state"`
	Fqdn                types.String `tfsdk:"fqdn"`
	E2bID               types.String `tfsdk:"e2b_id"`
	TimeoutAt           types.String `tfsdk:"timeout_at"`
	CurrentRunStatus    types.String `tfsdk:"current_run_status"`
	CurrentRunStartedAt types.String `tfsdk:"current_run_started_at"`
	LastResumeMode      types.String `tfsdk:"last_resume_mode"`
	LastResumeReason    types.String `tfsdk:"last_resume_reason"`
	LastResumedAt       types.String `tfsdk:"last_resumed_at"`
	Ssh                 types.String `tfsdk:"ssh"`
	Interfaces          types.List   `tfsdk:"interfaces"`
	Volumes             types.List   `tfsdk:"volumes"`

	EgressPolicy           types.Object `tfsdk:"egress_policy"`
	EgressPolicyState      types.String `tfsdk:"egress_policy_state"`
	EgressPolicyGeneration types.Int64  `tfsdk:"egress_policy_generation"`
}

type microvmIngressModel struct {
	HTTP  types.Object `tfsdk:"http"`
	Shell types.Object `tfsdk:"shell"`
}

type microvmHTTPIngressModel struct {
	Enabled    types.Bool   `tfsdk:"enabled"`
	Port       types.Int64  `tfsdk:"port"`
	HealthPath types.String `tfsdk:"health_path"`
	ExtraPorts types.List   `tfsdk:"extra_ports"`
}

type microvmShellIngressModel struct {
	Enabled types.Bool `tfsdk:"enabled"`
}

// microvmVolumeModel is one entry of iaas_microvm.volumes (a sandbox volume mounted by the sandbox).
type microvmVolumeModel struct {
	VolumeID              types.String `tfsdk:"volume_id"`
	MountPath             types.String `tfsdk:"mount_path"`
	ReadOnly              types.Bool   `tfsdk:"read_only"`
	Noexec                types.Bool   `tfsdk:"noexec"`
	DeleteOnSandboxDelete types.Bool   `tfsdk:"delete_on_sandbox_delete"`
}

var microvmVolumeAttrTypes = map[string]attr.Type{
	"volume_id":                types.StringType,
	"mount_path":               types.StringType,
	"read_only":                types.BoolType,
	"noexec":                   types.BoolType,
	"delete_on_sandbox_delete": types.BoolType,
}

type microvmNetworkModel struct {
	Kind             types.String `tfsdk:"kind"`
	SubnetID         types.String `tfsdk:"subnet_id"`
	VPCSubnetID      types.String `tfsdk:"vpc_subnet_id"`
	StaticIpID       types.String `tfsdk:"static_ip_id"`
	SecurityGroupIDs types.List   `tfsdk:"security_group_ids"`
	RateMbit         types.Int64  `tfsdk:"rate_mbit"`
}

var microvmStaticIpAttrTypes = map[string]attr.Type{
	"id": types.StringType,
	"ip": types.StringType,
}

var microvmInterfaceAttrTypes = map[string]attr.Type{
	"kind":      types.StringType,
	"ipv4":      types.StringType,
	"ipv6":      types.StringType,
	"static_ip": types.ObjectType{AttrTypes: microvmStaticIpAttrTypes},
}

func (r *microvmResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_microvm"
}

func (r *microvmResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replaceString := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Description: "Creates and manages a MicroVM from a reusable image. Placement, image, ingress, network, and runtime policy changes replace the MicroVM; environment variables, custom domains and the outbound network policy update in place.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, Description: "UUID assigned to the MicroVM.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"hypervisor_group_id": schema.StringAttribute{
				Optional: true,
				Computed: true,
				DeprecationMessage: "Use location_id instead. hypervisor_group_id is deprecated " +
					"and will be removed in the next release.",
				Description: "Location UUID used for placement. Changing this forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplaceIfConfigured(),
				},
			},
			"location_id": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "Location UUID used for placement. Canonical replacement for " +
					"hypervisor_group_id; exactly one of the two must be set. Changing this forces a new resource.",
				PlanModifiers: []planmodifier.String{
					locationIDFromAliasModifier{},
					stringplanmodifier.RequiresReplaceIfConfigured(),
				},
			},
			"image_id": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Ready platform or account image UUID. Exactly one of image_id and snapshot_id must be set; for a sandbox started from a snapshot it reports the snapshot's image. Changing a configured value forces a new MicroVM.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplaceIfConfigured(),
				},
			},
			"snapshot_id": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "Start the MicroVM from an iaas_microvm_snapshot (a fork) instead of an image: it boots cold from a copy of the snapshot's disk, in the snapshot's location and on the node that holds the snapshot, and its plan must hold the snapshot's root filesystem by the node's rule (any plan when that filesystem is 2 GiB or less, otherwise a plan disk of at least its size in whole GiB; disk_gib is the source's plan and is not the test). " +
					"Environment variables, secrets and the outbound policy are not inherited: set them here (env, egress_policy) or use inherit_env. Exactly one of image_id and snapshot_id must be set. Changing it forces a new MicroVM. The platform reports it back (also after import), and it stays set when the snapshot is later destroyed.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplaceIfConfigured(),
				},
			},
			"inherit_env": schema.BoolAttribute{
				Optional:      true,
				Description:   "With snapshot_id: also start with the source sandbox's current environment variables (env wins on a clash). Fails if the source sandbox no longer exists. Create-time only and not returned by the platform: changing it forces a new MicroVM.",
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplace()},
			},
			"fast_start": schema.StringAttribute{
				Optional: true,
				Description: "How the MicroVM should start: allow (the platform default) takes the fastest start the location offers (a warm pooled sandbox or a restore from a template snapshot) and otherwise starts normally; cold_only always starts normally; require never starts normally and fails with HTTP 429 reason fast_start_unavailable instead (the error text says why and when to retry; nothing is created, so the apply can simply be repeated). " +
					"require is refused at once where it can never be honoured: the location has fast start switched off, or the MicroVM is a snapshot fork, has MCP servers, has more than one network interface or has no shell ingress. Create-time only and not returned by the platform: changing it forces a new MicroVM, and an imported MicroVM carries none.",
				Validators:    []validator.String{stringvalidator.OneOf("allow", "require", "cold_only")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"image_version_id": schema.StringAttribute{Optional: true, Description: "Ready version UUID. The current image version is used when omitted.", PlanModifiers: replaceString},
			"plan_id":          schema.StringAttribute{Optional: true, Description: "Plan UUID. Its disk must hold the kit's image (`required_disk_gib` on the kit, see the `iaas_microvm_catalog` data source with `image_id`): a smaller plan is refused with reason `plan_disk_too_small` and nothing is created (an image of 2 GiB or less fits any plan). When omitted, the smallest enabled location plan that holds the kit is used.", PlanModifiers: replaceString},
			"name":             schema.StringAttribute{Required: true, Description: "Account-unique lowercase name used in generated hostnames.", PlanModifiers: replaceString},
			"ssh_key_ids": schema.ListAttribute{
				Optional:      true,
				ElementType:   types.StringType,
				Description:   "Account SSH key UUIDs installed to /root/.ssh/authorized_keys and every non-system user's authorized_keys (C3). Changing this forces a new resource; the daemon writes keys once, at first boot.",
				PlanModifiers: []planmodifier.List{listplanmodifier.RequiresReplace()},
			},
			"security_group_ids": schema.SetAttribute{
				Optional:      true,
				Computed:      true,
				ElementType:   types.StringType,
				Description:   "Security groups owned by the account (at most 10) that apply to the whole MicroVM, every network interface. They are in force before the MicroVM can receive traffic; without groups the MicroVM is open on every port. Changing the set attaches and detaches groups in place (the node applies it within about 10 seconds). Read back from the API, so groups attached outside Terraform show as drift. Do not also manage the same MicroVM through iaas_security_group.sandbox_ids.",
				PlanModifiers: []planmodifier.Set{setplanmodifier.UseStateForUnknown()},
			},
			"ingress": schema.SingleNestedAttribute{
				Optional:      true,
				Description:   "HTTP and shell ingress settings.",
				PlanModifiers: []planmodifier.Object{objectplanmodifier.RequiresReplace()},
				Attributes: map[string]schema.Attribute{
					"http": schema.SingleNestedAttribute{
						Optional: true,
						Attributes: map[string]schema.Attribute{
							"enabled":     schema.BoolAttribute{Optional: true},
							"port":        schema.Int64Attribute{Optional: true, Description: "HTTP service port. Image detection or 8080 applies when omitted."},
							"health_path": schema.StringAttribute{Optional: true, Description: "HTTP deploy health-check path."},
							"extra_ports": schema.ListAttribute{Optional: true, ElementType: types.Int64Type, Description: "Additional HTTP ports that receive generated hostnames."},
						},
					},
					"shell": schema.SingleNestedAttribute{
						Optional: true,
						Attributes: map[string]schema.Attribute{
							"enabled": schema.BoolAttribute{Optional: true},
						},
					},
				},
			},
			"volumes": schema.ListNestedAttribute{
				Optional: true,
				Description: "Persistent data volumes (iaas_sandbox_volume) the sandbox starts with, in slot order (the guest sees them as the data disks in that order). At most 4, all in the sandbox's location and on ONE node: the sandbox is placed on that node and never elsewhere. " +
					"They keep their data when the sandbox stops, pauses, resumes and is deleted (unless delete_on_sandbox_delete). Create-time only: changing the list forces a new MicroVM, because a volume can be attached or detached only while the sandbox is stopped. " +
					"A volume attached or detached outside Terraform shows as a change of this list and would replace the sandbox: use lifecycle { ignore_changes = [volumes] } to manage attachments yourself. Snapshots of the sandbox do not contain these volumes, and a sandbox started from a snapshot starts without volumes unless it lists its own.",
				PlanModifiers: []planmodifier.List{listplanmodifier.RequiresReplace()},
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"volume_id":                schema.StringAttribute{Required: true, Description: "A free iaas_sandbox_volume of the account in the sandbox's location."},
					"mount_path":               schema.StringAttribute{Optional: true, Description: "Absolute path where the guest mounts the volume (letters, digits and . _ + - /, outside system directories). Default /mnt/<volume name>."},
					"read_only":                schema.BoolAttribute{Optional: true, Description: "Mount read-only."},
					"noexec":                   schema.BoolAttribute{Optional: true, Description: "Mount with noexec."},
					"delete_on_sandbox_delete": schema.BoolAttribute{Optional: true, Description: "Delete the volume (and stop billing it) when the sandbox is deleted. Default: the volume is kept, data intact."},
				}},
			},
			"network": schema.ListNestedAttribute{
				Optional:      true,
				Description:   "Ordered public and VPC network interfaces (there is no isolated kind - C1). The account's default network (iaas_microvm_settings) is used when omitted; a create with neither is a 422.",
				PlanModifiers: []planmodifier.List{listplanmodifier.RequiresReplace()},
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"kind":          schema.StringAttribute{Required: true, Validators: []validator.String{stringvalidator.OneOf("public", "vpc")}},
					"subnet_id":     schema.StringAttribute{Optional: true, Description: "Public subnet UUID for kind = public. Optional - the platform auto-assigns a subnet with free capacity in the microVM's location when omitted (C8.1)."},
					"vpc_subnet_id": schema.StringAttribute{Optional: true, Description: "Owned VPC subnet UUID. Required for kind = vpc."},
					"static_ip_id": schema.StringAttribute{
						Optional:    true,
						Description: "kind = public only: one of the account's allocated static IPs in this microVM's location. Distinct across every network entry in the same create (C8.3).",
					},
					"security_group_ids": schema.ListAttribute{Optional: true, ElementType: types.StringType, Description: "Same as the top-level security_group_ids, kept for existing configurations. Security groups apply to the whole MicroVM, not to one interface, so every network entry must name the same set; different sets are refused (422 security_groups_differ_per_nic). Prefer the top-level attribute."},
					"rate_mbit":          schema.Int64Attribute{Optional: true, Description: "Optional interface rate limit in Mbit/s."},
				}},
			},
			"max_lifetime_seconds":     schema.Int64Attribute{Optional: true, Description: "Maximum lifetime, capped server-side at 28800 seconds.", PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}},
			"on_timeout":               schema.StringAttribute{Optional: true, Description: "Timeout action: pause or kill.", Validators: []validator.String{stringvalidator.OneOf("pause", "kill")}, PlanModifiers: replaceString},
			"idle_timeout_seconds":     schema.Int64Attribute{Optional: true, Description: "Inactivity interval before pause. Null disables idle pause.", PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}},
			"always_on":                schema.BoolAttribute{Optional: true, Description: "Keep the MicroVM running and restart it after failure.", PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplace()}},
			"env":                      schema.MapAttribute{Optional: true, Sensitive: true, ElementType: types.StringType, Description: "Write-only environment variables, replaceable in place."},
			"metadata":                 schema.MapAttribute{Optional: true, ElementType: types.StringType, Description: "Customer metadata: string keys to string values, updated in place. At most 32 keys; a key is 1-64 characters of letters, digits, '_', '.' and '-' and must not start with 'vc.'; a value is at most 256 characters. Also a list filter on the API (metadata[key]=value)."},
			"lifecycle_hooks":          schema.StringAttribute{Optional: true, Description: "Lifecycle hook overrides as a JSON object.", PlanModifiers: replaceString},
			"secure":                   schema.BoolAttribute{Optional: true, Description: "Require an access token for shell ingress.", PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplace()}},
			"domains":                  schema.SetAttribute{Optional: true, ElementType: types.StringType, Description: "Custom hostnames reconciled through the domain endpoints."},
			"egress_policy":            microvmEgressSchemaAttribute(),
			"egress_policy_state":      schema.StringAttribute{Computed: true, Description: "How far the node has applied the outbound policy: `none` (no policy), `pending` (stored, not confirmed by the node yet), `applied`, `failed` (the node refused it), `unavailable` (the node cannot enforce a policy right now; the policy is not in force) or `inactive` (the sandbox is killed or errored: the stored policy is kept for reading and is in force nowhere). Null on a platform without outbound policies."},
			"egress_policy_generation": schema.Int64Attribute{Computed: true, Description: "Generation of the stored outbound policy; it rises by one on every change, whoever makes it."},
			"state":                    schema.StringAttribute{Computed: true, Description: "Current lifecycle state."},
			"fqdn":                     schema.StringAttribute{Computed: true, Description: "Generated HTTP hostname, when HTTP ingress is enabled."},
			"e2b_id":                   schema.StringAttribute{Computed: true, Description: "Short wire identifier assigned by the API.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"timeout_at":               schema.StringAttribute{Computed: true, Description: "Current maximum-lifetime deadline."},
			"current_run_status":       schema.StringAttribute{Computed: true, Description: "Status of the current run."},
			"current_run_started_at":   schema.StringAttribute{Computed: true, Description: "Start timestamp of the current run."},
			"last_resume_mode":         schema.StringAttribute{Computed: true, Description: "How the last resume brought the sandbox back: snapshot (memory kept) or fresh_boot (restarted from disk, in-memory state not kept). Null until the first resume."},
			"last_resume_reason":       schema.StringAttribute{Computed: true, Description: "Short machine reason for a fresh_boot resume, for example snapshot_missing or snapshot_version_mismatch. Null otherwise."},
			"last_resumed_at":          schema.StringAttribute{Computed: true, Description: "When the last resume happened. Null until the first resume."},
			"ssh":                      schema.StringAttribute{Computed: true, Description: "SSH command for the default-route interface (C3), e.g. \"ssh root@203.0.113.10\"."},
			"interfaces": schema.ListNestedAttribute{
				Computed:    true,
				Description: "Allocated interface addresses.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"kind": schema.StringAttribute{Computed: true},
					"ipv4": schema.StringAttribute{Computed: true},
					"ipv6": schema.StringAttribute{Computed: true},
					"static_ip": schema.SingleNestedAttribute{
						Computed:    true,
						Description: "The owner's static IP mapped to this interface, when kind = public and one was requested (C8.3/C8.6).",
						Attributes: map[string]schema.Attribute{
							"id": schema.StringAttribute{Computed: true},
							"ip": schema.StringAttribute{Computed: true},
						},
					},
				}},
			},
		},
	}
}

// ValidateConfig checks the cross-field rules a single attribute validator cannot express: exactly one of image_id
// and snapshot_id, and a location (location_id or the deprecated hypervisor_group_id, not both) unless the sandbox
// is started from a snapshot, which fixes its location.
func (r *microvmResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var image, snapshot, location, group types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("image_id"), &image)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("snapshot_id"), &snapshot)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("location_id"), &location)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("hypervisor_group_id"), &group)...)
	if resp.Diagnostics.HasError() {
		return
	}
	known := func(v types.String) bool { return !v.IsUnknown() }
	if known(image) && known(snapshot) {
		switch {
		case !image.IsNull() && !snapshot.IsNull():
			resp.Diagnostics.AddAttributeError(path.Root("snapshot_id"), "Conflicting source", "Set either image_id or snapshot_id, not both.")
		case image.IsNull() && snapshot.IsNull():
			resp.Diagnostics.AddAttributeError(path.Root("image_id"), "Missing source", "Set exactly one of image_id and snapshot_id.")
		}
	}
	if known(location) && known(group) {
		switch {
		case !location.IsNull() && !group.IsNull():
			resp.Diagnostics.AddAttributeError(path.Root("location_id"), "Conflicting location", "Set either location_id or the deprecated hypervisor_group_id, not both.")
		case location.IsNull() && group.IsNull() && known(snapshot) && snapshot.IsNull():
			resp.Diagnostics.AddAttributeError(path.Root("location_id"), "Missing location", "Set location_id (a sandbox started from snapshot_id takes the snapshot's location).")
		}
	}
}

func (r *microvmResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *microvmResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan microvmModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, diags := microvmCreateBody(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	obj, envDeferred, err := r.createWithEnvFallback(ctx, body)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error creating MicroVM", err))
		return
	}
	state := microvmStateFromAPI(ctx, obj, nil, plan, &resp.Diagnostics)
	policySet, generation, policyState, supported := microvmEgressSummary(obj)
	state.EgressPolicyState, state.EgressPolicyGeneration = types.StringNull(), types.Int64Null()
	if supported {
		state.EgressPolicyState, state.EgressPolicyGeneration = types.StringValue(policyState), types.Int64Value(generation)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	if !plan.EgressPolicy.IsNull() && !plan.EgressPolicy.IsUnknown() {
		if !supported || !policySet {
			// Fail closed: a platform that predates outbound policies drops the field and
			// would leave an unrestricted sandbox behind a configuration that asks for one.
			resp.Diagnostics.AddError("MicroVM created without its outbound policy",
				"The platform did not store egress_policy for this MicroVM (it may predate outbound network policies). The MicroVM exists and is NOT restricted; remove egress_policy or upgrade the platform, then apply again.")
			return
		}
		resp.Diagnostics.Append(r.waitEgress(ctx, id, state.State.ValueString())...)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	// Env went out in the create body (microvmCreateBody). A Master older than 3381 ignores
	// env on create without an error, so read back the key names and fall back to the env
	// endpoint (the pre-3381 behaviour) for anything that did not arrive.
	var envErr error
	if envDeferred {
		// The create was retried without env (older Master): set it the pre-3381 way.
		envErr = r.reconcileEnv(ctx, id, plan.Env, types.MapNull(types.StringType))
	} else {
		envErr = r.ensureCreateEnv(ctx, id, plan.Env)
	}
	if envErr != nil {
		resp.Diagnostics.Append(diagFromErr("Error setting MicroVM environment", envErr))
		return
	}
	if err := r.reconcileDomains(ctx, id, plan.Domains, nil); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error setting MicroVM domains", err))
		return
	}
	r.refresh(ctx, id, plan, true, &resp.State, &resp.Diagnostics)
}

// createWithEnvFallback creates the MicroVM with env in the body. A Master older than the
// create-env fix validates env as a string, so an object map is refused with a 422 whose
// errors name only env (and creates no row). In exactly that case the create is retried ONCE
// without env and the caller sets the variables through PUT /microvm/vm/{id}/env, the
// sequence the provider used before env was sent on create. A 422 about any other field, or
// a failure of the retry, is returned unchanged.
//
// If create ever carries an Idempotency-Key, the retry MUST use a new key: the first request
// was refused and a replayed key could return that refusal again.
func (r *microvmResource) createWithEnvFallback(ctx context.Context, body map[string]any) (map[string]any, bool, error) {
	obj, err := r.client.CreateMicrovm(ctx, body)
	if err == nil {
		return obj, false, nil
	}
	if _, hasEnv := body["env"]; !hasEnv || !isEnvOnlyValidationError(err) {
		return nil, false, err
	}
	retry := make(map[string]any, len(body))
	for k, v := range body {
		if k != "env" {
			retry[k] = v
		}
	}
	obj, err = r.client.CreateMicrovm(ctx, retry)
	if err != nil {
		return nil, false, err
	}
	return obj, true, nil
}

// isEnvOnlyValidationError is true for a 422 whose field errors name env (or env.*) and
// nothing else.
func isEnvOnlyValidationError(err error) bool {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnprocessableEntity || len(apiErr.FieldErrors) == 0 {
		return false
	}
	for field := range apiErr.FieldErrors {
		if field != "env" && !strings.HasPrefix(field, "env.") {
			return false
		}
	}
	return true
}

// ensureCreateEnv compares the env key names the Master reports with the configured ones and,
// when any is missing (a Master that ignored env on create), replaces the env through
// PUT /microvm/vm/{id}/env exactly as the provider did before env was sent on create.
func (r *microvmResource) ensureCreateEnv(ctx context.Context, id string, desired types.Map) error {
	if desired.IsNull() || desired.IsUnknown() || len(desired.Elements()) == 0 {
		return nil
	}
	details, err := r.client.GetMicrovm(ctx, id)
	if err != nil {
		return err
	}
	if envKeysPresent(details, desired.Elements()) {
		return nil
	}
	return r.reconcileEnv(ctx, id, desired, types.MapNull(types.StringType))
}

// envKeysPresent reports whether every configured variable name is in the SHOW envelope's
// env_keys. Only names are compared; values are never returned by the API.
func envKeysPresent(envelope map[string]any, configured map[string]attr.Value) bool {
	have := map[string]bool{}
	if arr, ok := envelope["env_keys"].([]any); ok {
		for _, v := range arr {
			if s, ok := v.(string); ok {
				have[s] = true
			}
		}
	}
	for name := range configured {
		if !have[name] {
			return false
		}
	}
	return true
}

func (r *microvmResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state microvmModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	details, err := r.client.GetMicrovm(ctx, state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("Error reading MicroVM", err))
		return
	}
	obj, domains := microvmEnvelopeParts(details)
	newState := microvmStateFromAPI(ctx, obj, domains, state, &resp.Diagnostics)
	r.applyEgressToState(ctx, state.ID.ValueString(), obj, state, &newState, false, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

func (r *microvmResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state microvmModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	if err := r.reconcileSecurityGroups(ctx, id, plan.SecurityGroupIDs, state.SecurityGroupIDs); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error updating MicroVM security groups", err))
		return
	}
	if err := r.reconcileEnv(ctx, id, plan.Env, state.Env); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error updating MicroVM environment", err))
		return
	}
	if err := r.reconcileMetadata(ctx, id, plan.Metadata, state.Metadata); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error updating MicroVM metadata", err))
		return
	}
	details, err := r.client.GetMicrovm(ctx, id)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("Error reading MicroVM domains", err))
		return
	}
	_, domains := microvmEnvelopeParts(details)
	if err := r.reconcileDomains(ctx, id, plan.Domains, domains); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error updating MicroVM domains", err))
		return
	}
	wrote, egressDiags := r.reconcileEgress(ctx, id, plan.EgressPolicy, state.EgressPolicy)
	resp.Diagnostics.Append(egressDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if wrote {
		// A node refusal is an error, but the policy IS stored: record it before returning.
		waitDiags := r.waitEgress(ctx, id, state.State.ValueString())
		r.refresh(ctx, id, plan, true, &resp.State, &resp.Diagnostics)
		resp.Diagnostics.Append(waitDiags...)
		return
	}
	r.refresh(ctx, id, plan, false, &resp.State, &resp.Diagnostics)
}

func (r *microvmResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state microvmModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteMicrovm(ctx, state.ID.ValueString()); err != nil {
		resp.Diagnostics.Append(diagFromErr("Error deleting MicroVM", err))
	}
}

func (r *microvmResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func (r *microvmResource) refresh(ctx context.Context, id string, prior microvmModel, forceEgress bool, state *tfsdk.State, diags *diag.Diagnostics) {
	details, err := r.client.GetMicrovm(ctx, id)
	if err != nil {
		diags.Append(diagFromErr("Error reading MicroVM", err))
		return
	}
	obj, domains := microvmEnvelopeParts(details)
	model := microvmStateFromAPI(ctx, obj, domains, prior, diags)
	r.applyEgressToState(ctx, id, obj, prior, &model, forceEgress, diags)
	if diags.HasError() {
		return
	}
	diags.Append(state.Set(ctx, model)...)
}

func microvmCreateBody(ctx context.Context, plan microvmModel) (map[string]any, diag.Diagnostics) {
	var diags diag.Diagnostics
	body := map[string]any{
		"name": plan.Name.ValueString(),
	}
	// A fork (snapshot_id) takes its location and image from the snapshot; the location may still be named, and the
	// platform then checks it is the snapshot's own.
	if location := effectiveLocationID(plan.LocationID, plan.HypervisorGroupID); location != "" {
		body["location_id"] = location
	}
	putOptionalString(body, "image_id", plan.ImageID)
	putOptionalString(body, "snapshot_id", plan.SnapshotID)
	putOptionalBool(body, "inherit_env", plan.InheritEnv)
	putOptionalString(body, "fast_start", plan.FastStart)
	putOptionalString(body, "image_version_id", plan.ImageVersionID)
	putOptionalString(body, "plan_id", plan.PlanID)
	putOptionalInt64(body, "max_lifetime_seconds", plan.MaxLifetimeSeconds)
	putOptionalString(body, "on_timeout", plan.OnTimeout)
	putOptionalInt64(body, "idle_timeout_seconds", plan.IdleTimeoutSeconds)
	putOptionalBool(body, "always_on", plan.AlwaysOn)
	putOptionalBool(body, "secure", plan.Secure)
	if !plan.Metadata.IsNull() && !plan.Metadata.IsUnknown() {
		var metadata map[string]string
		diags.Append(plan.Metadata.ElementsAs(ctx, &metadata, false)...)
		body["metadata"] = metadata
	}

	// 3381: the API applies env on create (object map, the same shape as the
	// env update endpoint), so the first boot already has it and the node env
	// gate answers at create time. Create verifies the key names afterwards and
	// falls back to PUT /microvm/vm/{id}/env on an older Master (ensureCreateEnv).
	if !plan.Env.IsNull() && !plan.Env.IsUnknown() {
		env := map[string]string{}
		diags.Append(plan.Env.ElementsAs(ctx, &env, false)...)
		if len(env) > 0 {
			body["env"] = env
		}
	}

	if !plan.SshKeyIDs.IsNull() && !plan.SshKeyIDs.IsUnknown() {
		var ids []string
		diags.Append(plan.SshKeyIDs.ElementsAs(ctx, &ids, false)...)
		body["ssh_key_ids"] = ids
	}

	if !plan.SecurityGroupIDs.IsNull() && !plan.SecurityGroupIDs.IsUnknown() {
		var ids []string
		diags.Append(plan.SecurityGroupIDs.ElementsAs(ctx, &ids, false)...)
		body["security_group_ids"] = ids
	}

	if !plan.Ingress.IsNull() && !plan.Ingress.IsUnknown() {
		var ingress microvmIngressModel
		diags.Append(plan.Ingress.As(ctx, &ingress, basetypes.ObjectAsOptions{})...)
		if !diags.HasError() {
			body["ingress"] = microvmIngressToAPI(ctx, ingress, &diags)
		}
	}
	if !plan.Network.IsNull() && !plan.Network.IsUnknown() {
		var entries []microvmNetworkModel
		diags.Append(plan.Network.ElementsAs(ctx, &entries, false)...)
		network := make([]map[string]any, 0, len(entries))
		for _, entry := range entries {
			item := map[string]any{"kind": entry.Kind.ValueString()}
			putOptionalString(item, "subnet_id", entry.SubnetID)
			putOptionalString(item, "vpc_subnet_id", entry.VPCSubnetID)
			putOptionalString(item, "static_ip_id", entry.StaticIpID)
			putOptionalInt64(item, "rate_mbit", entry.RateMbit)
			if !entry.SecurityGroupIDs.IsNull() && !entry.SecurityGroupIDs.IsUnknown() {
				var ids []string
				diags.Append(entry.SecurityGroupIDs.ElementsAs(ctx, &ids, false)...)
				item["security_group_ids"] = ids
			}
			network = append(network, item)
		}
		body["network"] = network
	}
	if !plan.Volumes.IsNull() && !plan.Volumes.IsUnknown() {
		var entries []microvmVolumeModel
		diags.Append(plan.Volumes.ElementsAs(ctx, &entries, false)...)
		volumes := make([]map[string]any, 0, len(entries))
		for _, entry := range entries {
			item := map[string]any{"volume_id": entry.VolumeID.ValueString()}
			putOptionalString(item, "mount_path", entry.MountPath)
			putOptionalBool(item, "read_only", entry.ReadOnly)
			putOptionalBool(item, "noexec", entry.Noexec)
			putOptionalBool(item, "delete_on_sandbox_delete", entry.DeleteOnSandboxDelete)
			volumes = append(volumes, item)
		}
		body["volumes"] = volumes
	}
	// The outbound policy is part of the create request: the sandbox is placed on a
	// node that can enforce it and never runs without it.
	egress, egressDiags := microvmEgressToAPI(ctx, plan.EgressPolicy)
	diags.Append(egressDiags...)
	if egress != nil {
		body["egress_policy"] = egress
	}
	if !plan.LifecycleHooks.IsNull() && !plan.LifecycleHooks.IsUnknown() {
		var hooks map[string]any
		if err := json.Unmarshal([]byte(plan.LifecycleHooks.ValueString()), &hooks); err != nil {
			diags.AddError("Invalid lifecycle_hooks", fmt.Sprintf("lifecycle_hooks must contain a JSON object: %s", err))
		} else {
			body["lifecycle_hooks"] = hooks
		}
	}
	return body, diags
}

func microvmIngressToAPI(ctx context.Context, ingress microvmIngressModel, diags *diag.Diagnostics) map[string]any {
	body := map[string]any{}
	if !ingress.HTTP.IsNull() && !ingress.HTTP.IsUnknown() {
		var httpIngress microvmHTTPIngressModel
		diags.Append(ingress.HTTP.As(ctx, &httpIngress, basetypes.ObjectAsOptions{})...)
		httpBody := map[string]any{}
		putOptionalBool(httpBody, "enabled", httpIngress.Enabled)
		putOptionalInt64(httpBody, "port", httpIngress.Port)
		putOptionalString(httpBody, "health_path", httpIngress.HealthPath)
		if !httpIngress.ExtraPorts.IsNull() && !httpIngress.ExtraPorts.IsUnknown() {
			var ports []int64
			diags.Append(httpIngress.ExtraPorts.ElementsAs(ctx, &ports, false)...)
			httpBody["extra_ports"] = ports
		}
		body["http"] = httpBody
	}
	if !ingress.Shell.IsNull() && !ingress.Shell.IsUnknown() {
		var shell microvmShellIngressModel
		diags.Append(ingress.Shell.As(ctx, &shell, basetypes.ObjectAsOptions{})...)
		shellBody := map[string]any{}
		putOptionalBool(shellBody, "enabled", shell.Enabled)
		body["shell"] = shellBody
	}
	return body
}

func (r *microvmResource) reconcileEnv(ctx context.Context, id string, desired, prior types.Map) error {
	if desired.Equal(prior) {
		return nil
	}
	env := map[string]string{}
	if !desired.IsNull() && !desired.IsUnknown() {
		if diags := desired.ElementsAs(ctx, &env, false); diags.HasError() {
			return fmt.Errorf("decoding env: %v", diags)
		}
	}
	_, err := r.client.SetMicrovmEnv(ctx, id, env)
	return err
}

// microvmMetadataPatch is the merge patch that turns prior into desired: every
// desired pair is sent, every prior key missing from desired is sent as nil
// (removal). Equal maps give a nil patch.
func microvmMetadataPatch(ctx context.Context, desired, prior types.Map) (map[string]any, error) {
	want := map[string]string{}
	if !desired.IsNull() && !desired.IsUnknown() {
		if diags := desired.ElementsAs(ctx, &want, false); diags.HasError() {
			return nil, fmt.Errorf("decoding metadata: %v", diags)
		}
	}
	have := map[string]string{}
	if !prior.IsNull() && !prior.IsUnknown() {
		if diags := prior.ElementsAs(ctx, &have, false); diags.HasError() {
			return nil, fmt.Errorf("decoding metadata: %v", diags)
		}
	}
	patch := map[string]any{}
	for k, v := range want {
		if old, ok := have[k]; !ok || old != v {
			patch[k] = v
		}
	}
	for k := range have {
		if _, ok := want[k]; !ok {
			patch[k] = nil
		}
	}
	if len(patch) == 0 {
		return nil, nil
	}
	return patch, nil
}

func (r *microvmResource) reconcileMetadata(ctx context.Context, id string, desired, prior types.Map) error {
	patch, err := microvmMetadataPatch(ctx, desired, prior)
	if err != nil || patch == nil {
		return err
	}
	_, err = r.client.UpdateMicrovmMetadata(ctx, id, patch)
	return err
}

func (r *microvmResource) reconcileDomains(ctx context.Context, id string, desired types.Set, current []map[string]any) error {
	wanted := map[string]bool{}
	if !desired.IsNull() && !desired.IsUnknown() {
		var names []string
		if diags := desired.ElementsAs(ctx, &names, false); diags.HasError() {
			return fmt.Errorf("decoding domains: %v", diags)
		}
		for _, name := range names {
			wanted[name] = true
		}
	}
	existing := map[string]string{}
	for _, domain := range current {
		hostname, _ := domain["hostname"].(string)
		domainID, _ := domain["id"].(string)
		if hostname != "" && domainID != "" {
			existing[hostname] = domainID
		}
	}
	for hostname, domainID := range existing {
		if !wanted[hostname] {
			if err := r.client.RemoveMicrovmDomain(ctx, id, domainID); err != nil {
				return err
			}
		}
	}
	for hostname := range wanted {
		if _, ok := existing[hostname]; !ok {
			if _, err := r.client.AddMicrovmDomain(ctx, id, hostname); err != nil {
				return err
			}
		}
	}
	return nil
}

// reconcileSecurityGroups attaches the groups added to the plan and detaches the
// ones removed, through the security-group attach-sandboxes / detach-sandboxes
// routes. A plan that does not name the attribute (unknown or null) leaves the
// groups as they are.
func (r *microvmResource) reconcileSecurityGroups(ctx context.Context, id string, desired, current types.Set) error {
	if desired.IsNull() || desired.IsUnknown() {
		return nil
	}
	want, diags := stringsFromSet(ctx, desired)
	if diags.HasError() {
		return fmt.Errorf("decoding security_group_ids: %v", diags)
	}
	have, diags := stringsFromSet(ctx, current)
	if diags.HasError() {
		return fmt.Errorf("decoding security_group_ids: %v", diags)
	}
	wantSet := map[string]bool{}
	for _, sg := range want {
		wantSet[sg] = true
	}
	haveSet := map[string]bool{}
	for _, sg := range have {
		haveSet[sg] = true
	}
	for _, sg := range have {
		if !wantSet[sg] {
			if err := r.client.DetachSecurityGroupSandboxes(ctx, sg, []string{id}); err != nil {
				return err
			}
		}
	}
	for _, sg := range want {
		if !haveSet[sg] {
			if err := r.client.AttachSecurityGroupSandboxes(ctx, sg, []string{id}); err != nil {
				return err
			}
		}
	}
	return nil
}

// microvmEnvelopeParts splits the GetMicrovm SHOW envelope into the presented
// microvm object and its domains (always non-nil so reconcilers can
// distinguish "no domains yet" from "not fetched").
func microvmEnvelopeParts(envelope map[string]any) (map[string]any, []map[string]any) {
	obj, _ := envelope["microvm"].(map[string]any)
	domains := []map[string]any{}
	if arr, ok := envelope["domains"].([]any); ok {
		for _, entry := range arr {
			if domain, ok := entry.(map[string]any); ok {
				domains = append(domains, domain)
			}
		}
	}
	return obj, domains
}

// resumeStringFromAPI reads one of the read-only resume fields. A Master that
// predates them omits the key; an unknown prior value must still become null,
// never stay unknown after apply.
func resumeStringFromAPI(obj map[string]any, key string, prior types.String) types.String {
	if _, ok := obj[key]; !ok && prior.IsUnknown() {
		return types.StringNull()
	}
	return optionalStringFromAPI(obj, key, prior)
}

func microvmStateFromAPI(ctx context.Context, obj map[string]any, domains []map[string]any, prior microvmModel, diags *diag.Diagnostics) microvmModel {
	state := prior
	state.ID = stringFromAPI(obj, "id", prior.ID)
	state.HypervisorGroupID = hypervisorGroupIDFromAPI(obj, prior.HypervisorGroupID)
	state.LocationID = locationIDFromAPI(obj, prior.LocationID)
	state.ImageID = stringFromAPI(obj, "image_id", prior.ImageID)
	state.SnapshotID = optionalStringFromAPI(obj, "source_snapshot_id", prior.SnapshotID)
	if state.SnapshotID.IsUnknown() {
		// A platform that predates snapshots does not return the field: a sandbox it created is not a fork.
		state.SnapshotID = types.StringNull()
	}
	state.ImageVersionID = optionalStringFromAPI(obj, "image_version_id", prior.ImageVersionID)
	state.PlanID = optionalStringFromAPI(obj, "plan_id", prior.PlanID)
	state.Name = stringFromAPI(obj, "name", prior.Name)
	state.State = stringFromAPI(obj, "state", prior.State)
	state.Fqdn = optionalStringFromAPI(obj, "fqdn", prior.Fqdn)
	state.E2bID = stringFromAPI(obj, "e2b_sandbox_id", prior.E2bID)
	state.TimeoutAt = optionalStringFromAPI(obj, "timeout_at", prior.TimeoutAt)
	state.Ssh = optionalStringFromAPI(obj, "ssh", prior.Ssh)
	state.LastResumeMode = resumeStringFromAPI(obj, "last_resume_mode", prior.LastResumeMode)
	state.LastResumeReason = resumeStringFromAPI(obj, "last_resume_reason", prior.LastResumeReason)
	state.LastResumedAt = resumeStringFromAPI(obj, "last_resumed_at", prior.LastResumedAt)
	if run, ok := obj["current_run"].(map[string]any); ok {
		state.CurrentRunStatus = stringFromAPI(run, "status", prior.CurrentRunStatus)
		state.CurrentRunStartedAt = optionalStringFromAPI(run, "started_at", prior.CurrentRunStartedAt)
	}
	if domains != nil {
		names := make([]string, 0, len(domains))
		for _, domain := range domains {
			if hostname, ok := domain["hostname"].(string); ok {
				names = append(names, hostname)
			}
		}
		set, domainDiags := types.SetValueFrom(ctx, types.StringType, names)
		diags.Append(domainDiags...)
		state.Domains = set
	}
	if groups, ok := obj["security_groups"].([]any); ok {
		ids := make([]string, 0, len(groups))
		for _, entry := range groups {
			if group, ok := entry.(map[string]any); ok {
				if gid, ok := group["id"].(string); ok && gid != "" {
					ids = append(ids, gid)
				}
			}
		}
		set, groupDiags := types.SetValueFrom(ctx, types.StringType, ids)
		diags.Append(groupDiags...)
		state.SecurityGroupIDs = set
	} else if state.SecurityGroupIDs.IsUnknown() {
		// An API without the field (older Master): never leave a computed value unknown after apply.
		state.SecurityGroupIDs = types.SetNull(types.StringType)
	}
	interfaces, interfaceDiags := microvmInterfacesFromAPI(obj["interfaces"])
	diags.Append(interfaceDiags...)
	state.Interfaces = interfaces
	// Metadata is managed only when the configuration (or prior state) names
	// it, so a sandbox whose metadata was set outside Terraform does not make
	// an unconfigured attribute appear. Non-string values (the E2B adapter
	// stores whatever its SDK sent) are not representable and are skipped.
	if !prior.Metadata.IsNull() {
		values := map[string]attr.Value{}
		if raw, ok := obj["metadata"].(map[string]any); ok {
			for k, v := range raw {
				if s, ok := v.(string); ok {
					values[k] = types.StringValue(s)
				}
			}
		}
		state.Metadata = types.MapValueMust(types.StringType, values)
	}
	volumes, volumeDiags := microvmVolumesFromAPI(ctx, obj["volumes"], prior.Volumes)
	diags.Append(volumeDiags...)
	state.Volumes = volumes
	return state
}

// microvmVolumesFromAPI maps the sandbox's volumes (slot order) onto the list attribute. It is tolerant of what the
// configuration left out: an optional field that is null in the prior value and has its default on the platform (no
// path chosen, flags false) stays null, so a default never shows as a change; a value that differs from a configured
// one, a volume added or removed outside Terraform, shows as drift. With no prior value (import) every field is read.
func microvmVolumesFromAPI(ctx context.Context, raw any, prior types.List) (types.List, diag.Diagnostics) {
	objectType := types.ObjectType{AttrTypes: microvmVolumeAttrTypes}
	if prior.ElementType(ctx) == nil {
		// A model built without the attribute (the zero value) is a typed null, never a typeless list.
		prior = types.ListNull(objectType)
	}
	items, ok := raw.([]any)
	if !ok {
		// A platform that predates sandbox volumes does not return the field: keep what the configuration says.
		return prior, nil
	}
	if len(items) == 0 && (prior.IsNull() || prior.IsUnknown()) {
		return types.ListNull(objectType), nil
	}
	var priorEntries []attr.Value
	if !prior.IsNull() && !prior.IsUnknown() {
		priorEntries = prior.Elements()
	}
	values := make([]attr.Value, 0, len(items))
	var diags diag.Diagnostics
	for i, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		var priorObj map[string]attr.Value
		if i < len(priorEntries) {
			if o, ok := priorEntries[i].(types.Object); ok {
				priorObj = o.Attributes()
			}
		}
		keepNull := func(name string) bool {
			if priorObj == nil {
				return false
			}
			v, ok := priorObj[name]
			return ok && v.IsNull()
		}
		pathValue := nullableStringAttr(item["mount_path"])
		if keepNull("mount_path") {
			pathValue = types.StringNull()
		}
		flag := func(name string) attr.Value {
			on, _ := item[name].(bool)
			if keepNull(name) && !on {
				return types.BoolNull()
			}
			return types.BoolValue(on)
		}
		value, itemDiags := types.ObjectValue(microvmVolumeAttrTypes, map[string]attr.Value{
			"volume_id":                nullableStringAttr(item["id"]),
			"mount_path":               pathValue,
			"read_only":                flag("read_only"),
			"noexec":                   flag("noexec"),
			"delete_on_sandbox_delete": flag("delete_on_sandbox_delete"),
		})
		diags.Append(itemDiags...)
		values = append(values, value)
	}
	list, listDiags := types.ListValue(objectType, values)
	diags.Append(listDiags...)
	return list, diags
}

func microvmInterfacesFromAPI(raw any) (types.List, diag.Diagnostics) {
	objectType := types.ObjectType{AttrTypes: microvmInterfaceAttrTypes}
	items, ok := raw.([]any)
	if !ok {
		return types.ListNull(objectType), nil
	}
	values := make([]attr.Value, 0, len(items))
	var diags diag.Diagnostics
	for _, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		staticIP, staticIPDiags := microvmStaticIpFromAPI(item["static_ip"])
		diags.Append(staticIPDiags...)
		value, itemDiags := types.ObjectValue(microvmInterfaceAttrTypes, map[string]attr.Value{
			"kind":      nullableStringAttr(item["kind"]),
			"ipv4":      nullableStringAttr(item["ipv4"]),
			"ipv6":      nullableStringAttr(item["ipv6"]),
			"static_ip": staticIP,
		})
		diags.Append(itemDiags...)
		values = append(values, value)
	}
	list, listDiags := types.ListValue(objectType, values)
	diags.Append(listDiags...)
	return list, diags
}

// microvmStaticIpFromAPI reads an interface's static_ip sub-object ({id, ip}
// | null, C8.6). A missing/non-object/nil value is a null object, matching
// the "no static IP mapped to this interface" case for a plain public or vpc
// entry.
func microvmStaticIpFromAPI(raw any) (types.Object, diag.Diagnostics) {
	obj, ok := raw.(map[string]any)
	if !ok {
		return types.ObjectNull(microvmStaticIpAttrTypes), nil
	}
	return types.ObjectValue(microvmStaticIpAttrTypes, map[string]attr.Value{
		"id": nullableStringAttr(obj["id"]),
		"ip": nullableStringAttr(obj["ip"]),
	})
}

func nullableStringAttr(raw any) types.String {
	if raw == nil {
		return types.StringNull()
	}
	if value, ok := raw.(string); ok {
		return types.StringValue(value)
	}
	return types.StringValue(fmt.Sprintf("%v", raw))
}

func putOptionalInt64(body map[string]any, key string, value types.Int64) {
	if !value.IsNull() && !value.IsUnknown() {
		body[key] = value.ValueInt64()
	}
}

func putOptionalBool(body map[string]any, key string, value types.Bool) {
	if !value.IsNull() && !value.IsUnknown() {
		body[key] = value.ValueBool()
	}
}
