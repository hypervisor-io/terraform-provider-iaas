package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func bulkIDs(n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
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

// A 503 can mean that a node received the delete even though its ACK was lost.
func TestBulkDestroyInstanceBackups_UncertainDispatchIsNotReplayed(t *testing.T) {
	for _, count := range []int{2, 250} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if count > 100 && calls == 1 {
					_, _ = w.Write([]byte(`{"success":true,"data":{"queued":0,"refused":0,"results":[]}}`))
					return
				}
				w.WriteHeader(503)
				_, _ = w.Write([]byte(`{"success":false,"message":"Backup deletes: 1 queued; 1 refused.","data":{"queued":1,"refused":1,"results":[{"id":"accepted","status":200,"success":true},{"id":"uncertain","status":503,"success":false,"reason":"delete_dispatch_unconfirmed"}]}}`))
			}))
			defer srv.Close()
			c := New(srv.URL+"/api", "tok", 10*time.Second, false)
			err := c.BulkDestroyInstanceBackups(context.Background(), "i1", map[string]any{"backups": bulkIDs(count)})
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.Status != 503 || !strings.Contains(err.Error(), "backup uncertain: 503 (reason: delete_dispatch_unconfirmed)") {
				t.Fatalf("missing refusal detail: %v", err)
			}
			wantCalls := 1
			if count > 100 {
				wantCalls = 2
			}
			if calls != wantCalls {
				t.Fatalf("calls=%d, want %d; no replay or later chunk", calls, wantCalls)
			}
		})
	}
}

func TestBulkDestroyInstanceBackups_PreservesPerBackupRefusals(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(409)
		_, _ = w.Write([]byte(`{"success":false,"message":"Backup deletes: 0 queued; 2 refused.","data":{"queued":0,"refused":2,"results":[{"id":"restoring","status":409,"success":false,"reason":"backup_in_use_by_restore"},{"id":"deleting","status":409,"success":false,"reason":"backup_being_deleted"}]}}`))
	}))
	defer srv.Close()
	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	err := c.BulkDestroyInstanceBackups(context.Background(), "i1", map[string]any{"backups": bulkIDs(2)})
	if err == nil {
		t.Fatal("expected refusal")
	}
	for _, detail := range []string{"backup restoring: 409 (reason: backup_in_use_by_restore)", "backup deleting: 409 (reason: backup_being_deleted)"} {
		if !strings.Contains(err.Error(), detail) {
			t.Fatalf("missing %s in %v", detail, err)
		}
	}
}
