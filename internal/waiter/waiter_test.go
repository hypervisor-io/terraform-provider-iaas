package waiter_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iaas/terraform-provider-iaas/internal/waiter"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

// countingRefresh returns done on the Nth call (1-based) and records how many
// times it has been invoked.
func countingRefresh(doneOnCall int, calls *int32) func() (string, bool, error) {
	return func() (string, bool, error) {
		n := int(atomic.AddInt32(calls, 1))
		if n >= doneOnCall {
			return "active", true, nil
		}
		return "pending", false, nil
	}
}

// ─── WaitFor tests ───────────────────────────────────────────────────────────

// TestConvergesAfterNPolls: Refresh signals done on the Nth call; WaitFor must
// return nil and Refresh must have been called exactly N times.
func TestConvergesAfterNPolls(t *testing.T) {
	const N = 4
	var calls int32

	opts := waiter.Options{
		Interval: time.Millisecond,
		Timeout:  5 * time.Second,
		Refresh:  countingRefresh(N, &calls),
	}

	err := waiter.WaitFor(context.Background(), opts)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if got := int(atomic.LoadInt32(&calls)); got != N {
		t.Fatalf("expected Refresh called %d times, got %d", N, got)
	}
}

// TestFailStateReturnsError: StatePoller detects a fail state and WaitFor must
// surface it as a non-nil error whose message contains the fail state name.
// This exercises the full StatePoller→WaitFor path end-to-end.
func TestFailStateReturnsError(t *testing.T) {
	get := func() (map[string]any, error) {
		return map[string]any{"state": "error"}, nil
	}
	refresh := waiter.StatePoller(get, "state", []string{"deployed"}, []string{"error"})

	opts := waiter.Options{
		Interval: time.Millisecond,
		Timeout:  5 * time.Second,
		Refresh:  refresh,
	}

	err := waiter.WaitFor(context.Background(), opts)
	if err == nil {
		t.Fatal("expected an error for fail state, got nil")
	}
	if !strings.Contains(err.Error(), "error") {
		t.Fatalf("expected error message to contain fail state %q, got: %v", "error", err)
	}
}

// TestTimeoutReturnsDeadlineExceeded: Refresh never signals done; Timeout is
// tiny; WaitFor must return quickly with an error that wraps
// context.DeadlineExceeded and names the last observed state.
func TestTimeoutReturnsDeadlineExceeded(t *testing.T) {
	const lastState = "pending"

	opts := waiter.Options{
		Interval: time.Millisecond,
		Timeout:  10 * time.Millisecond,
		Refresh: func() (string, bool, error) {
			return lastState, false, nil
		},
	}

	start := time.Now()
	err := waiter.WaitFor(context.Background(), opts)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected error to wrap context.DeadlineExceeded, got: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("WaitFor took too long: %v (expected fast timeout)", elapsed)
	}
	if !strings.Contains(err.Error(), lastState) {
		t.Fatalf("timeout error %q does not mention last state %q", err.Error(), lastState)
	}
}

// TestCtxCancelReturnsContextCanceled: cancelling ctx mid-wait must cause
// WaitFor to return promptly with context.Canceled.
func TestCtxCancelReturnsContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	var calls int32
	opts := waiter.Options{
		Interval: 50 * time.Millisecond, // long enough that we cancel before 2nd poll
		Timeout:  30 * time.Second,
		Refresh: func() (string, bool, error) {
			n := atomic.AddInt32(&calls, 1)
			if n == 1 {
				// Cancel after first poll so the waiter is sleeping when cancelled.
				cancel()
			}
			return "pending", false, nil
		},
	}

	start := time.Now()
	err := waiter.WaitFor(ctx, opts)
	elapsed := time.Since(start)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("WaitFor took too long after cancel: %v", elapsed)
	}
}

// ─── StatePoller tests ───────────────────────────────────────────────────────

// TestStatePollerReadyState: when get() returns the ready state the Refresh
// closure must return done=true, err=nil.
func TestStatePollerReadyState(t *testing.T) {
	get := func() (map[string]any, error) {
		return map[string]any{"state": "active"}, nil
	}
	refresh := waiter.StatePoller(get, "state", []string{"active"}, []string{"error"})

	state, done, err := refresh()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !done {
		t.Fatalf("expected done=true for ready state, got false (state=%q)", state)
	}
	if state != "active" {
		t.Fatalf("expected state=%q, got %q", "active", state)
	}
}

// TestStatePollerFailState: when get() returns the fail state the Refresh
// closure must return done=false and a non-nil error that names the state.
func TestStatePollerFailState(t *testing.T) {
	get := func() (map[string]any, error) {
		return map[string]any{"state": "error"}, nil
	}
	refresh := waiter.StatePoller(get, "state", []string{"active"}, []string{"error"})

	state, done, err := refresh()
	if err == nil {
		t.Fatal("expected error for fail state, got nil")
	}
	if done {
		t.Fatal("expected done=false for fail state")
	}
	if !strings.Contains(err.Error(), "error") {
		t.Fatalf("error message %q does not name fail state %q", err.Error(), state)
	}
}

// TestStatePollerUnknownState: an unrecognised state must keep polling —
// done=false, err=nil.
func TestStatePollerUnknownState(t *testing.T) {
	get := func() (map[string]any, error) {
		return map[string]any{"state": "building"}, nil
	}
	refresh := waiter.StatePoller(get, "state", []string{"active"}, []string{"error"})

	state, done, err := refresh()
	if err != nil {
		t.Fatalf("unexpected error for unknown state: %v", err)
	}
	if done {
		t.Fatalf("expected done=false for unknown state %q", state)
	}
}

// TestStatePollerGetError: when get() returns an error it must be propagated
// as the Refresh error.
func TestStatePollerGetError(t *testing.T) {
	getErr := errors.New("network error")
	get := func() (map[string]any, error) {
		return nil, getErr
	}
	refresh := waiter.StatePoller(get, "state", []string{"active"}, []string{"error"})

	_, done, err := refresh()
	if !errors.Is(err, getErr) {
		t.Fatalf("expected getErr to be propagated, got: %v", err)
	}
	if done {
		t.Fatal("expected done=false on get error")
	}
}

// TestStatePollerMissingField: when the field is absent from the map, treat as
// empty state and keep polling (done=false, err=nil, no panic).
func TestStatePollerMissingField(t *testing.T) {
	get := func() (map[string]any, error) {
		return map[string]any{"other": "value"}, nil
	}
	refresh := waiter.StatePoller(get, "state", []string{"active"}, []string{"error"})

	_, done, err := refresh()
	if err != nil {
		t.Fatalf("unexpected error for missing field: %v", err)
	}
	if done {
		t.Fatal("expected done=false for missing field")
	}
}

// TestStatePollerNonStringField: when the field value is not a string, treat
// as empty state and keep polling (done=false, err=nil, no panic).
func TestStatePollerNonStringField(t *testing.T) {
	get := func() (map[string]any, error) {
		return map[string]any{"state": 42}, nil
	}
	refresh := waiter.StatePoller(get, "state", []string{"active"}, []string{"error"})

	_, done, err := refresh()
	if err != nil {
		t.Fatalf("unexpected error for non-string field: %v", err)
	}
	if done {
		t.Fatal("expected done=false for non-string field")
	}
}
