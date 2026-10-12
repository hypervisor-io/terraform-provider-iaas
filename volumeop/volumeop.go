// Package volumeop drives a user volume mutation (attach, detach, resize,
// delete) to its CONFIRMED outcome.
//
// The platform API accepts these four mutations asynchronously: a 200 means the
// operation was recorded and sent to the node, the volume's "active_operation"
// field names it while it is pending, and the volume's size, plan or attachment
// (or the volume itself, for delete) change only when the node confirms. A node
// failure clears "active_operation" and leaves the volume exactly as it was.
// A definite refusal is an HTTP error with a typed "reason" and changes nothing.
//
// Both consumers of the API (the OpenTofu provider and the MCP server) call
// this package, so the two converge on one definition of "done", "failed" and
// "refused" (spec 17: one behaviour, two consumers).
//
// Outcome rules, decided from GET /storage/volume/{id} only:
//
//   - active_operation equals the requested action: still pending, keep polling.
//   - otherwise the desired state is observed (attachment / plan / 404 for
//     delete): confirmed success.
//   - otherwise: the operation ended without the requested change, which is
//     the node failure outcome (*FailedError). The volume kept its previous
//     state and billing.
//   - the deadline passes while the operation is still pending: *PendingError.
//     The operation is NOT abandoned or retried; it may still complete.
//
// A refused request (any 4xx other than the transient gate cases below) is a
// *RefusedError carrying the API reason code.
//
// Transient admission refusals are waited out, never surfaced, while the
// deadline allows:
//
//   - another volume operation is in flight (the refusal is followed by a read
//     that shows a non-null active_operation): wait until it ends, then either
//     observe that the desired state already holds or send the request again
//     (when the in-flight operation was this same action and it ended without
//     the change, that is reported as the node failure instead);
//   - a busy refusal while the read shows no marker (the API records the
//     pending operation before it publishes active_operation, or the other
//     operation just ended): back off and ask again;
//   - the per-volume cooldown after a completed operation: the API refuses
//     with a message naming the remaining seconds (or HTTP 429); wait that
//     long and send again.
//
// After ANY refusal the volume is read back first: when it is idle and already
// in the requested state (a completion raced the refusal), that is success.
//
// An uncertain answer (HTTP 5xx or a transport error; the client sends these
// four commands in ONE attempt, never replaying them) is never treated as a
// refusal and never re-sent: the volume is read back and the wait continues
// when the operation turns out to be pending, succeeds when the requested
// state is already observed, and otherwise ends in *UnknownOutcomeError.
package volumeop

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
	"github.com/hypervisor-io/terraform-provider-iaas/waiter"
)

// Action is a user volume lifecycle mutation. The values are exactly the
// lifecycle values the API writes into the volume's active_operation field.
type Action string

const (
	Attach Action = "attach"
	Detach Action = "detach"
	Resize Action = "resize"
	Delete Action = "delete"
)

const (
	// defaultInterval is the base poll interval when Options.Interval is unset.
	defaultInterval = 5 * time.Second
	// maxConsecutiveReadErrors is how many transport blips in a row a poll
	// tolerates before the read error becomes terminal.
	maxConsecutiveReadErrors = 3
)

// Variables (not constants) only so the package tests can shrink them.
var (
	// cooldownFallback is the wait used when a cooldown refusal names no
	// parsable number of seconds.
	cooldownFallback = 30 * time.Second
	// cooldownMargin is added to the server's remaining cooldown so a small
	// clock or rounding difference does not cause one wasted refusal.
	cooldownMargin = 2 * time.Second
)

// Options bounds one confirmed operation.
type Options struct {
	// Interval is the base poll interval (capped exponential backoff applies,
	// see package waiter). Zero means 5 seconds.
	Interval time.Duration
	// Timeout bounds the WHOLE operation: admission waits (busy, cooldown) plus
	// the completion poll. Zero means the caller's ctx governs.
	Timeout time.Duration
}

// RefusedError is a definite refusal: the API answered the request with a 4xx
// and nothing was changed. Reason is the API's typed reason code.
type RefusedError struct {
	Action   Action
	VolumeID string
	Status   int
	Reason   string
	Err      error
}

func (e *RefusedError) Error() string {
	reason := e.Reason
	if reason == "" {
		reason = "none"
	}
	return fmt.Sprintf("volume %s %s refused (HTTP %d, reason: %s): %v", e.VolumeID, e.Action, e.Status, reason, e.Err)
}

func (e *RefusedError) Unwrap() error { return e.Err }

// FailedError means the API accepted the operation but it ended without the
// requested change: the node reported a failure (or the platform could not
// apply it) and the volume kept its previous size, plan, attachment and
// billing. Volume is the last observed object.
type FailedError struct {
	Action   Action
	VolumeID string
	Volume   map[string]any
}

