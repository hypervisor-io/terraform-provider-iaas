package resources_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/hypervisor-io/terraform-provider-iaas/internal/acctest"
)

const (
	ipsecGwID   = "22222222-2222-2222-2222-222222222222"
	ipsecConnID = "88888888-8888-8888-8888-888888888888"
	ipsecPSK    = "correct-horse-battery-staple"
	ipsecPSK2   = "another-Secret-Key-2026!"
)

// ipsecFake is a stateful fake of the user API's IPsec routes. Writes bump the
// gateway generation; the node "confirms" a generation after a few polls, so the
// apply waits really loop. The PSK is kept only to bump psk_version and is never
// serialised into any response.
type ipsecFake struct {
	mu sync.Mutex

	exists     bool
	deleted    bool
	conn       map[string]any
	pskVersion int

	generation int
	applied    int
	pollsLeft  int
	failApply  bool

	installingLeft int    // answer 409 ipsec_installing this many times before the create
	create500      bool   // record the connection but answer 500 (uncertain outcome)
	delete500      bool   // record the delete but answer 500 once (the client replays and meets 404)
	gwState        string // gateway ipsec_state; empty means ready
	deleteCalls    int
	tooManyLeft    int // answer 429 this many times before the create

	postBodies  []map[string]any
	patchBodies []map[string]any
	postCount   int
	listCalls   int
}

func newIpsecFake() *ipsecFake { return &ipsecFake{generation: 0, applied: 0} }

func (f *ipsecFake) connectionView() map[string]any {
	out := map[string]any{}
	for k, v := range f.conn {
		out[k] = v
	}
	out["id"] = ipsecConnID
	out["conn_name"] = "vc-888888888888"
	out["vpn_gateway_id"] = ipsecGwID
	out["psk_version"] = float64(f.pskVersion)
	out["sa_state"] = "unknown"
	out["last_error_code"] = nil
	out["last_error"] = nil
	out["apply_state"] = f.applyState()
	return out
}

func (f *ipsecFake) applyState() string {
	if f.failApply {
		return "failed"
	}
	if f.applied < f.generation {
		return "pending"
	}
	return "applied"
}

// tick is called on each read: the node confirms after a few polls.
func (f *ipsecFake) tick() {
	if f.applied < f.generation && !f.failApply {
		f.pollsLeft--
		if f.pollsLeft <= 0 {
			f.applied = f.generation
		}
	}
}

func (f *ipsecFake) write() {
	f.generation++
	f.pollsLeft = 2
}

func (f *ipsecFake) gatewayBlock() map[string]any {
	state := "idle"
	if f.failApply {
		state = "failed"
	} else if f.applied < f.generation {
		state = "pending"
	}
	return map[string]any{
		"ipsec_state":              map[bool]string{true: f.gwState, false: "ready"}[f.gwState != ""],
		"ipsec_version":            "6.0.1",
		"ipsec_apply_state":        state,
		"ipsec_apply_error":        map[bool]any{true: "load_failed", false: nil}[f.failApply],
		"ipsec_generation":         float64(f.generation),
		"ipsec_applied_generation": float64(f.applied),
	}
}

func ipsecNotFound(w http.ResponseWriter) {
	writeJSON(w, http.StatusNotFound, map[string]any{"success": false, "reason": "not_found", "message": "IPsec connection not found."})
}

