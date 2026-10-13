package volumeop

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
)

// Tests for the 3.3.5 gate contract: busy is HTTP 409 reason volume_busy,
// cooldown is HTTP 429 reason volume_cooldown with Retry-After (whole
// seconds), the account in-flight limit is HTTP 429 reason
// account_inflight_limit. Older masters answer all three as 422
// volume_operation_rejected with the gate's text; those shapes stay covered
// by the tests in volumeop_test.go and the legacy cases below.

func noMargin(t *testing.T) {
	t.Helper()
	cooldownMargin = 0
	t.Cleanup(func() { cooldownMargin = 2 * time.Second })
}

func TestModern_Busy409WithMarkerWaitsThenSends(t *testing.T) {
	f, c := setup(t)
	f.modern = true
	f.busyFor = 3

	v, err := AttachVolume(context.Background(), c, "vol-1", "inst-1", fast)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if v["instance_id"] != "inst-1" {
		t.Fatalf("final volume = %v", v)
	}
	if got := f.count("POST /storage/volume/vol-1/attach"); got != 2 {
		t.Fatalf("attach requests = %d; want 2 (409 busy, then accepted)", got)
	}
}

func TestModern_Busy409WithoutMarkerBacksOffAndAsksAgain(t *testing.T) {
	f, c := setup(t)
	f.modern = true
	f.hiddenBusy = 2

	v, err := AttachVolume(context.Background(), c, "vol-1", "inst-1", fast)
	if err != nil {
		t.Fatalf("attach: %v (a 409 volume_busy is transient)", err)
	}
	if v["instance_id"] != "inst-1" {
		t.Fatalf("final volume = %v", v)
	}
	if got := f.writes(); got != 3 {
		t.Fatalf("mutations = %d; want 3", got)
	}
}

// A typed volume_busy is recognised without any "busy" in the message text.
func TestModern_BusyReasonWinsWithoutText(t *testing.T) {
	f, c := setup(t)
	f.typed = []typedRefusal{{http.StatusConflict, "volume_busy", "Please wait.", ""}}

	if _, err := AttachVolume(context.Background(), c, "vol-1", "inst-1", fast); err != nil {
		t.Fatalf("attach: %v (volume_busy is transient whatever the text)", err)
	}
	if got := f.writes(); got != 2 {
		t.Fatalf("mutations = %d; want 2", got)
	}
}

func TestModern_Cooldown429HonoursRetryAfter(t *testing.T) {
	f, c := setup(t)
	noMargin(t)
	f.vol["status"], f.vol["instance_id"] = "attached", "inst-1"
	f.typed = []typedRefusal{{http.StatusTooManyRequests, "volume_cooldown", "Volume is in cooldown. Try again in 1 seconds.", "1"}}

	start := time.Now()
	v, err := DetachVolume(context.Background(), c, "vol-1", fast)
	if err != nil {
		t.Fatalf("detach: %v", err)
	}
	if elapsed := time.Since(start); elapsed < time.Second {
		t.Fatalf("elapsed %v; Retry-After 1 must be waited out", elapsed)
	}
	if v["status"] != "available" {
		t.Fatalf("final volume = %v", v)
	}
	if got := f.count("POST /storage/volume/vol-1/detach"); got != 2 {
		t.Fatalf("detach requests = %d; want 2 (the client never replays the 429 itself)", got)
	}
}

// The header is authoritative over the message text.
func TestModern_RetryAfterWinsOverMessageText(t *testing.T) {
	f, c := setup(t)
	noMargin(t)
	f.typed = []typedRefusal{{http.StatusTooManyRequests, "volume_cooldown", "Volume is in cooldown. Try again in 0.01 seconds.", "1"}}

	start := time.Now()
	if _, err := ResizeVolume(context.Background(), c, "vol-1", "plan-100", fast); err != nil {
		t.Fatalf("resize: %v", err)
	}
	if elapsed := time.Since(start); elapsed < time.Second {
		t.Fatalf("elapsed %v; Retry-After (1s) must win over the text (0.01s)", elapsed)
	}
}

