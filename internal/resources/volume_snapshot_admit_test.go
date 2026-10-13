package resources_test

import (
	"net/http"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/hypervisor-io/terraform-provider-iaas/internal/acctest"
)

// iaas_volume_snapshot create against the 3.3.5 volume gate contract: the
// create is sent in ONE attempt, the gate's transient refusals (409
// volume_busy, 429 volume_cooldown with Retry-After, 429
// account_inflight_limit) are waited out and the create sent again, any other
// refusal fails with its reason, and an uncertain answer (5xx) is resolved by
// one lookup by name, never by sending the create again.

const (
	foreignSameNameID = "ffffffff-ffff-ffff-ffff-fffffffffffd"
	admitVolumeID     = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaab"
	admitSnapshotID   = "ffffffff-ffff-ffff-ffff-fffffffffffb"
	admitSnapName     = "nightly-admit"
)

type snapAnswer struct {
	status     int
	reason     string
	message    string
	retryAfter string
	record     bool // record the snapshot before answering (uncertain 5xx)
	foreign    bool // a same-name snapshot by someone else appears at this send
}

// snapshotAdmitServer serves the volume SHOW, the snapshot create (answers
// in order, then 200 + record) and the snapshot delete.
func snapshotAdmitServer(t *testing.T, answers []snapAnswer) *acctest.MockServer {
	t.Helper()
	srv := acctest.NewMockServer(t)
	var mu sync.Mutex
	exists := false
	foreignAppeared := false
	creates := 0

	srv.Handle("GET", "/storage/volume/"+admitVolumeID, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		snaps := []any{}
		if exists {
			snaps = append(snaps, map[string]any{"id": admitSnapshotID, "name": admitSnapName, "status": "available", "size": 1024})
		}
		if foreignAppeared {
			snaps = append(snaps, map[string]any{"id": foreignSameNameID, "name": admitSnapName, "status": "available", "size": 1024})
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "volume": map[string]any{
			"id": admitVolumeID, "status": "available", "snapshots": snaps,
		}})
	})
	srv.Handle("POST", "/storage/volume/"+admitVolumeID+"/snapshot", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		i := creates
		creates++
		if i < len(answers) {
			a := answers[i]
			if a.record {
				exists = true
			}
			if a.foreign {
				foreignAppeared = true
			}
			if a.retryAfter != "" {
				w.Header().Set("Retry-After", a.retryAfter)
			}
			writeJSON(w, a.status, map[string]any{"success": false, "reason": a.reason, "message": a.message})
			return
		}
		exists = true
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Snapshot queued.", "queue": map[string]any{"id": "q-1"}})
	})
	srv.Handle("DELETE", "/storage/volume/"+admitVolumeID+"/snapshot/"+admitSnapshotID, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		exists = false
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "queue": map[string]any{"id": "q-2"}})
	})
	return srv
}

func snapshotAdmitConfig(srv *acctest.MockServer) string {
	return acctest.ProviderConfig(srv.Endpoint()) + `
resource "iaas_volume_snapshot" "test" {
  volume_id = "` + admitVolumeID + `"
  name      = "` + admitSnapName + `"
}
`
}

func TestUnitVolumeSnapshot_GateRefusalsAreWaitedOut(t *testing.T) {
	ensureTFBinary(t)
	t.Setenv("IAAS_INSTANCE_POLL_INTERVAL", "1ms")
	srv := snapshotAdmitServer(t, []snapAnswer{
		{status: http.StatusConflict, reason: "volume_busy", message: "Volume is busy with operation: backup. Please wait."},
		{status: http.StatusTooManyRequests, reason: "volume_cooldown", message: "Volume is in cooldown. Try again in 1 seconds.", retryAfter: "1"},
		{status: http.StatusTooManyRequests, reason: "account_inflight_limit", message: "Too many in-flight volume operations (3). Wait for some to finish."},
	})

	start := time.Now()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{{
			Config: snapshotAdmitConfig(srv),
			Check:  resource.TestCheckResourceAttr("iaas_volume_snapshot.test", "id", admitSnapshotID),
		}},
	})
	if elapsed := time.Since(start); elapsed < time.Second {
		t.Fatalf("elapsed %v; the Retry-After of the cooldown must be waited out", elapsed)
	}
	if got := len(srv.Requests("POST", "/storage/volume/"+admitVolumeID+"/snapshot")); got != 4 {
		t.Fatalf("snapshot creates = %d; want 4 (three transient refusals, then accepted)", got)
	}
}

func TestUnitVolumeSnapshot_DefiniteRefusalFailsWithReason(t *testing.T) {
	ensureTFBinary(t)
	t.Setenv("IAAS_INSTANCE_POLL_INTERVAL", "1ms")
	srv := snapshotAdmitServer(t, []snapAnswer{
		{status: http.StatusConflict, reason: "volume_snapshot_limit", message: "Snapshot limit reached (3/3). Delete an existing snapshot first."},
	})

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{{
			Config:      snapshotAdmitConfig(srv),
			ExpectError: regexp.MustCompile(`volume_snapshot_limit`),
		}},
	})
	if got := len(srv.Requests("POST", "/storage/volume/"+admitVolumeID+"/snapshot")); got != 1 {
		t.Fatalf("snapshot creates = %d; a definite refusal is never re-sent", got)
	}
	// Only the pre-send name check reads the volume: a definite refusal is
	// never followed by the uncertain-answer read-back.
	if got := len(srv.Requests("GET", "/storage/volume/"+admitVolumeID)); got != 1 {
		t.Fatalf("volume reads = %d; want 1 (the name check), a definite refusal has no read-back", got)
	}
}