func (f *ipsecFake) register(srv *acctest.MockServer) {
	base := "/vpn-gateway/" + ipsecGwID + "/ipsec"
	item := base + "/" + ipsecConnID

	srv.Handle("GET", base, func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.listCalls++
		f.tick()
		conns := []any{}
		if f.exists && !f.deleted {
			conns = append(conns, f.connectionView())
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "connections": conns, "gateway": f.gatewayBlock()})
	})

	srv.Handle("POST", base, func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.postCount++
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.postBodies = append(f.postBodies, body)
		if f.tooManyLeft > 0 {
			f.tooManyLeft--
			w.Header().Set("Retry-After", "0")
			writeJSON(w, http.StatusTooManyRequests, map[string]any{"success": false, "message": "Too many requests"})
			return
		}
		if f.installingLeft > 0 {
			f.installingLeft--
			writeJSON(w, http.StatusConflict, map[string]any{"success": false, "reason": "ipsec_installing", "message": "IPsec is still being set up on this gateway; try again in a minute."})
			return
		}
		conn := map[string]any{
			"name": body["name"], "remote_address": body["remote_address"], "local_id_type": "address", "local_id": nil,
			"remote_id_type": "address", "remote_id": nil, "ike_version": float64(2), "proposal_preset": "modern",
			"ike_proposal": nil, "esp_proposal": nil, "local_ts": []any{"10.60.0.0/16"}, "remote_ts": body["remote_ts"],
			"role": "initiator", "dpd_delay": float64(30), "dpd_action": "restart", "ike_lifetime": float64(14400),
			"child_lifetime": float64(3600), "udp_encapsulation": true, "enabled": true,
		}
		for _, k := range []string{"ike_version", "proposal_preset", "role", "dpd_delay", "dpd_action", "ike_lifetime", "child_lifetime", "udp_encapsulation", "enabled", "local_ts"} {
			if v, ok := body[k]; ok {
				conn[k] = v
			}
		}
		f.conn, f.exists, f.deleted, f.pskVersion = conn, true, false, 1
		f.write()
		if f.create500 {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "boom"})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"success": true, "message": "IPsec connection created.", "connection": f.connectionView(), "warnings": []any{}})
	})

	srv.Handle("GET", item, func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if !f.exists || f.deleted {
			ipsecNotFound(w)
			return
		}
		f.tick()
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "connection": f.connectionView()})
	})

	srv.Handle("PATCH", item, func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if !f.exists || f.deleted {
			ipsecNotFound(w)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.patchBodies = append(f.patchBodies, body)
		for k, v := range body {
			if k == "psk" {
				f.pskVersion++
				continue
			}
			f.conn[k] = v
		}
		f.write()
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "IPsec connection updated.", "connection": f.connectionView(), "warnings": []any{}})
	})

	srv.Handle("DELETE", item, func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.deleteCalls++
		if !f.exists || f.deleted {
			ipsecNotFound(w)
			return
		}
		f.deleted = true
		f.write()
		if f.delete500 {
			f.delete500 = false
			writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "boom"})
			return
		}
		view := f.connectionView()
		view["apply_state"] = "pending"
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "IPsec connection deleted.", "connection": view})
	})

	srv.Handle("GET", item+"/remote-settings", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if !f.exists || f.deleted {
			ipsecNotFound(w)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "remote_settings": map[string]any{
			"our_public_ip": "203.0.113.10", "ike_version": float64(2), "mode": "tunnel", "auth": "psk",
			"remote_networks": f.conn["remote_ts"], "nat_traversal": "forced", "vendor_notes": []any{},
		}})
	})
}

func ipsecConfig(endpoint, name, psk, extra string) string {
	return acctest.ProviderConfig(endpoint) + fmt.Sprintf(`
resource "iaas_vpn_ipsec_connection" "test" {
  vpn_gateway_id = %q
  name           = %q
  remote_address = "198.51.100.7"
  psk            = %q
  remote_ts      = ["192.168.50.0/24"]
%s}
`, ipsecGwID, name, psk, extra)
}

