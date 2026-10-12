package volumeop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
)

// fakeVolumeAPI models the Master user volume API after the confirmed-
// operation change: a mutation is recorded (active_operation set, nothing else
// changes) and only the simulated node completion changes the volume. It
// answers with the exact envelopes the Master UserApi\VolumeController sends.
type fakeVolumeAPI struct {
	t  *testing.T
	mu sync.Mutex

	vol     map[string]any // nil once deleted
	pending string         // active operation recorded by the fake
	desired map[string]any // fields applied on completion
	polls   int            // GETs seen while pending

	// completeAfter is how many GETs a pending operation survives.
	completeAfter int
	// nodeFails makes the completion a node failure (state kept).
	nodeFails bool
	// never completes the operation at all (uncertain dispatch, dead node).
	never bool
	// stoppedInstance makes attach/detach synchronous (no hotplug command).
	stoppedInstance bool

	// refusals are answered, in order, before normal processing of a mutation.
	refusals []refusal
	// busyFor is the number of GETs another operation stays active.
	busyFor int
	// failFirstWith answers the first mutation with this status AFTER
	// recording it (an uncertain answer from the API).
	failFirstWith int
	// hangUpFirst closes the connection on the first mutation AFTER recording.
	hangUpFirst bool
	// always5xx answers every mutation 503 (the client's own retries are
	// exhausted); record5xx decides whether the first one was recorded.
	always5xx bool
	record5xx bool
	// completeOnRecord makes the node finish an operation at the moment it is
	// recorded, i.e. BEFORE any readback (wall-clock completion).
	completeOnRecord bool
	// failOnce makes only the first completion a node failure.
	failOnce bool
	// cooldownWindow, when set, refuses every mutation with the Master's
	// cooldown 422 until cooldownWindow after the last completion.
	cooldownWindow time.Duration
	cooldownUntil  time.Time
	// hiddenBusy answers this many mutations with the busy 422 while the
	// volume still reads active_operation null (the API's pending operation
	// row exists before it publishes the marker).
	hiddenBusy int
	// busyDesired applies the desired fields of the refused request when a
	// hidden busy refusal is answered (a completion that raced the refusal).
	busyDesired bool

	requests []string
}

// finish completes the pending operation the way the Master reducer does.
// It reports whether the volume is now gone. Callers hold mu.
func (f *fakeVolumeAPI) finish() bool {
	fail := f.nodeFails || f.failOnce
	f.failOnce = false
	if f.cooldownWindow > 0 {
		f.cooldownUntil = time.Now().Add(f.cooldownWindow)
	}
	if !fail {
		if f.pending == "delete" {
			f.vol, f.pending = nil, ""
			return true
		}
		for k, v := range f.desired {
			f.vol[k] = v
		}
	}
	f.pending = ""
	f.vol["active_operation"] = nil
	f.vol["active_operation_at"] = time.Now().UTC().Format(time.RFC3339)
	return false
}

func (f *fakeVolumeAPI) writes() int {
	return f.count("POST /storage/volume/vol-1/") + f.count("PATCH ") + f.count("DELETE ")
}

type refusal struct {
	status  int
	reason  string
	message string
}

func newFake(t *testing.T) *fakeVolumeAPI {
	return &fakeVolumeAPI{
		t: t,
		vol: map[string]any{
			"id":                  "vol-1",
			"name":                "data",
			"status":              "available",
			"instance_id":         nil,
			"dev":                 nil,
			"size":                50,
			"volume_plan_id":      "plan-50",
			"active_operation":    nil,
			"active_operation_at": nil,
		},
		completeAfter: 2,
	}
}

func (f *fakeVolumeAPI) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.requests {
		if strings.HasPrefix(r, prefix) {
			n++
		}
	}
	return n
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (f *fakeVolumeAPI) snapshot() map[string]any {
	out := make(map[string]any, len(f.vol))
	for k, v := range f.vol {
		out[k] = v
	}
	return out
}

