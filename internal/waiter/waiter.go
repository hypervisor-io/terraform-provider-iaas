// Package waiter provides a generic async convergence poller for resources that
// are modified by enqueuing a platform task and then converge to a terminal
// state asynchronously.
//
// It is framework-agnostic and client-agnostic: callers supply closures so the
// package has no dependency on terraform-plugin-framework or internal/client.
package waiter

import (
	"context"
	"fmt"
	"time"
)

const (
	backoffFactor = 1.5
	backoffCap    = 30 * time.Second
)

// Options configures a [WaitFor] run.
type Options struct {
	// Interval is the base poll interval (must be > 0).
	Interval time.Duration

	// Timeout is the overall deadline enforced by the waiter itself.
	// 0 means no waiter-imposed timeout; the caller's ctx governs instead.
	Timeout time.Duration

	// Refresh polls once. It returns:
	//   state: current state string (used in diagnostic/timeout messages)
	//   done:  true when the target ready condition is met
	//   err:   non-nil for a terminal failure (fail-state or unrecoverable error)
	Refresh func() (state string, done bool, err error)
}

// WaitFor polls opts.Refresh on opts.Interval until done, a terminal error,
// the timeout, or ctx cancellation.
//
//   - done==true       → returns nil
//   - Refresh err!=nil → returns that err (wrapped with last state for context)
//   - timeout reached  → returns an error wrapping context.DeadlineExceeded that
//     names the last observed state
//   - ctx canceled     → returns ctx.Err()
//
// Between polls it applies a light capped exponential backoff starting at
// Interval, and every sleep is interruptible by ctx / timeout cancellation.
func WaitFor(ctx context.Context, opts Options) error {
	// Wrap ctx with our own timeout when the caller requests one.
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	interval := opts.Interval
	var lastState string

	for {
		// ── first action: poll immediately ───────────────────────────────
		state, done, err := opts.Refresh()
		lastState = state

		switch {
		case err != nil:
			// Terminal failure — wrap with last state for diagnostics.
			return fmt.Errorf("waiter: resource in state %q: %w", lastState, err)
		case done:
			return nil
		}

		// ── check context before sleeping ────────────────────────────────
		if ctxErr := ctx.Err(); ctxErr != nil {
			return deadlineOrCanceled(ctx, lastState)
		}

		// ── interruptible sleep ──────────────────────────────────────────
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return deadlineOrCanceled(ctx, lastState)
		case <-timer.C:
		}

		// ── advance backoff for next iteration ───────────────────────────
		interval = nextInterval(interval)
	}
}

// deadlineOrCanceled distinguishes a waiter-imposed timeout (DeadlineExceeded)
// from an upstream ctx cancellation and returns an appropriately worded error.
func deadlineOrCanceled(ctx context.Context, lastState string) error {
	if ctx.Err() == context.DeadlineExceeded {
		// Wrap DeadlineExceeded so errors.Is works, but add human context.
		return fmt.Errorf("waiter: timed out waiting for resource; last observed state: %q: %w",
			lastState, context.DeadlineExceeded)
	}
	return ctx.Err() // context.Canceled (or other)
}

// nextInterval advances the backoff interval by backoffFactor, capped at
// backoffCap.
func nextInterval(d time.Duration) time.Duration {
	next := time.Duration(float64(d) * backoffFactor)
	if next > backoffCap {
		return backoffCap
	}
	return next
}

// StatePoller builds a Refresh closure from a getter and ready/fail state sets.
//
//   - get   fetches the resource as a map (e.g. a thin client.Get<Resource> call)
//   - field is the key holding the state string (e.g. "state", "status")
//   - ready are the states meaning success
//   - fail  are the states meaning terminal failure
//
// Behavior of the returned Refresh:
//
//   - get() error          → ("", false, err)   terminal; the client already retried
//   - state in fail        → (state, false, error describing the fail state)
//   - state in ready       → (state, true, nil)
//   - otherwise            → (state, false, nil) keep polling
//   - missing/non-string   → ("", false, nil)   keep polling, no panic
func StatePoller(
	get func() (map[string]any, error),
	field string,
	ready []string,
	fail []string,
) func() (state string, done bool, err error) {
	readySet := toSet(ready)
	failSet := toSet(fail)

	return func() (string, bool, error) {
		m, err := get()
		if err != nil {
			return "", false, err
		}

		// Safely extract the state string; any problem → empty string.
		state := ""
		if v, ok := m[field]; ok {
			if s, ok := v.(string); ok {
				state = s
			}
		}

		switch {
		case failSet[state]:
			return state, false, fmt.Errorf("resource entered fail state: %q", state)
		case readySet[state]:
			return state, true, nil
		default:
			return state, false, nil
		}
	}
}

// toSet converts a slice of strings into a lookup map.
func toSet(ss []string) map[string]bool {
	m := make(map[string]bool, len(ss))
	for _, s := range ss {
		m[s] = true
	}
	return m
}
