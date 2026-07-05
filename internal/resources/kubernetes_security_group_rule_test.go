package resources_test

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/hypervisor-io/terraform-provider-iaas/internal/acctest"
)

func TestAccKubernetesSecurityGroupRule_basic(t *testing.T) {
	t.Skip("TestAccKubernetesSecurityGroupRule_basic: acceptance test runs only with TF_ACC + a real cluster id (manual staging gate)")
}

// TestUnitKubernetesSecurityGroupRule_rejectNoTarget is a NEGATIVE
// ConfigValidators test: none of cidr/remote_group_id/ip_set_id set must be
// rejected at PLAN time (mirrors the Master's SecurityGroupService::addRule
// mutual-exclusivity rule) - no API call is ever made.
func TestUnitKubernetesSecurityGroupRule_rejectNoTarget(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)
	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: providerCfg + `
resource "iaas_kubernetes_security_group_rule" "bad" {
  cluster_id = "11111111-1111-1111-1111-111111111111"
  scope      = "worker"
  direction  = "ingress"
  protocol   = "tcp"
  ip_version = "ipv4"
}
`,
				ExpectError: regexp.MustCompile(`(?i)exactly one of cidr, remote_group_id, or ip_set_id is required`),
			},
		},
	})
}

// TestUnitKubernetesSecurityGroupRule_rejectMultipleTargets is a NEGATIVE
// ConfigValidators test: setting BOTH cidr and remote_group_id must be
// rejected at PLAN time - the two are mutually exclusive.
func TestUnitKubernetesSecurityGroupRule_rejectMultipleTargets(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)
	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: providerCfg + `
resource "iaas_kubernetes_security_group_rule" "bad" {
  cluster_id      = "11111111-1111-1111-1111-111111111111"
  scope           = "worker"
  direction       = "ingress"
  protocol        = "tcp"
  ip_version      = "ipv4"
  cidr            = "10.0.0.0/8"
  remote_group_id = "22222222-2222-2222-2222-222222222222"
}
`,
				ExpectError: regexp.MustCompile(`(?i)mutually exclusive`),
			},
		},
	})
}