func (f *fakeVolumeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)

	if r.Method == http.MethodGet && r.URL.Path == "/storage/volume/vol-1" {
		f.handleGet(w)
		return
	}
	if f.vol == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"success": false, "reason": "not_found", "message": "Volume not found."})
		return
	}

	var action string
	body := map[string]any{}
	_ = json.NewDecoder(r.Body).Decode(&body)
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/storage/volume/vol-1/attach":
		action = "attach"
	case r.Method == http.MethodPost && r.URL.Path == "/storage/volume/vol-1/detach":
		action = "detach"
	case r.Method == http.MethodPatch && r.URL.Path == "/storage/volume/vol-1/resize":
		action = "resize"
	case r.Method == http.MethodDelete && r.URL.Path == "/storage/volume/vol-1":
		action = "delete"
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "no route"})
		return
	}

	if f.always5xx {
		if f.record5xx && f.pending == "" && f.vol["active_operation"] == nil {
			f.record5xx = false
			f.pending, f.polls = action, 0
			f.vol["active_operation"] = action
			switch action {
			case "attach":
				f.desired = map[string]any{"instance_id": body["instance_id"], "dev": "vdb", "status": "attached"}
			case "resize":
				f.desired = map[string]any{"volume_plan_id": body["volume_plan_id"], "size": 100}
			}
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "Service Unavailable"})
		return
	}
	if len(f.refusals) > 0 {
		ref := f.refusals[0]
		f.refusals = f.refusals[1:]
		writeJSON(w, ref.status, map[string]any{"success": false, "reason": ref.reason, "message": ref.message})
		return
	}
	if f.hiddenBusy > 0 {
		f.hiddenBusy--
		if f.busyDesired && action == "attach" {
			f.vol["instance_id"], f.vol["dev"], f.vol["status"] = body["instance_id"], "vdb", "attached"
		}
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"success": false, "reason": "volume_operation_rejected",
			"message": "Volume is busy with operation: . Please wait.",
		})
		return
	}
	if f.pending == "" && time.Now().Before(f.cooldownUntil) {
		left := time.Until(f.cooldownUntil).Seconds()
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"success": false, "reason": "volume_operation_rejected",
			"message": fmt.Sprintf("Volume is in cooldown. Try again in %.3f seconds.", left),
		})
		return
	}
	// The Master gate: a pending operation refuses every new mutation.
	if f.pending != "" || f.busyFor > 0 {
		active := f.pending
		if active == "" {
			active = "snapshot"
		}
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"success": false, "reason": "volume_operation_rejected",
			"message": "Volume is busy with operation: " + active + ". Please wait.",
		})
		return
	}

	desired := map[string]any{}
	switch action {
	case "attach":
		desired = map[string]any{"instance_id": body["instance_id"], "dev": "vdb", "status": "attached"}
	case "detach":
		desired = map[string]any{"instance_id": nil, "dev": nil, "status": "available"}
	case "resize":
		desired = map[string]any{"volume_plan_id": body["volume_plan_id"], "size": 100}
	}

	if f.stoppedInstance && (action == "attach" || action == "detach") {
		for k, v := range desired {
			f.vol[k] = v
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "volume": f.snapshot()})
		return
	}

	f.pending, f.desired, f.polls = action, desired, 0
	f.vol["active_operation"] = action
	if f.completeOnRecord {
		f.finish()
	}

	if f.failFirstWith != 0 {
		status := f.failFirstWith
		f.failFirstWith = 0
		writeJSON(w, status, map[string]any{"message": "Server Error"})
		return
	}
	if f.hangUpFirst {
		f.hangUpFirst = false
		hj, ok := w.(http.Hijacker)
		if !ok {
			f.t.Fatal("hijacking unsupported")
		}
		conn, _, err := hj.Hijack()
		if err == nil {
			_ = conn.Close()
		}
		return
	}
	if action == "delete" {
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Volume deletion initiated."})
		return
	}
	resp := map[string]any{"success": true, "volume": f.snapshot()}
	if action == "resize" {
		resp["is_downgrade"] = false
	}
	writeJSON(w, http.StatusOK, resp)
}

