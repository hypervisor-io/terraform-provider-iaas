package resources

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
	"github.com/hypervisor-io/terraform-provider-iaas/waiter"
)

// The outbound network policy of a sandbox, modelled as the `egress_policy`
// attribute of iaas_microvm (like env and domains: a sub-resource of the
// sandbox reconciled in place through its own endpoints).
//
//	create  the policy is part of the create request, so the sandbox never
//	        runs without it
//	update  PUT /microvm/vm/{id}/egress (whole document) or DELETE when the
//	        attribute is removed
//	read    the sandbox read carries a summary with the policy generation;
//	        when it moved, the policy is fetched and compared with the
//	        configuration by meaning, so a change made outside OpenTofu shows
//	        as a diff and a merely normalised value does not
//
// The platform stores a normalised document (rule ids assigned, names
// lower-cased, protocol and inbound_replies defaulted, single ports as
// numbers). State keeps the configured spelling while it means the same.

var microvmEgressRuleAttrTypes = map[string]attr.Type{
	"id":       types.StringType,
	"action":   types.StringType,
	"target":   types.StringType,
	"protocol": types.StringType,
	"ports":    types.ListType{ElemType: types.StringType},
	"comment":  types.StringType,
}

var microvmEgressPolicyAttrTypes = map[string]attr.Type{
	"default_action":  types.StringType,
	"inbound_replies": types.StringType,
	"rules":           types.ListType{ElemType: types.ObjectType{AttrTypes: microvmEgressRuleAttrTypes}},
}

type microvmEgressPolicyModel struct {
	DefaultAction  types.String `tfsdk:"default_action"`
	InboundReplies types.String `tfsdk:"inbound_replies"`
	Rules          types.List   `tfsdk:"rules"`
}

type microvmEgressRuleModel struct {
	ID       types.String `tfsdk:"id"`
	Action   types.String `tfsdk:"action"`
	Target   types.String `tfsdk:"target"`
	Protocol types.String `tfsdk:"protocol"`
	Ports    types.List   `tfsdk:"ports"`
	Comment  types.String `tfsdk:"comment"`
}

