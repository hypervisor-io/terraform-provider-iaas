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

// confirmedVolumeAPI is a mock of the Master user volume API with CONFIRMED
// asynchronous mutations (Master 3342-VOLUME-CONVERGE): attach, detach, resize
// and delete only record the operation (active_operation names it) and the
// simulated node completes it a few SHOW polls later. A completion either
// applies the desired fields (or deletes the volume) or, on node failure,
// leaves the volume unchanged; both clear active_operation. A new mutation
// while one is pending is refused with the Master gate's busy 422.
type confirmedVolumeAPI struct {
	mu sync.Mutex

	vol           map[string]any
	pending       string
	desired       map[string]any
	polls         int
	completeAfter int
	nodeFails     map[string]bool
	refuse        map[string][2]string // action -> {reason, message}, answered 422 once
	order         []string
}

const (
	cvID     = "aaaaaaaa-1111-1111-1111-aaaaaaaaaaaa"
	cvPlan   = "bbbbbbbb-1111-1111-1111-bbbbbbbbbbbb"
	cvPlan2  = "cccccccc-1111-1111-1111-cccccccccccc"
	cvGroup  = "dddddddd-1111-1111-1111-dddddddddddd"
	cvInstA  = "eeeeeeee-1111-1111-1111-eeeeeeeeeeee"
	cvInstB  = "ffffffff-1111-1111-1111-ffffffffffff"
	cvVolume = "confirmed-vol"
)

func newConfirmedVolumeAPI(t *testing.T) (*confirmedVolumeAPI, *acctest.MockServer) {
	t.Helper()
	api := &confirmedVolumeAPI{completeAfter: 2, nodeFails: map[string]bool{}, refuse: map[string][2]string{}}
	srv := acctest.NewMockServer(t)

	srv.Handle("POST", "/storage/volumes", func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()
		api.vol = map[string]any{
			"id": cvID, "name": cvVolume, "volume_plan_id": cvPlan, "hypervisor_group_id": cvGroup,
			"status": "available", "deployed": 1, "size": 50, "path": "pool/" + cvID,
			"instance_id": nil, "dev": nil, "active_operation": nil, "active_operation_at": nil,
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "volume": api.copyVol()})
	})
	srv.Handle("GET", "/storage/volume/"+cvID, func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()
		api.advance()
		if api.vol == nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"success": false, "reason": "not_found", "message": "Volume not found."})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"volume": api.copyVol()})
	})
	mutation := func(action string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			api.mu.Lock()
			defer api.mu.Unlock()
			api.order = append(api.order, action)
			if api.vol == nil {
				writeJSON(w, http.StatusNotFound, map[string]any{"success": false, "reason": "not_found", "message": "Volume not found."})
				return
			}
			if ref, ok := api.refuse[action]; ok {
				delete(api.refuse, action)
				writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"success": false, "reason": ref[0], "message": ref[1]})
				return
			}
			if api.pending != "" {
				writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"success": false, "reason": "volume_operation_rejected",
					"message": "Volume is busy with operation: " + api.pending + ". Please wait."})
				return
			}
			if action == "delete" && (api.vol["instance_id"] != nil || api.vol["status"] == "attached") {
				writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"success": false, "reason": "volume_attached",
					"message": "Detach the volume before deleting it."})
				return
			}
			switch action {
			case "attach":
				api.desired = map[string]any{"instance_id": body["instance_id"], "dev": "vdb", "status": "attached"}
			case "detach":
				api.desired = map[string]any{"instance_id": nil, "dev": nil, "status": "available"}
			case "resize":
				api.desired = map[string]any{"volume_plan_id": body["volume_plan_id"], "size": 100}
			case "delete":
				api.desired = nil
			}
			api.pending, api.polls = action, 0
			api.vol["active_operation"] = action
			if action == "delete" {
				writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Volume deletion initiated."})
				return
			}
			out := map[string]any{"success": true, "volume": api.copyVol()}
			if action == "resize" {
				out["is_downgrade"] = false
			}
			writeJSON(w, http.StatusOK, out)
		}
	}
	srv.Handle("POST", "/storage/volume/"+cvID+"/attach", mutation("attach"))
	srv.Handle("POST", "/storage/volume/"+cvID+"/detach", mutation("detach"))
	srv.Handle("PATCH", "/storage/volume/"+cvID+"/resize", mutation("resize"))
	srv.Handle("DELETE", "/storage/volume/"+cvID, mutation("delete"))
	return api, srv
}

// advance simulates the node: a pending operation completes after
// completeAfter SHOW polls. Callers hold mu.
func (a *confirmedVolumeAPI) advance() {
	if a.pending == "" || a.vol == nil {
		return
	}
	a.polls++
	if a.polls <= a.completeAfter {
		return
	}
	if !a.nodeFails[a.pending] {
		if a.pending == "delete" {
			a.vol, a.pending = nil, ""
			return
		}
		for k, v := range a.desired {
			a.vol[k] = v
		}
	}
	a.pending = ""
	a.vol["active_operation"] = nil
	a.vol["active_operation_at"] = "2026-10-12T10:00:00.000000Z"
}

func (a *confirmedVolumeAPI) copyVol() map[string]any {
	out := make(map[string]any, len(a.vol))
	for k, v := range a.vol {
		out[k] = v
	}
	return out
}