func (f *fakeVolumeAPI) handleGet(w http.ResponseWriter) {
	if f.vol == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"success": false, "reason": "not_found", "message": "Volume not found."})
		return
	}
	if f.busyFor > 0 {
		f.busyFor--
		v := f.snapshot()
		v["active_operation"] = "snapshot"
		writeJSON(w, http.StatusOK, map[string]any{"volume": v})
		if f.busyFor == 0 {
			// The foreign operation completed: cooldown starts.
			f.vol["active_operation_at"] = time.Now().UTC().Format(time.RFC3339)
		}
		return
	}
	if f.pending != "" && !f.never {
		f.polls++
		if f.polls > f.completeAfter && f.finish() {
			writeJSON(w, http.StatusNotFound, map[string]any{"success": false, "reason": "not_found", "message": "Volume not found."})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"volume": f.snapshot()})
}

func setup(t *testing.T) (*fakeVolumeAPI, *client.Client) {
	t.Helper()
	f := newFake(t)
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, client.New(srv.URL, "test-token", 5*time.Second, false)
}

var fast = Options{Interval: time.Millisecond, Timeout: 5 * time.Second}

func TestAttach_AcceptedThenNodeCompletes(t *testing.T) {
	f, c := setup(t)
	f.completeAfter = 3

	v, err := AttachVolume(context.Background(), c, "vol-1", "inst-1", fast)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if v["instance_id"] != "inst-1" || v["status"] != "attached" || v["active_operation"] != nil {
		t.Fatalf("final volume = %v; want attached to inst-1 with no active operation", v)
	}
	if got := f.count("GET "); got < 4 {
		t.Fatalf("GETs = %d; the wait must poll through the pending phase (want >= 4)", got)
	}
	if got := f.count("POST /storage/volume/vol-1/attach"); got != 1 {
		t.Fatalf("attach requests = %d; want exactly 1", got)
	}
}

func TestAttach_StoppedInstanceIsImmediate(t *testing.T) {
	f, c := setup(t)
	f.stoppedInstance = true

	v, err := AttachVolume(context.Background(), c, "vol-1", "inst-1", fast)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if v["instance_id"] != "inst-1" {
		t.Fatalf("final volume = %v", v)
	}
}

func TestDetach_AcceptedThenNodeCompletes(t *testing.T) {
	f, c := setup(t)
	f.vol["instance_id"], f.vol["dev"], f.vol["status"] = "inst-1", "vdb", "attached"

	v, err := DetachVolume(context.Background(), c, "vol-1", fast)
	if err != nil {
		t.Fatalf("detach: %v", err)
	}
	if v["instance_id"] != nil || v["status"] != "available" {
		t.Fatalf("final volume = %v; want detached", v)
	}
}

func TestResize_WaitsForObservedPlanAndSize(t *testing.T) {
	f, c := setup(t)
	f.completeAfter = 2

	v, err := ResizeVolume(context.Background(), c, "vol-1", "plan-100", fast)
	if err != nil {
		t.Fatalf("resize: %v", err)
	}
	if v["volume_plan_id"] != "plan-100" {
		t.Fatalf("final volume = %v; want plan-100", v)
	}
	if size, _ := v["size"].(float64); size != 100 {
		t.Fatalf("size = %v; want the node-confirmed 100", v["size"])
	}
}

func TestResize_NodeFailureIsFailedErrorAndStateKept(t *testing.T) {
	f, c := setup(t)
	f.nodeFails = true

	v, err := ResizeVolume(context.Background(), c, "vol-1", "plan-100", fast)
	var failed *FailedError
	if !errors.As(err, &failed) {
		t.Fatalf("err = %v (%T); want *FailedError", err, err)
	}
	if failed.Action != Resize || failed.VolumeID != "vol-1" {
		t.Fatalf("FailedError = %+v", failed)
	}
	if v["volume_plan_id"] != "plan-50" {
		t.Fatalf("observed volume = %v; the previous plan must be reported", v)
	}
	if !strings.Contains(err.Error(), "failed on the node") {
		t.Fatalf("message = %q", err.Error())
	}
}

func TestAttach_NodeFailureIsFailedError(t *testing.T) {
	f, c := setup(t)
	f.nodeFails = true

	_, err := AttachVolume(context.Background(), c, "vol-1", "inst-1", fast)
	var failed *FailedError
	if !errors.As(err, &failed) {
		t.Fatalf("err = %v; want *FailedError", err)
	}
}