func (e *FailedError) Error() string {
	return fmt.Sprintf("volume %s %s failed on the node: the operation ended without the requested change; "+
		"the volume kept its previous state (status %q)", e.VolumeID, e.Action, stringField(e.Volume, "status"))
}

// PendingError means the deadline passed while the operation was still
// pending. The operation is not cancelled and may still complete; read the
// volume (its active_operation field) to follow it.
type PendingError struct {
	Action          Action
	VolumeID        string
	ActiveOperation string
	Err             error
}

func (e *PendingError) Error() string {
	if e.ActiveOperation != "" {
		return fmt.Sprintf("timed out waiting for volume %s %s to complete: active_operation is still %q; "+
			"the operation was not cancelled and may still complete (read the volume to follow it): %v",
			e.VolumeID, e.Action, e.ActiveOperation, e.Err)
	}
	return fmt.Sprintf("timed out before volume %s %s could be confirmed: %v", e.VolumeID, e.Action, e.Err)
}

func (e *PendingError) Unwrap() error { return e.Err }

// UnknownOutcomeError means the request got no definite answer (HTTP 5xx or a
// transport error) and reading the volume back showed neither a pending
// operation nor the requested state. The request may never have been recorded,
// or it may have been recorded and already failed; it is NOT sent again.
type UnknownOutcomeError struct {
	Action   Action
	VolumeID string
	Detail   string
}

func (e *UnknownOutcomeError) Error() string {
	return fmt.Sprintf("volume %s %s: outcome unknown (%s); the request was not repeated, read the volume and retry if needed",
		e.VolumeID, e.Action, e.Detail)
}

// ErrGone is returned by Settle when the volume reads 404.
var ErrGone = errors.New("volume not found")

// Settle waits until the volume has no active_operation and returns it, or
// ErrGone when it reads 404. Use it before a decision that depends on the
// volume's settled attachment (e.g. whether a destroy must detach first).
func Settle(ctx context.Context, c *client.Client, id string, o Options) (map[string]any, error) {
	if o.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Timeout)
		defer cancel()
	}
	interval := o.Interval
	if interval <= 0 {
		interval = defaultInterval
	}
	v, err := waitIdle(ctx, c, id, interval)
	if errors.Is(err, errGone) {
		return nil, ErrGone
	}
	if err != nil {
		return v, &PendingError{Action: "settle", VolumeID: id, ActiveOperation: stringField(v, "active_operation"), Err: err}
	}
	return v, nil
}

// AttachVolume attaches volume id to instanceID and waits for the confirmed
// attachment. It returns the last observed volume object.
func AttachVolume(ctx context.Context, c *client.Client, id, instanceID string, o Options) (map[string]any, error) {
	return run(ctx, c, id, Attach, o,
		func(ctx context.Context) error {
			_, err := c.AttachVolume(ctx, id, map[string]any{"instance_id": instanceID})
			return err
		},
		func(v map[string]any) bool {
			return stringField(v, "instance_id") == instanceID && stringField(v, "status") == "attached"
		})
}

// DetachVolume detaches volume id from its instance and waits for the
// confirmed detachment. It returns the last observed volume object.
func DetachVolume(ctx context.Context, c *client.Client, id string, o Options) (map[string]any, error) {
	return run(ctx, c, id, Detach, o,
		func(ctx context.Context) error {
			_, err := c.DetachVolume(ctx, id)
			return err
		},
		func(v map[string]any) bool {
			return stringField(v, "instance_id") == "" && stringField(v, "status") != "attached"
		})
}

// ResizeVolume moves volume id to volumePlanID and waits until the node
// confirmed the new capacity (the API switches volume_plan_id and size
// together, only on that confirmation). It returns the last observed volume.
//
// A resize to the plan the volume already has cannot be told apart from a
// failed one by the volume alone; such a request is reported as done once the
// operation ends.
func ResizeVolume(ctx context.Context, c *client.Client, id, volumePlanID string, o Options) (map[string]any, error) {
	return run(ctx, c, id, Resize, o,
		func(ctx context.Context) error {
			_, err := c.ResizeVolume(ctx, id, map[string]any{"volume_plan_id": volumePlanID})
			return err
		},
		func(v map[string]any) bool {
			return stringField(v, "volume_plan_id") == volumePlanID
		})
}

// DeleteVolume deletes volume id and waits until the node confirmed the
// deletion (the volume then reads 404). The API refuses to delete an attached
// volume (reason volume_attached); detach it first.
func DeleteVolume(ctx context.Context, c *client.Client, id string, o Options) error {
	_, err := run(ctx, c, id, Delete, o,
		func(ctx context.Context) error { return c.DeleteVolume(ctx, id) },
		func(map[string]any) bool { return false }) // only a 404 is a confirmed delete
	return err
}