// A Retry-After that cannot fit the deadline ends at once as pending, with
// one request and no sleep until the deadline.
func TestModern_RetryAfterBoundedByDeadline(t *testing.T) {
	f, c := setup(t)
	f.typed = []typedRefusal{{http.StatusTooManyRequests, "volume_cooldown", "Volume is in cooldown. Try again in 300 seconds.", "300"}}

	start := time.Now()
	_, err := ResizeVolume(context.Background(), c, "vol-1", "plan-100", Options{Interval: time.Millisecond, Timeout: 2 * time.Second})
	elapsed := time.Since(start)
	var pending *PendingError
	if !errors.As(err, &pending) {
		t.Fatalf("err = %v (%T); want *PendingError", err, err)
	}
	if !strings.Contains(err.Error(), "cooldown") {
		t.Fatalf("message %q must name the cooldown", err.Error())
	}
	if elapsed > time.Second {
		t.Fatalf("elapsed %v; a wait longer than the deadline must end at once", elapsed)
	}
	if got := f.writes(); got != 1 {
		t.Fatalf("mutations = %d; want 1", got)
	}
}

// Context cancellation (no deadline) interrupts a Retry-After wait.
func TestModern_RetryAfterInterruptedByCancel(t *testing.T) {
	f, c := setup(t)
	f.typed = []typedRefusal{{http.StatusTooManyRequests, "volume_cooldown", "cooldown", "300"}}

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	_, err := ResizeVolume(ctx, c, "vol-1", "plan-100", Options{Interval: time.Millisecond})
	var pending *PendingError
	if !errors.As(err, &pending) || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v (%T); want *PendingError wrapping context.Canceled", err, err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("elapsed %v; cancel must end the wait", elapsed)
	}
}

func TestModern_CooldownWithoutRetryAfterReadsText(t *testing.T) {
	for _, header := range []string{"", "soon", "0", "-3"} {
		t.Run("header="+header, func(t *testing.T) {
			f, c := setup(t)
			noMargin(t)
			f.typed = []typedRefusal{{http.StatusTooManyRequests, "volume_cooldown", "Volume is in cooldown. Try again in 0.2 seconds.", header}}

			start := time.Now()
			if _, err := ResizeVolume(context.Background(), c, "vol-1", "plan-100", fast); err != nil {
				t.Fatalf("resize: %v", err)
			}
			elapsed := time.Since(start)
			if elapsed < 200*time.Millisecond || elapsed > 900*time.Millisecond {
				t.Fatalf("elapsed %v; want the text's 0.2s (no usable Retry-After)", elapsed)
			}
			if got := f.writes(); got != 2 {
				t.Fatalf("mutations = %d; want 2", got)
			}
		})
	}
}

func TestModern_InflightLimitBacksOffAndAsksAgain(t *testing.T) {
	f, c := setup(t)
	f.typed = []typedRefusal{
		{http.StatusTooManyRequests, "account_inflight_limit", "Too many in-flight volume operations (3). Wait for some to finish.", ""},
		{http.StatusTooManyRequests, "account_inflight_limit", "Too many in-flight volume operations (3). Wait for some to finish.", ""},
	}

	if _, err := AttachVolume(context.Background(), c, "vol-1", "inst-1", fast); err != nil {
		t.Fatalf("attach: %v (the in-flight limit is transient)", err)
	}
	if got := f.writes(); got != 3 {
		t.Fatalf("mutations = %d; want 3", got)
	}
}

func TestModern_InflightLimitHonoursRetryAfter(t *testing.T) {
	f, c := setup(t)
	f.typed = []typedRefusal{{http.StatusTooManyRequests, "account_inflight_limit", "Wait.", "1"}}

	start := time.Now()
	if _, err := AttachVolume(context.Background(), c, "vol-1", "inst-1", fast); err != nil {
		t.Fatalf("attach: %v", err)
	}
	if elapsed := time.Since(start); elapsed < time.Second {
		t.Fatalf("elapsed %v; Retry-After 1 must be waited out", elapsed)
	}
}

