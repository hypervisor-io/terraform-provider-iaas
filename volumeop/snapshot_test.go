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

// snapVolume serves GET /storage/volume/vol-1 with the given embedded
// snapshots (id -> name), or the given failure status.
type snapVolume struct {
	mu    sync.Mutex
	snaps [][2]string
	fail  int
	gets  int
}

func (s *snapVolume) server(t *testing.T) *client.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.gets++
		if r.Method != http.MethodGet || r.URL.Path != "/storage/volume/vol-1" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if s.fail != 0 {
			writeJSON(w, s.fail, map[string]any{"success": false, "message": "boom"})
			return
		}
		list := []any{}
		for _, sn := range s.snaps {
			list = append(list, map[string]any{"id": sn[0], "name": sn[1], "status": "available"})
		}
		list = append(list, "not an object, skipped")
		writeJSON(w, http.StatusOK, map[string]any{"volume": map[string]any{"id": "vol-1", "snapshots": list}})
	}))
	t.Cleanup(srv.Close)
	c := client.New(srv.URL, "t", 2*time.Second, false)
	return c
}

func TestSnapshotIDsNamed_FiltersByNameSorted(t *testing.T) {
	sv := &snapVolume{snaps: [][2]string{{"s-3", "nightly"}, {"s-1", "nightly"}, {"s-2", "weekly"}}}
	c := sv.server(t)
	ids, err := SnapshotIDsNamed(context.Background(), c, "vol-1", "nightly")
	if err != nil || strings.Join(ids, ",") != "s-1,s-3" {
		t.Fatalf("ids = %v, err = %v; want [s-1 s-3]", ids, err)
	}
}

func TestCheckSnapshotNameFree(t *testing.T) {
	sv := &snapVolume{snaps: [][2]string{{"s-2", "weekly"}}}
	c := sv.server(t)
	before, err := CheckSnapshotNameFree(context.Background(), c, "vol-1", "nightly")
	if err != nil || len(before) != 0 {
		t.Fatalf("free name: before=%v err=%v", before, err)
	}

	sv.snaps = append(sv.snaps, [2]string{"s-9", "nightly"})
	before, err = CheckSnapshotNameFree(context.Background(), c, "vol-1", "nightly")
	var taken *SnapshotNameTakenError
	if !errors.As(err, &taken) || strings.Join(taken.IDs, ",") != "s-9" || strings.Join(before, ",") != "s-9" {
		t.Fatalf("taken name: before=%v err=%v; want *SnapshotNameTakenError naming s-9", before, err)
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("message %q", err.Error())
	}

	sv.fail = http.StatusNotFound
	_, err = CheckSnapshotNameFree(context.Background(), c, "vol-1", "nightly")
	if !client.IsNotFound(err) || errors.As(err, &taken) {
		t.Fatalf("unreadable volume: err = %v; want the 404 wrapped, not a name refusal", err)
	}
}

func TestAdoptAfterUncertain(t *testing.T) {
	cases := []struct {
		name    string
		snaps   [][2]string
		before  []string
		fail    int
		wantID  string
		wantErr string
	}{
		{"one new is adopted", [][2]string{{"s-1", "n"}, {"s-x", "other"}}, nil, 0, "s-1", ""},
		{"none", [][2]string{{"s-x", "other"}}, nil, 0, "", "no new snapshot named"},
		{"only a pre-send id", [][2]string{{"s-old", "n"}}, []string{"s-old"}, 0, "", "no new snapshot named"},
		{"new beside a pre-send id", [][2]string{{"s-old", "n"}, {"s-new", "n"}}, []string{"s-old"}, 0, "s-new", ""},
		{"two new is ambiguous", [][2]string{{"s-1", "n"}, {"s-2", "n"}}, nil, 0, "", "2 new snapshots named"},
		{"lookup failed", nil, nil, http.StatusInternalServerError, "", "could not be read back"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sv := &snapVolume{snaps: tc.snaps, fail: tc.fail}
			c := sv.server(t)
			id, err := AdoptAfterUncertain(context.Background(), c, "vol-1", "n", tc.before)
			if tc.wantErr == "" {
				if err != nil || id != tc.wantID {
					t.Fatalf("id=%q err=%v; want %q", id, err, tc.wantID)
				}
				return
			}
			if err == nil || id != "" || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("id=%q err=%v; want an error containing %q", id, err, tc.wantErr)
			}
		})
	}
}

// The read-back runs on a fresh context: an expired create deadline must not
// turn a recorded snapshot into an orphan.
func TestAdoptAfterUncertain_SurvivesExpiredParentContext(t *testing.T) {
	sv := &snapVolume{snaps: [][2]string{{"s-1", "n"}}}
	c := sv.server(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	id, err := AdoptAfterUncertain(ctx, c, "vol-1", "n", nil)
	if err != nil || id != "s-1" {
		t.Fatalf("id=%q err=%v; the read-back must not inherit the expired deadline", id, err)
	}
}

// ...and it is still bounded on its own.
func TestAdoptAfterUncertain_IsBounded(t *testing.T) {
	prev := adoptLookupTimeout
	adoptLookupTimeout = 100 * time.Millisecond
	t.Cleanup(func() { adoptLookupTimeout = prev })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	t.Cleanup(srv.Close)
	c := client.New(srv.URL, "t", 10*time.Second, false)
	start := time.Now()
	_, err := AdoptAfterUncertain(context.Background(), c, "vol-1", "n", nil)
	if err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("err=%v after %v; the read-back must be bounded", err, time.Since(start))
	}
}