var (
	microvmEgressPortPattern = regexp.MustCompile(`^[0-9]{1,5}(-[0-9]{1,5})?$`)
	microvmEgressIDPattern   = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,32}$`)
	// An id the platform assigned itself; never tracked in state.
	microvmEgressAutoIDPattern = regexp.MustCompile(`^r_[0-9a-f]{8}$`)

	// How long an apply waits for the node to confirm a changed policy, and
	// how often it asks. Variables so tests do not sleep.
	microvmEgressWaitTimeout  = 90 * time.Second
	microvmEgressWaitInterval = 2 * time.Second
)

func microvmEgressSchemaAttribute() schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		Optional: true,
		Description: "Outbound network policy of the sandbox, changed in place. Omit for unrestricted outbound access; removing the attribute removes the policy. " +
			"A policy set at create is in force from the first boot (the sandbox is placed only on a node that can enforce it). " +
			"A change is sent to the node in the background: the apply waits for a running or paused sandbox until `egress_policy_state` is `applied` and fails when the node refuses the policy; a stopped sandbox keeps it `pending` and starts with it. " +
			"The platform always blocks some destinations whatever the policy says (metadata and link-local addresses, loopback, private networks other than the sandbox's own VPC subnets, the platform's own addresses, tcp 25 and tcp 853); an allow rule that could only match those is refused. " +
			"A policy changed outside OpenTofu shows as a diff on the next plan.",
		Attributes: map[string]schema.Attribute{
			"default_action": schema.StringAttribute{
				Required:    true,
				Description: "What happens to outbound traffic no rule matches: `allow` or `deny`.",
				Validators:  []validator.String{stringvalidator.OneOf("allow", "deny")},
			},
			"inbound_replies": schema.StringAttribute{
				Optional:    true,
				Description: "Which replies to inbound connections may leave the sandbox: `service_ports` (only the ports the platform dials or publishes) or `any`. When omitted the platform uses `service_ports` with `default_action = \"deny\"` and `any` with `allow`. `any` is not accepted together with `deny`.",
				Validators:  []validator.String{stringvalidator.OneOf("service_ports", "any")},
			},
			"rules": schema.ListNestedAttribute{
				Optional:    true,
				Description: "Rules, matched before the default action, at most 100. A deny rule wins over an allow rule for the same destination.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"action": schema.StringAttribute{
						Required:    true,
						Description: "`allow` or `deny`.",
						Validators:  []validator.String{stringvalidator.OneOf("allow", "deny")},
					},
					"target": schema.StringAttribute{
						Required:    true,
						Description: "Host name (`api.github.com`), wildcard (`*.example.com`, which does not match `example.com` itself), IPv4 or IPv6 address, or a network in CIDR form. Write internationalised names in their `xn--` form.",
						Validators:  []validator.String{stringvalidator.LengthAtLeast(1)},
					},
					"protocol": schema.StringAttribute{
						Optional:    true,
						Description: "`tcp`, `udp`, `icmp` or `any`. Omitted means `any`.",
						Validators:  []validator.String{stringvalidator.OneOf("tcp", "udp", "icmp", "any")},
					},
					"ports": schema.ListAttribute{
						Optional:    true,
						ElementType: types.StringType,
						Description: "Destination ports as strings: a port (`\"443\"`) or, on address and network rules only, a range (`\"8000-8100\"`). Omitted means every port. A host name rule takes at most 8 ports.",
						Validators:  []validator.List{microvmEgressPortsValidator{}},
					},
					"comment": schema.StringAttribute{
						Optional:    true,
						Description: "A note, at most 120 characters.",
						Validators:  []validator.String{stringvalidator.LengthAtMost(120)},
					},
					"id": schema.StringAttribute{
						Optional:    true,
						Description: "Rule id (1 to 32 letters, digits, `_`, `.`, `-`), unique within the policy. Optional: the platform assigns one when omitted, and an assigned id is not tracked here.",
						Validators:  []validator.String{stringvalidator.RegexMatches(microvmEgressIDPattern, "must be 1 to 32 characters of letters, digits, underscore, dot or hyphen")},
					},
				}},
			},
		},
	}
}

// microvmEgressPortsValidator checks every element is a port or a range at
// plan time, so a typo is reported against the configuration and not as an
// API refusal half way through an apply.
type microvmEgressPortsValidator struct{}

func (microvmEgressPortsValidator) Description(context.Context) string {
	return "each element is a port (\"443\") or a range (\"8000-8100\")"
}

