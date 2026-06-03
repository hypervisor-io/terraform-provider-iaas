package resources_test

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/iaas/terraform-provider-iaas/internal/acctest"
)

// ---------------------------------------------------------------------------
// TestAccS3AccessKey_basic — LIVE acceptance test (manual staging gate).
//
// Auto-skips unless TF_ACC is set. secret_key is shown only once, so it is added
// to ImportStateVerifyIgnore (an imported key cannot recover it).
// ---------------------------------------------------------------------------
func TestAccS3AccessKey_basic(t *testing.T) {
	const config = `
resource "iaas_s3_access_key" "test" {
  name = "tf-acc-key-001"
}
`
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("iaas_s3_access_key.test", "id"),
					resource.TestCheckResourceAttrSet("iaas_s3_access_key.test", "access_key"),
					resource.TestCheckResourceAttrSet("iaas_s3_access_key.test", "secret_key"),
				),
			},
			{
				ResourceName:            "iaas_s3_access_key.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"secret_key"},
			},
		},
	})
}

// ---------------------------------------------------------------------------
// s3KeyMock is a stateful mock of the access-key API. It models a single key
// record (id/name/access_key/active) and tracks the create + update counts. The
// secret_key is returned ONLY by the create handler (the shown-once contract);
// the listing never includes it.
// ---------------------------------------------------------------------------
type s3KeyMock struct {
	mu sync.Mutex

	id          string
	name        string
	accessKey   string
	secretKey   string
	active      int
	createCalls int
	updateCalls int
}

func (s *s3KeyMock) listPaginator() map[string]any {
	// NOTE: NO secret_key here — it is $hidden in the real model.
	return map[string]any{
		"current_page": 1,
		"last_page":    1,
		"data": []any{
			map[string]any{
				"id":         s.id,
				"name":       s.name,
				"access_key": s.accessKey,
				"active":     s.active,
			},
		},
	}
}

// ---------------------------------------------------------------------------
// TestUnitS3AccessKey_lifecycle — MOCK-backed lifecycle proof (the key test).
//
// Steps:
//  1. Create → asserts the secret is CAPTURED into state from the create
//     response (secret_key = "sk_shown_once"), the public access_key + id are
//     hydrated via the by-access-key readback, and the create POST carried only
//     {name}.
//  2. Import by id → asserts the secret is IGNORED on import (it cannot be
//     recovered from the listing) while everything else round-trips.
//  3. Update: rename + deactivate → asserts the PATCH fired with name+active and
//     the captured secret is PRESERVED through the read-back (no churn), proving
//     Read does not overwrite the shown-once secret.
//
// ---------------------------------------------------------------------------
func TestUnitS3AccessKey_lifecycle(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)

	const keyID = "22222222-2222-2222-2222-222222222222"

	store := &s3KeyMock{
		id:        keyID,
		accessKey: "ak_shown",
		secretKey: "sk_shown_once",
		active:    1,
	}

	// CREATE — POST /object-storage/access-keys returns data:{access_key,secret_key}
	// with NO id (forcing the by-access-key readback) and the shown-once secret.
	srv.Handle("POST", "/object-storage/access-keys", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		store.mu.Lock()
		defer store.mu.Unlock()
		store.createCalls++
		store.name, _ = body["name"].(string)
		store.active = 1
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Access key created successfully.",
			"data": map[string]any{
				"access_key": store.accessKey,
				"secret_key": store.secretKey,
			},
		})
	})

	// LIST — GET /object-storage/access-keys (used for both readbacks + Read).
	// The secret_key is NEVER present here.
	srv.Handle("GET", "/object-storage/access-keys", func(w http.ResponseWriter, r *http.Request) {
		store.mu.Lock()
		defer store.mu.Unlock()
		writeJSON(w, http.StatusOK, store.listPaginator())
	})

	// UPDATE — PATCH /object-storage/access-key/{id}: name and/or active.
	srv.Handle("PATCH", "/object-storage/access-key/"+keyID, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		store.mu.Lock()
		defer store.mu.Unlock()
		store.updateCalls++
		if n, ok := body["name"].(string); ok {
			store.name = n
		}
		if a, ok := body["active"].(bool); ok {
			if a {
				store.active = 1
			} else {
				store.active = 0
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "updated"})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	createCfg := providerCfg + `
resource "iaas_s3_access_key" "test" {
  name = "my-key"
}
`

	updateCfg := providerCfg + `
resource "iaas_s3_access_key" "test" {
  name   = "my-key-renamed"
  active = false
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			// 1. Create — secret captured into state.
			{
				Config: createCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_s3_access_key.test", "id", keyID),
					resource.TestCheckResourceAttr("iaas_s3_access_key.test", "name", "my-key"),
					resource.TestCheckResourceAttr("iaas_s3_access_key.test", "access_key", "ak_shown"),
					// THE central assertion: the shown-once secret is in state.
					resource.TestCheckResourceAttr("iaas_s3_access_key.test", "secret_key", "sk_shown_once"),
					resource.TestCheckResourceAttr("iaas_s3_access_key.test", "active", "true"),
				),
			},
			// 2. Import by id — secret_key cannot be recovered, so ignore it.
			{
				ResourceName:            "iaas_s3_access_key.test",
				ImportState:             true,
				ImportStateId:           keyID,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"secret_key"},
			},
			// 3. Update: rename + deactivate; the secret is PRESERVED (no churn).
			{
				Config: updateCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_s3_access_key.test", "name", "my-key-renamed"),
					resource.TestCheckResourceAttr("iaas_s3_access_key.test", "active", "false"),
					// Still populated after update + read-back (Read must NOT overwrite it).
					resource.TestCheckResourceAttr("iaas_s3_access_key.test", "secret_key", "sk_shown_once"),
				),
			},
		},
	})

	// Assert the create POST carried only {name} (no stray computed fields).
	creates := srv.Requests("POST", "/object-storage/access-keys")
	if len(creates) == 0 {
		t.Fatal("expected at least one POST /object-storage/access-keys")
	}
	var createBody map[string]any
	if err := json.Unmarshal(creates[0].Body, &createBody); err != nil {
		t.Fatalf("decoding create body: %v", err)
	}
	if createBody["name"] != "my-key" {
		t.Errorf("create body name = %v; want my-key", createBody["name"])
	}
	for _, stray := range []string{"id", "access_key", "secret_key", "active"} {
		if _, present := createBody[stray]; present {
			t.Errorf("create body must NOT include %q; got %v", stray, createBody)
		}
	}

	// Assert the update PATCH carried name + active=false.
	updates := srv.Requests("PATCH", "/object-storage/access-key/"+keyID)
	if len(updates) == 0 {
		t.Fatal("expected at least one PATCH /object-storage/access-key/{id}")
	}
	var lastUpdate map[string]any
	if err := json.Unmarshal(updates[len(updates)-1].Body, &lastUpdate); err != nil {
		t.Fatalf("decoding update body: %v", err)
	}
	if lastUpdate["name"] != "my-key-renamed" {
		t.Errorf("update body name = %v; want my-key-renamed", lastUpdate["name"])
	}
	if lastUpdate["active"] != false {
		t.Errorf("update body active = %v; want false", lastUpdate["active"])
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if store.createCalls != 1 {
		t.Errorf("createCalls = %d; want 1", store.createCalls)
	}
}