func TestRefusal422_CarriesReasonAndDoesNotPoll(t *testing.T) {
	f, c := setup(t)
	f.refusals = []refusal{{422, "volume_operation_refused", "The node refused the volume operation."}}

	_, err := ResizeVolume(context.Background(), c, "vol-1", "plan-100", fast)
	var refused *RefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("err = %v (%T); want *RefusedError", err, err)
	}
	if refused.Reason != "volume_operation_refused" || refused.Status != 422 {
		t.Fatalf("refusal = %+v", refused)
	}
	if !strings.Contains(err.Error(), "reason: volume_operation_refused") {
		t.Fatalf("message %q must name the reason", err.Error())
	}
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.Reason != "volume_operation_refused" {
		t.Fatalf("the APIError must stay reachable with its reason: %v", err)
	}
	if got := f.count("PATCH "); got != 1 {
		t.Fatalf("resize requests = %d; a definite refusal is never re-sent", got)
	}
	// Exactly one read (the gate check), no completion polling.
	if got := f.count("GET "); got != 1 {
		t.Fatalf("GETs = %d; want 1", got)
	}
}

func TestRefusal409_CarriesReason(t *testing.T) {
	f, c := setup(t)
	f.vol["status"] = "attached"
	f.refusals = []refusal{{409, "volume_attached", "Detach the volume before deleting it."}}

	err := DeleteVolume(context.Background(), c, "vol-1", fast)
	var refused *RefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("err = %v; want *RefusedError", err)
	}
	if refused.Status != 409 || refused.Reason != "volume_attached" {
		t.Fatalf("refusal = %+v", refused)
	}
}

func TestDelete_RefusedWhileAttached422(t *testing.T) {
	f, c := setup(t)
	f.vol["status"], f.vol["instance_id"] = "attached", "inst-1"
	f.refusals = []refusal{{422, "volume_attached", "Detach the volume before deleting it."}}

	err := DeleteVolume(context.Background(), c, "vol-1", fast)
	var refused *RefusedError
	if !errors.As(err, &refused) || refused.Reason != "volume_attached" {
		t.Fatalf("err = %v; want RefusedError volume_attached", err)
	}
}

func TestDelete_AcceptedThenGone(t *testing.T) {
	f, c := setup(t)
	f.completeAfter = 2

	if err := DeleteVolume(context.Background(), c, "vol-1", fast); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if f.vol != nil {
		t.Fatal("fake volume should be gone")
	}
	if got := f.count("GET "); got < 3 {
		t.Fatalf("GETs = %d; delete must wait for the 404 (want >= 3)", got)
	}
}

func TestDelete_NodeFailureKeepsVolume(t *testing.T) {
	f, c := setup(t)
	f.nodeFails = true

	err := DeleteVolume(context.Background(), c, "vol-1", fast)
	var failed *FailedError
	if !errors.As(err, &failed) || failed.Action != Delete {
		t.Fatalf("err = %v; want *FailedError for delete", err)
	}
}

func TestTimeout_PendingErrorNamesActiveOperation(t *testing.T) {
	f, c := setup(t)
	f.never = true

	_, err := AttachVolume(context.Background(), c, "vol-1", "inst-1", Options{Interval: time.Millisecond, Timeout: 150 * time.Millisecond})
	var pending *PendingError
	if !errors.As(err, &pending) {
		t.Fatalf("err = %v (%T); want *PendingError", err, err)
	}
	if pending.ActiveOperation != "attach" {
		t.Fatalf("ActiveOperation = %q; want attach", pending.ActiveOperation)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err must wrap context.DeadlineExceeded: %v", err)
	}
	if !strings.Contains(err.Error(), "not cancelled") {
		t.Fatalf("message %q must say the operation continues", err.Error())
	}
	if got := f.count("POST /storage/volume/vol-1/attach"); got != 1 {
		t.Fatalf("attach requests = %d; a pending operation is never re-sent", got)
	}
}

func TestBusy_WaitsForOtherOperationThenSends(t *testing.T) {
	f, c := setup(t)
	// A foreign snapshot is in flight: the first attach meets the busy gate,
	// the wait drains it through GETs, then the attach is sent again.
	f.busyFor = 3

	v, err := AttachVolume(context.Background(), c, "vol-1", "inst-1", fast)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if v["instance_id"] != "inst-1" {
		t.Fatalf("final volume = %v", v)
	}
	if got := f.count("POST /storage/volume/vol-1/attach"); got != 2 {
		t.Fatalf("attach requests = %d; want 2 (busy refusal, then accepted)", got)
	}
}

