package resources_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/iaas/terraform-provider-iaas/internal/acctest"
)

// ---------------------------------------------------------------------------
// TestAccKubernetesNodePool_basic — LIVE acceptance test (manual staging gate).
//
// Auto-skips unless TF_ACC is set. Requires an existing K8s cluster id and a
// worker instance plan id, supplied via env vars. Skips cleanly when absent.
// ---------------------------------------------------------------------------
func TestAccKubernetesNodePool_basic(t *testing.T) {
	clusterID := os.Getenv("IAAS_TEST_K8S_CLUSTER_ID")
	planID := os.Getenv("IAAS_TEST_K8S_WORKER_PLAN_ID")
	if clusterID == "" || planID == "" {
		t.Skip("TestAccKubernetesNodePool_basic: set IAAS_TEST_K8S_CLUSTER_ID and IAAS_TEST_K8S_WORKER_PLAN_ID to run this acceptance test")
	}

	config := fmt.Sprintf(`
resource "iaas_kubernetes_node_pool" "test" {
  cluster_id       = %q
  name             = "tf-acc-pool"
  instance_plan_id = %q
  min_size         = 1
  max_size         = 3
  target_count     = 1
}
`, clusterID, planID)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("iaas_kubernetes_node_pool.test", "id"),
					resource.TestCheckResourceAttr("iaas_kubernetes_node_pool.test", "name", "tf-acc-pool"),
				),
			},
			{
				ResourceName: "iaas_kubernetes_node_pool.test",
				ImportState:  true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					rs := s.RootModule().Resources["iaas_kubernetes_node_pool.test"]
					return rs.Primary.Attributes["cluster_id"] + "/" + rs.Primary.ID, nil
				},
				ImportStateVerify: true,
			},
		},
	})
}

