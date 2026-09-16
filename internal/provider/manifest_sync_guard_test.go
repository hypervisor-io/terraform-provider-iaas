package provider_test

// Tri-sync sync guard (spec 17). Pins the sha256 of the vendored copy of the
// platform's api-manifest.json to the Master repo's current file content. The
// vendor copy is refreshed via `make sync-manifest` (which copies Master's
// api-manifest.json into testdata/api-manifest.json). If the two ever diverge
// (a hand-edited copy, a stale sync, or a Master change the operator forgot to
// propagate) this test fails with a one-line fix instruction.
//
// The Master repo is private, so the manifest is vendored (a committed copy)
// rather than fetched in CI. The binding is the content sha256, NOT a Master
// commit sha, because Master's api-manifest.json is frequently uncommitted
// while a worktree is alive. To update after a Master change:
//   make sync-manifest MASTER_MANIFEST=/path/to/master/api-manifest.json
//   sha256sum testdata/api-manifest.json
// then paste the new hash into expectedManifestSha256 below.

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"
)

// expectedManifestSha256 is the sha256 of the Master repo api-manifest.json
// this vendored copy must match byte-for-byte.
const expectedManifestSha256 = "6a1d5336db6a594c4fa3f5335cf2c6d4e3c0a1f9d23e8a6be773818a0bf5aa6c"

func TestManifestSyncGuard(t *testing.T) {
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("reading %s: %v", manifestPath, err)
	}

	sum := sha256.Sum256(raw)
	got := hex.EncodeToString(sum[:])
	if got != expectedManifestSha256 {
		t.Fatalf(
			"vendored api-manifest.json (%s) is out of sync with Master's current file:\n  got      %s\n  expected %s\nrun `make sync-manifest` then update expectedManifestSha256 to the new hash.",
			manifestPath, got, expectedManifestSha256,
		)
	}
}
