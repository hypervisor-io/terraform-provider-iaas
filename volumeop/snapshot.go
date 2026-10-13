package volumeop

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
)

// Snapshot creation is identified by NAME: the create answers only a queue
// handle, so the consumers resolve the new snapshot's id by the name they
// sent. The platform does NOT enforce unique snapshot names per volume, so a
// name lookup can find a snapshot this request never created. Two helpers keep
// the consumers from adopting (and later deleting) such a foreign snapshot:
//
//   - CheckSnapshotNameFree, before the first send: refuses when a snapshot of
//     that name already exists on the volume, and returns the ids it saw;
//   - AdoptAfterUncertain, after an uncertain create answer (5xx, transport):
//     adopts only a snapshot of that name whose id was NOT seen before the
//     send, and only when exactly one exists.
//
// Residual: a same-name snapshot created by someone else between the check
// and an uncertain answer cannot be told apart from ours; when both exist the
// adoption reports the ambiguity instead of guessing.

// adoptLookupTimeout bounds the read-back after an uncertain answer. It runs
// on a fresh context so an expired create deadline cannot turn a recorded
// snapshot into an orphan. A var only so tests can shrink it.
var adoptLookupTimeout = 30 * time.Second

// SnapshotNameTakenError refuses a snapshot create whose name is already used
// on the volume.
type SnapshotNameTakenError struct {
	VolumeID string
	Name     string
	IDs      []string
}

func (e *SnapshotNameTakenError) Error() string {
	return fmt.Sprintf("a snapshot named %q already exists on volume %s (id %s); snapshot names must be unique per volume "+
		"(the name is how the new snapshot is found), choose another name or import the existing snapshot",
		e.Name, e.VolumeID, strings.Join(e.IDs, ", "))
}

// ErrSnapshotNameEmpty refuses a snapshot create without a name.
var ErrSnapshotNameEmpty = errors.New("a snapshot name is required: the new snapshot is found by its name, " +
	"and without one the platform would name it itself and it could not be found")

// SnapshotIDsNamed returns the ids of the volume's snapshots named name (read
// from the volume SHOW's embedded snapshots[]), sorted.
func SnapshotIDsNamed(ctx context.Context, c *client.Client, volumeID, name string) ([]string, error) {
	vol, err := c.GetVolume(ctx, volumeID)
	if err != nil {
		return nil, err
	}
	raw, _ := vol["snapshots"].([]any)
	var ids []string
	for _, e := range raw {
		s, ok := e.(map[string]any)
		if !ok {
			continue
		}
		// A null, missing or non-string name never matches (not even "").
		if n, ok := s["name"].(string); !ok || n != name {
			continue
		}
		if id, _ := s["id"].(string); id != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// CheckSnapshotNameFree runs before the first create send. It returns the ids
// of same-name snapshots it saw (the "before" set for AdoptAfterUncertain)
// and a *SnapshotNameTakenError when there are any.
func CheckSnapshotNameFree(ctx context.Context, c *client.Client, volumeID, name string) ([]string, error) {
	if name == "" {
		// The API would name it itself (snap-<id>-<time>) and the new
		// snapshot could never be found by name: it would be orphaned.
		return nil, ErrSnapshotNameEmpty
	}
	ids, err := SnapshotIDsNamed(ctx, c, volumeID, name)
	if err != nil {
		return nil, fmt.Errorf("reading the snapshots of volume %s before creating %q: %w", volumeID, name, err)
	}
	if len(ids) > 0 {
		return ids, &SnapshotNameTakenError{VolumeID: volumeID, Name: name, IDs: ids}
	}
	return ids, nil
}

// SnapshotIDsBeforeSend refreshes the pre-send set right before a repeated
// send (after a transient admission refusal): it returns before plus every
// same-name id on the volume now. Our own create was refused definitely, so
// nothing in the result is ours; a snapshot someone else created meanwhile is
// thereby excluded from adoption and resolution. A failed read keeps before
// (the set only ever grows).
func SnapshotIDsBeforeSend(ctx context.Context, c *client.Client, volumeID, name string, before []string) []string {
	ids, err := SnapshotIDsNamed(ctx, c, volumeID, name)
	if err != nil {
		return before
	}
	seen := make(map[string]bool, len(before)+len(ids))
	out := make([]string, 0, len(before)+len(ids))
	for _, id := range append(append([]string{}, before...), ids...) {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// NewSnapshotID returns the single snapshot named name whose id is not in
// before. found is false while there is none; several are an error (which one
// is ours cannot be told). Used to resolve the id after a successful create
// and, through AdoptAfterUncertain, after an uncertain one.
func NewSnapshotID(ctx context.Context, c *client.Client, volumeID, name string, before []string) (id string, found bool, err error) {
	ids, err := SnapshotIDsNamed(ctx, c, volumeID, name)
	if err != nil {
		return "", false, err
	}
	seen := make(map[string]bool, len(before))
	for _, b := range before {
		seen[b] = true
	}
	var fresh []string
	for _, x := range ids {
		if !seen[x] {
			fresh = append(fresh, x)
		}
	}
	switch len(fresh) {
	case 0:
		return "", false, nil
	case 1:
		return fresh[0], true, nil
	default:
		return "", false, &AmbiguousSnapshotError{VolumeID: volumeID, Name: name, IDs: fresh}
	}
}

// AmbiguousSnapshotError: more than one new snapshot carries the name.
type AmbiguousSnapshotError struct {
	VolumeID string
	Name     string
	IDs      []string
}

func (e *AmbiguousSnapshotError) Error() string {
	return fmt.Sprintf("%d new snapshots named %q exist on volume %s (%s); which one this request created cannot be told",
		len(e.IDs), e.Name, e.VolumeID, strings.Join(e.IDs, ", "))
}

// AdoptAfterUncertain decides, after a create answer that was not definite,
// whether this request recorded a snapshot: exactly one snapshot named name
// whose id is not in before. It reads on a fresh context (ctx's values kept,
// its cancellation and deadline dropped) bounded by 30 seconds. The error
// says which case it was: the lookup failed, no new snapshot exists, or more
// than one does.
func AdoptAfterUncertain(ctx context.Context, c *client.Client, volumeID, name string, before []string) (string, error) {
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), adoptLookupTimeout)
	defer cancel()
	id, found, err := NewSnapshotID(readCtx, c, volumeID, name, before)
	var amb *AmbiguousSnapshotError
	switch {
	case errors.As(err, &amb):
		return "", err
	case err != nil:
		return "", fmt.Errorf("the snapshots of volume %s could not be read back: %w", volumeID, err)
	case !found:
		return "", fmt.Errorf("no new snapshot named %q exists on volume %s, so the request was most likely not recorded", name, volumeID)
	}
	return id, nil
}