// M34 (from the round-1 review): the busy backoff must GROW between busy
// refusals. With Interval 200ms and three hidden busy answers the gaps between
// the four sends are >= 200, 300, 450ms (1.5x each); a non-growing backoff
// gives ~200ms gaps. Sleeps never return early, so lower bounds are robust.
func TestBusyBackoffGrowsBetweenSends(t *testing.T) {
	f := newFake(t)
	f.hiddenBusy = 3
	var mu sync.Mutex
	var sends []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			mu.Lock()
			sends = append(sends, time.Now())
			mu.Unlock()
		}
		f.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	c := client.New(srv.URL, "test-token", 5*time.Second, false)

	if _, err := AttachVolume(context.Background(), c, "vol-1", "inst-1", Options{Interval: 200 * time.Millisecond}); err != nil {
		t.Fatalf("attach: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sends) != 4 {
		t.Fatalf("sends = %d; want 4", len(sends))
	}
	gaps := []time.Duration{sends[1].Sub(sends[0]), sends[2].Sub(sends[1]), sends[3].Sub(sends[2])}
	if gaps[0] < 195*time.Millisecond || gaps[1] < 290*time.Millisecond || gaps[2] < 440*time.Millisecond {
		t.Fatalf("busy backoff did not grow: gaps %v; want >= 200, 300, 450ms", gaps)
	}
}

// The same growth for the in-flight limit through Admit.
func TestAdmit_InflightBackoffGrowsBetweenSends(t *testing.T) {
	var mu sync.Mutex
	var sends []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		sends = append(sends, time.Now())
		n := len(sends)
		mu.Unlock()
		if n <= 3 {
			writeJSON(w, http.StatusTooManyRequests, map[string]any{"success": false, "reason": "account_inflight_limit", "message": "Wait."})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "queue": map[string]any{"id": "q"}})
	}))
	t.Cleanup(srv.Close)
	c := client.New(srv.URL, "t", 5*time.Second, false)
	if err := Admit(context.Background(), "vol-1", Snapshot, Options{Interval: 200 * time.Millisecond}, func(ctx context.Context) error {
		_, err := c.CreateVolumeSnapshot(ctx, "vol-1", map[string]any{"name": "n"})
		return err
	}); err != nil {
		t.Fatalf("admit: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	gaps := []time.Duration{sends[1].Sub(sends[0]), sends[2].Sub(sends[1]), sends[3].Sub(sends[2])}
	if gaps[1] < 290*time.Millisecond || gaps[2] < 440*time.Millisecond {
		t.Fatalf("in-flight backoff did not grow: gaps %v", gaps)
	}
}

// An empty name is refused before anything is read or sent: the platform
// would name the snapshot itself and it could never be found by name.
func TestCheckSnapshotNameFree_EmptyNameRefused(t *testing.T) {
	sv := &snapVolume{}
	c := sv.server(t)
	_, err := CheckSnapshotNameFree(context.Background(), c, "vol-1", "")
	if !errors.Is(err, ErrSnapshotNameEmpty) {
		t.Fatalf("err = %v; want ErrSnapshotNameEmpty", err)
	}
	if sv.gets != 0 {
		t.Fatalf("volume reads = %d; an empty name is refused without a read", sv.gets)
	}
}

func TestSnapshotIDsBeforeSend(t *testing.T) {
	sv := &snapVolume{snaps: [][2]string{{"s-new", "n"}, {"s-x", "other"}}}
	c := sv.server(t)
	got := SnapshotIDsBeforeSend(context.Background(), c, "vol-1", "n", []string{"s-old"})
	if strings.Join(got, ",") != "s-old,s-new" {
		t.Fatalf("set = %v; want the previous ids plus the same-name ids now", got)
	}
	sv.fail = http.StatusInternalServerError
	got = SnapshotIDsBeforeSend(context.Background(), c, "vol-1", "n", []string{"s-old"})
	if strings.Join(got, ",") != "s-old" {
		t.Fatalf("set = %v; a failed read keeps the previous set", got)
	}
}

func TestNewSnapshotID(t *testing.T) {
	sv := &snapVolume{snaps: [][2]string{{"s-old", "n"}}}
	c := sv.server(t)
	if id, found, err := NewSnapshotID(context.Background(), c, "vol-1", "n", []string{"s-old"}); err != nil || found || id != "" {
		t.Fatalf("only a pre-send id: id=%q found=%v err=%v; want not found", id, found, err)
	}
	sv.snaps = append(sv.snaps, [2]string{"s-ours", "n"})
	if id, found, err := NewSnapshotID(context.Background(), c, "vol-1", "n", []string{"s-old"}); err != nil || !found || id != "s-ours" {
		t.Fatalf("one new id: id=%q found=%v err=%v; want s-ours", id, found, err)
	}
	sv.snaps = append(sv.snaps, [2]string{"s-other", "n"})
	_, found, err := NewSnapshotID(context.Background(), c, "vol-1", "n", []string{"s-old"})
	var amb *AmbiguousSnapshotError
	if found || !errors.As(err, &amb) || len(amb.IDs) != 2 {
		t.Fatalf("two new ids: found=%v err=%v; want *AmbiguousSnapshotError", found, err)
	}
}