// A typed reason that is not a gate refusal is definite, even on 409 and
// even when its text says "busy".
func TestModern_OtherTypedReasonsAreRefusals(t *testing.T) {
	for _, ref := range []typedRefusal{
		{http.StatusConflict, "volume_snapshot_limit", "Snapshot limit reached (3/3). Delete an existing snapshot first.", ""},
		{http.StatusConflict, "volume_backup_limit", "Backup limit reached (3/3). Delete an existing backup first.", ""},
		{http.StatusUnprocessableEntity, "volume_operation_refused", "The node is busy and refused the operation.", ""},
	} {
		t.Run(ref.reason, func(t *testing.T) {
			f, c := setup(t)
			f.typed = []typedRefusal{ref}
			_, err := AttachVolume(context.Background(), c, "vol-1", "inst-1", fast)
			var refused *RefusedError
			if !errors.As(err, &refused) || refused.Reason != ref.reason || refused.Status != ref.status {
				t.Fatalf("err = %v (%T); want *RefusedError %d %s", err, err, ref.status, ref.reason)
			}
			if got := f.writes(); got != 1 {
				t.Fatalf("mutations = %d; a refusal is never re-sent", got)
			}
		})
	}
}

// End to end against the modern fake: the cooldown after a completed
// operation is announced with Retry-After and waited out.
func TestModern_CompletionCooldownCycle(t *testing.T) {
	f, c := setup(t)
	noMargin(t)
	f.modern = true
	f.cooldownWindow = 300 * time.Millisecond

	if _, err := AttachVolume(context.Background(), c, "vol-1", "inst-1", fast); err != nil {
		t.Fatalf("attach: %v", err)
	}
	if _, err := DetachVolume(context.Background(), c, "vol-1", fast); err != nil {
		t.Fatalf("detach: %v", err)
	}
	if got := f.count("POST /storage/volume/vol-1/detach"); got != 2 {
		t.Fatalf("detach requests = %d; want 2 (429 cooldown, then accepted)", got)
	}
}

// Older masters: the in-flight limit arrives as 422 volume_operation_rejected
// with the gate's text.
func TestLegacy_InflightTextBacksOffAndAsksAgain(t *testing.T) {
	f, c := setup(t)
	f.refusals = []refusal{{422, "volume_operation_rejected", "Too many in-flight volume operations (3). Wait for some to finish."}}

	if _, err := AttachVolume(context.Background(), c, "vol-1", "inst-1", fast); err != nil {
		t.Fatalf("attach: %v", err)
	}
	if got := f.writes(); got != 2 {
		t.Fatalf("mutations = %d; want 2", got)
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		e    client.APIError
		want gateKind
	}{
		{"typed busy", client.APIError{Status: 409, Reason: "volume_busy", Message: "Please wait."}, gateBusy},
		{"typed cooldown", client.APIError{Status: 429, Reason: "volume_cooldown", Message: "x"}, gateCooldown},
		{"typed inflight", client.APIError{Status: 429, Reason: "account_inflight_limit", Message: "x"}, gateInflight},
		{"legacy busy", client.APIError{Status: 422, Reason: "volume_operation_rejected", Message: "Volume is busy with operation: attach. Please wait."}, gateBusy},
		{"legacy cooldown", client.APIError{Status: 422, Reason: "volume_operation_rejected", Message: "Volume is in cooldown. Try again in 12 seconds."}, gateCooldown},
		{"legacy inflight", client.APIError{Status: 422, Reason: "volume_operation_rejected", Message: "Too many in-flight volume operations (3). Wait for some to finish."}, gateInflight},
		{"no reason busy text", client.APIError{Status: 409, Message: "Volume is busy with operation: attach. Please wait."}, gateBusy},
		{"bare 429", client.APIError{Status: 429, Message: "Too Many Attempts."}, gateCooldown},
		{"typed other 409 with busy text", client.APIError{Status: 409, Reason: "volume_snapshot_limit", Message: "busy"}, gateNone},
		{"typed other 422 with cooldown text", client.APIError{Status: 422, Reason: "volume_attached", Message: "cooldown 5 seconds"}, gateNone},
		{"legacy plain refusal", client.APIError{Status: 422, Reason: "volume_operation_rejected", Message: "Volume must be in available status to attach."}, gateNone},
		{"plain 409", client.APIError{Status: 409, Message: "Conflict"}, gateNone},
	}
	for _, tc := range cases {
		if got := classify(&tc.e); got != tc.want {
			t.Errorf("%s: classify = %v; want %v", tc.name, got, tc.want)
		}
	}
}