// errGone signals that the volume read 404.
var errGone = ErrGone

// run is the shared admission + completion loop.
func run(
	ctx context.Context,
	c *client.Client,
	id string,
	action Action,
	o Options,
	send func(context.Context) error,
	desired func(map[string]any) bool,
) (map[string]any, error) {
	if id == "" {
		return nil, fmt.Errorf("volume %s: empty volume id", action)
	}
	if o.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Timeout)
		defer cancel()
	}
	interval := o.Interval
	if interval <= 0 {
		interval = defaultInterval
	}

	busyBackoff := interval

	// ── admission ────────────────────────────────────────────────────────────
	for {
		sendErr := send(ctx)
		if sendErr == nil {
			break
		}
		if ctx.Err() != nil {
			return nil, &PendingError{Action: action, VolumeID: id, Err: fmt.Errorf("%w (last answer: %v)", ctx.Err(), sendErr)}
		}

		var apiErr *client.APIError
		uncertain := !errors.As(sendErr, &apiErr) || apiErr.Status >= 500
		if uncertain {
			// The request may have been recorded. Read the volume back.
			v, err := read(ctx, c, id)
			switch {
			case errors.Is(err, errGone) && action == Delete:
				return nil, nil
			case err != nil:
				return nil, &UnknownOutcomeError{Action: action, VolumeID: id, Detail: fmt.Sprintf("request answered %v and the volume could not be read back: %v", sendErr, err)}
			case stringField(v, "active_operation") == string(action):
				// Recorded and pending: wait for it below.
			case desired(v):
				return v, nil
			default:
				// Never recorded, or recorded and already ended without the
				// change: the two cannot be told apart, and a resend could
				// repeat a failed operation. Report, do not resend.
				return v, &UnknownOutcomeError{Action: action, VolumeID: id, Detail: fmt.Sprintf("request answered %v; no operation is pending and the requested state is not observed", sendErr)}
			}
			break
		}
		if apiErr.Status == http.StatusUnauthorized || apiErr.Status == http.StatusForbidden || apiErr.Status == http.StatusNotFound {
			return nil, &RefusedError{Action: action, VolumeID: id, Status: apiErr.Status, Reason: apiErr.Reason, Err: apiErr}
		}

		// A 4xx refusal. Decide whether it is a transient admission gate.
		v, err := read(ctx, c, id)
		if errors.Is(err, errGone) && action == Delete {
			return nil, nil
		}
		if err != nil {
			// Cannot tell; surface the refusal itself.
			return nil, &RefusedError{Action: action, VolumeID: id, Status: apiErr.Status, Reason: apiErr.Reason, Err: apiErr}
		}
		if active := stringField(v, "active_operation"); active != "" {
			// Another operation (or an earlier copy of this one) is in flight.
			settled, werr := waitIdle(ctx, c, id, interval)
			if errors.Is(werr, errGone) && action == Delete {
				return nil, nil
			}
			if werr != nil {
				return nil, &PendingError{Action: action, VolumeID: id, ActiveOperation: active, Err: werr}
			}
			if desired(settled) {
				return settled, nil
			}
			if active == string(action) {
				// The in-flight operation was this same action (for example
				// one sent by an earlier, timed-out apply) and it ended
				// without the change: that node failure is the answer to this
				// request too, never a reason to send it again.
				return settled, &FailedError{Action: action, VolumeID: id, Volume: settled}
			}
			continue // a different operation ended and the volume is idle: ask again
		}
		// Idle volume. The refusal may have raced a completion that already
		// produced the requested state (or the request was redundant): decide
		// from state, not from the refusal.
		if desired(v) {
			return v, nil
		}
		if isBusy(apiErr) {
			// Busy with no visible marker: the API records a pending
			// operation row BEFORE it publishes active_operation, and an
			// operation can also end between the refusal and this read. The
			// refusal was definite (nothing of ours was recorded), so asking
			// again after a backoff is safe.
			if err := sleep(ctx, busyBackoff); err != nil {
				return nil, &PendingError{Action: action, VolumeID: id, Err: fmt.Errorf("volume stayed busy until the deadline: %w", err)}
			}
			busyBackoff = nextBackoff(busyBackoff)
			continue
		}
		if wait, ok := cooldownWait(apiErr); ok {
			if err := sleep(ctx, wait); err != nil {
				return nil, &PendingError{Action: action, VolumeID: id, Err: fmt.Errorf("volume cooldown outlasted the deadline: %w", err)}
			}
			continue
		}
		return nil, &RefusedError{Action: action, VolumeID: id, Status: apiErr.Status, Reason: apiErr.Reason, Err: apiErr}
	}

	// ── completion ───────────────────────────────────────────────────────────
	var last map[string]any
	var outcome error
	consecutive := 0
	waitErr := waiter.WaitFor(ctx, waiter.Options{
		Interval: interval,
		Refresh: func() (string, bool, error) {
			v, err := read(ctx, c, id)
			if errors.Is(err, errGone) {
				if action == Delete {
					return "deleted", true, nil
				}
				outcome = fmt.Errorf("volume %s no longer exists while waiting for %s", id, action)
				return "gone", false, outcome
			}
			if err != nil {
				consecutive++
				if consecutive <= maxConsecutiveReadErrors {
					return "unreadable", false, nil
				}
				return "unreadable", false, err
			}
			consecutive = 0
			last = v
			active := stringField(v, "active_operation")
			if active == string(action) {
				return "pending " + active, false, nil
			}
			if desired(v) {
				return "done", true, nil
			}
			outcome = &FailedError{Action: action, VolumeID: id, Volume: v}
			return "failed", false, outcome
		},
	})
	if waitErr != nil {
		if outcome != nil {
			return last, outcome
		}
		if errors.Is(waitErr, context.DeadlineExceeded) || errors.Is(waitErr, context.Canceled) {
			return last, &PendingError{Action: action, VolumeID: id, ActiveOperation: stringField(last, "active_operation"), Err: waitErr}
		}
		return last, fmt.Errorf("waiting for volume %s %s: %w", id, action, waitErr)
	}
	return last, nil
}