// TestUnitVpnIpsecConnection_lifecycle drives create (through two 409
// ipsec_installing refusals and a pending-then-applied wait), import, an update
// that does not touch the key, a key rotation, an out-of-band key rotation
// (drift), and a delete that waits for the gateway generation to be confirmed.
func TestUnitVpnIpsecConnection_lifecycle(t *testing.T) {
	ensureTFBinary(t)
	t.Setenv("IAAS_INSTANCE_POLL_INTERVAL", "1ms")
	srv := acctest.NewMockServer(t)
	f := newIpsecFake()
	f.installingLeft = 2
	f.register(srv)

	const addr = "iaas_vpn_ipsec_connection.test"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: ipsecConfig(srv.Endpoint(), "office-hq", ipsecPSK, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", ipsecConnID),
					resource.TestCheckResourceAttr(addr, "conn_name", "vc-888888888888"),
					resource.TestCheckResourceAttr(addr, "apply_state", "applied"),
					resource.TestCheckResourceAttr(addr, "sa_state", "unknown"),
					resource.TestCheckResourceAttr(addr, "ike_version", "2"),
					resource.TestCheckResourceAttr(addr, "role", "initiator"),
					resource.TestCheckResourceAttr(addr, "dpd_action", "restart"),
					resource.TestCheckResourceAttr(addr, "udp_encapsulation", "true"),
					resource.TestCheckResourceAttr(addr, "psk", ipsecPSK),
					resource.TestCheckResourceAttr(addr, "local_ts.#", "1"),
					resource.TestCheckTypeSetElemAttr(addr, "local_ts.*", "10.60.0.0/16"),
					resource.TestMatchResourceAttr(addr, "remote_settings", regexp.MustCompile(`"our_public_ip":"203\.0\.113\.10"`)),
				),
			},
			// Import by composite id; the key cannot be recovered.
			{
				ResourceName:            addr,
				ImportState:             true,
				ImportStateId:           ipsecGwID + "/" + ipsecConnID,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"psk", "timeouts"},
			},
			// Rename + change the lifetimes: a partial PATCH without the key.
			{
				Config: ipsecConfig(srv.Endpoint(), "office-main", ipsecPSK, "  ike_lifetime   = 7200\n"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "name", "office-main"),
					resource.TestCheckResourceAttr(addr, "ike_lifetime", "7200"),
					resource.TestCheckResourceAttr(addr, "apply_state", "applied"),
				),
			},
			// Key rotation: the key is sent, and updates in place (same id).
			{
				Config: ipsecConfig(srv.Endpoint(), "office-main", ipsecPSK2, "  ike_lifetime   = 7200\n"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", ipsecConnID),
					resource.TestCheckResourceAttr(addr, "psk", ipsecPSK2),
				),
			},
			// Rotation outside Terraform: the next apply sets the configured key again.
			{
				PreConfig: func() {
					f.mu.Lock()
					f.pskVersion++
					f.mu.Unlock()
				},
				Config: ipsecConfig(srv.Endpoint(), "office-main", ipsecPSK2, "  ike_lifetime   = 7200\n"),
				Check:  resource.TestCheckResourceAttr(addr, "psk", ipsecPSK2),
			},
		},
	})

	f.mu.Lock()
	defer f.mu.Unlock()
	// 2 refusals + 1 recorded create: the refusals are replayed, the create never is.
	if f.postCount != 3 {
		t.Errorf("POST count = %d; want 3 (two ipsec_installing refusals, then one create)", f.postCount)
	}
	last := f.postBodies[len(f.postBodies)-1]
	if last["psk"] != ipsecPSK || last["name"] != "office-hq" || last["remote_address"] != "198.51.100.7" {
		t.Errorf("create body = %v", last)
	}
	if rts, _ := last["remote_ts"].([]any); len(rts) != 1 || rts[0] != "192.168.50.0/24" {
		t.Errorf("create body remote_ts = %v", last["remote_ts"])
	}
	for _, stray := range []string{"id", "conn_name", "sa_state", "apply_state", "remote_settings", "timeouts"} {
		if _, present := last[stray]; present {
			t.Errorf("create body must not include %q", stray)
		}
	}
	if len(f.patchBodies) != 3 {
		t.Fatalf("PATCH count = %d; want 3 (rename+lifetime, rotation, drift reset): %v", len(f.patchBodies), f.patchBodies)
	}
	if _, has := f.patchBodies[0]["psk"]; has {
		t.Errorf("the rename PATCH must not carry the key: %v", f.patchBodies[0])
	}
	if f.patchBodies[0]["name"] != "office-main" || f.patchBodies[0]["ike_lifetime"] != float64(7200) {
		t.Errorf("rename PATCH = %v", f.patchBodies[0])
	}
	if len(f.patchBodies[0]) != 2 {
		t.Errorf("the rename PATCH must carry only the changed fields: %v", f.patchBodies[0])
	}
	if f.patchBodies[1]["psk"] != ipsecPSK2 || len(f.patchBodies[1]) != 1 {
		t.Errorf("rotation PATCH = %v; want only the new key", f.patchBodies[1])
	}
	if f.patchBodies[2]["psk"] != ipsecPSK2 || len(f.patchBodies[2]) != 1 {
		t.Errorf("drift-reset PATCH = %v; want only the configured key", f.patchBodies[2])
	}
	// Destroy ran at the end of the test case: the generation was confirmed.
	if !f.deleted || f.applied != f.generation {
		t.Errorf("after destroy deleted=%v applied=%d generation=%d; want deleted and confirmed", f.deleted, f.applied, f.generation)
	}
}