func TestGateWait(t *testing.T) {
	const backoff = 7 * time.Millisecond
	cases := []struct {
		name string
		e    client.APIError
		kind gateKind
		want time.Duration
	}{
		{"cooldown header", client.APIError{RetryAfter: 42 * time.Second, Message: "Try again in 1 seconds (cooldown)"}, gateCooldown, 42*time.Second + cooldownMargin},
		{"cooldown header clamped high", client.APIError{RetryAfter: 99999 * time.Second}, gateCooldown, maxGateWait + cooldownMargin},
		{"cooldown header clamped low", client.APIError{RetryAfter: 10 * time.Millisecond}, gateCooldown, minGateWait + cooldownMargin},
		{"cooldown text", client.APIError{Message: "Volume is in cooldown. Try again in 12 seconds."}, gateCooldown, 12*time.Second + cooldownMargin},
		{"cooldown text clamped", client.APIError{Message: "Volume is in cooldown. Try again in 464.776842 seconds."}, gateCooldown, maxGateWait + cooldownMargin},
		{"cooldown nothing", client.APIError{Message: "Too Many Attempts."}, gateCooldown, cooldownFallback},
		{"busy no header", client.APIError{}, gateBusy, backoff},
		{"busy header", client.APIError{RetryAfter: 3 * time.Second}, gateBusy, 3 * time.Second},
		{"inflight no header", client.APIError{}, gateInflight, backoff},
		{"inflight header", client.APIError{RetryAfter: 5 * time.Second}, gateInflight, 5 * time.Second},
	}
	for _, tc := range cases {
		if got := gateWait(&tc.e, tc.kind, backoff); got != tc.want {
			t.Errorf("%s: gateWait = %v; want %v", tc.name, got, tc.want)
		}
	}
}

func TestIsUncertain(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"transport", errors.New("connection reset"), true},
		{"500", &client.APIError{Status: 500}, true},
		{"503", &client.APIError{Status: 503}, true},
		{"429", &client.APIError{Status: 429, Reason: "volume_cooldown"}, false},
		{"409", &client.APIError{Status: 409, Reason: "volume_busy"}, false},
		{"pending", &PendingError{Err: context.DeadlineExceeded}, false},
	}
	for _, tc := range cases {
		if got := IsUncertain(tc.err); got != tc.want {
			t.Errorf("%s: IsUncertain = %v; want %v", tc.name, got, tc.want)
		}
	}
}

// ── Admit (snapshot and backup creation) ────────────────────────────────────

type scripted struct {
	status     int
	reason     string
	message    string
	retryAfter string
	hangUp     bool
}

// admitServer answers POST /storage/volume/vol-1/snapshot with the scripted
// answers in order, then 200.
func admitServer(t *testing.T, script []scripted) (*client.Client, func() int) {
	t.Helper()
	var mu sync.Mutex
	sends := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/storage/volume/vol-1/snapshot" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			return
		}
		mu.Lock()
		i := sends
		sends++
		mu.Unlock()
		if i >= len(script) {
			writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Snapshot queued.", "queue": map[string]any{"id": "q-1"}})
			return
		}
		s := script[i]
		if s.hangUp {
			hj, _ := w.(http.Hijacker)
			conn, _, err := hj.Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		if s.retryAfter != "" {
			w.Header().Set("Retry-After", s.retryAfter)
		}
		writeJSON(w, s.status, map[string]any{"success": false, "reason": s.reason, "message": s.message})
	}))
	t.Cleanup(srv.Close)
	c := client.New(srv.URL, "test-token", 5*time.Second, false)
	return c, func() int { mu.Lock(); defer mu.Unlock(); return sends }
}

func admitSnapshot(ctx context.Context, c *client.Client, o Options) error {
	return Admit(ctx, "vol-1", Snapshot, o, func(ctx context.Context) error {
		_, err := c.CreateVolumeSnapshot(ctx, "vol-1", map[string]any{"name": "s"})
		return err
	})
}

