package resources_test

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/hypervisor-io/terraform-provider-iaas/internal/acctest"
)

// ---------------------------------------------------------------------------
// TestAccSecurityGroup_basic - LIVE acceptance test (manual staging gate).
//
// Auto-skips unless TF_ACC is set (resource.Test enforces this), so it never
// runs or blocks CI. Requires a reachable panel + IP-locked token via
// IAAS_API_ENDPOINT / IAAS_API_TOKEN (checked by acctest.PreCheck).
// ---------------------------------------------------------------------------
func TestAccSecurityGroup_basic(t *testing.T) {
	const config = `
resource "iaas_security_group" "test" {
  name        = "tf-acc-web-sg"
  description = "Acceptance test security group"

  rules = [
    {
      direction      = "ingress"
      protocol       = "tcp"
      port_range_min = 80
      port_range_max = 80
      ip_version     = "ipv4"
      cidr           = "0.0.0.0/0"
      description    = "http"
    },
    {
      direction  = "ingress"
      protocol   = "icmp"
      ip_version = "ipv4"
      cidr       = "0.0.0.0/0"
    },
  ]
}
`
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("iaas_security_group.test", "id"),
					resource.TestCheckResourceAttr("iaas_security_group.test", "name", "tf-acc-web-sg"),
					resource.TestCheckResourceAttr("iaas_security_group.test", "rules.#", "2"),
				),
			},
			{
				ResourceName:      "iaas_security_group.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// ---------------------------------------------------------------------------
// sgMockServer is a stateful mock of the security-group API for the lifecycle
// test.
//
// It models the parent security group plus a server-side rules store keyed by
// rule id and an attached-instance set, and records how many add-rule /
// remove-rule / attach / detach calls happened so the test can assert the
// nested-set + attachment diff logic ran.
// ---------------------------------------------------------------------------
type sgMockServer struct {
	mu sync.Mutex

	name string
	desc *string

	// rules: server rule id → rule object (the raw fields sent on add).
	rules  map[string]map[string]any
	nextID int

	// attached: server instance id → present.
	attached map[string]struct{}

	addRuleCalls    int // POST /security-group/{id}/rules
	removeRuleCalls int // DELETE /security-group/{id}/rule/{ruleId}
	attachCalls     int // POST /security-group/{id}/attach-instances
	detachCalls     int // POST /security-group/{id}/detach-instances
}

func (s *sgMockServer) addRule(fields map[string]any) string {
	s.nextID++
	id := "rule-" + sgItoa(s.nextID)
	// Copy the fields and stamp the id.
	rule := map[string]any{"id": id}
	for k, v := range fields {
		rule[k] = v
	}
	s.rules[id] = rule
	return id
}

// sgItoa avoids importing strconv just for this tiny helper.
func sgItoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// sgEnvelope builds the full SHOW envelope: the security_group object with
// embedded rules, plus the top-level attached_instances array.
func (s *sgMockServer) sgEnvelope(id string) map[string]any {
	rules := make([]any, 0, len(s.rules))
	for _, rule := range s.rules {
		// Return a fresh copy so the test handler does not share map state.
		out := map[string]any{}
		for k, v := range rule {
			out[k] = v
		}
		rules = append(rules, out)
	}

	sg := map[string]any{
		"id":          id,
		"name":        s.name,
		"rules":       rules,
		"rules_count": len(rules),
	}
	if s.desc != nil {
		sg["description"] = *s.desc
	} else {
		sg["description"] = nil
	}

	attached := make([]any, 0, len(s.attached))
	for instID := range s.attached {
		attached = append(attached, map[string]any{"id": instID, "name": instID})
	}

	return map[string]any{
		"success":             true,
		"security_group":      sg,
		"attached_instances":  attached,
		"all_security_groups": []any{},
		"all_ip_sets":         []any{},
		"user_instances":      []any{},
	}
}

// ---------------------------------------------------------------------------
// TestUnitSecurityGroup_lifecycle - MOCK-backed lifecycle proof (the key test).
//
// Steps:
//  1. Create with TWO rules + ONE attached instance → assert id/name, rules.# = 2,
//     instance_ids.# = 1; assert the create issued exactly TWO POST .../rules
//     calls (with the rule bodies) and ONE attach call carrying the instance id.
//  2. Import by id → rules + attached instances rehydrated from SHOW.
//  3. Update: ADD one rule, REMOVE one rule, ATTACH one instance, DETACH the
//     original → assert the resulting state and that exactly one MORE add-rule,
//     one remove-rule, one attach, and one detach fired (the multi-diff logic).
//
// Delete is implicit teardown after the final step.
// ---------------------------------------------------------------------------
func TestUnitSecurityGroup_lifecycle(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)

	const sgID = "44444444-4444-4444-4444-444444444444"

	store := &sgMockServer{
		rules:    map[string]map[string]any{},
		attached: map[string]struct{}{},
	}

	// CREATE - POST /security-groups stores name/description, returns the group.
	srv.Handle("POST", "/security-groups", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		store.mu.Lock()
		defer store.mu.Unlock()
		if n, ok := body["name"].(string); ok {
			store.name = n
		}
		if d, ok := body["description"].(string); ok {
			store.desc = &d
		} else {
			store.desc = nil
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success":        true,
			"message":        "Security group created successfully",
			"security_group": map[string]any{"id": sgID, "name": store.name},
		})
	})

	// ADD RULE - POST /security-group/{id}/rules stores a new rule, returns it.
	srv.Handle("POST", "/security-group/"+sgID+"/rules", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		store.mu.Lock()
		defer store.mu.Unlock()
		store.addRuleCalls++
		rid := store.addRule(body)
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Rule added successfully",
			"rule":    store.rules[rid],
		})
	})

	// SHOW - GET /security-group/{id} returns the envelope (rules + attached).
	srv.Handle("GET", "/security-group/"+sgID, func(w http.ResponseWriter, r *http.Request) {
		store.mu.Lock()
		defer store.mu.Unlock()
		writeJSON(w, http.StatusOK, store.sgEnvelope(sgID))
	})

	// UPDATE - PATCH /security-group/{id} applies name/description; NO body.
	srv.Handle("PATCH", "/security-group/"+sgID, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		store.mu.Lock()
		defer store.mu.Unlock()
		if n, ok := body["name"].(string); ok {
			store.name = n
		}
		if desc, exists := body["description"]; exists {
			if desc == nil {
				store.desc = nil
			} else if s, ok := desc.(string); ok {
				store.desc = &s
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Security group updated successfully",
		})
	})

	// REMOVE RULE - DELETE /security-group/{id}/rule/{ruleId}. The mock can only
	// exact-match, so register a handler per rule id the mock can assign.
	for _, rid := range []string{"rule-1", "rule-2", "rule-3", "rule-4", "rule-5"} {
		srv.Handle("DELETE", "/security-group/"+sgID+"/rule/"+rid, makeRemoveRuleHandler(store, rid))
	}

	// ATTACH - POST /security-group/{id}/attach-instances adds the ids.
	srv.Handle("POST", "/security-group/"+sgID+"/attach-instances", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		store.mu.Lock()
		defer store.mu.Unlock()
		store.attachCalls++
		if ids, ok := body["instance_ids"].([]any); ok {
			for _, idv := range ids {
				if s, ok := idv.(string); ok {
					store.attached[s] = struct{}{}
				}
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Security group attached to instances",
		})
	})

	// DETACH - POST /security-group/{id}/detach-instances removes the ids.
	srv.Handle("POST", "/security-group/"+sgID+"/detach-instances", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		store.mu.Lock()
		defer store.mu.Unlock()
		store.detachCalls++
		if ids, ok := body["instance_ids"].([]any); ok {
			for _, idv := range ids {
				if s, ok := idv.(string); ok {
					delete(store.attached, s)
				}
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Security group detached from instances",
		})
	})

	// DELETE GROUP - DELETE /security-group/{id}.
	srv.Handle("DELETE", "/security-group/"+sgID, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Security group deleted successfully",
		})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	// Create config: two rules (a tcp/80 with description, an icmp without ports)
	// + one attached instance.
	createCfg := providerCfg + `
resource "iaas_security_group" "test" {
  name        = "web-sg"
  description = "web servers"

  instance_ids = ["inst-a"]

  rules = [
    {
      direction      = "ingress"
      protocol       = "tcp"
      port_range_min = 80
      port_range_max = 80
      ip_version     = "ipv4"
      cidr           = "0.0.0.0/0"
      description    = "http"
    },
    {
      direction  = "ingress"
      protocol   = "icmp"
      ip_version = "ipv4"
      cidr       = "0.0.0.0/0"
    },
  ]
}
`

	// Update config: rename, KEEP the tcp/80 rule, REMOVE the icmp rule, ADD a
	// tcp/443 rule; DETACH inst-a, ATTACH inst-b. Net per-set: one add + one
	// remove rule, one attach + one detach instance.
	updateCfg := providerCfg + `
resource "iaas_security_group" "test" {
  name        = "web-sg-v2"
  description = "web servers"

  instance_ids = ["inst-b"]

  rules = [
    {
      direction      = "ingress"
      protocol       = "tcp"
      port_range_min = 80
      port_range_max = 80
      ip_version     = "ipv4"
      cidr           = "0.0.0.0/0"
      description    = "http"
    },
    {
      direction      = "ingress"
      protocol       = "tcp"
      port_range_min = 443
      port_range_max = 443
      ip_version     = "ipv4"
      cidr           = "0.0.0.0/0"
      description    = "https"
    },
  ]
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			// 1. Create + read-back with two rules and one attached instance.
			{
				Config: createCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_security_group.test", "id", sgID),
					resource.TestCheckResourceAttr("iaas_security_group.test", "name", "web-sg"),
					resource.TestCheckResourceAttr("iaas_security_group.test", "rules.#", "2"),
					resource.TestCheckResourceAttr("iaas_security_group.test", "instance_ids.#", "1"),
					resource.TestCheckTypeSetElemAttr("iaas_security_group.test", "instance_ids.*", "inst-a"),
					resource.TestCheckTypeSetElemNestedAttrs("iaas_security_group.test", "rules.*", map[string]string{
						"protocol":       "tcp",
						"port_range_min": "80",
						"description":    "http",
					}),
					resource.TestCheckTypeSetElemNestedAttrs("iaas_security_group.test", "rules.*", map[string]string{
						"protocol": "icmp",
					}),
					// A rule with a non-empty server id proves Read hydrated ids.
					resource.TestCheckTypeSetElemNestedAttrs("iaas_security_group.test", "rules.*", map[string]string{
						"protocol": "tcp",
						"id":       "rule-1",
					}),
				),
			},
			// 2. Import by id; rules + attached instances rehydrated from SHOW.
			{
				ResourceName:      "iaas_security_group.test",
				ImportState:       true,
				ImportStateId:     sgID,
				ImportStateVerify: true,
			},
			// 3. Update: add a rule, remove a rule, attach + detach an instance.
			{
				Config: updateCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_security_group.test", "name", "web-sg-v2"),
					resource.TestCheckResourceAttr("iaas_security_group.test", "rules.#", "2"),
					resource.TestCheckResourceAttr("iaas_security_group.test", "instance_ids.#", "1"),
					resource.TestCheckTypeSetElemAttr("iaas_security_group.test", "instance_ids.*", "inst-b"),
					resource.TestCheckTypeSetElemNestedAttrs("iaas_security_group.test", "rules.*", map[string]string{
						"protocol":       "tcp",
						"port_range_min": "443",
						"description":    "https",
					}),
				),
			},
		},
	})

	// Assert the create issued exactly TWO add-rule calls and ONE attach call,
	// and the update issued exactly one MORE add-rule, one remove-rule, one
	// attach, and one detach (the multi-set diff).
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.addRuleCalls != 3 {
		t.Errorf("addRuleCalls = %d; want 3 (2 on create + 1 on update)", store.addRuleCalls)
	}
	if store.removeRuleCalls != 1 {
		t.Errorf("removeRuleCalls = %d; want 1 (1 removed on update)", store.removeRuleCalls)
	}
	if store.attachCalls != 2 {
		t.Errorf("attachCalls = %d; want 2 (1 on create + 1 on update)", store.attachCalls)
	}
	if store.detachCalls != 1 {
		t.Errorf("detachCalls = %d; want 1 (1 on update)", store.detachCalls)
	}

	// Assert the create POST .../rules calls carried the rule fields (direction,
	// protocol), proving the per-rule add body is built correctly.
	adds := srv.Requests("POST", "/security-group/"+sgID+"/rules")
	if len(adds) < 2 {
		t.Fatalf("expected at least 2 POST .../rules calls; got %d", len(adds))
	}
	sawHTTP := false
	for _, req := range adds {
		var b map[string]any
		if err := json.Unmarshal(req.Body, &b); err != nil {
			t.Fatalf("decoding add-rule body: %v", err)
		}
		if b["direction"] == nil || b["protocol"] == nil || b["ip_version"] == nil {
			t.Errorf("add-rule body missing required field: %v", b)
		}
		if b["protocol"] == "tcp" && b["description"] == "http" {
			sawHTTP = true
		}
	}
	if !sawHTTP {
		t.Error("expected an add-rule call to carry protocol=tcp description=http")
	}

	// Assert the create attach call carried instance id inst-a.
	attaches := srv.Requests("POST", "/security-group/"+sgID+"/attach-instances")
	if len(attaches) < 1 {
		t.Fatalf("expected at least 1 attach call; got %d", len(attaches))
	}
	sawInstA := false
	for _, req := range attaches {
		var b map[string]any
		if err := json.Unmarshal(req.Body, &b); err != nil {
			t.Fatalf("decoding attach body: %v", err)
		}
		ids, _ := b["instance_ids"].([]any)
		for _, idv := range ids {
			if idv == "inst-a" {
				sawInstA = true
			}
		}
	}
	if !sawInstA {
		t.Error("expected an attach call to carry instance_ids including inst-a")
	}

	// Assert the update detach call carried inst-a.
	detaches := srv.Requests("POST", "/security-group/"+sgID+"/detach-instances")
	if len(detaches) != 1 {
		t.Errorf("detach requests = %d; want 1", len(detaches))
	}
	for _, req := range detaches {
		var b map[string]any
		_ = json.Unmarshal(req.Body, &b)
		ids, _ := b["instance_ids"].([]any)
		found := false
		for _, idv := range ids {
			if idv == "inst-a" {
				found = true
			}
		}
		if !found {
			t.Errorf("detach body did not carry inst-a: %v", b)
		}
	}

	// Assert exactly one rule DELETE happened during update.
	totalRuleDeletes := 0
	for _, rid := range []string{"rule-1", "rule-2", "rule-3", "rule-4", "rule-5"} {
		totalRuleDeletes += len(srv.Requests("DELETE", "/security-group/"+sgID+"/rule/"+rid))
	}
	if totalRuleDeletes != 1 {
		t.Errorf("total rule DELETE requests = %d; want 1", totalRuleDeletes)
	}
}

// makeRemoveRuleHandler returns a handler that deletes the given rule id from the
// store and bumps the remove counter.
func makeRemoveRuleHandler(store *sgMockServer, ruleID string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		store.mu.Lock()
		defer store.mu.Unlock()
		delete(store.rules, ruleID)
		store.removeRuleCalls++
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Rule removed successfully",
		})
	}
}
