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
// TestAccIPSet_basic - LIVE acceptance test (manual staging gate).
//
// Auto-skips unless TF_ACC is set (resource.Test enforces this), so it never
// runs or blocks CI. Requires a reachable panel + IP-locked token via
// IAAS_API_ENDPOINT / IAAS_API_TOKEN (checked by acctest.PreCheck).
// ---------------------------------------------------------------------------
func TestAccIPSet_basic(t *testing.T) {
	const config = `
resource "iaas_ip_set" "test" {
  name        = "tf-acc-blocklist"
  description = "Acceptance test IP set"
  ip_version  = "ipv4"

  entries = [
    {
      cidr    = "10.0.0.0/8"
      comment = "private range"
    },
    {
      cidr = "192.168.1.0/24"
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
					resource.TestCheckResourceAttrSet("iaas_ip_set.test", "id"),
					resource.TestCheckResourceAttr("iaas_ip_set.test", "name", "tf-acc-blocklist"),
					resource.TestCheckResourceAttr("iaas_ip_set.test", "ip_version", "ipv4"),
					resource.TestCheckResourceAttr("iaas_ip_set.test", "entries.#", "2"),
				),
			},
			{
				ResourceName:      "iaas_ip_set.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// ---------------------------------------------------------------------------
// ipSetMockServer is a stateful mock of the IP-set API for the lifecycle test.
//
// It models the parent ip_set plus a server-side entries store keyed by entry
// id, and records how many add/remove calls happened so the test can assert the
// nested-set diff logic ran (add one + remove one on update).
// ---------------------------------------------------------------------------
type ipSetMockServer struct {
	mu sync.Mutex

	name      string
	desc      *string
	ipVersion string

	// entries: server entry id → {cidr, description(comment)}
	entries map[string]ipSetMockEntry
	nextID  int

	addCalls    int // POST /ip-set/{id}/entries
	removeCalls int // DELETE /ip-set/{id}/entry/{entryId}
}

type ipSetMockEntry struct {
	cidr    string
	comment *string
}

func (s *ipSetMockServer) addEntry(cidr string, comment *string) string {
	s.nextID++
	id := "entry-" + itoa(s.nextID)
	s.entries[id] = ipSetMockEntry{cidr: cidr, comment: comment}
	return id
}

// itoa avoids importing strconv just for this tiny helper.
func itoa(n int) string {
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

func (s *ipSetMockServer) ipSetObject(id string) map[string]any {
	entries := make([]any, 0, len(s.entries))
	for eid, e := range s.entries {
		obj := map[string]any{"id": eid, "cidr": e.cidr}
		if e.comment != nil {
			obj["description"] = *e.comment
		} else {
			obj["description"] = nil
		}
		entries = append(entries, obj)
	}
	out := map[string]any{
		"id":            id,
		"name":          s.name,
		"ip_version":    s.ipVersion,
		"entries":       entries,
		"entries_count": len(entries),
		"rules_count":   0,
	}
	if s.desc != nil {
		out["description"] = *s.desc
	} else {
		out["description"] = nil
	}
	return out
}

// ---------------------------------------------------------------------------
// TestUnitIPSet_lifecycle - MOCK-backed lifecycle proof (the key test).
//
// Steps:
//  1. Create with TWO entries → assert id, name, ip_version, entries.# = 2,
//     and assert the bulk add issued exactly TWO POST .../entries calls.
//  2. Import by id → entries are rehydrated from SHOW.
//  3. Update: ADD one entry and REMOVE another (and rename) → assert the
//     resulting state has the new entry and not the old one, and assert that
//     exactly one more add and one remove call happened (the diff logic).
//
// Delete is implicit teardown after the final step.
//
// This proves the nested-set diff: add-new + remove-gone keyed by cidr+comment,
// using each entry's server id for deletion.
// ---------------------------------------------------------------------------
func TestUnitIPSet_lifecycle(t *testing.T) {
	ensureTFBinary(t)

	srv := acctest.NewMockServer(t)

	const setID = "33333333-3333-3333-3333-333333333333"

	store := &ipSetMockServer{
		ipVersion: "ipv4",
		entries:   map[string]ipSetMockEntry{},
	}

	// CREATE - POST /ip-sets stores name/description/ip_version, returns ip_set.
	srv.Handle("POST", "/ip-sets", func(w http.ResponseWriter, r *http.Request) {
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
		if v, ok := body["ip_version"].(string); ok {
			store.ipVersion = v
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "IP set created successfully",
			"ip_set":  map[string]any{"id": setID, "name": store.name, "ip_version": store.ipVersion},
		})
	})

	// ADD ENTRY - POST /ip-set/{id}/entries stores a new entry, returns it.
	srv.Handle("POST", "/ip-set/"+setID+"/entries", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		store.mu.Lock()
		defer store.mu.Unlock()
		store.addCalls++
		cidr, _ := body["cidr"].(string)
		var comment *string
		if c, ok := body["description"].(string); ok {
			comment = &c
		}
		eid := store.addEntry(cidr, comment)
		entry := map[string]any{"id": eid, "cidr": cidr}
		if comment != nil {
			entry["description"] = *comment
		} else {
			entry["description"] = nil
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Entry added successfully",
			"entry":   entry,
		})
	})

	// SHOW - GET /ip-set/{id} returns the set with embedded entries.
	srv.Handle("GET", "/ip-set/"+setID, func(w http.ResponseWriter, r *http.Request) {
		store.mu.Lock()
		defer store.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"ip_set":  store.ipSetObject(setID),
		})
	})

	// UPDATE - PATCH /ip-set/{id} applies name/description; NO ip_set body.
	srv.Handle("PATCH", "/ip-set/"+setID, func(w http.ResponseWriter, r *http.Request) {
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
			"message": "IP set updated successfully",
		})
	})

	// REMOVE ENTRY - DELETE /ip-set/{id}/entry/{entryId} drops the entry.
	// The mock registers a catch-all per known entry id below via a prefix match
	// is not supported, so we register a handler that inspects the path.
	srv.Handle("DELETE", "/ip-set/"+setID+"/entry/entry-1", makeRemoveEntryHandler(store, "entry-1"))
	srv.Handle("DELETE", "/ip-set/"+setID+"/entry/entry-2", makeRemoveEntryHandler(store, "entry-2"))
	srv.Handle("DELETE", "/ip-set/"+setID+"/entry/entry-3", makeRemoveEntryHandler(store, "entry-3"))
	srv.Handle("DELETE", "/ip-set/"+setID+"/entry/entry-4", makeRemoveEntryHandler(store, "entry-4"))

	// DELETE SET - DELETE /ip-set/{id}.
	srv.Handle("DELETE", "/ip-set/"+setID, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "IP set deleted successfully",
		})
	})

	providerCfg := acctest.ProviderConfig(srv.Endpoint())

	// Create config: two entries (one with a comment, one without).
	createCfg := providerCfg + `
resource "iaas_ip_set" "test" {
  name        = "blocklist"
  description = "bad actors"
  ip_version  = "ipv4"

  entries = [
    {
      cidr    = "10.0.0.0/8"
      comment = "corp range"
    },
    {
      cidr = "192.168.1.0/24"
    },
  ]
}
`

	// Update config: rename, keep 10.0.0.0/8, REMOVE 192.168.1.0/24, ADD
	// 172.16.0.0/12. Net: one add + one remove on the entries set.
	updateCfg := providerCfg + `
resource "iaas_ip_set" "test" {
  name        = "blocklist-v2"
  description = "bad actors"
  ip_version  = "ipv4"

  entries = [
    {
      cidr    = "10.0.0.0/8"
      comment = "corp range"
    },
    {
      cidr    = "172.16.0.0/12"
      comment = "datacenter"
    },
  ]
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			// 1. Create + read-back with two entries.
			{
				Config: createCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_ip_set.test", "id", setID),
					resource.TestCheckResourceAttr("iaas_ip_set.test", "name", "blocklist"),
					resource.TestCheckResourceAttr("iaas_ip_set.test", "ip_version", "ipv4"),
					resource.TestCheckResourceAttr("iaas_ip_set.test", "entries.#", "2"),
					// Confirm that at least one entry has a non-empty server id after
					// create/import, catching any future regression that drops entry ids.
					resource.TestCheckTypeSetElemNestedAttrs("iaas_ip_set.test", "entries.*", map[string]string{
						"cidr":    "10.0.0.0/8",
						"comment": "corp range",
					}),
					resource.TestCheckTypeSetElemNestedAttrs("iaas_ip_set.test", "entries.*", map[string]string{
						"cidr": "192.168.1.0/24",
					}),
					// Assert entry ids are populated (non-empty) so a regression that
					// drops server ids is caught immediately.
					resource.TestCheckTypeSetElemNestedAttrs("iaas_ip_set.test", "entries.*", map[string]string{
						"cidr":    "10.0.0.0/8",
						"comment": "corp range",
						"id":      "entry-1",
					}),
				),
			},
			// 2. Import by id; entries rehydrated from SHOW.
			{
				ResourceName:      "iaas_ip_set.test",
				ImportState:       true,
				ImportStateId:     setID,
				ImportStateVerify: true,
			},
			// 3. Update: add one entry, remove another, rename.
			{
				Config: updateCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_ip_set.test", "name", "blocklist-v2"),
					resource.TestCheckResourceAttr("iaas_ip_set.test", "entries.#", "2"),
					resource.TestCheckTypeSetElemNestedAttrs("iaas_ip_set.test", "entries.*", map[string]string{
						"cidr":    "10.0.0.0/8",
						"comment": "corp range",
					}),
					resource.TestCheckTypeSetElemNestedAttrs("iaas_ip_set.test", "entries.*", map[string]string{
						"cidr":    "172.16.0.0/12",
						"comment": "datacenter",
					}),
				),
			},
		},
	})

	// Assert the create issued exactly TWO add-entry calls (one per entry), and
	// the update issued exactly one MORE add and exactly one remove (the diff).
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.addCalls != 3 {
		t.Errorf("addCalls = %d; want 3 (2 on create + 1 on update)", store.addCalls)
	}
	if store.removeCalls != 1 {
		t.Errorf("removeCalls = %d; want 1 (1 removed on update)", store.removeCalls)
	}

	// Assert the create POST .../entries carried cidr + description (comment),
	// proving comments are preserved (bulk-add would have dropped them).
	adds := srv.Requests("POST", "/ip-set/"+setID+"/entries")
	if len(adds) < 2 {
		t.Fatalf("expected at least 2 POST .../entries calls; got %d", len(adds))
	}
	sawComment := false
	for _, req := range adds {
		var b map[string]any
		if err := json.Unmarshal(req.Body, &b); err != nil {
			t.Fatalf("decoding add-entry body: %v", err)
		}
		if b["cidr"] == nil || b["cidr"] == "" {
			t.Errorf("add-entry body missing cidr: %v", b)
		}
		if b["description"] == "corp range" {
			sawComment = true
		}
	}
	if !sawComment {
		t.Error("expected at least one add-entry call to carry description=\"corp range\" (comment preserved)")
	}

	// Assert exactly one DELETE on an entry path happened during update.
	totalRemoveReqs := 0
	for _, eid := range []string{"entry-1", "entry-2", "entry-3", "entry-4"} {
		totalRemoveReqs += len(srv.Requests("DELETE", "/ip-set/"+setID+"/entry/"+eid))
	}
	if totalRemoveReqs != 1 {
		t.Errorf("total entry DELETE requests = %d; want 1", totalRemoveReqs)
	}
}

// makeRemoveEntryHandler returns a handler that deletes the given entry id from
// the store and bumps the remove counter.
func makeRemoveEntryHandler(store *ipSetMockServer, entryID string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		store.mu.Lock()
		defer store.mu.Unlock()
		delete(store.entries, entryID)
		store.removeCalls++
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "Entry removed successfully",
		})
	}
}