func TestUnitVolumeSnapshot_Uncertain5xxRecordedIsAdopted(t *testing.T) {
	ensureTFBinary(t)
	t.Setenv("IAAS_INSTANCE_POLL_INTERVAL", "1ms")
	srv := snapshotAdmitServer(t, []snapAnswer{
		{status: http.StatusServiceUnavailable, message: "Service Unavailable", record: true},
	})

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{{
			Config: snapshotAdmitConfig(srv),
			Check:  resource.TestCheckResourceAttr("iaas_volume_snapshot.test", "id", admitSnapshotID),
		}},
	})
	if got := len(srv.Requests("POST", "/storage/volume/"+admitVolumeID+"/snapshot")); got != 1 {
		t.Fatalf("snapshot creates = %d; an uncertain answer is never replayed", got)
	}
}

func TestUnitVolumeSnapshot_Uncertain5xxNotRecordedFails(t *testing.T) {
	ensureTFBinary(t)
	t.Setenv("IAAS_INSTANCE_POLL_INTERVAL", "1ms")
	srv := snapshotAdmitServer(t, []snapAnswer{
		{status: http.StatusServiceUnavailable, message: "Service Unavailable"},
	})

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{{
			Config:      snapshotAdmitConfig(srv),
			ExpectError: regexp.MustCompile(`no\s+definite\s+answer`),
		}},
	})
	if got := len(srv.Requests("POST", "/storage/volume/"+admitVolumeID+"/snapshot")); got != 1 {
		t.Fatalf("snapshot creates = %d; an uncertain answer is never replayed", got)
	}
}

// A same-name snapshot that appears during the create (another creator) next
// to ours after an uncertain answer: two new snapshots carry the name, so the
// create fails naming the ambiguity and adopts neither (no DELETE of either).
func TestUnitVolumeSnapshot_Uncertain5xxWithConcurrentSameNameIsAmbiguous(t *testing.T) {
	ensureTFBinary(t)
	t.Setenv("IAAS_INSTANCE_POLL_INTERVAL", "1ms")
	srv := snapshotAdmitServer(t, []snapAnswer{
		{status: http.StatusServiceUnavailable, message: "Service Unavailable", record: true, foreign: true},
	})

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{{
			Config:      snapshotAdmitConfig(srv),
			ExpectError: regexp.MustCompile(`2\s+new\s+snapshots\s+named`),
		}},
	})
	if got := len(srv.Requests("DELETE", "/storage/volume/"+admitVolumeID+"/snapshot/"+foreignSameNameID)) +
		len(srv.Requests("DELETE", "/storage/volume/"+admitVolumeID+"/snapshot/"+admitSnapshotID)); got != 0 {
		t.Fatalf("deletes = %d; an ambiguous create adopts nothing", got)
	}
}

