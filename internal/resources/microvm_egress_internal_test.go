package resources

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
)

// ---- fixtures ---------------------------------------------------------------

type egressRuleFixture struct {
	action, target, protocol, comment, id string
	ports                                 []string
}

func egressRuleValue(t *testing.T, rule egressRuleFixture) attr.Value {
	t.Helper()
	optional := func(text string) types.String {
		if text == "" {
			return types.StringNull()
		}
		return types.StringValue(text)
	}
	ports := types.ListNull(types.StringType)
	if rule.ports != nil {
		values := make([]attr.Value, 0, len(rule.ports))
		for _, port := range rule.ports {
			values = append(values, types.StringValue(port))
		}
		ports = types.ListValueMust(types.StringType, values)
	}
	value, diags := types.ObjectValue(microvmEgressRuleAttrTypes, map[string]attr.Value{
		"id": optional(rule.id), "action": types.StringValue(rule.action), "target": types.StringValue(rule.target),
		"protocol": optional(rule.protocol), "ports": ports, "comment": optional(rule.comment),
	})
	if diags.HasError() {
		t.Fatal(diags)
	}
	return value
}

func egressPolicyValue(t *testing.T, defaultAction, inboundReplies string, rules ...egressRuleFixture) types.Object {
	t.Helper()
	ruleType := types.ObjectType{AttrTypes: microvmEgressRuleAttrTypes}
	list := types.ListNull(ruleType)
	if rules != nil {
		values := make([]attr.Value, 0, len(rules))
		for _, rule := range rules {
			values = append(values, egressRuleValue(t, rule))
		}
		list = types.ListValueMust(ruleType, values)
	}
	replies := types.StringNull()
	if inboundReplies != "" {
		replies = types.StringValue(inboundReplies)
	}
	value, diags := types.ObjectValue(microvmEgressPolicyAttrTypes, map[string]attr.Value{
		"default_action": types.StringValue(defaultAction), "inbound_replies": replies, "rules": list,
	})
	if diags.HasError() {
		t.Fatal(diags)
	}
	return value
}

// The configuration a user would write: mixed case, no protocol, ports as strings.
func egressConfigured(t *testing.T) types.Object {
	return egressPolicyValue(t, "deny", "",
		egressRuleFixture{action: "allow", target: "API.GitHub.com", ports: []string{"443"}},
		egressRuleFixture{action: "allow", target: "8.8.8.8", protocol: "tcp", ports: []string{"443", "8000-8100"}, comment: "resolver over https"},
	)
}

// What the platform stores for egressConfigured: normalised, ids assigned.
const egressStoredPolicy = `{"default_action":"deny","inbound_replies":"service_ports","rules":[{"id":"r_4f2a9c1e","action":"allow","target":"api.github.com","protocol":"any","ports":[443]},{"id":"r_0b7d33aa","action":"allow","target":"8.8.8.8","protocol":"tcp","ports":[443,"8000-8100"],"comment":"resolver over https"}]}`

func decodeObject(t *testing.T, text string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("bad fixture JSON: %v", err)
	}
	return out
}

// microvmTestModel is a model with every collection attribute typed, as the
// framework hands one over from a plan or state.
func microvmTestModel(t *testing.T, egress types.Object) microvmModel {
	t.Helper()
	res := NewMicrovmResource()
	var resp resource.SchemaResponse
	res.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	objectTypeOf := func(name string) map[string]attr.Type {
		return resp.Schema.Attributes[name].GetType().(types.ObjectType).AttrTypes
	}
	return microvmModel{
		ID:                     types.StringValue("vm-1"),
		HypervisorGroupID:      types.StringValue("group-1"),
		LocationID:             types.StringValue("group-1"),
		ImageID:                types.StringValue("image-1"),
		ImageVersionID:         types.StringNull(),
		PlanID:                 types.StringNull(),
		Name:                   types.StringValue("agent"),
		SecurityGroupIDs:       types.SetNull(types.StringType),
		Metadata:               types.MapNull(types.StringType),
		SshKeyIDs:              types.ListNull(types.StringType),
		Ingress:                types.ObjectNull(objectTypeOf("ingress")),
		Network:                types.ListNull(resp.Schema.Attributes["network"].GetType().(types.ListType).ElemType),
		MaxLifetimeSeconds:     types.Int64Null(),
		OnTimeout:              types.StringNull(),
		IdleTimeoutSeconds:     types.Int64Null(),
		AlwaysOn:               types.BoolNull(),
		Env:                    types.MapNull(types.StringType),
		LifecycleHooks:         types.StringNull(),
		Secure:                 types.BoolNull(),
		Domains:                types.SetNull(types.StringType),
		State:                  types.StringValue("running"),
		Fqdn:                   types.StringNull(),
		E2bID:                  types.StringValue("e2b-1"),
		TimeoutAt:              types.StringNull(),
		CurrentRunStatus:       types.StringNull(),
		CurrentRunStartedAt:    types.StringNull(),
		Ssh:                    types.StringNull(),
		Interfaces:             types.ListNull(types.ObjectType{AttrTypes: microvmInterfaceAttrTypes}),
		EgressPolicy:           egress,
		EgressPolicyState:      types.StringNull(),
		EgressPolicyGeneration: types.Int64Null(),
	}
}

