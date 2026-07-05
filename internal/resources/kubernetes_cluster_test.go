package resources_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/hypervisor-io/terraform-provider-iaas/internal/acctest"
)

// ---------------------------------------------------------------------------
// TestAccKubernetesCluster_basic - LIVE acceptance test (manual staging gate).
//
// Auto-skips unless TF_ACC is set (resource.Test enforces this). Requires a
// reachable panel + IP-locked token, plus a fully prepared K8s-eligible region:
// a hypervisor group with kubernetes+vpc+lb enabled, a VPC with a NAT gateway,
// a private CP subnet + a worker subnet, an active K8s version, CP + worker
// instance plans, and a CP LB plan - all supplied via env vars. The test skips
// cleanly when any var is absent so a bare TF_ACC=1 run does not fail.
// ---------------------------------------------------------------------------
func TestAccKubernetesCluster_basic(t *testing.T) {
	vars := map[string]string{
		"hg":     os.Getenv("IAAS_TEST_K8S_HG_ID"),
		"vpc":    os.Getenv("IAAS_TEST_K8S_VPC_ID"),
		"cpsub":  os.Getenv("IAAS_TEST_K8S_CP_SUBNET_ID"),
		"wksub":  os.Getenv("IAAS_TEST_K8S_WORKER_SUBNET_ID"),
		"ver":    os.Getenv("IAAS_TEST_K8S_VERSION_ID"),
		"cpplan": os.Getenv("IAAS_TEST_K8S_CP_PLAN_ID"),
		"lbplan": os.Getenv("IAAS_TEST_K8S_CP_LB_PLAN_ID"),
		"wkplan": os.Getenv("IAAS_TEST_K8S_WORKER_PLAN_ID"),
	}
	for _, v := range vars {
		if v == "" {
			t.Skip("TestAccKubernetesCluster_basic: set IAAS_TEST_K8S_* vars (hg/vpc/cp+worker subnet/version/cp+worker plan/cp lb plan) to run this acceptance test")
		}
	}

	config := fmt.Sprintf(`
resource "iaas_kubernetes_cluster" "test" {
  name                    = "tf-acc-k8s"
  slug                    = "tf-acc-k8s"
  hypervisor_group_id     = %q
  vpc_id                  = %q
  cp_vpc_subnet_id        = %q
  worker_vpc_subnet_id    = %q
  kubernetes_version_id   = %q
  control_node_count      = 1
  endpoint_mode           = "public_and_private"
  cp_instance_plan_id     = %q
  cp_lb_plan_id           = %q
  worker_instance_plan_id = %q
  worker_count            = 1
}
`, vars["hg"], vars["vpc"], vars["cpsub"], vars["wksub"], vars["ver"], vars["cpplan"], vars["lbplan"], vars["wkplan"])

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("iaas_kubernetes_cluster.test", "id"),
					resource.TestCheckResourceAttr("iaas_kubernetes_cluster.test", "state", "running"),
				),
			},
			{
				ResourceName:            "iaas_kubernetes_cluster.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"timeouts", "upgrade_drain_grace_period", "upgrade_max_surge", "upgrade_ccm"},
			},
		},
	})
}