func TestCooldown_WaitsNamedSecondsThenSends(t *testing.T) {
	f, c := setup(t)
	cooldownMargin = 0
	t.Cleanup(func() { cooldownMargin = 2 * time.Second })
	f.vol["status"], f.vol["instance_id"] = "attached", "inst-1"
	f.refusals = []refusal{{422, "volume_operation_rejected", "Volume is in cooldown. Try again in 0.2 seconds."}}

	start := time.Now()
	v, err := DetachVolume(context.Background(), c, "vol-1", fast)
	if err != nil {
		t.Fatalf("detach: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 200*time.Millisecond {
		t.Fatalf("elapsed %v; the named cooldown must be waited out", elapsed)
	}
	if v["status"] != "available" {
		t.Fatalf("final volume = %v", v)
	}
	if got := f.count("POST /storage/volume/vol-1/detach"); got != 2 {
		t.Fatalf("detach requests = %d; want 2", got)
	}
}

func TestCooldown_OutlastingDeadlineIsPending(t *testing.T) {
	f, c := setup(t)
	f.refusals = []refusal{{422, "volume_operation_rejected", "Volume is in cooldown. Try again in 287.5 seconds."}}

	_, err := ResizeVolume(context.Background(), c, "vol-1", "plan-100", Options{Interval: time.Millisecond, Timeout: 100 * time.Millisecond})
	var pending *PendingError
	if !errors.As(err, &pending) {
		t.Fatalf("err = %v; want *PendingError", err)
	}
	if got := f.count("PATCH "); got != 1 {
		t.Fatalf("resize requests = %d; want 1", got)
	}
}

func TestUncertain5xx_RecordedOperationIsFollowed(t *testing.T) {
	f, c := setup(t)
	// The API recorded the attach but answered 500; the client's own retry
	// then meets the busy gate. Neither may be reported as a refusal.
	f.failFirstWith = 500

	v, err := AttachVolume(context.Background(), c, "vol-1", "inst-1", fast)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if v["instance_id"] != "inst-1" {
		t.Fatalf("final volume = %v", v)
	}
	if got := f.writes(); got != 1 {
		t.Fatalf("mutations = %d; an uncertain answer is never replayed", got)
	}
}

func TestUncertain5xx_RecordedThenNodeFailureIsFailedSingleWrite(t *testing.T) {
	f, c := setup(t)
	f.failFirstWith = 500
	f.nodeFails = true

	_, err := AttachVolume(context.Background(), c, "vol-1", "inst-1", fast)
	var failed *FailedError
	if !errors.As(err, &failed) {
		t.Fatalf("err = %v (%T); want *FailedError", err, err)
	}
	if got := f.writes(); got != 1 {
		t.Fatalf("mutations = %d; an uncertain answer is never replayed", got)
	}
}

// The reviewer's reproduction: the first operation is recorded and FAILS on
// the node before anything reads it back, the answer is a 500, and any later
// mutation would meet the completion cooldown and then be admitted. A replay
// (transport or admission loop) would issue a second operation and hide the
// failure. Exactly one mutation, and an error, are required.
func TestUncertain5xx_EarlyNodeFailureBeforeReadbackIsNeverResent(t *testing.T) {
	f, c := setup(t)
	cooldownMargin = 0
	t.Cleanup(func() { cooldownMargin = 2 * time.Second })
	f.failFirstWith = 500
	f.completeOnRecord = true
	f.failOnce = true
	f.cooldownWindow = 300 * time.Millisecond

	_, err := ResizeVolume(context.Background(), c, "vol-1", "plan-100", fast)
	var unknown *UnknownOutcomeError
	if !errors.As(err, &unknown) {
		t.Fatalf("err = %v (%T); want *UnknownOutcomeError (never a silent success)", err, err)
	}
	if got := f.writes(); got != 1 {
		t.Fatalf("mutations = %d; want exactly 1", got)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.vol["volume_plan_id"] != "plan-50" {
		t.Fatalf("volume = %v; a second resize was admitted", f.vol)
	}
}

func TestUncertain5xx_EarlyCompletionBeforeReadbackIsSuccessSingleWrite(t *testing.T) {
	f, c := setup(t)
	f.failFirstWith = 500
	f.completeOnRecord = true
	f.cooldownWindow = time.Minute

	v, err := AttachVolume(context.Background(), c, "vol-1", "inst-1", fast)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if v["instance_id"] != "inst-1" {
		t.Fatalf("final volume = %v", v)
	}
	if got := f.writes(); got != 1 {
		t.Fatalf("mutations = %d; want exactly 1", got)
	}
}

func TestUncertainTransport_EarlyNodeFailureIsNeverResent(t *testing.T) {
	f, c := setup(t)
	cooldownMargin = 0
	t.Cleanup(func() { cooldownMargin = 2 * time.Second })
	f.hangUpFirst = true
	f.completeOnRecord = true
	f.failOnce = true
	f.cooldownWindow = 300 * time.Millisecond

	err := DeleteVolume(context.Background(), c, "vol-1", fast)
	var unknown *UnknownOutcomeError
	if !errors.As(err, &unknown) {
		t.Fatalf("err = %v (%T); want *UnknownOutcomeError", err, err)
	}
	if got := f.writes(); got != 1 {
		t.Fatalf("mutations = %d; want exactly 1", got)
	}
}

func TestUncertainTransport_EarlyCompletionIsSuccessSingleWrite(t *testing.T) {
	f, c := setup(t)
	f.hangUpFirst = true
	f.completeOnRecord = true

	if err := DeleteVolume(context.Background(), c, "vol-1", fast); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := f.writes(); got != 1 {
		t.Fatalf("mutations = %d; want exactly 1", got)
	}
}

func TestBusyWithNoVisibleMarker_BacksOffAndAsksAgain(t *testing.T) {
	f, c := setup(t)
	f.hiddenBusy = 2

	v, err := AttachVolume(context.Background(), c, "vol-1", "inst-1", fast)
	if err != nil {
		t.Fatalf("attach: %v (a busy refusal with an unpublished marker is transient)", err)
	}
	if v["instance_id"] != "inst-1" {
		t.Fatalf("final volume = %v", v)
	}
	if got := f.writes(); got != 3 {
		t.Fatalf("mutations = %d; want 3 (two busy refusals, then admitted)", got)
	}
}

func TestBusyWithNoVisibleMarker_TimesOutAsPending(t *testing.T) {
	f, c := setup(t)
	f.hiddenBusy = 1 << 30

	_, err := AttachVolume(context.Background(), c, "vol-1", "inst-1", Options{Interval: time.Millisecond, Timeout: 100 * time.Millisecond})
	var pending *PendingError
	if !errors.As(err, &pending) {
		t.Fatalf("err = %v (%T); want *PendingError, never a RefusedError for busy", err, err)
	}
}

func TestRefusalRacedByCompletion_DesiredStateIsSuccess(t *testing.T) {
	f, c := setup(t)
	// Busy refusal, and by the time of the read the other writer's attach to
	// the same instance has completed.
	f.hiddenBusy = 1
	f.busyDesired = true

	v, err := AttachVolume(context.Background(), c, "vol-1", "inst-1", fast)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if v["instance_id"] != "inst-1" {
		t.Fatalf("final volume = %v", v)
	}
	if got := f.writes(); got != 1 {
		t.Fatalf("mutations = %d; the desired state was already observed, nothing may be re-sent", got)
	}
}

func TestIdleRefusalWithDesiredStateAlreadyHeld_IsSuccess(t *testing.T) {
	f, c := setup(t)
	f.refusals = []refusal{{422, "volume_operation_rejected", "Volume is not attached to any instance."}}

	v, err := DetachVolume(context.Background(), c, "vol-1", fast)
	if err != nil {
		t.Fatalf("detach of an already detached volume: %v", err)
	}
	if v["status"] != "available" {
		t.Fatalf("final volume = %v", v)
	}
}

// An identical operation is already in flight (a previous, timed-out apply)
// and it fails on the node: that failure is the answer to this request too,
// reported as FailedError; the refused request is not sent a second time.
func TestBusyWithSameActionInFlightThatFails_IsFailedNotResent(t *testing.T) {
	f, c := setup(t)
	f.pending, f.desired = "attach", map[string]any{"instance_id": "inst-1", "dev": "vdb", "status": "attached"}
	f.vol["active_operation"] = "attach"
	f.nodeFails = true

	_, err := AttachVolume(context.Background(), c, "vol-1", "inst-1", fast)
	var failed *FailedError
	if !errors.As(err, &failed) {
		t.Fatalf("err = %v (%T); want *FailedError", err, err)
	}
	if got := f.writes(); got != 1 {
		t.Fatalf("mutations = %d; want 1 (the busy refusal only)", got)
	}
}

func TestSettle_WaitsForPendingWorkAndReportsGone(t *testing.T) {
	f, c := setup(t)
	f.pending, f.desired = "attach", map[string]any{"instance_id": "inst-1", "dev": "vdb", "status": "attached"}
	f.vol["active_operation"] = "attach"
	f.completeAfter = 2

	v, err := Settle(context.Background(), c, "vol-1", fast)
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	if v["instance_id"] != "inst-1" || v["active_operation"] != nil {
		t.Fatalf("settled volume = %v; want the attach applied", v)
	}

	f.mu.Lock()
	f.vol = nil
	f.mu.Unlock()
	if _, err := Settle(context.Background(), c, "vol-1", fast); !errors.Is(err, ErrGone) {
		t.Fatalf("err = %v; want ErrGone", err)
	}
}

func TestUncertainTransportError_RecordedOperationIsFollowed(t *testing.T) {
	f, c := setup(t)
	f.hangUpFirst = true

	v, err := ResizeVolume(context.Background(), c, "vol-1", "plan-100", fast)
	if err != nil {
		t.Fatalf("resize: %v", err)
	}
	if v["volume_plan_id"] != "plan-100" {
		t.Fatalf("final volume = %v", v)
	}
	if got := f.count("PATCH "); got != 1 {
		t.Fatalf("resize requests = %d; a recorded operation is never re-sent", got)
	}
}

func TestUncertain5xxExhausted_RecordedOperationIsFollowed(t *testing.T) {
	f, c := setup(t)
	// Every attempt answers 503 (the client's retries are exhausted) but the
	// first one was recorded: the wait must follow it to completion.
	f.always5xx, f.record5xx = true, true

	v, err := AttachVolume(context.Background(), c, "vol-1", "inst-1", fast)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if v["instance_id"] != "inst-1" {
		t.Fatalf("final volume = %v", v)
	}
}

func TestUncertain5xxExhausted_NothingRecordedIsNotARefusal(t *testing.T) {
	f, c := setup(t)
	f.always5xx = true

	_, err := ResizeVolume(context.Background(), c, "vol-1", "plan-100", fast)
	if err == nil {
		t.Fatal("want an error")
	}
	var unknown *UnknownOutcomeError
	if !errors.As(err, &unknown) {
		t.Fatalf("a 5xx is an unknown outcome, never a definite refusal: %v (%T)", err, err)
	}
	if !strings.Contains(err.Error(), "outcome unknown") {
		t.Fatalf("message %q must say the outcome is unknown", err.Error())
	}
	if got := f.writes(); got != 1 {
		t.Fatalf("mutations = %d; want exactly 1", got)
	}
}

func TestCooldownWait_Parsing(t *testing.T) {
	cases := []struct {
		e    client.APIError
		want time.Duration
		ok   bool
	}{
		{client.APIError{Status: 422, Message: "Volume is in cooldown. Try again in 12 seconds."}, 12*time.Second + cooldownMargin, true},
		{client.APIError{Status: 422, Message: "Volume is in cooldown. Try again in 1.5 seconds."}, 1500*time.Millisecond + cooldownMargin, true},
		{client.APIError{Status: 429, Message: "Too Many Attempts."}, cooldownFallback, true},
		{client.APIError{Status: 422, Message: "The node refused the volume operation.", Reason: "volume_operation_refused"}, 0, false},
		{client.APIError{Status: 422, Message: "Volume must be in available status to attach."}, 0, false},
	}
	for _, tc := range cases {
		got, ok := cooldownWait(&tc.e)
		if ok != tc.ok || got != tc.want {
			t.Errorf("cooldownWait(%q) = %v,%v; want %v,%v", tc.e.Message, got, ok, tc.want, tc.ok)
		}
	}
}