func TestAdmit_WaitsOutBusyCooldownAndInflight(t *testing.T) {
	noMargin(t)
	c, sends := admitServer(t, []scripted{
		{status: 409, reason: "volume_busy", message: "Volume is busy with operation: backup. Please wait."},
		{status: 429, reason: "volume_cooldown", message: "Volume is in cooldown. Try again in 1 seconds.", retryAfter: "1"},
		{status: 429, reason: "account_inflight_limit", message: "Too many in-flight volume operations (3). Wait for some to finish."},
	})
	start := time.Now()
	if err := admitSnapshot(context.Background(), c, fast); err != nil {
		t.Fatalf("admit: %v", err)
	}
	if elapsed := time.Since(start); elapsed < time.Second {
		t.Fatalf("elapsed %v; Retry-After 1 must be waited out", elapsed)
	}
	if got := sends(); got != 4 {
		t.Fatalf("sends = %d; want 4 (three transient refusals, then accepted)", got)
	}
}

func TestAdmit_LegacyTextShapes(t *testing.T) {
	noMargin(t)
	c, sends := admitServer(t, []scripted{
		{status: 422, reason: "volume_operation_rejected", message: "Volume is busy with operation: backup. Please wait."},
		{status: 422, reason: "volume_operation_rejected", message: "Volume is in cooldown. Try again in 0.1 seconds."},
		{status: 422, reason: "volume_operation_rejected", message: "Too many in-flight volume operations (3). Wait for some to finish."},
	})
	if err := admitSnapshot(context.Background(), c, fast); err != nil {
		t.Fatalf("admit: %v", err)
	}
	if got := sends(); got != 4 {
		t.Fatalf("sends = %d; want 4", got)
	}
}

func TestAdmit_DefiniteRefusalReturnedUnchanged(t *testing.T) {
	for _, s := range []scripted{
		{status: 409, reason: "volume_snapshot_limit", message: "Snapshot limit reached (3/3). Delete an existing snapshot first."},
		{status: 422, reason: "volume_operation_rejected", message: "Volume must be available."},
		{status: 403, message: "The volume is busy for billing reasons."},
		{status: 404, reason: "not_found", message: "Volume not found."},
	} {
		c, sends := admitServer(t, []scripted{s})
		err := admitSnapshot(context.Background(), c, fast)
		var apiErr *client.APIError
		if !errors.As(err, &apiErr) || apiErr.Status != s.status || apiErr.Reason != s.reason {
			t.Fatalf("%d %s: err = %v; want the APIError unchanged", s.status, s.reason, err)
		}
		if got := sends(); got != 1 {
			t.Fatalf("%d %s: sends = %d; a definite refusal is never re-sent", s.status, s.reason, got)
		}
	}
}

func TestAdmit_UncertainAnswerIsNeverResent(t *testing.T) {
	for _, s := range []scripted{
		{status: 500, message: "Server Error"},
		{status: 503, message: "Unavailable"},
		// A 5xx is uncertain even when its text reads like a gate refusal.
		{status: 503, message: "The server is busy, try again later."},
		{status: 503, reason: "volume_busy", message: "Volume is busy with operation: backup."},
		{hangUp: true},
	} {
		c, sends := admitServer(t, []scripted{s})
		err := admitSnapshot(context.Background(), c, fast)
		if !IsUncertain(err) {
			t.Fatalf("%+v: err = %v (%T); want an uncertain answer", s, err, err)
		}
		if got := sends(); got != 1 {
			t.Fatalf("%+v: sends = %d; an uncertain answer is never replayed", s, got)
		}
	}
}

func TestAdmit_RetryAfterBeyondDeadlineIsPendingAtOnce(t *testing.T) {
	c, sends := admitServer(t, []scripted{{status: 429, reason: "volume_cooldown", message: "Volume is in cooldown. Try again in 287 seconds.", retryAfter: "287"}})
	start := time.Now()
	err := admitSnapshot(context.Background(), c, Options{Interval: time.Millisecond, Timeout: 2 * time.Second})
	var pending *PendingError
	if !errors.As(err, &pending) {
		t.Fatalf("err = %v (%T); want *PendingError", err, err)
	}
	if !strings.Contains(err.Error(), "nothing was recorded") {
		t.Fatalf("message %q must say nothing was recorded", err.Error())
	}
	if IsUncertain(err) {
		t.Fatal("an admission deadline is definite, never uncertain")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("elapsed %v; the wait must not run into the deadline", elapsed)
	}
	if got := sends(); got != 1 {
		t.Fatalf("sends = %d; want 1", got)
	}
}