// One deadline for the whole create: a snapshot that takes 1.2s to appear and
// never becomes available, with create = "2s", stops polling about 2s after
// the send (each stage used to get its own full create timeout: ~3.2s).
func TestUnitVolumeSnapshot_CreateSharesOneDeadline(t *testing.T) {
	ensureTFBinary(t)
	t.Setenv("IAAS_INSTANCE_POLL_INTERVAL", "10ms")
	srv := acctest.NewMockServer(t)
	var mu sync.Mutex
	var postedAt, lastGet time.Time
	deleted := false
	srv.Handle("GET", "/storage/volume/"+admitVolumeID, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if !deleted {
			lastGet = time.Now()
		}
		snaps := []any{}
		if !deleted && !postedAt.IsZero() && time.Since(postedAt) > 1200*time.Millisecond {
			snaps = append(snaps, map[string]any{"id": admitSnapshotID, "name": admitSnapName, "status": "creating"})
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "volume": map[string]any{"id": admitVolumeID, "snapshots": snaps}})
	})
	srv.Handle("POST", "/storage/volume/"+admitVolumeID+"/snapshot", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		postedAt = time.Now()
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "queue": map[string]any{"id": "q-1"}})
	})
	srv.Handle("DELETE", "/storage/volume/"+admitVolumeID+"/snapshot/"+admitSnapshotID, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		deleted = true
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "queue": map[string]any{"id": "q-2"}})
	})
	cfg := acctest.ProviderConfig(srv.Endpoint()) + `
resource "iaas_volume_snapshot" "test" {
  volume_id = "` + admitVolumeID + `"
  name      = "` + admitSnapName + `"
  timeouts {
    create = "2s"
  }
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{{
			Config:      cfg,
			ExpectError: regexp.MustCompile(`did\s+not\s+become\s+available`),
		}},
	})
	mu.Lock()
	defer mu.Unlock()
	if postedAt.IsZero() {
		t.Fatal("the create was never sent")
	}
	if span := lastGet.Sub(postedAt); span > 2500*time.Millisecond {
		t.Fatalf("polling went on %v after the send; the create stages must share one 2s deadline", span)
	}
}

// A snapshot of the same name already on the volume (made in the panel, or by
// another resource): the create is refused BEFORE anything is sent, so the
// foreign snapshot is never adopted and never deleted by this resource.
// Adapted from the round-1 review (TestReviewAdopt_*): uncertain and definite
// create answers alike never get the chance to adopt it.
func TestUnitVolumeSnapshot_PreExistingSameNameIsRefusedBeforeSend(t *testing.T) {
	ensureTFBinary(t)
	t.Setenv("IAAS_INSTANCE_POLL_INTERVAL", "1ms")
	for _, post := range []struct {
		status int
		body   map[string]any
	}{
		{http.StatusServiceUnavailable, map[string]any{"message": "Service Unavailable"}},
		{http.StatusConflict, map[string]any{"success": false, "reason": "volume_snapshot_limit", "message": "Snapshot limit reached (5/5)."}},
		{http.StatusOK, map[string]any{"success": true, "queue": map[string]any{"id": "q-1"}}},
	} {
		srv := acctest.NewMockServer(t)
		var mu sync.Mutex
		deletes := 0
		srv.Handle("GET", "/storage/volume/"+admitVolumeID, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, map[string]any{"success": true, "volume": map[string]any{
				"id": admitVolumeID, "status": "available",
				"snapshots": []any{map[string]any{"id": foreignSameNameID, "name": admitSnapName, "status": "available", "size": 1024}},
			}})
		})
		srv.Handle("POST", "/storage/volume/"+admitVolumeID+"/snapshot", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, post.status, post.body)
		})
		srv.Handle("DELETE", "/storage/volume/"+admitVolumeID+"/snapshot/"+foreignSameNameID, func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			deletes++
			mu.Unlock()
			writeJSON(w, http.StatusOK, map[string]any{"success": true, "queue": map[string]any{"id": "q-2"}})
		})

		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: acctest.Factories,
			Steps: []resource.TestStep{{
				Config:      snapshotAdmitConfig(srv),
				ExpectError: regexp.MustCompile(`already\s+exists\s+on\s+volume`),
			}},
		})
		if got := len(srv.Requests("POST", "/storage/volume/"+admitVolumeID+"/snapshot")); got != 0 {
			t.Fatalf("POST %d: creates = %d; a taken name is refused before any send", post.status, got)
		}
		mu.Lock()
		if deletes != 0 {
			t.Fatalf("POST %d: the pre-existing snapshot was deleted %d time(s)", post.status, deletes)
		}
		mu.Unlock()
	}
}

// An empty name is refused at plan time (length validator): nothing is sent,
// so the platform cannot create a snapshot under a name it chose itself.
func TestUnitVolumeSnapshot_EmptyNameIsRejected(t *testing.T) {
	ensureTFBinary(t)
	srv := snapshotAdmitServer(t, nil)
	cfg := acctest.ProviderConfig(srv.Endpoint()) + `
resource "iaas_volume_snapshot" "test" {
  volume_id = "` + admitVolumeID + `"
  name      = ""
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{{
			Config:      cfg,
			ExpectError: regexp.MustCompile(`at\s+least\s+1`),
		}},
	})
	if got := len(srv.Requests("POST", "/storage/volume/"+admitVolumeID+"/snapshot")); got != 0 {
		t.Fatalf("snapshot creates = %d; an empty name is refused before any send", got)
	}
}

// A same-name snapshot made by someone else during an admission wait (here a
// 409 volume_busy), then a DEFINITE success: the pre-send set refreshed before
// the second send excludes the foreign snapshot, so ours is resolved and the
// foreign one is never adopted or deleted.
func TestUnitVolumeSnapshot_ForeignSameNameDuringAdmissionWaitIsNotOurs(t *testing.T) {
	ensureTFBinary(t)
	t.Setenv("IAAS_INSTANCE_POLL_INTERVAL", "1ms")
	srv := snapshotAdmitServer(t, []snapAnswer{
		{status: http.StatusConflict, reason: "volume_busy", message: "Volume is busy with operation: snapshot. Please wait.", foreign: true},
	})

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Factories,
		Steps: []resource.TestStep{{
			Config: snapshotAdmitConfig(srv),
			Check:  resource.TestCheckResourceAttr("iaas_volume_snapshot.test", "id", admitSnapshotID),
		}},
	})
	if got := len(srv.Requests("DELETE", "/storage/volume/"+admitVolumeID+"/snapshot/"+foreignSameNameID)); got != 0 {
		t.Fatalf("the foreign snapshot was deleted %d time(s)", got)
	}
	if got := len(srv.Requests("POST", "/storage/volume/"+admitVolumeID+"/snapshot")); got != 2 {
		t.Fatalf("snapshot creates = %d; want 2 (busy, then accepted)", got)
	}
}