// TestUnitVpnIpsecConnection_uncertainCreateFailsClosed: a 500 after the connection
// was recorded is NOT adopted (the API has no request token, so a same-name
// connection may belong to another writer); the error names the connection and the
// exact import command, the create is never replayed and no state is kept.
func TestUnitVpnIpsecConnection_uncertainCreateFailsClosed(t *testing.T) {
	ensureTFBinary(t)
	t.Setenv("IAAS_INSTANCE_POLL_INTERVAL", "1ms")
	srv := acctest.NewMockServer(t)
	f := newIpsecFake()
	f.create500 = true
	f.register(srv)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{{
			Config:      ipsecConfig(srv.Endpoint(), "office-hq", ipsecPSK, ""),
			ExpectError: regexp.MustCompile(`(?s)outcome unknown.*office-hq.*` + ipsecConnID + `.*terraform\s+import\s+<resource\s+address>\s+` + ipsecGwID + `/` + ipsecConnID),
		}},
	})
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.postCount != 1 {
		t.Errorf("POST count = %d; want exactly 1 (an uncertain create is never replayed)", f.postCount)
	}
}

// TestUnitVpnIpsecConnection_replayedDeleteStillWaitsForTheGateway: the client
// retries a 500 DELETE and meets 404; the delete must still wait for the gateway.
func TestUnitVpnIpsecConnection_replayedDeleteStillWaitsForTheGateway(t *testing.T) {
	ensureTFBinary(t)
	t.Setenv("IAAS_INSTANCE_POLL_INTERVAL", "1ms")
	srv := acctest.NewMockServer(t)
	f := newIpsecFake()
	f.register(srv)
	cfg := ipsecConfig(srv.Endpoint(), "office-hq", ipsecPSK, "")
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{Config: cfg},
			{PreConfig: func() { f.mu.Lock(); f.delete500 = true; f.mu.Unlock() }, Config: acctest.ProviderConfig(srv.Endpoint())},
		},
	})
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deleteCalls < 2 {
		t.Fatalf("DELETE calls = %d; the test needs the 500 replay", f.deleteCalls)
	}
	if f.applied != f.generation {
		t.Errorf("destroy returned before the gateway confirmed the removal (applied %d, generation %d)", f.applied, f.generation)
	}
}

// TestUnitVpnIpsecConnection_omittedLifetimesAreNotValidatedAgainstDefaults:
// removing child_lifetime while lowering ike_lifetime keeps the stored child value
// and must plan and apply (the server is the authority for that combination).
func TestUnitVpnIpsecConnection_omittedLifetimesAreNotValidatedAgainstDefaults(t *testing.T) {
	ensureTFBinary(t)
	t.Setenv("IAAS_INSTANCE_POLL_INTERVAL", "1ms")
	srv := acctest.NewMockServer(t)
	f := newIpsecFake()
	f.register(srv)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{Config: ipsecConfig(srv.Endpoint(), "office-hq", ipsecPSK, "  ike_lifetime   = 7200\n  child_lifetime = 1800\n")},
			{Config: ipsecConfig(srv.Endpoint(), "office-hq", ipsecPSK, "  ike_lifetime   = 2400\n"),
				Check: resource.TestCheckResourceAttr("iaas_vpn_ipsec_connection.test", "ike_lifetime", "2400")},
		},
	})
}

// TestUnitVpnIpsecConnection_installFailureFailsFast: the gateway's IPsec install
// fails while the change is still pending; the apply wait must not run to timeout.
func TestUnitVpnIpsecConnection_installFailureFailsFast(t *testing.T) {
	ensureTFBinary(t)
	t.Setenv("IAAS_INSTANCE_POLL_INTERVAL", "1ms")
	srv := acctest.NewMockServer(t)
	f := newIpsecFake()
	f.gwState = "failed"
	f.register(srv)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{{
			Config:      ipsecConfig(srv.Endpoint(), "office-hq", ipsecPSK, "  timeouts {\n    create = \"30s\"\n  }\n"),
			ExpectError: regexp.MustCompile(`installing IPsec on the gateway failed`),
		}},
	})
}

// TestUnitVpnIpsecConnection_rateLimitIsRetriedNotUnknown: a 429 recorded nothing,
// so the create is retried after the wait and is never reported as unknown.
func TestUnitVpnIpsecConnection_rateLimitIsRetriedNotUnknown(t *testing.T) {
	ensureTFBinary(t)
	t.Setenv("IAAS_INSTANCE_POLL_INTERVAL", "1ms")
	srv := acctest.NewMockServer(t)
	f := newIpsecFake()
	f.tooManyLeft = 2
	f.register(srv)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{{
			Config: ipsecConfig(srv.Endpoint(), "office-hq", ipsecPSK, ""),
			Check:  resource.TestCheckResourceAttr("iaas_vpn_ipsec_connection.test", "id", ipsecConnID),
		}},
	})
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.postCount != 3 {
		t.Errorf("POST count = %d; want 3 (two 429s, then the recorded create)", f.postCount)
	}
}