func (a *confirmedVolumeAPI) mutations() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return strings.Join(a.order, ",")
}

func confirmedVolumeConfig(endpoint, plan, instance string) string {
	cfg := acctest.ProviderConfig(endpoint) + `
resource "iaas_volume" "test" {
  name           = "` + cvVolume + `"
  volume_plan_id = "` + plan + `"
  location_id    = "` + cvGroup + `"
`
	if instance != "" {
		cfg += `  instance_id    = "` + instance + `"
`
	}
	return cfg + "}\n"
}

// TestUnitVolume_confirmedOperations drives create+attach, resize plus a move
// to another instance, and destroy of an attached volume against the confirmed
// API. Every step must wait for the node: a provider that trusted the 200
// would record the old plan/size/attachment (the mock only changes them on
// completion) and fail the checks, and a destroy that did not detach first
// would be refused volume_attached.
func TestUnitVolume_confirmedOperations(t *testing.T) {
	ensureTFBinary(t)
	t.Setenv("IAAS_INSTANCE_POLL_INTERVAL", "1ms")
	api, srv := newConfirmedVolumeAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{
				Config: confirmedVolumeConfig(srv.Endpoint(), cvPlan, cvInstA),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_volume.test", "instance_id", cvInstA),
					resource.TestCheckResourceAttr("iaas_volume.test", "status", "attached"),
					resource.TestCheckResourceAttr("iaas_volume.test", "dev", "vdb"),
				),
			},
			{
				Config: confirmedVolumeConfig(srv.Endpoint(), cvPlan2, cvInstB),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("iaas_volume.test", "volume_plan_id", cvPlan2),
					resource.TestCheckResourceAttr("iaas_volume.test", "size", "100"),
					resource.TestCheckResourceAttr("iaas_volume.test", "instance_id", cvInstB),
					resource.TestCheckResourceAttr("iaas_volume.test", "status", "attached"),
				),
			},
		},
	})

	if got, want := api.mutations(), "attach,resize,detach,attach,detach,delete"; got != want {
		t.Fatalf("mutation order = %s; want %s", got, want)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if api.vol != nil {
		t.Fatalf("destroy must wait for the confirmed deletion; volume still present: %v", api.vol)
	}
}

// TestUnitVolume_resizeNodeFailureFailsApply proves a node failure is an apply
// error naming the failure, never a silent success.
func TestUnitVolume_resizeNodeFailureFailsApply(t *testing.T) {
	ensureTFBinary(t)
	t.Setenv("IAAS_INSTANCE_POLL_INTERVAL", "1ms")
	api, srv := newConfirmedVolumeAPI(t)
	api.nodeFails["resize"] = true

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{Config: confirmedVolumeConfig(srv.Endpoint(), cvPlan, "")},
			{
				Config:      confirmedVolumeConfig(srv.Endpoint(), cvPlan2, ""),
				ExpectError: regexp.MustCompile(`(?s)Error resizing volume.*failed on the node`),
			},
		},
	})
}

// TestUnitVolume_refusalReasonSurfaces proves a definite refusal reaches the
// operator with its reason code.
func TestUnitVolume_refusalReasonSurfaces(t *testing.T) {
	ensureTFBinary(t)
	t.Setenv("IAAS_INSTANCE_POLL_INTERVAL", "1ms")
	api, srv := newConfirmedVolumeAPI(t)
	api.refuse["attach"] = [2]string{"volume_operation_refused", "The node refused the volume operation."}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{Config: confirmedVolumeConfig(srv.Endpoint(), cvPlan, "")},
			{
				Config:      confirmedVolumeConfig(srv.Endpoint(), cvPlan, cvInstA),
				ExpectError: regexp.MustCompile(`(?s)Error attaching volume.*The node refused the volume operation.*reason: volume_operation_refused`),
			},
		},
	})
}

// TestUnitVolume_destroySettlesPendingAttachBeforeDeciding pins the destroy of
// a volume whose earlier attach is still pending (e.g. a timed-out apply): the
// API then reports the OLD state (available, no instance) with
// active_operation "attach", and the attach later attaches it. Deciding on
// detach from that first read would skip the detach and the DELETE would be
// refused volume_attached; the destroy must settle first, then detach, then
// delete, and end with the volume gone.
func TestUnitVolume_destroySettlesPendingAttachBeforeDeciding(t *testing.T) {
	ensureTFBinary(t)
	t.Setenv("IAAS_INSTANCE_POLL_INTERVAL", "1ms")
	api, srv := newConfirmedVolumeAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{
			{Config: confirmedVolumeConfig(srv.Endpoint(), cvPlan, "")},
			{
				PreConfig: func() {
					api.mu.Lock()
					defer api.mu.Unlock()
					api.pending, api.polls, api.completeAfter = "attach", 0, 8
					api.desired = map[string]any{"instance_id": cvInstA, "dev": "vdb", "status": "attached"}
					api.vol["active_operation"] = "attach"
				},
				// No resource in config: Terraform destroys the volume.
				Config: acctest.ProviderConfig(srv.Endpoint()),
			},
		},
	})

	if got, want := api.mutations(), "detach,delete"; got != want {
		t.Fatalf("mutation order = %s; want %s (settle the pending attach, then detach, then delete)", got, want)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if api.vol != nil {
		t.Fatalf("volume still present after destroy: %v", api.vol)
	}
}
