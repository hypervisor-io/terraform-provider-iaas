package resources_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/iaas/terraform-provider-iaas/internal/acctest"
)

// ---------------------------------------------------------------------------
// TestUnitKubernetesCluster_versionUpgrade - MOCK-backed proof of the T7/id-G8
// in-place version-upgrade path folded into iaas_kubernetes_cluster's Update.
//
// Drives: create at version A → update kubernetes_version_id to version B →
// assert the STAGED sequence fired (control plane first, then workers, then a
// CCM redeploy since upgrade_ccm defaults true) and that the final state
// reflects the new version on BOTH the worker baseline (kubernetes_version)
// and the control-plane tracker (cp_kubernetes_version) exposed read-only by
// this resource. The mock's cluster.state never leaves "running" (mirroring
// the Master's real behavior - CP/worker rolling upgrades never touch
// `state`), and each rolling-upgrade stage's KubernetesClusterTask is reported
// "completed" starting from the FIRST poll after its POST fires, so - combined
// with the tiny IAAS_INSTANCE_POLL_INTERVAL seam - the test does not sleep.
// ---------------------------------------------------------------------------
func TestUnitKubernetesCluster_versionUpgrade(t *testing.T) {
	ensureTFBinary(t)

	// TEST-ONLY poll-interval seam: instant convergence.
	t.Setenv("IAAS_INSTANCE_POLL_INTERVAL", "1ms")

	srv := acctest.NewMockServer(t)

	const (
		clusterID = "a1111111-1111-1111-1111-111111111111"
		hgID      = "a2222222-2222-2222-2222-222222222222"
		vpcID     = "a3333333-3333-3333-3333-333333333333"
		cpSubID   = "a4444444-4444-4444-4444-444444444444"
		wkSubID   = "a5555555-5555-5555-5555-555555555555"
		verID1    = "a6666666-6666-6666-6666-666666666666" // 1.30.2 - create version
		verID2    = "a6666666-6666-6666-6666-666666666667" // 1.31.0 - upgrade target
		cpPlanID  = "a7777777-7777-7777-7777-777777777777"
		lbPlanID  = "a8888888-8888-8888-8888-888888888888"
		wkPlanID  = "a9999999-9999-9999-9999-999999999999"
		basePath  = "/kubernetes/clusters"
	)
	itemPath := "/kubernetes/cluster/" + clusterID

	semver := map[string]string{verID1: "1.30.2", verID2: "1.31.0"}

	var mu sync.Mutex
	// Mutable mock cluster state - mirrors the Master's two independent
	// version columns, both starting equal at create.
	workerVersionID := verID1
	cpVersionID := verID1
	// tasks accumulates completed KubernetesClusterTask rows, newest first,
	// mirroring the SHOW endpoint's eager-loaded "tasks" array.
	var tasks []map[string]any
	deleted := false

	showCluster := func() map[string]any {
		mu.Lock()
		defer mu.Unlock()
		tasksCopy := make([]any, len(tasks))
		for i, tk := range tasks {
			tasksCopy[i] = tk
		}
		return map[string]any{
			"id":                             clusterID,
			"name":                           "prod",
			"slug":                           "prod",
			"state":                          "running", // NEVER flips during CP/worker upgrades.
			"hypervisor_group_id":            hgID,
			"vpc_id":                         vpcID,
			"cp_vpc_subnet_id":               cpSubID,
			"worker_vpc_subnet_id":           wkSubID,
			"kubernetes_version_id":          workerVersionID,
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
			"kubernetes_version":             map[string]any{"id": workerVersionID, "semantic_version": semver[workerVersionID]},
			"cp_kubernetes_version_id":       cpVersionID,
			"cp_kubernetes_version":          map[string]any{"id": cpVersionID, "semantic_version": semver[cpVersionID]},
			"tasks":                          tasksCopy,
		}
	}

	// CREATE.
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

	// SHOW - 404 once delete has been enqueued (mirrors
	// TestUnitKubernetesCluster_lifecycle's pattern).
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

	// UPDATE (metadata PATCH) - registered defensively; this test never
	// changes name/description/project_id, so it should not be hit.
	srv.Handle("PATCH", itemPath, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "cluster": showCluster()})
	})

	// UPGRADE CP - flips cp_kubernetes_version_id to the target and marks its
	// task "completed" immediately (ready on the first poll, no sleep).
	srv.Handle("POST", itemPath+"/upgrade/cp", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		target, _ := body["target_version_id"].(string)

		mu.Lock()
		cpVersionID = target
		tasks = append([]map[string]any{{"id": "cp-task-1", "status": "completed", "phase": "finalize", "progress": float64(100)}}, tasks...)
		mu.Unlock()

		writeJSON(w, http.StatusOK, map[string]any{
			"task_id":            "cp-task-1",
			"target_version_id":  target,
			"current_version_id": verID1,
			"planned_waves":      []any{float64(1)},
		})
	})

	// UPGRADE WORKERS - flips kubernetes_version_id (worker baseline) to the
	// target and marks its task "completed" immediately.
	srv.Handle("POST", itemPath+"/upgrade/workers", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		target, _ := body["target_version_id"].(string)

		mu.Lock()
		workerVersionID = target
		tasks = append([]map[string]any{{"id": "wk-task-1", "status": "completed", "phase": "finalize", "progress": float64(100)}}, tasks...)
		mu.Unlock()

		writeJSON(w, http.StatusOK, map[string]any{
			"task_id":            "wk-task-1",
			"target_version_id":  target,
			"current_version_id": verID1,
			"planned_waves":      []any{float64(2), float64(2)},
		})
	})

	// UPGRADE CCM - synchronous, no task.
	srv.Handle("POST", itemPath+"/upgrade/ccm", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "CCM redeployed"})
	})

	// DELETE - the test harness destroys the resource at the end of the run.
	srv.Handle("DELETE", itemPath, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		deleted = true
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "task_id": "task-delete-1"})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	cfg := func(versionID string) string {
		return providerCfg + fmt.Sprintf(`
resource "iaas_kubernetes_cluster" "test" {
  name                    = "prod"
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
`, hgID, vpcID, cpSubID, wkSubID, versionID, cpPlanID, lbPlanID, wkPlanID)
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			// Create at verID1 (1.30.2).
			{
				Config: cfg(verID1),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_kubernetes_cluster.test", "id", clusterID),
					resource.TestCheckResourceAttr("iaas_kubernetes_cluster.test", "kubernetes_version_id", verID1),
					resource.TestCheckResourceAttr("iaas_kubernetes_cluster.test", "kubernetes_version", "1.30.2"),
					resource.TestCheckResourceAttr("iaas_kubernetes_cluster.test", "cp_kubernetes_version_id", verID1),
					resource.TestCheckResourceAttr("iaas_kubernetes_cluster.test", "cp_kubernetes_version", "1.30.2"),
					resource.TestCheckResourceAttr("iaas_kubernetes_cluster.test", "state", "running"),
				),
			},
			// Update: bump kubernetes_version_id to verID2 (1.31.0) - drives
			// the staged cp -> workers -> ccm upgrade instead of a replace.
			{
				Config: cfg(verID2),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_kubernetes_cluster.test", "id", clusterID),
					resource.TestCheckResourceAttr("iaas_kubernetes_cluster.test", "kubernetes_version_id", verID2),
					resource.TestCheckResourceAttr("iaas_kubernetes_cluster.test", "kubernetes_version", "1.31.0"),
					resource.TestCheckResourceAttr("iaas_kubernetes_cluster.test", "cp_kubernetes_version_id", verID2),
					resource.TestCheckResourceAttr("iaas_kubernetes_cluster.test", "cp_kubernetes_version", "1.31.0"),
					resource.TestCheckResourceAttr("iaas_kubernetes_cluster.test", "state", "running"),
				),
			},
		},
	})

	// ── Assert the staged sequence actually fired, in order, exactly once ────
	cpCalls := srv.Requests("POST", itemPath+"/upgrade/cp")
	wkCalls := srv.Requests("POST", itemPath+"/upgrade/workers")
	ccmCalls := srv.Requests("POST", itemPath+"/upgrade/ccm")

	if len(cpCalls) != 1 {
		t.Fatalf("expected exactly 1 POST %s/upgrade/cp, got %d", itemPath, len(cpCalls))
	}
	if len(wkCalls) != 1 {
		t.Fatalf("expected exactly 1 POST %s/upgrade/workers, got %d", itemPath, len(wkCalls))
	}
	if len(ccmCalls) != 1 {
		t.Fatalf("expected exactly 1 POST %s/upgrade/ccm, got %d", itemPath, len(ccmCalls))
	}

	// Ordering: cp before workers before ccm, using the AllRequests() sequence
	// (chronological order of every request the mock received).
	all := srv.AllRequests()
	indexOf := func(method, path string) int {
		for i, r := range all {
			if r.Method == method && r.Path == "/api"+path {
				return i
			}
		}
		return -1
	}
	cpIdx := indexOf("POST", itemPath+"/upgrade/cp")
	wkIdx := indexOf("POST", itemPath+"/upgrade/workers")
	ccmIdx := indexOf("POST", itemPath+"/upgrade/ccm")
	if !(cpIdx < wkIdx && wkIdx < ccmIdx) {
		t.Errorf("expected staged order cp(%d) < workers(%d) < ccm(%d)", cpIdx, wkIdx, ccmIdx)
	}

	// Bodies: both upgrade calls target verID2, and carry the resource's
	// default upgrade knobs (drain_grace_period=120, max_surge=1).
	var cpBody, wkBody map[string]any
	if err := json.Unmarshal(cpCalls[0].Body, &cpBody); err != nil {
		t.Fatalf("decoding cp upgrade body: %v", err)
	}
	if cpBody["target_version_id"] != verID2 {
		t.Errorf("cp upgrade body target_version_id = %v; want %v", cpBody["target_version_id"], verID2)
	}
	if cpBody["drain_grace_period"] != float64(120) {
		t.Errorf("cp upgrade body drain_grace_period = %v; want 120", cpBody["drain_grace_period"])
	}
	if err := json.Unmarshal(wkCalls[0].Body, &wkBody); err != nil {
		t.Fatalf("decoding workers upgrade body: %v", err)
	}
	if wkBody["target_version_id"] != verID2 {
		t.Errorf("workers upgrade body target_version_id = %v; want %v", wkBody["target_version_id"], verID2)
	}
	if wkBody["max_surge"] != float64(1) {
		t.Errorf("workers upgrade body max_surge = %v; want 1", wkBody["max_surge"])
	}
	if wkBody["drain_grace_period"] != float64(120) {
		t.Errorf("workers upgrade body drain_grace_period = %v; want 120", wkBody["drain_grace_period"])
	}

	// Idempotency-Key must be attached to both async upgrade calls (routes
	// carry idempotency.user).
	if got := cpCalls[0].Header.Get("Idempotency-Key"); got == "" {
		t.Error("expected cp upgrade request to carry a non-empty Idempotency-Key header")
	}
	if got := wkCalls[0].Header.Get("Idempotency-Key"); got == "" {
		t.Error("expected workers upgrade request to carry a non-empty Idempotency-Key header")
	}
}