// TestUnitVpnIpsecConnection_updatePlanKeepsDpdActionKnown: an unrelated update
// must not show dpd_action as "known after apply".
func TestUnitVpnIpsecConnection_updatePlanKeepsDpdActionKnown(t *testing.T) {
	ensureTFBinary(t)
	t.Setenv("IAAS_INSTANCE_POLL_INTERVAL", "1ms")
	srv := acctest.NewMockServer(t)
	f := newIpsecFake()
	f.register(srv)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{Config: ipsecConfig(srv.Endpoint(), "office-hq", ipsecPSK, "")},
			{
				Config: ipsecConfig(srv.Endpoint(), "office-main", ipsecPSK, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectKnownValue("iaas_vpn_ipsec_connection.test", tfjsonpath.New("dpd_action"), knownvalue.StringExact("restart")),
				}},
			},
		},
	})
}

// TestUnitVpnIpsecConnection_failedApplyIsAnError: the gateway reports a failed
// apply; the apply fails with the gateway's code.
func TestUnitVpnIpsecConnection_failedApplyIsAnError(t *testing.T) {
	ensureTFBinary(t)
	t.Setenv("IAAS_INSTANCE_POLL_INTERVAL", "1ms")
	srv := acctest.NewMockServer(t)
	f := newIpsecFake()
	f.failApply = true
	f.register(srv)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{{
			Config:      ipsecConfig(srv.Endpoint(), "office-hq", ipsecPSK, ""),
			ExpectError: regexp.MustCompile(`failed to apply the change\s+\(load_failed\)`),
		}},
	})
}

// TestUnitVpnIpsecConnection_refusalIsDefinite: a 422 reason is shown and no
// state is kept.
func TestUnitVpnIpsecConnection_refusalIsDefinite(t *testing.T) {
	ensureTFBinary(t)
	srv := acctest.NewMockServer(t)
	srv.Handle("GET", "/vpn-gateway/"+ipsecGwID+"/ipsec", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "connections": []any{}, "gateway": map[string]any{}})
	})
	srv.Handle("POST", "/vpn-gateway/"+ipsecGwID+"/ipsec", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"success": false, "reason": "destination_overlap", "message": "The remote network overlaps another destination.",
			"data": map[string]any{"kind": "vpc", "cidr": "10.60.0.0/16"},
		})
	})
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{{
			Config:      ipsecConfig(srv.Endpoint(), "office-hq", ipsecPSK, ""),
			ExpectError: regexp.MustCompile(`destination_overlap`),
		}},
	})
}