func (v microvmEgressPortsValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (microvmEgressPortsValidator) ValidateList(_ context.Context, req validator.ListRequest, resp *validator.ListResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	for i, element := range req.ConfigValue.Elements() {
		value, ok := element.(types.String)
		if !ok || value.IsNull() || value.IsUnknown() {
			continue
		}
		if _, valid := microvmEgressNormalPort(value.ValueString()); !valid {
			resp.Diagnostics.AddAttributeError(req.Path.AtListIndex(i), "Invalid port",
				fmt.Sprintf("%q is not a port from 1 to 65535 or a range such as \"8000-8100\".", value.ValueString()))
		}
	}
}

// microvmEgressNormalPort returns the canonical text of a port or range
// ("443", "8000-8100"; a range of one port is that port).
func microvmEgressNormalPort(text string) (string, bool) {
	text = strings.TrimSpace(text)
	if !microvmEgressPortPattern.MatchString(text) {
		return "", false
	}
	lo, hi, isRange := strings.Cut(text, "-")
	low, _ := strconv.Atoi(lo)
	high := low
	if isRange {
		high, _ = strconv.Atoi(hi)
	}
	if low < 1 || high > 65535 || low > high {
		return "", false
	}
	if low == high {
		return strconv.Itoa(low), true
	}
	return strconv.Itoa(low) + "-" + strconv.Itoa(high), true
}

// microvmEgressToAPI turns the configured attribute into the policy document
// the API takes. A null or unknown attribute is nil (no policy). Only what
// the configuration says is sent; the platform applies its own defaults.
func microvmEgressToAPI(ctx context.Context, value types.Object) (map[string]any, diag.Diagnostics) {
	var diags diag.Diagnostics
	if value.IsNull() || value.IsUnknown() {
		return nil, diags
	}
	var policy microvmEgressPolicyModel
	diags.Append(value.As(ctx, &policy, basetypes.ObjectAsOptions{})...)
	if diags.HasError() {
		return nil, diags
	}
	body := map[string]any{"default_action": policy.DefaultAction.ValueString()}
	putOptionalString(body, "inbound_replies", policy.InboundReplies)

	rules := []any{}
	if !policy.Rules.IsNull() && !policy.Rules.IsUnknown() {
		var models []microvmEgressRuleModel
		diags.Append(policy.Rules.ElementsAs(ctx, &models, false)...)
		for _, model := range models {
			rule := map[string]any{"action": model.Action.ValueString(), "target": model.Target.ValueString()}
			putOptionalString(rule, "protocol", model.Protocol)
			putOptionalString(rule, "comment", model.Comment)
			putOptionalString(rule, "id", model.ID)
			if !model.Ports.IsNull() && !model.Ports.IsUnknown() {
				var texts []string
				diags.Append(model.Ports.ElementsAs(ctx, &texts, false)...)
				ports := make([]any, 0, len(texts))
				for _, text := range texts {
					// A single port travels as a number and a range as "lo-hi", the API's own shapes.
					normal, ok := microvmEgressNormalPort(text)
					if !ok {
						ports = append(ports, text)
					} else if number, err := strconv.Atoi(normal); err == nil {
						ports = append(ports, number)
					} else {
						ports = append(ports, normal)
					}
				}
				if len(ports) > 0 {
					rule["ports"] = ports
				}
			}
			rules = append(rules, rule)
		}
	}
	body["rules"] = rules
	return body, diags
}

// microvmEgressRule is one rule reduced to what it means, for comparison.
type microvmEgressRule struct {
	id, action, target, protocol, comment string
	ports                                 []string
}

func microvmEgressDefaultReplies(defaultAction string) string {
	if defaultAction == "allow" {
		return "any"
	}
	return "service_ports"
}

// microvmEgressNormalTarget compares targets the way the platform stores
// them: names lower-cased without a trailing dot, addresses and networks in
// their canonical text.
func microvmEgressNormalTarget(target string) string {
	target = strings.ToLower(strings.TrimSpace(target))
	if prefix, err := netip.ParsePrefix(target); err == nil {
		return prefix.Masked().String()
	}
	if addr, err := netip.ParseAddr(target); err == nil {
		return addr.String()
	}
	return strings.TrimSuffix(target, ".")
}

func microvmEgressRuleFromAPI(raw any) microvmEgressRule {
	obj, _ := raw.(map[string]any)
	rule := microvmEgressRule{
		id:       stringOrEmpty(obj["id"]),
		action:   stringOrEmpty(obj["action"]),
		target:   microvmEgressNormalTarget(stringOrEmpty(obj["target"])),
		protocol: stringOrEmpty(obj["protocol"]),
		comment:  stringOrEmpty(obj["comment"]),
	}
	if rule.protocol == "" {
		rule.protocol = "any"
	}
	if ports, ok := obj["ports"].([]any); ok {
		for _, port := range ports {
			text := stringOrEmpty(port)
			if number, isNumber := port.(float64); isNumber {
				text = strconv.Itoa(int(number))
			}
			if normal, valid := microvmEgressNormalPort(text); valid {
				text = normal
			}
			rule.ports = append(rule.ports, text)
		}
	}
	return rule
}

func stringOrEmpty(raw any) string {
	if text, ok := raw.(string); ok {
		return text
	}
	return ""
}

func microvmEgressRuleFromModel(ctx context.Context, model microvmEgressRuleModel, diags *diag.Diagnostics) microvmEgressRule {
	rule := microvmEgressRule{
		id:       model.ID.ValueString(),
		action:   model.Action.ValueString(),
		target:   microvmEgressNormalTarget(model.Target.ValueString()),
		protocol: model.Protocol.ValueString(),
		comment:  model.Comment.ValueString(),
	}
	if rule.protocol == "" {
		rule.protocol = "any"
	}
	if !model.Ports.IsNull() && !model.Ports.IsUnknown() {
		var texts []string
		diags.Append(model.Ports.ElementsAs(ctx, &texts, false)...)
		for _, text := range texts {
			if normal, valid := microvmEgressNormalPort(text); valid {
				text = normal
			}
			rule.ports = append(rule.ports, text)
		}
	}
	return rule
}

// microvmEgressSameRule: the stored rule means what the configured one says.
// A configured rule without an id matches whatever id the platform holds.
func microvmEgressSameRule(configured, stored microvmEgressRule) bool {
	if configured.action != stored.action || configured.target != stored.target ||
		configured.protocol != stored.protocol || configured.comment != stored.comment {
		return false
	}
	if configured.id != "" && configured.id != stored.id {
		return false
	}
	if len(configured.ports) != len(stored.ports) {
		return false
	}
	for i := range configured.ports {
		if configured.ports[i] != stored.ports[i] {
			return false
		}
	}
	return true
}

// microvmEgressFromAPI builds the state value of egress_policy from the
// policy the platform holds (nil = none). While the stored policy means the
// same as prior (the configured or previously read value), prior is kept
// verbatim so normalisation never shows as a change; anything else is drift
// and state takes the stored policy in its shortest form.
func microvmEgressFromAPI(ctx context.Context, policy map[string]any, prior types.Object) (types.Object, diag.Diagnostics) {
	var diags diag.Diagnostics
	if policy == nil {
		return types.ObjectNull(microvmEgressPolicyAttrTypes), diags
	}
	defaultAction := stringOrEmpty(policy["default_action"])
	replies := stringOrEmpty(policy["inbound_replies"])
	if replies == "" {
		replies = microvmEgressDefaultReplies(defaultAction)
	}
	rawRules, _ := policy["rules"].([]any)
	stored := make([]microvmEgressRule, 0, len(rawRules))
	for _, raw := range rawRules {
		stored = append(stored, microvmEgressRuleFromAPI(raw))
	}

	if !prior.IsNull() && !prior.IsUnknown() && len(prior.AttributeTypes(ctx)) > 0 {
		var configured microvmEgressPolicyModel
		diags.Append(prior.As(ctx, &configured, basetypes.ObjectAsOptions{})...)
		if !diags.HasError() && microvmEgressSamePolicy(ctx, configured, defaultAction, replies, stored, &diags) {
			return prior, diags
		}
	}

	rules := make([]attr.Value, 0, len(stored))
	for _, rule := range stored {
		ports := types.ListNull(types.StringType)
		if len(rule.ports) > 0 {
			values := make([]attr.Value, 0, len(rule.ports))
			for _, port := range rule.ports {
				values = append(values, types.StringValue(port))
			}
			list, listDiags := types.ListValue(types.StringType, values)
			diags.Append(listDiags...)
			ports = list
		}
		id := types.StringNull()
		if rule.id != "" && !microvmEgressAutoIDPattern.MatchString(rule.id) {
			id = types.StringValue(rule.id)
		}
		protocol := types.StringNull()
		if rule.protocol != "any" {
			protocol = types.StringValue(rule.protocol)
		}
		comment := types.StringNull()
		if rule.comment != "" {
			comment = types.StringValue(rule.comment)
		}
		value, ruleDiags := types.ObjectValue(microvmEgressRuleAttrTypes, map[string]attr.Value{
			"id": id, "action": types.StringValue(rule.action), "target": types.StringValue(rule.target),
			"protocol": protocol, "ports": ports, "comment": comment,
		})
		diags.Append(ruleDiags...)
		rules = append(rules, value)
	}
	ruleType := types.ObjectType{AttrTypes: microvmEgressRuleAttrTypes}
	ruleList := types.ListNull(ruleType)
	if len(rules) > 0 {
		list, listDiags := types.ListValue(ruleType, rules)
		diags.Append(listDiags...)
		ruleList = list
	}
	inbound := types.StringNull()
	if replies != microvmEgressDefaultReplies(defaultAction) {
		inbound = types.StringValue(replies)
	}
	value, valueDiags := types.ObjectValue(microvmEgressPolicyAttrTypes, map[string]attr.Value{
		"default_action": types.StringValue(defaultAction), "inbound_replies": inbound, "rules": ruleList,
	})
	diags.Append(valueDiags...)
	return value, diags
}

func microvmEgressSamePolicy(ctx context.Context, configured microvmEgressPolicyModel, defaultAction, replies string, stored []microvmEgressRule, diags *diag.Diagnostics) bool {
	if configured.DefaultAction.ValueString() != defaultAction {
		return false
	}
	wantReplies := configured.InboundReplies.ValueString()
	if wantReplies == "" {
		wantReplies = microvmEgressDefaultReplies(defaultAction)
	}
	if wantReplies != replies {
		return false
	}
	var models []microvmEgressRuleModel
	if !configured.Rules.IsNull() && !configured.Rules.IsUnknown() {
		diags.Append(configured.Rules.ElementsAs(ctx, &models, false)...)
	}
	if len(models) != len(stored) {
		return false
	}
	for i, model := range models {
		if !microvmEgressSameRule(microvmEgressRuleFromModel(ctx, model, diags), stored[i]) {
			return false
		}
	}
	return true
}

// microvmEgressSummary reads the sandbox's `egress` summary. ok is false on a
// platform that predates outbound policies (the key is absent).
func microvmEgressSummary(obj map[string]any) (policySet bool, generation int64, state string, ok bool) {
	summary, has := obj["egress"].(map[string]any)
	if !has {
		return false, 0, "", false
	}
	policySet, _ = summary["policy_set"].(bool)
	return policySet, int64(numberFromAPI(summary["generation"])), stringOrEmpty(summary["state"]), true
}

func numberFromAPI(raw any) float64 {
	switch value := raw.(type) {
	case float64:
		return value
	case int:
		return float64(value)
	case int64:
		return float64(value)
	}
	return 0
}

// applyEgressToState fills egress_policy, egress_policy_state and
// egress_policy_generation of state from the sandbox object. The policy
// document is fetched only when the platform's generation differs from the
// one in prior (or prior has none: first read, import), or when force is set
// (right after this resource wrote it).
func (r *microvmResource) applyEgressToState(ctx context.Context, id string, obj map[string]any, prior microvmModel, state *microvmModel, force bool, diags *diag.Diagnostics) {
	priorPolicy := prior.EgressPolicy
	if len(priorPolicy.AttributeTypes(ctx)) == 0 {
		// A model built without the attribute (zero value) carries no type.
		priorPolicy = types.ObjectNull(microvmEgressPolicyAttrTypes)
	}
	state.EgressPolicy = priorPolicy

	policySet, generation, policyState, supported := microvmEgressSummary(obj)
	if !supported {
		// The platform has no outbound policies: nothing is set there.
		state.EgressPolicy = types.ObjectNull(microvmEgressPolicyAttrTypes)
		state.EgressPolicyState = types.StringNull()
		state.EgressPolicyGeneration = types.Int64Null()
		return
	}
	state.EgressPolicyState = types.StringValue(policyState)
	state.EgressPolicyGeneration = types.Int64Value(generation)

	known := !prior.EgressPolicyGeneration.IsNull() && !prior.EgressPolicyGeneration.IsUnknown()
	if !force && known && prior.EgressPolicyGeneration.ValueInt64() == generation && policySet == !priorPolicy.IsNull() {
		return
	}
	read, err := r.client.GetMicrovmEgress(ctx, id)
	if err != nil {
		diags.Append(diagFromErr("Error reading MicroVM outbound policy", err))
		return
	}
	policy, _ := read["policy"].(map[string]any)
	value, valueDiags := microvmEgressFromAPI(ctx, policy, priorPolicy)
	diags.Append(valueDiags...)
	state.EgressPolicy = value
	state.EgressPolicyState = types.StringValue(stringOrEmpty(read["state"]))
	state.EgressPolicyGeneration = types.Int64Value(int64(numberFromAPI(read["generation"])))
}

// reconcileEgress sends the configured policy when it differs from prior:
// PUT for a policy, DELETE when the attribute was removed. It returns whether
// a write went out.
func (r *microvmResource) reconcileEgress(ctx context.Context, id string, desired, prior types.Object) (bool, diag.Diagnostics) {
	var diags diag.Diagnostics
	if desired.IsUnknown() || desired.Equal(prior) {
		return false, diags
	}
	if desired.IsNull() {
		if prior.IsNull() || len(prior.AttributeTypes(ctx)) == 0 {
			return false, diags
		}
		if _, err := r.client.ClearMicrovmEgress(ctx, id); err != nil {
			diags.Append(diagFromErr("Error removing MicroVM outbound policy", err))
		}
		return !diags.HasError(), diags
	}
	body, bodyDiags := microvmEgressToAPI(ctx, desired)
	diags.Append(bodyDiags...)
	if diags.HasError() {
		return false, diags
	}
	if _, err := r.client.SetMicrovmEgress(ctx, id, body); err != nil {
		diags.Append(diagFromErr("Error setting MicroVM outbound policy", err))
	}
	return !diags.HasError(), diags
}

// waitEgress waits for the node to confirm the policy this resource just
// wrote. A stopped sandbox is not waited for: it keeps the policy pending and
// starts with it. A node refusal fails the apply; a node that has simply not
// answered within the wait is a warning, because the policy is stored and the
// platform keeps retrying.
func (r *microvmResource) waitEgress(ctx context.Context, id string, sandboxState string) diag.Diagnostics {
	var diags diag.Diagnostics
	if sandboxState != "running" && sandboxState != "paused" {
		return diags
	}
	var last map[string]any
	ended := false
	err := waiter.WaitFor(ctx, waiter.Options{
		Interval: microvmEgressWaitInterval,
		Timeout:  microvmEgressWaitTimeout,
		Refresh: func() (string, bool, error) {
			read, err := r.client.GetMicrovmEgress(ctx, id)
			if err != nil {
				return "", false, err
			}
			last = read
			state := stringOrEmpty(read["state"])
			code := stringOrEmpty(read["last_error_code"])
			switch state {
			case "applied", "none":
				if numberFromAPI(read["applied_generation"]) >= numberFromAPI(read["generation"]) {
					return state, true, nil
				}
				return state, false, nil
			case "failed":
				return state, false, &microvmEgressRefused{state: state, code: code}
			case "unavailable":
				return state, false, &microvmEgressRefused{state: state, code: code}
			case "inactive":
				// The sandbox was killed or errored while this apply waited: no node
				// will ever confirm the policy, so there is nothing left to wait for.
				ended = true
				return state, true, nil
			}
			return state, false, nil
		},
	})
	if err == nil {
		if ended {
			diags.AddWarning("MicroVM outbound policy is stored and not in force",
				"The sandbox is no longer running (it was killed or failed), so the stored policy is in force nowhere (egress_policy_state is inactive).")
		}
		return diags
	}
	var refused *microvmEgressRefused
	if errors.As(err, &refused) {
		if refused.state == "unavailable" {
			diags.AddError("MicroVM outbound policy is not in force",
				fmt.Sprintf("The policy is stored, but the sandbox's node cannot enforce an outbound policy right now (%s). The platform retries; check egress_policy_state.", refused.code))
		} else {
			diags.AddError("MicroVM outbound policy was refused by the node",
				fmt.Sprintf("The node refused the stored policy (%s). The policy is not in force; correct it and apply again.", refused.code))
		}
		return diags
	}
	if client.IsNotFound(err) || last == nil {
		diags.Append(diagFromErr("Error reading MicroVM outbound policy", err))
		return diags
	}
	diags.AddWarning("MicroVM outbound policy not confirmed yet",
		fmt.Sprintf("The policy is stored (generation %d) and the node has not confirmed it yet (state %q). The platform keeps retrying; run a refresh to see egress_policy_state.",
			int64(numberFromAPI(last["generation"])), stringOrEmpty(last["state"])))
	return diags
}

type microvmEgressRefused struct{ state, code string }

func (e *microvmEgressRefused) Error() string {
	return "outbound policy " + e.state + ": " + e.code
}
