package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func bulkIDs(n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("b-%03d", i)
	}
	return ids
}

// The Master caps a bulk backup delete at 100 ids (422 beyond), so the client must chunk.
func TestBulkDestroyInstanceBackups_ChunksAtTheMasterCap(t *testing.T) {
	var sizes []int
	var paths []string
	var extra []any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got map[string]any
		_ = json.NewDecoder(r.Body).Decode(&got)
		paths = append(paths, r.URL.Path)
		sizes = append(sizes, len(got["backups"].([]any)))
		extra = append(extra, got["note"])
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.BulkDestroyInstanceBackups(context.Background(), "i1", map[string]any{"backups": bulkIDs(250), "note": "keep"}); err != nil {
		t.Fatalf("BulkDestroyInstanceBackups: %v", err)
	}
	want := []int{100, 100, 50}
	if len(sizes) != len(want) {
		t.Fatalf("requests = %v; want sizes %v", sizes, want)
	}
	for i := range want {
		if sizes[i] != want[i] {
			t.Errorf("chunk %d size = %d; want %d", i, sizes[i], want[i])
		}
		if paths[i] != "/api/instance/i1/backups/destroy" {
			t.Errorf("path = %s", paths[i])
		}
		if extra[i] != "keep" {
			t.Errorf("chunk %d lost the other body keys: %v", i, extra[i])
		}
	}
}

func TestBulkDestroyInstanceBackups_ExactlyTheCapIsOneRequestAndFailureStopsLaterChunks(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 2 {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"success":false,"message":"nope"}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()
	c := New(srv.URL+"/api", "tok", 10*time.Second, false)

	if err := c.BulkDestroyInstanceBackups(context.Background(), "i1", map[string]any{"backups": bulkIDs(MaxBulkBackupIDs)}); err != nil || calls != 1 {
		t.Fatalf("100 ids: err=%v calls=%d; want one request", err, calls)
	}

	calls = 0
	if err := c.BulkDestroyInstanceBackups(context.Background(), "i1", map[string]any{"backups": bulkIDs(350)}); err == nil {
		t.Fatalf("a refused chunk must surface as an error")
	}
	if calls != 2 {
		t.Errorf("calls = %d; want 2 (stop at the first failing chunk)", calls)
	}
}