// TestUnitKubernetesSecurityGroupRule_lifecycle drives the CHILD lifecycle (no
// update - every field is RequiresReplace):
//
//  1. Create - POST .../security-group/{scope} with direction/protocol/
//     port_range_min/port_range_max/ip_version/cidr/description; asserts the
//     create body carried them AND the Idempotency-Key header
//     (idempotency.user). The rule then appears in the (cluster,scope) LIST,
//     carrying its own security_group_id + internal columns.
//  2. Read - lists the cluster's scope rules and matches by id.
//  3. Import - 3-part composite "<cluster_id>/<scope>/<rule_id>".
//  4. Delete - DELETEs ".../security-group/{scope}/rule/{ruleId}", also
//     carrying the Idempotency-Key header.
//
// Every recorded request is asserted to hit the CLUSTER+SCOPE-scoped path
// (never a bare "/security-group/{id}/..." standalone-SG path).
func TestUnitKubernetesSecurityGroupRule_lifecycle(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)

	const (
		clusterID = "11111111-1111-1111-1111-111111111111"
		scope     = "worker"
		ruleID    = "44444444-4444-4444-4444-444444444444"
		sgID      = "55555555-5555-5555-5555-555555555555"
	)

	var mu sync.Mutex
	exists := false

	rules := func() []any {
		mu.Lock()
		defer mu.Unlock()
		if !exists {
			return []any{}
		}
		return []any{
			map[string]any{
				"id":                ruleID,
				"security_group_id": sgID,
				"direction":         "ingress",
				"protocol":          "tcp",
				"port_range_min":    30000,
				"port_range_max":    32767,
				"ip_version":        "ipv4",
				"cidr":              "10.0.0.0/8",
				"description":       "nodeport range",
				"internal":          false,
			},
		}
	}

	listPath := "/kubernetes/cluster/" + clusterID + "/security-group/" + scope

	srv.Handle("GET", listPath, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success":        true,
			"rules":          rules(),
			"security_group": map[string]any{"id": sgID, "name": "cluster-worker-prod"},
		})
	})

	srv.Handle("POST", listPath, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		exists = true
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Rule added",
			"rule": map[string]any{
				"id":                ruleID,
				"security_group_id": sgID,
				"direction":         "ingress",
				"protocol":          "tcp",
				"port_range_min":    30000,
				"port_range_max":    32767,
				"ip_version":        "ipv4",
				"cidr":              "10.0.0.0/8",
				"description":       "nodeport range",
				"internal":          false,
			},
		})
	})

	srv.Handle("DELETE", listPath+"/rule/"+ruleID, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		exists = false
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Rule removed"})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	createCfg := providerCfg + `
resource "iaas_kubernetes_security_group_rule" "test" {
  cluster_id      = "` + clusterID + `"
  scope           = "` + scope + `"
  direction       = "ingress"
  protocol        = "tcp"
  port_range_min  = 30000
  port_range_max  = 32767
  ip_version      = "ipv4"
  cidr            = "10.0.0.0/8"
  description     = "nodeport range"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: createCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_kubernetes_security_group_rule.test", "id", ruleID),
					resource.TestCheckResourceAttr("iaas_kubernetes_security_group_rule.test", "cluster_id", clusterID),
					resource.TestCheckResourceAttr("iaas_kubernetes_security_group_rule.test", "scope", scope),
					resource.TestCheckResourceAttr("iaas_kubernetes_security_group_rule.test", "direction", "ingress"),
					resource.TestCheckResourceAttr("iaas_kubernetes_security_group_rule.test", "protocol", "tcp"),
					resource.TestCheckResourceAttr("iaas_kubernetes_security_group_rule.test", "port_range_min", "30000"),
					resource.TestCheckResourceAttr("iaas_kubernetes_security_group_rule.test", "port_range_max", "32767"),
					resource.TestCheckResourceAttr("iaas_kubernetes_security_group_rule.test", "cidr", "10.0.0.0/8"),
					resource.TestCheckResourceAttr("iaas_kubernetes_security_group_rule.test", "security_group_id", sgID),
					resource.TestCheckResourceAttr("iaas_kubernetes_security_group_rule.test", "internal", "false"),
				),
			},
			{
				ResourceName:      "iaas_kubernetes_security_group_rule.test",
				ImportState:       true,
				ImportStateId:     clusterID + "/" + scope + "/" + ruleID,
				ImportStateVerify: true,
			},
		},
	})

	// Assert the CREATE request hit the cluster+scope path and carried the
	// rule fields + Idempotency-Key header.
	creates := srv.Requests("POST", listPath)
	if len(creates) == 0 {
		t.Fatal("expected at least one POST .../security-group/{scope}")
	}
	var createBody map[string]any
	if err := json.Unmarshal(creates[0].Body, &createBody); err != nil {
		t.Fatalf("decoding rule create body: %v", err)
	}
	if createBody["direction"] != "ingress" {
		t.Errorf("create body direction = %v; want ingress", createBody["direction"])
	}
	if createBody["protocol"] != "tcp" {
		t.Errorf("create body protocol = %v; want tcp", createBody["protocol"])
	}
	if createBody["port_range_min"] != float64(30000) || createBody["port_range_max"] != float64(32767) {
		t.Errorf("create body ports = %v/%v; want 30000/32767", createBody["port_range_min"], createBody["port_range_max"])
	}
	if createBody["cidr"] != "10.0.0.0/8" {
		t.Errorf("create body cidr = %v; want 10.0.0.0/8", createBody["cidr"])
	}
	if creates[0].Header.Get("Idempotency-Key") == "" {
		t.Error("expected a non-empty Idempotency-Key header on create (idempotency.user)")
	}

	// Assert the DELETE request hit the cluster+scope+rule path and carried
	// the Idempotency-Key header.
	deletes := srv.Requests("DELETE", listPath+"/rule/"+ruleID)
	if len(deletes) == 0 {
		t.Fatal("expected at least one DELETE .../security-group/{scope}/rule/{id}")
	}
	if deletes[0].Header.Get("Idempotency-Key") == "" {
		t.Error("expected a non-empty Idempotency-Key header on delete (idempotency.user)")
	}

	// Belt-and-braces: every recorded create/delete request hit the
	// CLUSTER+SCOPE-scoped route - never a bare "/security-group/{id}/..."
	// standalone-SG path.
	for _, req := range append(append([]acctest.RecordedRequest{}, creates...), deletes...) {
		if !strings.Contains(req.Path, "/kubernetes/cluster/"+clusterID+"/security-group/"+scope) {
			t.Errorf("request path %s did not hit the cluster+scope-scoped route", req.Path)
		}
	}
}