// TestUnitVpnIpsecConnection_planTimeValidation: server grammars fail before any
// request, and the key never appears in the diagnostics.
func TestUnitVpnIpsecConnection_planTimeValidation(t *testing.T) {
	ensureTFBinary(t)
	srv := acctest.NewMockServer(t)
	cases := []struct {
		name  string
		cfg   string
		match string
	}{
		{"short key", ipsecConfig(srv.Endpoint(), "office-hq", "short", ""), `Invalid Pre-Shared Key`},
		{"key with a quote", ipsecConfig(srv.Endpoint(), "office-hq", `abcdefghijklmnop"q`, ""), `Invalid Pre-Shared Key`},
		{"bad name", ipsecConfig(srv.Endpoint(), "Office_HQ", ipsecPSK, ""), `lower-case letters`},
		{"ikev1 needs compat", ipsecConfig(srv.Endpoint(), "office-hq", ipsecPSK, "  ike_version = 1\n  proposal_preset = \"modern\"\n"), `IKEv1 Needs The Compat Preset`},
		{"custom needs proposals", ipsecConfig(srv.Endpoint(), "office-hq", ipsecPSK, "  proposal_preset = \"custom\"\n"), `Custom Preset Needs Proposals`},
		{"proposal needs custom", ipsecConfig(srv.Endpoint(), "office-hq", ipsecPSK, "  proposal_preset = \"modern\"\n  ike_proposal = \"aes256gcm16-prfsha384-ecp384\"\n"), `Proposal Needs The Custom Preset`},
		{"child above ike", ipsecConfig(srv.Endpoint(), "office-hq", ipsecPSK, "  ike_lifetime = 14400\n  child_lifetime = 20000\n"), `must be between|Child Lifetime Too Long`},
		{"default route as remote", strings.Replace(ipsecConfig(srv.Endpoint(), "office-hq", ipsecPSK, ""), "192.168.50.0/24", "0.0.0.0/0", 1), `Invalid IPv4 CIDR Prefix`},
		{"any identity needs responder", ipsecConfig(srv.Endpoint(), "office-hq", ipsecPSK, "  role = \"initiator\"\n  remote_id_type = \"any\"\n"), `Any Identity Is For A Responder`},
		{"empty optional string", ipsecConfig(srv.Endpoint(), "office-hq", ipsecPSK, "  local_id = \"\"\n"), `Empty Or Padded Value`},
		{"padded optional string", ipsecConfig(srv.Endpoint(), "office-hq", ipsecPSK, "  remote_id = \" x\"\n"), `Empty Or Padded Value`},
		{"non canonical remote", strings.Replace(ipsecConfig(srv.Endpoint(), "office-hq", ipsecPSK, ""), "192.168.50.0/24", "192.168.50.7/24", 1), `canonical form`},
		{"reserved remote", strings.Replace(ipsecConfig(srv.Endpoint(), "office-hq", ipsecPSK, ""), "192.168.50.0/24", "127.0.0.0/24", 1), `reserved range`},
		{"overlapping remote list", strings.Replace(ipsecConfig(srv.Endpoint(), "office-hq", ipsecPSK, ""), `["192.168.50.0/24"]`, `["192.168.50.0/24", "192.168.0.0/16"]`, 1), `overlap`},
		{"local overlaps remote", ipsecConfig(srv.Endpoint(), "office-hq", ipsecPSK, "  local_ts = [\"192.168.0.0/16\"]\n"), `Local And Remote Networks Overlap`},
		{"control character", ipsecConfig(srv.Endpoint(), "office-hq", ipsecPSK, "  local_id = \"a\\nb\"\n"), `Invalid Control Character`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: acctest.Factories,
				Steps: []resource.TestStep{{
					Config:      tc.cfg,
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(tc.match),
				}},
			})
		})
	}
	if n := len(srv.AllRequests()); n != 0 {
		t.Errorf("plan-time validation made %d requests; want 0", n)
	}
}

// TestAccVpnIpsecConnection_basic - LIVE acceptance test (manual staging gate).
//
// Auto-skips unless TF_ACC is set. Requires an EXISTING active VPN gateway whose
// node supports IPsec:
//
//	IAAS_TEST_VPN_GATEWAY_ID - UUID of an active iaas_vpn_gateway
//
// Skips cleanly when absent. The connection created here points at a TEST-NET-2
// address, so the tunnel stays "connecting"; the test waits for the gateway to
// apply it, not for the tunnel.
func TestAccVpnIpsecConnection_basic(t *testing.T) {
	gwID := os.Getenv("IAAS_TEST_VPN_GATEWAY_ID")
	if gwID == "" {
		t.Skip("TestAccVpnIpsecConnection_basic: set IAAS_TEST_VPN_GATEWAY_ID to run this acceptance test")
	}
	config := fmt.Sprintf(`
resource "iaas_vpn_ipsec_connection" "test" {
  vpn_gateway_id = %q
  name           = "tf-acc-ipsec"
  remote_address = "198.51.100.77"
  psk            = "tf-acc-correct-horse-battery"
  remote_ts      = ["192.168.77.0/24"]
}
`, gwID)
	updated := fmt.Sprintf(`
resource "iaas_vpn_ipsec_connection" "test" {
  vpn_gateway_id = %q
  name           = "tf-acc-ipsec-2"
  remote_address = "198.51.100.77"
  psk            = "tf-acc-another-key-2026"
  remote_ts      = ["192.168.77.0/24"]
  dpd_delay      = 60
}
`, gwID)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("iaas_vpn_ipsec_connection.test", "id"),
					resource.TestCheckResourceAttr("iaas_vpn_ipsec_connection.test", "apply_state", "applied"),
					resource.TestCheckResourceAttrSet("iaas_vpn_ipsec_connection.test", "remote_settings"),
				),
			},
			{
				ResourceName: "iaas_vpn_ipsec_connection.test",
				ImportState:  true,
				ImportStateIdFunc: func(st *terraform.State) (string, error) {
					return gwID + "/" + st.RootModule().Resources["iaas_vpn_ipsec_connection.test"].Primary.ID, nil
				},
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"psk", "timeouts"},
			},
			{
				Config: updated,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_vpn_ipsec_connection.test", "name", "tf-acc-ipsec-2"),
					resource.TestCheckResourceAttr("iaas_vpn_ipsec_connection.test", "dpd_delay", "60"),
				),
			},
		},
	})
}