// ---------------------------------------------------------------------------
// TestUnitKubernetesCluster_lifecycle - MOCK-backed lifecycle proof.
//
// Drives the full ASYNC multi-stage cluster lifecycle against canned API
// responses with no live panel:
//
//  1. Create - POST /kubernetes/clusters returns {cluster:{id,state:"created"},
//     task_id}; the SHOW then immediately returns state="running" (ready on the
//     FIRST poll → the waiter converges instantly, no sleep). Asserts the create
//     body (required topology inputs) and that the Idempotency-Key header is
//     present.
//  2. Import - by the cluster id, verifies state matches (ignoring timeouts).
//  3. Update - rename (PATCH /kubernetes/cluster/{id}) → read-back.
//  4. Delete - implicit teardown; DELETE soft-deletes and the next SHOW 404s,
//     which the delete waiter converges on the FIRST poll.
//
// The IAAS_INSTANCE_POLL_INTERVAL seam is set tiny so the waiter cannot hang;
// combined with running-on-first-poll the test must NOT sleep. resource.UnitTest
// needs an OpenTofu/Terraform binary (ensureTFBinary); absent one, it skips.
// ---------------------------------------------------------------------------
func TestUnitKubernetesCluster_lifecycle(t *testing.T) {
	ensureTFBinary(t)

	// TEST-ONLY poll-interval seam: instant convergence.
	t.Setenv("IAAS_INSTANCE_POLL_INTERVAL", "1ms")

	srv := acctest.NewMockServer(t)

	const (
		clusterID = "11111111-1111-1111-1111-111111111111"
		hgID      = "22222222-2222-2222-2222-222222222222"
		vpcID     = "33333333-3333-3333-3333-333333333333"
		cpSubID   = "44444444-4444-4444-4444-444444444444"
		wkSubID   = "55555555-5555-5555-5555-555555555555"
		verID     = "66666666-6666-6666-6666-666666666666"
		cpPlanID  = "77777777-7777-7777-7777-777777777777"
		lbPlanID  = "88888888-8888-8888-8888-888888888888"
		wkPlanID  = "99999999-9999-9999-9999-999999999999"
		basePath  = "/kubernetes/clusters"
	)
	itemPath := "/kubernetes/cluster/" + clusterID

	var mu sync.Mutex
	deleted := false
	name := "prod"

	// SHOW payload - already "running" so the create waiter converges on the
	// first poll (no sleep).
	showCluster := func() map[string]any {
		mu.Lock()
		n := name
		mu.Unlock()
		return map[string]any{
			"id":                             clusterID,
			"name":                           n,
			"slug":                           "prod",
			"state":                          "running",
			"hypervisor_group_id":            hgID,
			"vpc_id":                         vpcID,
			"cp_vpc_subnet_id":               cpSubID,
			"worker_vpc_subnet_id":           wkSubID,
			"kubernetes_version_id":          verID,
			"control_node_count":             float64(1),
			"endpoint_mode":                  "public_and_private",
			"pod_cidr":                       "10.244.0.0/16",
			"service_cidr":                   "10.96.0.0/12",
			"lb_ha_enabled":                  false,
			"pod_security_admission_default": "baseline",
			"cp_instance_plan_id":            cpPlanID,
			"cp_lb_plan_id":                  lbPlanID,
			"worker_instance_plan_id":        wkPlanID,
			"worker_count":                   float64(2),
			"endpoint_url":                   "https://203.0.113.5:6443",
			"endpoint_url_public":            "https://203.0.113.5",
			"endpoint_url_private":           "https://10.0.0.5",
			"kubernetes_version":             map[string]any{"id": verID, "semantic_version": "1.30.2"},
			// cp_kubernetes_version_id/cp_kubernetes_version: the Master
			// initialises these equal to the worker baseline at create
			// (ClusterService::create sets cp_kubernetes_version_id ==
			// kubernetes_version_id); this test never bumps the version, so
			// they stay pinned to verID/1.30.2 throughout (see
			// TestUnitKubernetesCluster_versionUpgrade for the bump path).
			"cp_kubernetes_version_id": verID,
			"cp_kubernetes_version":    map[string]any{"id": verID, "semantic_version": "1.30.2"},
		}
	}

	// CREATE - record the row; the create response carries state "created" + a
	// tracking task_id (the SHOW already reports "running" so the waiter
	// converges immediately).
	srv.Handle("POST", basePath, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"cluster": map[string]any{
				"id":    clusterID,
				"name":  "prod",
				"slug":  "prod",
				"state": "created",
			},
			"task_id": "task-create-1",
		})
	})

	// SHOW - 404 once delete has been enqueued.
	srv.Handle("GET", itemPath, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gone := deleted
		mu.Unlock()
		if gone {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "Cluster not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "cluster": showCluster()})
	})

	// UPDATE - rename; echo the new name into the cluster envelope.
	srv.Handle("PATCH", itemPath, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if n, ok := body["name"].(string); ok && n != "" {
			mu.Lock()
			name = n
			mu.Unlock()
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "cluster": showCluster()})
	})

	// DELETE - soft-delete; the next SHOW 404s. Returns a task_id.
	srv.Handle("DELETE", itemPath, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		deleted = true
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "task_id": "task-delete-1"})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	cfg := func(clusterName string) string {
		return providerCfg + fmt.Sprintf(`
resource "iaas_kubernetes_cluster" "test" {
  name                    = %q
  slug                    = "prod"
  hypervisor_group_id     = %q
  vpc_id                  = %q
  cp_vpc_subnet_id        = %q
  worker_vpc_subnet_id    = %q
  kubernetes_version_id   = %q
  control_node_count      = 1
  endpoint_mode           = "public_and_private"
  cp_instance_plan_id     = %q
  cp_lb_plan_id           = %q
  worker_instance_plan_id = %q
  worker_count            = 2
}
`, clusterName, hgID, vpcID, cpSubID, wkSubID, verID, cpPlanID, lbPlanID, wkPlanID)
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			// Create + read-back (async wait converges immediately).
			{
				Config: cfg("prod"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_kubernetes_cluster.test", "id", clusterID),
					resource.TestCheckResourceAttr("iaas_kubernetes_cluster.test", "name", "prod"),
					resource.TestCheckResourceAttr("iaas_kubernetes_cluster.test", "slug", "prod"),
					resource.TestCheckResourceAttr("iaas_kubernetes_cluster.test", "state", "running"),
					resource.TestCheckResourceAttr("iaas_kubernetes_cluster.test", "control_node_count", "1"),
					resource.TestCheckResourceAttr("iaas_kubernetes_cluster.test", "worker_count", "2"),
					resource.TestCheckResourceAttr("iaas_kubernetes_cluster.test", "endpoint_url", "https://203.0.113.5:6443"),
					resource.TestCheckResourceAttr("iaas_kubernetes_cluster.test", "kubernetes_version", "1.30.2"),
				),
			},
			// Import the existing resource by id and verify state matches.
			{
				ResourceName:            "iaas_kubernetes_cluster.test",
				ImportState:             true,
				ImportStateId:           clusterID,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"timeouts", "upgrade_drain_grace_period", "upgrade_max_surge", "upgrade_ccm"},
			},
			// Update - rename in place (PATCH).
			{
				Config: cfg("prod-renamed"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_kubernetes_cluster.test", "name", "prod-renamed"),
					resource.TestCheckResourceAttr("iaas_kubernetes_cluster.test", "id", clusterID),
				),
			},
		},
	})

	// Assert the CREATE body carried the required topology inputs and the
	// Idempotency-Key header.
	creates := srv.Requests("POST", basePath)
	if len(creates) == 0 {
		t.Fatal("expected at least one POST " + basePath)
	}
	if got := creates[0].Header.Get("Idempotency-Key"); got == "" {
		t.Error("expected create request to carry a non-empty Idempotency-Key header")
	}
	var createBody map[string]any
	if err := json.Unmarshal(creates[0].Body, &createBody); err != nil {
		t.Fatalf("decoding create body: %v", err)
	}
	for k, want := range map[string]any{
		"name":                    "prod",
		"slug":                    "prod",
		"hypervisor_group_id":     hgID,
		"vpc_id":                  vpcID,
		"cp_vpc_subnet_id":        cpSubID,
		"worker_vpc_subnet_id":    wkSubID,
		"kubernetes_version_id":   verID,
		"endpoint_mode":           "public_and_private",
		"cp_instance_plan_id":     cpPlanID,
		"cp_lb_plan_id":           lbPlanID,
		"worker_instance_plan_id": wkPlanID,
	} {
		if createBody[k] != want {
			t.Errorf("create body[%q] = %v; want %v", k, createBody[k], want)
		}
	}
	// control_node_count + worker_count are JSON numbers → float64.
	if createBody["control_node_count"] != float64(1) {
		t.Errorf("create body control_node_count = %v; want 1", createBody["control_node_count"])
	}
	if createBody["worker_count"] != float64(2) {
		t.Errorf("create body worker_count = %v; want 2", createBody["worker_count"])
	}
	// Computed/server-only fields must NOT be in the create body.
	for _, stray := range []string{"id", "state", "endpoint_url", "endpoint_url_public", "endpoint_url_private", "kubernetes_version"} {
		if _, present := createBody[stray]; present {
			t.Errorf("create body must NOT include %q; got %v", stray, createBody)
		}
	}

	// Assert the UPDATE fired with the renamed name and an Idempotency-Key.
	updates := srv.Requests("PATCH", itemPath)
	if len(updates) == 0 {
		t.Fatal("expected at least one PATCH " + itemPath)
	}
	if got := updates[len(updates)-1].Header.Get("Idempotency-Key"); got == "" {
		t.Error("expected update request to carry a non-empty Idempotency-Key header")
	}
	var updBody map[string]any
	if err := json.Unmarshal(updates[len(updates)-1].Body, &updBody); err != nil {
		t.Fatalf("decoding update body: %v", err)
	}
	if updBody["name"] != "prod-renamed" {
		t.Errorf("update body name = %v; want prod-renamed", updBody["name"])
	}

	// Assert the DELETE fired exactly once with an Idempotency-Key.
	dels := srv.Requests("DELETE", itemPath)
	if len(dels) != 1 {
		t.Fatalf("expected exactly 1 DELETE %s, got %d", itemPath, len(dels))
	}
	if got := dels[0].Header.Get("Idempotency-Key"); got == "" {
		t.Error("expected delete request to carry a non-empty Idempotency-Key header")
	}
}