// waitIdle polls until the volume has no active_operation and returns it.
func waitIdle(ctx context.Context, c *client.Client, id string, interval time.Duration) (map[string]any, error) {
	var last map[string]any
	var gone bool
	err := waiter.WaitFor(ctx, waiter.Options{
		Interval: interval,
		Refresh: func() (string, bool, error) {
			v, err := read(ctx, c, id)
			if errors.Is(err, errGone) {
				gone = true
				return "gone", false, err
			}
			if err != nil {
				return "unreadable", false, err
			}
			last = v
			if active := stringField(v, "active_operation"); active != "" {
				return "busy " + active, false, nil
			}
			return "idle", true, nil
		},
	})
	if gone {
		return nil, errGone
	}
	return last, err
}

// read GETs the volume, mapping 404 to errGone.
func read(ctx context.Context, c *client.Client, id string) (map[string]any, error) {
	v, err := c.GetVolume(ctx, id)
	if err != nil {
		if client.IsNotFound(err) {
			return nil, errGone
		}
		return nil, err
	}
	return v, nil
}

// busyRe matches the API's busy refusal ("Volume is busy with operation: X.
// Please wait."). The gate's 409 is answered as 422 volume_operation_rejected,
// so the text is the only discriminator until a typed reason exists; a
// volume_busy reason is accepted for that future contract.
var busyRe = regexp.MustCompile(`(?i)\bbusy\b`)

func isBusy(e *client.APIError) bool {
	return e.Reason == "volume_busy" || busyRe.MatchString(e.Message)
}

// nextBackoff grows a wait by 1.5x, capped at 30 seconds (the waiter's curve).
func nextBackoff(d time.Duration) time.Duration {
	n := d * 3 / 2
	if n > 30*time.Second {
		n = 30 * time.Second
	}
	if n <= d {
		n = d + time.Millisecond
	}
	return n
}

// cooldownRe extracts the remaining seconds from the API's cooldown refusal
// ("Volume is in cooldown. Try again in 287 seconds."; the number can carry a
// fraction).
var cooldownRe = regexp.MustCompile(`(?i)cooldown.*?([0-9]+(?:\.[0-9]+)?)\s*seconds?`)

// cooldownWait reports whether a refusal is the per-volume cooldown and how
// long to wait. The API answers the cooldown as reason
// volume_operation_rejected with a message naming the remaining seconds (no
// dedicated reason code exists yet), or as HTTP 429.
func cooldownWait(e *client.APIError) (time.Duration, bool) {
	m := cooldownRe.FindStringSubmatch(e.Message)
	if m == nil {
		if e.Status == http.StatusTooManyRequests {
			return cooldownFallback, true
		}
		return 0, false
	}
	secs, err := strconv.ParseFloat(m[1], 64)
	if err != nil || secs < 0 {
		return cooldownFallback, true
	}
	return time.Duration(secs*float64(time.Second)) + cooldownMargin, true
}

// sleep waits d or until ctx ends.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// stringField reads a string field, treating absent, null and non-string as "".
func stringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}