// ---------------------------------------------------------------------------
// TestUnitKubernetesCluster_versionUpgrade_ccmDisabled verifies upgrade_ccm =
// false skips the CCM redeploy call while the cp/workers stages still run.
// ---------------------------------------------------------------------------
func TestUnitKubernetesCluster_versionUpgrade_ccmDisabled(t *testing.T) {
	ensureTFBinary(t)
	t.Setenv("IAAS_INSTANCE_POLL_INTERVAL", "1ms")

	srv := acctest.NewMockServer(t)

	const (
		clusterID = "b1111111-1111-1111-1111-111111111111"
		hgID      = "b2222222-2222-2222-2222-222222222222"
		vpcID     = "b3333333-3333-3333-3333-333333333333"
		cpSubID   = "b4444444-4444-4444-4444-444444444444"
		wkSubID   = "b5555555-5555-5555-5555-555555555555"
		verID1    = "b6666666-6666-6666-6666-666666666666"
		verID2    = "b6666666-6666-6666-6666-666666666667"
		cpPlanID  = "b7777777-7777-7777-7777-777777777777"
		lbPlanID  = "b8888888-8888-8888-8888-888888888888"
		wkPlanID  = "b9999999-9999-9999-9999-999999999999"
		basePath  = "/kubernetes/clusters"
	)
	itemPath := "/kubernetes/cluster/" + clusterID
	semver := map[string]string{verID1: "1.30.2", verID2: "1.31.0"}

	var mu sync.Mutex
	workerVersionID := verID1
	cpVersionID := verID1
	var tasks []map[string]any
	deleted := false

	showCluster := func() map[string]any {
		mu.Lock()
		defer mu.Unlock()
		tasksCopy := make([]any, len(tasks))
		for i, tk := range tasks {
			tasksCopy[i] = tk
		}
		return map[string]any{
			"id":                             clusterID,
			"name":                           "prod",
			"slug":                           "prod",
			"state":                          "running",
			"hypervisor_group_id":            hgID,
			"vpc_id":                         vpcID,
			"cp_vpc_subnet_id":               cpSubID,
			"worker_vpc_subnet_id":           wkSubID,
			"kubernetes_version_id":          workerVersionID,
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
			"kubernetes_version":             map[string]any{"id": workerVersionID, "semantic_version": semver[workerVersionID]},
			"cp_kubernetes_version_id":       cpVersionID,
			"cp_kubernetes_version":          map[string]any{"id": cpVersionID, "semantic_version": semver[cpVersionID]},
			"tasks":                          tasksCopy,
		}
	}

	srv.Handle("POST", basePath, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"cluster": map[string]any{"id": clusterID, "name": "prod", "slug": "prod", "state": "created"},
			"task_id": "task-create-1",
		})
	})
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
	srv.Handle("POST", itemPath+"/upgrade/cp", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		target, _ := body["target_version_id"].(string)
		mu.Lock()
		cpVersionID = target
		tasks = append([]map[string]any{{"id": "cp-task-2", "status": "completed"}}, tasks...)
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"task_id": "cp-task-2", "target_version_id": target, "current_version_id": verID1})
	})
	srv.Handle("POST", itemPath+"/upgrade/workers", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		target, _ := body["target_version_id"].(string)
		mu.Lock()
		workerVersionID = target
		tasks = append([]map[string]any{{"id": "wk-task-2", "status": "completed"}}, tasks...)
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"task_id": "wk-task-2", "target_version_id": target, "current_version_id": verID1})
	})
	srv.Handle("POST", itemPath+"/upgrade/ccm", func(w http.ResponseWriter, r *http.Request) {
		// Should never be hit in this test - fail loudly if it is.
		t.Error("unexpected call to POST .../upgrade/ccm with upgrade_ccm=false")
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "CCM redeployed"})
	})
	srv.Handle("DELETE", itemPath, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		deleted = true
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "task_id": "task-delete-2"})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())
	cfg := func(versionID string) string {
		return providerCfg + fmt.Sprintf(`
resource "iaas_kubernetes_cluster" "test" {
  name                    = "prod"
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
  upgrade_ccm             = false
}
`, hgID, vpcID, cpSubID, wkSubID, versionID, cpPlanID, lbPlanID, wkPlanID)
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: cfg(verID1),
				Check:  resource.TestCheckResourceAttr("iaas_kubernetes_cluster.test", "upgrade_ccm", "false"),
			},
			{
				Config: cfg(verID2),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_kubernetes_cluster.test", "kubernetes_version", "1.31.0"),
					resource.TestCheckResourceAttr("iaas_kubernetes_cluster.test", "cp_kubernetes_version", "1.31.0"),
				),
			},
		},
	})

	if len(srv.Requests("POST", itemPath+"/upgrade/cp")) != 1 {
		t.Error("expected the cp upgrade stage to still run with upgrade_ccm=false")
	}
	if len(srv.Requests("POST", itemPath+"/upgrade/workers")) != 1 {
		t.Error("expected the workers upgrade stage to still run with upgrade_ccm=false")
	}
	if len(srv.Requests("POST", itemPath+"/upgrade/ccm")) != 0 {
		t.Error("expected NO ccm upgrade call with upgrade_ccm=false")
	}
}