// ---------------------------------------------------------------------------
// TestUnitKubernetesNodePool_lifecycle — MOCK-backed lifecycle proof.
//
// Drives the full child-resource lifecycle against canned API responses with no
// live panel:
//
//  1. Create — POST /kubernetes/cluster/{id}/pools returns {pool:{id,...}}; the
//     LIST read-back then reports the same pool (no per-pool SHOW exists, so Read
//     scans the pool list). Asserts the create body and the Idempotency-Key.
//  2. Import — by composite "<cluster_id>/<pool_id>", verifies state matches.
//  3. Update — scale target_count + rename + change labels (PATCH) → read-back.
//  4. Delete — DELETE with force=true; the next LIST omits the pool.
//
// SYNC (no waiter), so no poll-interval seam is needed; the test cannot hang.
// resource.UnitTest needs an OpenTofu/Terraform binary (ensureTFBinary); absent
// one, it skips.
// ---------------------------------------------------------------------------
func TestUnitKubernetesNodePool_lifecycle(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)

	const (
		clusterID = "11111111-1111-1111-1111-111111111111"
		poolID    = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		planID    = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	)
	poolsPath := "/kubernetes/cluster/" + clusterID + "/pools"
	itemPath := "/kubernetes/cluster/" + clusterID + "/pool/" + poolID

	var mu sync.Mutex
	deleted := false
	name := "gpu-pool"
	target := int64(2)
	labels := map[string]any{"role": "gpu"}

	// poolObject — the canonical pool row echoed by create / list / patch.
	poolObject := func() map[string]any {
		mu.Lock()
		defer mu.Unlock()
		// copy labels so callers cannot mutate the shared map
		lab := map[string]any{}
		for k, v := range labels {
			lab[k] = v
		}
		return map[string]any{
			"id":                  poolID,
			"cluster_id":          clusterID,
			"name":                name,
			"instance_plan_id":    planID,
			"is_default":          false,
			"min_size":            target,
			"max_size":            float64(5),
			"target_count":        target,
			"weight":              float64(50),
			"autoscaling_enabled": true,
			"labels":              lab,
			"taints":              []any{},
			"vm_refs_count":       target,
		}
	}

	// CREATE — record the pool; return it under the "pool" envelope (HTTP 201).
	srv.Handle("POST", poolsPath, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusCreated, map[string]any{"pool": poolObject()})
	})

	// LIST — read-by-scan source; empty once deleted.
	srv.Handle("GET", poolsPath, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gone := deleted
		mu.Unlock()
		if gone {
			writeJSON(w, http.StatusOK, map[string]any{"pools": []any{}})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"pools": []any{poolObject()}})
	})

	// UPDATE — apply rename / scale / labels; echo the updated pool.
	srv.Handle("PATCH", itemPath, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		if n, ok := body["name"].(string); ok && n != "" {
			name = n
		}
		if tc, ok := body["target_count"].(float64); ok {
			target = int64(tc)
		}
		if lab, ok := body["labels"].(map[string]any); ok {
			labels = lab
		}
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"pool": poolObject()})
	})

	// DELETE — soft-delete; the next LIST omits the pool. Returns a task_id.
	srv.Handle("DELETE", itemPath, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		deleted = true
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"task_id": "pool-del-1", "force": true})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	cfg := func(poolName string, tc int, role string) string {
		return providerCfg + fmt.Sprintf(`
resource "iaas_kubernetes_node_pool" "test" {
  cluster_id       = %q
  name             = %q
  instance_plan_id = %q
  min_size         = %d
  max_size         = 5
  target_count     = %d
  labels = {
    role = %q
  }
}
`, clusterID, poolName, planID, tc, tc, role)
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			// Create + read-back.
			{
				Config: cfg("gpu-pool", 2, "gpu"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_kubernetes_node_pool.test", "id", poolID),
					resource.TestCheckResourceAttr("iaas_kubernetes_node_pool.test", "cluster_id", clusterID),
					resource.TestCheckResourceAttr("iaas_kubernetes_node_pool.test", "name", "gpu-pool"),
					resource.TestCheckResourceAttr("iaas_kubernetes_node_pool.test", "instance_plan_id", planID),
					resource.TestCheckResourceAttr("iaas_kubernetes_node_pool.test", "target_count", "2"),
					resource.TestCheckResourceAttr("iaas_kubernetes_node_pool.test", "min_size", "2"),
					resource.TestCheckResourceAttr("iaas_kubernetes_node_pool.test", "max_size", "5"),
					resource.TestCheckResourceAttr("iaas_kubernetes_node_pool.test", "is_default", "false"),
					resource.TestCheckResourceAttr("iaas_kubernetes_node_pool.test", "current_node_count", "2"),
					resource.TestCheckResourceAttr("iaas_kubernetes_node_pool.test", "labels.role", "gpu"),
				),
			},
			// Import via composite "<cluster_id>/<pool_id>".
			{
				ResourceName:      "iaas_kubernetes_node_pool.test",
				ImportState:       true,
				ImportStateId:     clusterID + "/" + poolID,
				ImportStateVerify: true,
			},
			// Update — rename + scale to 4 + change label.
			{
				Config: cfg("gpu-renamed", 4, "ml"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_kubernetes_node_pool.test", "name", "gpu-renamed"),
					resource.TestCheckResourceAttr("iaas_kubernetes_node_pool.test", "target_count", "4"),
					resource.TestCheckResourceAttr("iaas_kubernetes_node_pool.test", "min_size", "4"),
					resource.TestCheckResourceAttr("iaas_kubernetes_node_pool.test", "labels.role", "ml"),
					resource.TestCheckResourceAttr("iaas_kubernetes_node_pool.test", "id", poolID),
				),
			},
		},
	})

	// Assert the CREATE body carried name + instance_plan_id + sizing and the
	// Idempotency-Key header; computed fields must NOT be in the body.
	creates := srv.Requests("POST", poolsPath)
	if len(creates) == 0 {
		t.Fatal("expected at least one POST " + poolsPath)
	}
	if got := creates[0].Header.Get("Idempotency-Key"); got == "" {
		t.Error("expected create request to carry a non-empty Idempotency-Key header")
	}
	var createBody map[string]any
	if err := json.Unmarshal(creates[0].Body, &createBody); err != nil {
		t.Fatalf("decoding create body: %v", err)
	}
	if createBody["name"] != "gpu-pool" {
		t.Errorf("create body name = %v; want gpu-pool", createBody["name"])
	}
	if createBody["instance_plan_id"] != planID {
		t.Errorf("create body instance_plan_id = %v; want %s", createBody["instance_plan_id"], planID)
	}
	if createBody["target_count"] != float64(2) {
		t.Errorf("create body target_count = %v; want 2", createBody["target_count"])
	}
	if createBody["min_size"] != float64(2) {
		t.Errorf("create body min_size = %v; want 2", createBody["min_size"])
	}
	lab, ok := createBody["labels"].(map[string]any)
	if !ok || lab["role"] != "gpu" {
		t.Errorf("create body labels = %v; want {role:gpu}", createBody["labels"])
	}
	for _, stray := range []string{"id", "is_default", "vm_refs_count", "cluster_id"} {
		if _, present := createBody[stray]; present {
			t.Errorf("create body must NOT include %q; got %v", stray, createBody)
		}
	}

	// Assert the UPDATE fired with the renamed name + scaled target + new label
	// and an Idempotency-Key.
	updates := srv.Requests("PATCH", itemPath)
	if len(updates) == 0 {
		t.Fatal("expected at least one PATCH " + itemPath)
	}
	last := updates[len(updates)-1]
	if got := last.Header.Get("Idempotency-Key"); got == "" {
		t.Error("expected update request to carry a non-empty Idempotency-Key header")
	}
	var updBody map[string]any
	if err := json.Unmarshal(last.Body, &updBody); err != nil {
		t.Fatalf("decoding update body: %v", err)
	}
	if updBody["name"] != "gpu-renamed" {
		t.Errorf("update body name = %v; want gpu-renamed", updBody["name"])
	}
	if updBody["target_count"] != float64(4) {
		t.Errorf("update body target_count = %v; want 4", updBody["target_count"])
	}

	// Assert the DELETE fired exactly once with an Idempotency-Key. (The
	// force=true query param is asserted at the client level in
	// TestDeleteKubernetesNodePool_Success; the mock server records only the URL
	// path, not the query string.)
	dels := srv.Requests("DELETE", itemPath)
	if len(dels) != 1 {
		t.Fatalf("expected exactly 1 DELETE %s, got %d", itemPath, len(dels))
	}
	if got := dels[0].Header.Get("Idempotency-Key"); got == "" {
		t.Error("expected delete request to carry a non-empty Idempotency-Key header")
	}
}