// egressServer is a fake of the sandbox API: it answers the sandbox read with
// the summary and records every request to the policy endpoints.
type egressServer struct {
	mu          sync.Mutex
	requests    []string
	bodies      []string
	summary     string   // the `egress` summary on the sandbox read; "" = platform without policies
	egressReads []string // answers of successive GET /egress (the last one repeats)
	created     string   // the sandbox object answered by POST /microvm/vms
}

func (s *egressServer) start(t *testing.T) *client.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		defer s.mu.Unlock()
		s.requests = append(s.requests, r.Method+" "+r.URL.Path)
		s.bodies = append(s.bodies, string(raw))
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/microvm/vms":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"success":true,"microvm":` + s.created + `}`))
		case strings.HasSuffix(r.URL.Path, "/egress") && r.Method == http.MethodGet:
			answer := s.egressReads[0]
			if len(s.egressReads) > 1 {
				s.egressReads = s.egressReads[1:]
			}
			_, _ = w.Write([]byte(`{"success":true,"egress":` + answer + `}`))
		case strings.HasSuffix(r.URL.Path, "/egress"):
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"success":true,"egress":{"policy":null,"generation":9,"applied_generation":0,"state":"pending"}}`))
		default:
			_, _ = w.Write([]byte(`{"success":true,"microvm":` + s.sandbox() + `,"domains":[]}`))
		}
	}))
	t.Cleanup(server.Close)
	return client.New(server.URL+"/api", "token", 0, false)
}

func (s *egressServer) sandbox() string {
	base := `{"id":"vm-1","name":"agent","state":"running","image_id":"image-1","location_id":"group-1","e2b_sandbox_id":"e2b-1","interfaces":[]`
	if s.summary == "" {
		return base + `}`
	}
	return base + `,"egress":` + s.summary + `}`
}

func (s *egressServer) policyRequests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, request := range s.requests {
		if strings.HasSuffix(request, "/egress") {
			out = append(out, request)
		}
	}
	return out
}

func fastEgressWait(t *testing.T, timeout time.Duration) {
	t.Helper()
	oldTimeout, oldInterval := microvmEgressWaitTimeout, microvmEgressWaitInterval
	microvmEgressWaitTimeout, microvmEgressWaitInterval = timeout, time.Millisecond
	t.Cleanup(func() { microvmEgressWaitTimeout, microvmEgressWaitInterval = oldTimeout, oldInterval })
}

func mustNoError(t *testing.T, diags diag.Diagnostics) {
	t.Helper()
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}
}

// ---- schema -----------------------------------------------------------------

func TestMicrovmEgressSchemaIsOptionalInPlaceAndValidated(t *testing.T) {
	res := NewMicrovmResource()
	var resp resource.SchemaResponse
	res.Schema(context.Background(), resource.SchemaRequest{}, &resp)

	policy, ok := resp.Schema.Attributes["egress_policy"].(schema.SingleNestedAttribute)
	if !ok {
		t.Fatalf("egress_policy is not a SingleNestedAttribute: %T", resp.Schema.Attributes["egress_policy"])
	}
	if !policy.Optional || policy.Required || policy.Computed {
		t.Fatal("egress_policy must be Optional and not Computed: removing it must plan a removal")
	}
	if len(policy.PlanModifiers) != 0 {
		t.Fatal("egress_policy changes in place: it must carry no RequiresReplace")
	}
	for _, name := range []string{"egress_policy_state", "egress_policy_generation"} {
		if attribute := resp.Schema.Attributes[name]; attribute == nil || !attribute.IsComputed() || attribute.IsOptional() {
			t.Fatalf("%s must be Computed only", name)
		}
	}
	if !policy.Attributes["default_action"].IsRequired() {
		t.Fatal("default_action must be Required")
	}

	reject := func(validators []validator.String, value string) bool {
		var out validator.StringResponse
		for _, v := range validators {
			v.ValidateString(context.Background(), validator.StringRequest{ConfigValue: types.StringValue(value)}, &out)
		}
		return out.Diagnostics.HasError()
	}
	if !reject(policy.Attributes["default_action"].(schema.StringAttribute).Validators, "drop") {
		t.Error("default_action must reject values other than allow and deny")
	}
	if !reject(policy.Attributes["inbound_replies"].(schema.StringAttribute).Validators, "all") {
		t.Error("inbound_replies must reject values other than service_ports and any")
	}
	rule := policy.Attributes["rules"].(schema.ListNestedAttribute).NestedObject.Attributes
	if !reject(rule["action"].(schema.StringAttribute).Validators, "permit") || !reject(rule["protocol"].(schema.StringAttribute).Validators, "sctp") {
		t.Error("rule action and protocol must be validated")
	}
	if !reject(rule["id"].(schema.StringAttribute).Validators, "has space") || !reject(rule["comment"].(schema.StringAttribute).Validators, strings.Repeat("x", 121)) {
		t.Error("rule id and comment must be validated")
	}

	ports := rule["ports"].(schema.ListAttribute).Validators
	check := func(values ...string) bool {
		elements := make([]attr.Value, 0, len(values))
		for _, value := range values {
			elements = append(elements, types.StringValue(value))
		}
		var out validator.ListResponse
		for _, v := range ports {
			v.ValidateList(context.Background(), validator.ListRequest{Path: path.Root("ports"), ConfigValue: types.ListValueMust(types.StringType, elements)}, &out)
		}
		return out.Diagnostics.HasError()
	}
	if check("443", "8000-8100", "1", "65535") {
		t.Error("valid ports and ranges must pass")
	}
	for _, bad := range []string{"0", "65536", "https", "9000-8000", "80-", "-80", "80,443", ""} {
		if !check(bad) {
			t.Errorf("port %q must be refused at plan time", bad)
		}
	}
}

// ---- request bodies -----------------------------------------------------------

func TestMicrovmEgressToAPISendsOnlyWhatIsConfigured(t *testing.T) {
	body, diags := microvmEgressToAPI(context.Background(), egressConfigured(t))
	mustNoError(t, diags)
	want := decodeObject(t, `{"default_action":"deny","rules":[{"action":"allow","target":"API.GitHub.com","ports":[443]},{"action":"allow","target":"8.8.8.8","protocol":"tcp","ports":[443,"8000-8100"],"comment":"resolver over https"}]}`)
	raw, _ := json.Marshal(body)
	if got := decodeObject(t, string(raw)); !reflect.DeepEqual(got, want) {
		t.Fatalf("body = %s\nwant %v", raw, want)
	}

	// No rules is an empty list, never a missing key (the policy is then the default action alone).
	empty, diags := microvmEgressToAPI(context.Background(), egressPolicyValue(t, "allow", "any"))
	mustNoError(t, diags)
	raw, _ = json.Marshal(empty)
	if string(raw) != `{"default_action":"allow","inbound_replies":"any","rules":[]}` {
		t.Fatalf("empty policy body = %s", raw)
	}

	for _, absent := range []types.Object{types.ObjectNull(microvmEgressPolicyAttrTypes), types.ObjectUnknown(microvmEgressPolicyAttrTypes), {}} {
		if body, _ := microvmEgressToAPI(context.Background(), absent); body != nil {
			t.Fatalf("a null, unknown or untyped attribute must send no policy; got %v", body)
		}
	}
}

func TestMicrovmCreateBodyCarriesTheEgressPolicy(t *testing.T) {
	plan := microvmModel{LocationID: types.StringValue("group-1"), ImageID: types.StringValue("image-1"), Name: types.StringValue("agent"), EgressPolicy: egressConfigured(t)}
	body, diags := microvmCreateBody(context.Background(), plan)
	mustNoError(t, diags)
	policy, ok := body["egress_policy"].(map[string]any)
	if !ok || policy["default_action"] != "deny" || len(policy["rules"].([]any)) != 2 {
		t.Fatalf("create body egress_policy = %#v", body["egress_policy"])
	}

	plan.EgressPolicy = types.ObjectNull(microvmEgressPolicyAttrTypes)
	body, diags = microvmCreateBody(context.Background(), plan)
	mustNoError(t, diags)
	if _, exists := body["egress_policy"]; exists {
		t.Fatal("a sandbox without a configured policy must not send egress_policy")
	}
}

// ---- reading: normalisation is not drift, a real change is ---------------------

func TestMicrovmEgressFromAPIKeepsTheConfiguredSpellingWhileItMeansTheSame(t *testing.T) {
	configured := egressConfigured(t)
	got, diags := microvmEgressFromAPI(context.Background(), decodeObject(t, egressStoredPolicy), configured)
	mustNoError(t, diags)
	if !got.Equal(configured) {
		t.Fatalf("a normalised stored policy must leave the configured value untouched\n got %v\nwant %v", got, configured)
	}

	// Equivalent spellings: explicit defaults, a trailing dot, an unmasked network, a one-port range.
	explicit := egressPolicyValue(t, "deny", "service_ports",
		egressRuleFixture{action: "allow", target: "api.github.com.", protocol: "any", ports: []string{"443-443"}},
		egressRuleFixture{action: "allow", target: "8.8.8.8", protocol: "tcp", ports: []string{" 443", "8000-8100"}, comment: "resolver over https"},
	)
	got, diags = microvmEgressFromAPI(context.Background(), decodeObject(t, egressStoredPolicy), explicit)
	mustNoError(t, diags)
	if !got.Equal(explicit) {
		t.Fatal("explicit defaults and equivalent spellings are not drift")
	}
	network := egressPolicyValue(t, "allow", "", egressRuleFixture{action: "deny", target: "198.51.100.77/24"}, egressRuleFixture{action: "deny", target: "2001:DB8::1"})
	stored := decodeObject(t, `{"default_action":"allow","inbound_replies":"any","rules":[{"id":"r_00000001","action":"deny","target":"198.51.100.0/24","protocol":"any"},{"id":"r_00000002","action":"deny","target":"2001:db8::1","protocol":"any"}]}`)
	got, diags = microvmEgressFromAPI(context.Background(), stored, network)
	mustNoError(t, diags)
	if !got.Equal(network) {
		t.Fatal("canonical address text is not drift")
	}
}

func TestMicrovmEgressFromAPIDetectsEveryKindOfDrift(t *testing.T) {
	configured := egressConfigured(t)
	base := decodeObject(t, egressStoredPolicy)
	mutate := func(change func(policy map[string]any, rules []any)) map[string]any {
		policy := decodeObject(t, egressStoredPolicy)
		change(policy, policy["rules"].([]any))
		return policy
	}
	cases := map[string]map[string]any{
		"default action changed": mutate(func(p map[string]any, _ []any) { p["default_action"] = "allow"; p["inbound_replies"] = "any" }),
		"inbound replies changed": mutate(func(p map[string]any, _ []any) {
			p["default_action"] = "allow"
			p["inbound_replies"] = "service_ports"
		}),
		"rule added": mutate(func(p map[string]any, rules []any) {
			p["rules"] = append(rules, map[string]any{"id": "r_ffffffff", "action": "allow", "target": "1.1.1.1", "protocol": "any"})
		}),
		"rule removed":     mutate(func(p map[string]any, rules []any) { p["rules"] = rules[:1] }),
		"rules reordered":  mutate(func(p map[string]any, rules []any) { p["rules"] = []any{rules[1], rules[0]} }),
		"target changed":   mutate(func(_ map[string]any, rules []any) { rules[0].(map[string]any)["target"] = "api.gitlab.com" }),
		"action changed":   mutate(func(_ map[string]any, rules []any) { rules[0].(map[string]any)["action"] = "deny" }),
		"protocol changed": mutate(func(_ map[string]any, rules []any) { rules[0].(map[string]any)["protocol"] = "tcp" }),
		"port changed":     mutate(func(_ map[string]any, rules []any) { rules[0].(map[string]any)["ports"] = []any{float64(8443)} }),
		"port added": mutate(func(_ map[string]any, rules []any) {
			rules[0].(map[string]any)["ports"] = []any{float64(443), float64(80)}
		}),
		"ports removed":   mutate(func(_ map[string]any, rules []any) { delete(rules[1].(map[string]any), "ports") }),
		"comment changed": mutate(func(_ map[string]any, rules []any) { rules[1].(map[string]any)["comment"] = "edited" }),
	}
	for name, stored := range cases {
		got, diags := microvmEgressFromAPI(context.Background(), stored, configured)
		mustNoError(t, diags)
		if got.Equal(configured) {
			t.Errorf("%s: the stored policy differs from the configuration and must show as drift", name)
		}
		// The drifted value is the stored policy: reading it again against itself is stable.
		again, diags := microvmEgressFromAPI(context.Background(), stored, got)
		mustNoError(t, diags)
		if !again.Equal(got) {
			t.Errorf("%s: a drifted read must be stable on the next read", name)
		}
	}
	if got, _ := microvmEgressFromAPI(context.Background(), base, configured); !got.Equal(configured) {
		t.Fatal("control: the unmodified stored policy is not drift")
	}

	// A policy removed outside OpenTofu reads as null, and one that appeared reads as set.
	got, diags := microvmEgressFromAPI(context.Background(), nil, configured)
	mustNoError(t, diags)
	if !got.IsNull() {
		t.Fatal("a removed policy must read as null")
	}
	got, diags = microvmEgressFromAPI(context.Background(), base, types.ObjectNull(microvmEgressPolicyAttrTypes))
	mustNoError(t, diags)
	if got.IsNull() {
		t.Fatal("a policy set outside OpenTofu must read as set")
	}
}

func TestMicrovmEgressFromAPIAConfiguredRuleIDIsCompared(t *testing.T) {
	withID := egressPolicyValue(t, "deny", "", egressRuleFixture{action: "allow", target: "8.8.8.8", id: "dns"})
	same := decodeObject(t, `{"default_action":"deny","inbound_replies":"service_ports","rules":[{"id":"dns","action":"allow","target":"8.8.8.8","protocol":"any"}]}`)
	other := decodeObject(t, `{"default_action":"deny","inbound_replies":"service_ports","rules":[{"id":"r_4f2a9c1e","action":"allow","target":"8.8.8.8","protocol":"any"}]}`)
	if got, _ := microvmEgressFromAPI(context.Background(), same, withID); !got.Equal(withID) {
		t.Fatal("the configured id is stored: no drift")
	}
	if got, _ := microvmEgressFromAPI(context.Background(), other, withID); got.Equal(withID) {
		t.Fatal("a configured id that the platform no longer holds is drift")
	}
}

func TestMicrovmEgressImportReadsTheShortestForm(t *testing.T) {
	got, diags := microvmEgressFromAPI(context.Background(), decodeObject(t, egressStoredPolicy), types.ObjectNull(microvmEgressPolicyAttrTypes))
	mustNoError(t, diags)
	// Defaults and platform-assigned ids are left out, so the matching configuration plans no change.
	want := egressPolicyValue(t, "deny", "",
		egressRuleFixture{action: "allow", target: "api.github.com", ports: []string{"443"}},
		egressRuleFixture{action: "allow", target: "8.8.8.8", protocol: "tcp", ports: []string{"443", "8000-8100"}, comment: "resolver over https"},
	)
	if !got.Equal(want) {
		t.Fatalf("imported policy\n got %v\nwant %v", got, want)
	}
	// A rule id the customer chose survives an import; no rules reads as a null list.
	custom := decodeObject(t, `{"default_action":"allow","inbound_replies":"service_ports","rules":[]}`)
	got, _ = microvmEgressFromAPI(context.Background(), custom, types.ObjectNull(microvmEgressPolicyAttrTypes))
	if !got.Equal(egressPolicyValue(t, "allow", "service_ports")) {
		t.Fatalf("a non-default inbound_replies must be kept and an empty rule list read as null; got %v", got)
	}
	named := decodeObject(t, `{"default_action":"deny","inbound_replies":"service_ports","rules":[{"id":"dns","action":"allow","target":"8.8.8.8","protocol":"udp"}]}`)
	got, _ = microvmEgressFromAPI(context.Background(), named, types.ObjectNull(microvmEgressPolicyAttrTypes))
	if !got.Equal(egressPolicyValue(t, "deny", "", egressRuleFixture{action: "allow", target: "8.8.8.8", protocol: "udp", id: "dns"})) {
		t.Fatalf("a customer rule id must survive an import; got %v", got)
	}
}

// ---- the resource: read, update, create, import ---------------------------------

func readMicrovm(t *testing.T, res *microvmResource, prior microvmModel) (microvmModel, diag.Diagnostics) {
	t.Helper()
	s := credentialSchema(t, res)
	out := resource.ReadResponse{State: credentialState(t, s, prior)}
	res.Read(context.Background(), resource.ReadRequest{State: credentialState(t, s, prior)}, &out)
	var state microvmModel
	if !out.Diagnostics.HasError() {
		mustNoError(t, out.State.Get(context.Background(), &state))
	}
	return state, out.Diagnostics
}

func TestMicrovmReadFetchesThePolicyOnlyWhenItsGenerationMoved(t *testing.T) {
	api := &egressServer{summary: `{"policy_set":true,"default_action":"deny","rule_count":2,"generation":3,"state":"applied"}`,
		egressReads: []string{`{"policy":` + egressStoredPolicy + `,"generation":3,"applied_generation":3,"state":"applied"}`}}
	res := &microvmResource{client: api.start(t)}

	prior := microvmTestModel(t, egressConfigured(t))
	prior.EgressPolicyGeneration = types.Int64Value(3)
	prior.EgressPolicyState = types.StringValue("pending")
	state, diags := readMicrovm(t, res, prior)
	mustNoError(t, diags)
	if requests := api.policyRequests(); len(requests) != 0 {
		t.Fatalf("an unchanged generation must not fetch the policy; got %v", requests)
	}
	if !state.EgressPolicy.Equal(prior.EgressPolicy) || state.EgressPolicyState.ValueString() != "applied" || state.EgressPolicyGeneration.ValueInt64() != 3 {
		t.Fatalf("state = %v / %v / %v", state.EgressPolicy, state.EgressPolicyState, state.EgressPolicyGeneration)
	}

	// Someone replaced the policy outside OpenTofu: the generation moved, the policy is fetched and shows as drift.
	api.summary = `{"policy_set":true,"default_action":"allow","rule_count":0,"generation":4,"state":"applied"}`
	api.egressReads = []string{`{"policy":{"default_action":"allow","inbound_replies":"any","rules":[]},"generation":4,"applied_generation":4,"state":"applied"}`}
	state, diags = readMicrovm(t, res, prior)
	mustNoError(t, diags)
	if requests := api.policyRequests(); !reflect.DeepEqual(requests, []string{"GET /api/microvm/vm/vm-1/egress"}) {
		t.Fatalf("a moved generation must fetch the policy once; got %v", requests)
	}
	if state.EgressPolicy.Equal(prior.EgressPolicy) || !state.EgressPolicy.Equal(egressPolicyValue(t, "allow", "")) {
		t.Fatalf("drift must reach state; got %v", state.EgressPolicy)
	}
	if state.EgressPolicyGeneration.ValueInt64() != 4 {
		t.Fatalf("generation = %v", state.EgressPolicyGeneration)
	}
}

func TestMicrovmReadSeesAPolicyRemovedOrAddedOutside(t *testing.T) {
	// Removed outside: the summary says not set, the read confirms, state goes null (the plan then re-adds it).
	api := &egressServer{summary: `{"policy_set":false,"default_action":null,"rule_count":0,"generation":4,"state":"none"}`,
		egressReads: []string{`{"policy":null,"generation":4,"applied_generation":4,"state":"none"}`}}
	res := &microvmResource{client: api.start(t)}
	prior := microvmTestModel(t, egressConfigured(t))
	prior.EgressPolicyGeneration = types.Int64Value(3)
	state, diags := readMicrovm(t, res, prior)
	mustNoError(t, diags)
	if !state.EgressPolicy.IsNull() || state.EgressPolicyState.ValueString() != "none" {
		t.Fatalf("a policy removed outside must read as null; got %v", state.EgressPolicy)
	}

	// Added outside (also the import path: no prior generation): state takes the stored policy.
	api.summary = `{"policy_set":true,"default_action":"deny","rule_count":2,"generation":1,"state":"applied"}`
	api.egressReads = []string{`{"policy":` + egressStoredPolicy + `,"generation":1,"applied_generation":1,"state":"applied"}`}
	imported := microvmTestModel(t, types.ObjectNull(microvmEgressPolicyAttrTypes))
	state, diags = readMicrovm(t, res, imported)
	mustNoError(t, diags)
	if state.EgressPolicy.IsNull() || state.EgressPolicyGeneration.ValueInt64() != 1 {
		t.Fatalf("an import must read the stored policy; got %v", state.EgressPolicy)
	}

	// Same generation but the set flag disagrees with state: never trusted, the policy is fetched.
	api.requests = nil
	stale := microvmTestModel(t, types.ObjectNull(microvmEgressPolicyAttrTypes))
	stale.EgressPolicyGeneration = types.Int64Value(1)
	state, diags = readMicrovm(t, res, stale)
	mustNoError(t, diags)
	if len(api.policyRequests()) != 1 || state.EgressPolicy.IsNull() {
		t.Fatal("a set flag that disagrees with state must force a fetch")
	}
}

func TestMicrovmReadOnAPlatformWithoutPoliciesReadsNullAndAsksNothing(t *testing.T) {
	api := &egressServer{summary: ""}
	res := &microvmResource{client: api.start(t)}
	state, diags := readMicrovm(t, res, microvmTestModel(t, types.ObjectNull(microvmEgressPolicyAttrTypes)))
	mustNoError(t, diags)
	if !state.EgressPolicy.IsNull() || !state.EgressPolicyState.IsNull() || !state.EgressPolicyGeneration.IsNull() {
		t.Fatal("no summary means no policy support: every policy attribute is null")
	}
	if len(api.policyRequests()) != 0 {
		t.Fatalf("the policy endpoint must not be called on a platform without it; got %v", api.policyRequests())
	}
}

func updateMicrovm(t *testing.T, res *microvmResource, prior, plan microvmModel) (microvmModel, diag.Diagnostics) {
	t.Helper()
	s := credentialSchema(t, res)
	out := resource.UpdateResponse{State: credentialState(t, s, prior)}
	res.Update(context.Background(), resource.UpdateRequest{State: credentialState(t, s, prior), Plan: credentialPlan(t, s, plan)}, &out)
	var state microvmModel
	mustNoError(t, out.State.Get(context.Background(), &state))
	return state, out.Diagnostics
}

func TestMicrovmUpdateReplacesThePolicyInPlaceAndWaitsForTheNode(t *testing.T) {
	fastEgressWait(t, 5*time.Second)
	api := &egressServer{summary: `{"policy_set":true,"default_action":"deny","rule_count":2,"generation":2,"state":"applied"}`,
		egressReads: []string{
			`{"policy":` + egressStoredPolicy + `,"generation":2,"applied_generation":1,"state":"pending"}`,
			`{"policy":` + egressStoredPolicy + `,"generation":2,"applied_generation":2,"state":"applied"}`,
		}}
	res := &microvmResource{client: api.start(t)}
	prior := microvmTestModel(t, types.ObjectNull(microvmEgressPolicyAttrTypes))
	plan := microvmTestModel(t, egressConfigured(t))
	plan.EgressPolicyState, plan.EgressPolicyGeneration = types.StringUnknown(), types.Int64Unknown()

	state, diags := updateMicrovm(t, res, prior, plan)
	mustNoError(t, diags)
	requests := api.policyRequests()
	if len(requests) < 3 || requests[0] != "PUT /api/microvm/vm/vm-1/egress" {
		t.Fatalf("requests = %v; want a PUT, then reads until the node confirmed", requests)
	}
	for _, request := range requests[1:] {
		if request != "GET /api/microvm/vm/vm-1/egress" {
			t.Fatalf("unexpected policy request %s", request)
		}
	}
	if !state.EgressPolicy.Equal(plan.EgressPolicy) {
		t.Fatal("state after apply must be exactly the planned policy (the stored one only differs by normalisation)")
	}
	if state.EgressPolicyState.ValueString() != "applied" || state.EgressPolicyGeneration.ValueInt64() != 2 {
		t.Fatalf("computed = %v / %v", state.EgressPolicyState, state.EgressPolicyGeneration)
	}
	var sent map[string]any
	for i, request := range api.requests {
		if request == "PUT /api/microvm/vm/vm-1/egress" {
			sent = decodeObject(t, api.bodies[i])
		}
	}
	if sent["default_action"] != "deny" || len(sent["rules"].([]any)) != 2 {
		t.Fatalf("PUT body = %v", sent)
	}
}

func TestMicrovmUpdateRemovesThePolicyWhenTheAttributeIsRemoved(t *testing.T) {
	fastEgressWait(t, 5*time.Second)
	api := &egressServer{summary: `{"policy_set":false,"default_action":null,"rule_count":0,"generation":3,"state":"none"}`,
		egressReads: []string{`{"policy":null,"generation":3,"applied_generation":3,"state":"none"}`}}
	res := &microvmResource{client: api.start(t)}
	prior := microvmTestModel(t, egressConfigured(t))
	prior.EgressPolicyGeneration = types.Int64Value(2)
	plan := microvmTestModel(t, types.ObjectNull(microvmEgressPolicyAttrTypes))

	state, diags := updateMicrovm(t, res, prior, plan)
	mustNoError(t, diags)
	if requests := api.policyRequests(); len(requests) == 0 || requests[0] != "DELETE /api/microvm/vm/vm-1/egress" {
		t.Fatalf("requests = %v; want a DELETE first", requests)
	}
	if !state.EgressPolicy.IsNull() || state.EgressPolicyState.ValueString() != "none" {
		t.Fatalf("state = %v / %v", state.EgressPolicy, state.EgressPolicyState)
	}
}

func TestMicrovmUpdateWithoutAPolicyChangeSendsNoPolicyRequest(t *testing.T) {
	api := &egressServer{summary: `{"policy_set":true,"default_action":"deny","rule_count":2,"generation":2,"state":"applied"}`,
		egressReads: []string{`{"policy":` + egressStoredPolicy + `,"generation":2,"applied_generation":2,"state":"applied"}`}}
	res := &microvmResource{client: api.start(t)}
	prior := microvmTestModel(t, egressConfigured(t))
	prior.EgressPolicyGeneration = types.Int64Value(2)
	plan := prior

	_, diags := updateMicrovm(t, res, prior, plan)
	mustNoError(t, diags)
	if requests := api.policyRequests(); len(requests) != 0 {
		t.Fatalf("an unchanged policy must not be written or re-read; got %v", requests)
	}
}

func TestMicrovmUpdateFailsWhenTheNodeRefusesThePolicyAndStillRecordsIt(t *testing.T) {
	fastEgressWait(t, 5*time.Second)
	for name, tc := range map[string]struct{ read, summary string }{
		"failed":      {`{"policy":` + egressStoredPolicy + `,"generation":2,"applied_generation":0,"state":"failed","last_error_code":"egress_target_not_permitted"}`, "refused by the node"},
		"unavailable": {`{"policy":` + egressStoredPolicy + `,"generation":2,"applied_generation":0,"state":"unavailable","last_error_code":"egress_policy_unavailable"}`, "not in force"},
	} {
		api := &egressServer{summary: `{"policy_set":true,"default_action":"deny","rule_count":2,"generation":2,"state":"` + name + `"}`, egressReads: []string{tc.read}}
		res := &microvmResource{client: api.start(t)}
		plan := microvmTestModel(t, egressConfigured(t))

		state, diags := updateMicrovm(t, res, microvmTestModel(t, types.ObjectNull(microvmEgressPolicyAttrTypes)), plan)
		if !diags.HasError() {
			t.Fatalf("%s: a policy the node does not enforce must fail the apply", name)
		}
		text := diags.Errors()[0].Summary() + " " + diags.Errors()[0].Detail()
		if !strings.Contains(text, tc.summary) || !strings.Contains(text, "egress_") {
			t.Errorf("%s: error must say what happened and carry the node's code; got %q", name, text)
		}
		if state.EgressPolicy.IsNull() || state.EgressPolicyState.ValueString() != name {
			t.Errorf("%s: the stored policy and its state must be recorded; got %v / %v", name, state.EgressPolicy, state.EgressPolicyState)
		}
	}
}

func TestMicrovmEgressWaitWarnsWhenTheNodeHasNotAnsweredAndSkipsAStoppedSandbox(t *testing.T) {
	fastEgressWait(t, 30*time.Millisecond)
	api := &egressServer{egressReads: []string{`{"policy":` + egressStoredPolicy + `,"generation":2,"applied_generation":1,"state":"pending"}`}}
	res := &microvmResource{client: api.start(t)}

	diags := res.waitEgress(context.Background(), "vm-1", "running")
	if diags.HasError() || diags.WarningsCount() != 1 {
		t.Fatalf("a node that has not answered yet is a warning, not an error: %v", diags)
	}
	if !strings.Contains(diags.Warnings()[0].Detail(), "pending") {
		t.Errorf("the warning must name the state; got %q", diags.Warnings()[0].Detail())
	}

	// applied, but for an older generation: not confirmed.
	api.egressReads = []string{`{"policy":` + egressStoredPolicy + `,"generation":3,"applied_generation":2,"state":"applied"}`}
	if diags := res.waitEgress(context.Background(), "vm-1", "paused"); diags.WarningsCount() != 1 {
		t.Fatalf("an applied state of an older generation is not a confirmation: %v", diags)
	}

	// inactive: the sandbox ended while the apply waited. Nothing will confirm the
	// policy, so the wait stops at once with a warning, not after its timeout.
	fastEgressWait(t, 30*time.Second)
	api.egressReads = []string{`{"policy":` + egressStoredPolicy + `,"generation":3,"applied_generation":2,"state":"inactive"}`}
	started := time.Now()
	diags = res.waitEgress(context.Background(), "vm-1", "running")
	if diags.HasError() || diags.WarningsCount() != 1 || !strings.Contains(diags.Warnings()[0].Detail(), "inactive") {
		t.Fatalf("an ended sandbox is a warning that names the state: %v", diags)
	}
	if time.Since(started) > 5*time.Second {
		t.Fatalf("the wait must stop on inactive, it ran %v", time.Since(started))
	}
	fastEgressWait(t, 30*time.Millisecond)

	before := len(api.policyRequests())
	for _, state := range []string{"stopped", "creating", "killed", ""} {
		if diags := res.waitEgress(context.Background(), "vm-1", state); len(diags) != 0 {
			t.Fatalf("a %q sandbox is not waited for: %v", state, diags)
		}
	}
	if len(api.policyRequests()) != before {
		t.Fatal("no node holds a stopped sandbox: the wait must not poll for it")
	}
}

func createMicrovm(t *testing.T, res *microvmResource, plan microvmModel) (microvmModel, diag.Diagnostics) {
	t.Helper()
	s := credentialSchema(t, res)
	out := resource.CreateResponse{State: credentialNullState(s)}
	res.Create(context.Background(), resource.CreateRequest{Plan: credentialPlan(t, s, plan)}, &out)
	var state microvmModel
	if !out.State.Raw.IsNull() {
		_ = out.State.Get(context.Background(), &state)
	}
	return state, out.Diagnostics
}

func egressCreatePlan(t *testing.T) microvmModel {
	plan := microvmTestModel(t, egressConfigured(t))
	plan.ID, plan.State, plan.E2bID = types.StringUnknown(), types.StringUnknown(), types.StringUnknown()
	plan.HypervisorGroupID = types.StringUnknown()
	plan.EgressPolicyState, plan.EgressPolicyGeneration = types.StringUnknown(), types.Int64Unknown()
	return plan
}

func TestMicrovmCreateSendsThePolicyWithTheSandboxAndRecordsIt(t *testing.T) {
	fastEgressWait(t, 5*time.Second)
	summary := `{"policy_set":true,"default_action":"deny","rule_count":2,"generation":1,"state":"applied"}`
	api := &egressServer{summary: summary,
		created:     `{"id":"vm-1","name":"agent","state":"running","image_id":"image-1","location_id":"group-1","e2b_sandbox_id":"e2b-1","interfaces":[],"egress":` + summary + `}`,
		egressReads: []string{`{"policy":` + egressStoredPolicy + `,"generation":1,"applied_generation":1,"state":"applied"}`}}
	res := &microvmResource{client: api.start(t)}

	state, diags := createMicrovm(t, res, egressCreatePlan(t))
	mustNoError(t, diags)
	if api.requests[0] != "POST /api/microvm/vms" {
		t.Fatalf("first request = %s", api.requests[0])
	}
	body := decodeObject(t, api.bodies[0])
	policy, ok := body["egress_policy"].(map[string]any)
	if !ok || policy["default_action"] != "deny" {
		t.Fatalf("the create request must carry the policy; body = %v", body)
	}
	for _, request := range api.policyRequests() {
		if request != "GET /api/microvm/vm/vm-1/egress" {
			t.Fatalf("a policy sent at create must not be written a second time; got %s", request)
		}
	}
	if !state.EgressPolicy.Equal(egressConfigured(t)) || state.EgressPolicyState.ValueString() != "applied" || state.EgressPolicyGeneration.ValueInt64() != 1 {
		t.Fatalf("state = %v / %v / %v", state.EgressPolicy, state.EgressPolicyState, state.EgressPolicyGeneration)
	}
}

func TestMicrovmCreateFailsClosedWhenThePlatformDroppedThePolicy(t *testing.T) {
	for name, created := range map[string]string{
		"platform without policies": `{"id":"vm-1","name":"agent","state":"running","image_id":"image-1","location_id":"group-1","e2b_sandbox_id":"e2b-1","interfaces":[]}`,
		"policy not stored":         `{"id":"vm-1","name":"agent","state":"running","image_id":"image-1","location_id":"group-1","e2b_sandbox_id":"e2b-1","interfaces":[],"egress":{"policy_set":false,"default_action":null,"rule_count":0,"generation":0,"state":"none"}}`,
	} {
		api := &egressServer{created: created}
		res := &microvmResource{client: api.start(t)}

		state, diags := createMicrovm(t, res, egressCreatePlan(t))
		if !diags.HasError() || !strings.Contains(diags.Errors()[0].Detail(), "NOT restricted") {
			t.Fatalf("%s: a sandbox created without its configured policy must fail the apply loudly; got %v", name, diags)
		}
		if state.ID.ValueString() != "vm-1" {
			t.Errorf("%s: the created sandbox must stay in state so it can be destroyed; id = %v", name, state.ID)
		}
	}
}
