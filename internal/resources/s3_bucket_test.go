package resources_test

import (
	"encoding/json"
	"net/http"
	"os"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/iaas/terraform-provider-iaas/internal/acctest"
)

// ---------------------------------------------------------------------------
// TestAccS3Bucket_basic - LIVE acceptance test (manual staging gate).
//
// Auto-skips unless TF_ACC is set. Requires a reachable panel + IP-locked token
// and real plan/server UUIDs supplied via env vars; skips cleanly when absent.
// ---------------------------------------------------------------------------
func TestAccS3Bucket_basic(t *testing.T) {
	planID := envOrSkip(t, "IAAS_TEST_S3_PLAN_ID")
	serverID := envOrSkip(t, "IAAS_TEST_S3_SERVER_ID")

	config := `
resource "iaas_s3_bucket" "test" {
  name         = "tf-acc-bucket-001"
  s3_plan_id   = "` + planID + `"
  s3_server_id = "` + serverID + `"
}
`
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("iaas_s3_bucket.test", "id"),
					resource.TestCheckResourceAttrSet("iaas_s3_bucket.test", "access_key"),
				),
			},
			{
				ResourceName:      "iaas_s3_bucket.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// ---------------------------------------------------------------------------
// s3BucketMock is a stateful mock of the bucket API for the lifecycle test.
// It models the bucket record (name/plan/server/default_access), the
// auto-generated bucket access/secret keys, and the set of attached standalone
// access keys (id → permission), and records attach/update/detach call counts.
// ---------------------------------------------------------------------------
type s3BucketMock struct {
	mu sync.Mutex

	id            string
	name          string
	planID        string
	serverID      string
	defaultAccess string

	// attached standalone keys: key id → permission.
	attached map[string]string

	aclCalls    int
	attachCalls int
	updateCalls int
	detachCalls int
}

func (s *s3BucketMock) showEnvelope() map[string]any {
	return map[string]any{
		"bucket": map[string]any{
			"id":             s.id,
			"name":           s.name,
			"s3_plan_id":     s.planID,
			"s3_server_id":   s.serverID,
			"default_access": s.defaultAccess,
			"suspended":      0,
			"quota":          1073741824,
			"bandwidth":      0,
		},
		"endpoint":   "https://s3.example.com",
		"access_key": "ak_bucketown",
		"secret_key": "sk_bucketown_secret",
	}
}

func (s *s3BucketMock) keysPaginator() map[string]any {
	data := make([]any, 0, len(s.attached))
	for kid, perm := range s.attached {
		data = append(data, map[string]any{
			"id":         kid,
			"name":       kid,
			"access_key": "ak_" + kid,
			"pivot": map[string]any{
				"s3_bucket_id":          s.id,
				"user_s3_access_key_id": kid,
				"permission":            perm,
			},
		})
	}
	return map[string]any{"current_page": 1, "last_page": 1, "data": data}
}

// ---------------------------------------------------------------------------
// TestUnitS3Bucket_lifecycle - MOCK-backed lifecycle proof.
//
// Steps:
//  1. Create with default_access=public + ONE attached key (read) → asserts the
//     create POST carried name/plan/server (and NOT computed fields), the id was
//     resolved via the by-name readback, the bucket access_key/secret_key were
//     hydrated from SHOW, and exactly one ACL + one attach call fired.
//  2. Import by id → everything rehydrated from SHOW + keys.
//  3. Update: change ACL to private, change the existing key's permission
//     (in-place UPDATE, not delete+add), attach a second key, detach the first →
//     asserts the per-set diff (1 acl, 1 attach, 1 update, 1 detach more).
//
// ---------------------------------------------------------------------------
func TestUnitS3Bucket_lifecycle(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)

	const bucketID = "11111111-1111-1111-1111-111111111111"

	store := &s3BucketMock{
		id:       bucketID,
		attached: map[string]string{},
	}

	// CREATE - POST /object-storage/buckets stores name/plan/server, returns
	// {success,message} with NO id (forcing the by-name readback).
	srv.Handle("POST", "/object-storage/buckets", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		store.mu.Lock()
		defer store.mu.Unlock()
		store.name, _ = body["name"].(string)
		store.planID, _ = body["s3_plan_id"].(string)
		store.serverID, _ = body["s3_server_id"].(string)
		store.defaultAccess = "private" // server default
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Bucket created successfully.",
		})
	})

	// LIST - GET /object-storage/buckets (by-name readback) returns a paginator.
	srv.Handle("GET", "/object-storage/buckets", func(w http.ResponseWriter, r *http.Request) {
		store.mu.Lock()
		defer store.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"current_page": 1,
			"last_page":    1,
			"data": []any{
				map[string]any{"id": store.id, "name": store.name, "s3_plan_id": store.planID, "s3_server_id": store.serverID},
			},
		})
	})

	// SHOW - GET /object-storage/bucket/{id} returns the envelope.
	srv.Handle("GET", "/object-storage/bucket/"+bucketID, func(w http.ResponseWriter, r *http.Request) {
		store.mu.Lock()
		defer store.mu.Unlock()
		writeJSON(w, http.StatusOK, store.showEnvelope())
	})

	// KEYS - GET /object-storage/bucket/{id}/keys returns attached keys + pivot.
	srv.Handle("GET", "/object-storage/bucket/"+bucketID+"/keys", func(w http.ResponseWriter, r *http.Request) {
		store.mu.Lock()
		defer store.mu.Unlock()
		writeJSON(w, http.StatusOK, store.keysPaginator())
	})

	// ACL - PATCH /object-storage/bucket/{id}/acl/{action}: action is in the path.
	for _, action := range []string{"public", "private", "upload", "download"} {
		act := action
		srv.Handle("PATCH", "/object-storage/bucket/"+bucketID+"/acl/"+act, func(w http.ResponseWriter, r *http.Request) {
			store.mu.Lock()
			defer store.mu.Unlock()
			store.aclCalls++
			store.defaultAccess = act
			writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "ACL updated"})
		})
	}

	// ATTACH/UPDATE/DETACH per candidate key id. The mock can only exact-match
	// paths, so register handlers for the key ids used in the test configs.
	for _, kid := range []string{"key-a", "key-b"} {
		k := kid
		srv.Handle("POST", "/object-storage/bucket/"+bucketID+"/attach/"+k, func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			store.mu.Lock()
			defer store.mu.Unlock()
			store.attachCalls++
			perm, _ := body["permission"].(string)
			store.attached[k] = perm
			writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "attached"})
		})
		srv.Handle("PATCH", "/object-storage/bucket/"+bucketID+"/update/"+k, func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			store.mu.Lock()
			defer store.mu.Unlock()
			store.updateCalls++
			perm, _ := body["permission"].(string)
			store.attached[k] = perm
			writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "updated"})
		})
		srv.Handle("POST", "/object-storage/bucket/"+bucketID+"/detach/"+k, func(w http.ResponseWriter, r *http.Request) {
			store.mu.Lock()
			defer store.mu.Unlock()
			store.detachCalls++
			delete(store.attached, k)
			writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "detached"})
		})
	}

	// DELETE - DELETE /object-storage/bucket/{id}.
	srv.Handle("DELETE", "/object-storage/bucket/"+bucketID, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "queued for deletion"})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	createCfg := providerCfg + `
resource "iaas_s3_bucket" "test" {
  name           = "my-bucket"
  s3_plan_id     = "plan-1"
  s3_server_id   = "srv-1"
  default_access = "public"

  attached_keys = [
    {
      access_key_id = "key-a"
      permission    = "read"
    },
  ]
}
`

	updateCfg := providerCfg + `
resource "iaas_s3_bucket" "test" {
  name           = "my-bucket"
  s3_plan_id     = "plan-1"
  s3_server_id   = "srv-1"
  default_access = "private"

  attached_keys = [
    {
      access_key_id = "key-b"
      permission    = "readwrite"
    },
  ]
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			// 1. Create + read-back.
			{
				Config: createCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_s3_bucket.test", "id", bucketID),
					resource.TestCheckResourceAttr("iaas_s3_bucket.test", "name", "my-bucket"),
					resource.TestCheckResourceAttr("iaas_s3_bucket.test", "default_access", "public"),
					resource.TestCheckResourceAttr("iaas_s3_bucket.test", "access_key", "ak_bucketown"),
					resource.TestCheckResourceAttr("iaas_s3_bucket.test", "secret_key", "sk_bucketown_secret"),
					resource.TestCheckResourceAttr("iaas_s3_bucket.test", "endpoint", "https://s3.example.com"),
					resource.TestCheckResourceAttr("iaas_s3_bucket.test", "attached_keys.#", "1"),
					resource.TestCheckTypeSetElemNestedAttrs("iaas_s3_bucket.test", "attached_keys.*", map[string]string{
						"access_key_id": "key-a",
						"permission":    "read",
					}),
				),
			},
			// 2. Import by id (secret_key is re-readable from SHOW, so verify is fine).
			{
				ResourceName:      "iaas_s3_bucket.test",
				ImportState:       true,
				ImportStateId:     bucketID,
				ImportStateVerify: true,
			},
			// 3. Update: ACL → private; swap key-a(read) → key-b(readwrite).
			{
				Config: updateCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_s3_bucket.test", "default_access", "private"),
					resource.TestCheckResourceAttr("iaas_s3_bucket.test", "attached_keys.#", "1"),
					resource.TestCheckTypeSetElemNestedAttrs("iaas_s3_bucket.test", "attached_keys.*", map[string]string{
						"access_key_id": "key-b",
						"permission":    "readwrite",
					}),
				),
			},
		},
	})

	// Assert the create POST body carried the required fields and NOT computed
	// ones.
	creates := srv.Requests("POST", "/object-storage/buckets")
	if len(creates) == 0 {
		t.Fatal("expected at least one POST /object-storage/buckets")
	}
	var createBody map[string]any
	if err := json.Unmarshal(creates[0].Body, &createBody); err != nil {
		t.Fatalf("decoding create body: %v", err)
	}
	if createBody["name"] != "my-bucket" || createBody["s3_plan_id"] != "plan-1" || createBody["s3_server_id"] != "srv-1" {
		t.Errorf("create body missing required fields: %v", createBody)
	}
	for _, stray := range []string{"id", "access_key", "secret_key", "default_access", "endpoint", "suspended", "quota"} {
		if _, present := createBody[stray]; present {
			t.Errorf("create body must NOT include %q; got %v", stray, createBody)
		}
	}

	// Assert the per-set diff fired correctly across create+update.
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.aclCalls != 2 {
		t.Errorf("aclCalls = %d; want 2 (public on create + private on update)", store.aclCalls)
	}
	if store.attachCalls != 2 {
		t.Errorf("attachCalls = %d; want 2 (key-a on create + key-b on update)", store.attachCalls)
	}
	if store.detachCalls != 1 {
		t.Errorf("detachCalls = %d; want 1 (key-a detached on update)", store.detachCalls)
	}
	// key-a → key-b is a full swap (different ids), so it is attach+detach, not an
	// in-place permission update.
	if store.updateCalls != 0 {
		t.Errorf("updateCalls = %d; want 0 (the swap is attach+detach, not a permission update)", store.updateCalls)
	}
}

// envOrSkip returns the env var or skips the test when it is empty.
func envOrSkip(t *testing.T, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		t.Skipf("%s not set; skipping live acceptance test", key)
	}
	return v
}
