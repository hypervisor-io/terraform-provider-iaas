package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRestoreDatabaseToNewAcceptanceAndUncertainty(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		wantError bool
	}{
		{"accepted", 202, `{"success":true,"data":{"managed_database_id":"db-new","task_id":"task-new","status":"restoring"}}`, false},
		{"unconfirmed", 503, `{"success":false,"reason":"deploy_dispatch_unconfirmed","message":"dispatch uncertain","data":{"managed_database_id":"db-new","task_id":"task-new"}}`, true},
		{"bad_ack", 202, `{"success":true,"data":{"managed_database_id":"db-new","task_id":"task-new"}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "POST" || r.URL.Path != "/database/source/restore-to-new" {
					t.Errorf("request %s %s", r.Method, r.URL.Path)
				}
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				if body["backup_id"] != "backup" || body["name"] != "copy" {
					t.Errorf("body=%v", body)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c := New(srv.URL, "token", time.Second, false)
			result, err := c.RestoreManagedDatabaseToNew(context.Background(), "source", map[string]any{"backup_id": "backup", "name": "copy"})
			if (err != nil) != tc.wantError || calls != 1 || result.ManagedDatabaseID != "db-new" || result.TaskID != "task-new" {
				t.Fatalf("result=%v calls=%d err=%v", result, calls, err)
			}
			if tc.wantError {
				if !strings.Contains(err.Error(), "do not retry") || !strings.Contains(err.Error(), "db-new") || !strings.Contains(err.Error(), "task-new") {
					t.Fatal(err)
				}
			}
			if tc.status == 503 {
				var api *APIError
				if !errors.As(err, &api) || api.Reason != "deploy_dispatch_unconfirmed" {
					t.Fatalf("typed error lost: %v", err)
				}
			}
		})
	}
}
func TestRestoreDatabaseToNewInvalidChoiceAndTypedRefusal(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(409)
		_, _ = w.Write([]byte(`{"success":false,"reason":"pitr_archive_pruned","message":"choose a later point"}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "token", time.Second, false)
	for _, body := range []map[string]any{{}, {"backup_id": "b", "target_time": "2026-10-28T00:00:00Z"}} {
		if _, err := c.RestoreManagedDatabaseToNew(context.Background(), "source", body); err == nil {
			t.Fatal("invalid choice accepted")
		}
	}
	if calls != 0 {
		t.Fatalf("invalid choice made %d requests", calls)
	}
	_, err := c.RestoreManagedDatabaseToNew(context.Background(), "source", map[string]any{"target_time": "2026-10-28T00:00:00Z"})
	var api *APIError
	if !errors.As(err, &api) || api.Reason != "pitr_archive_pruned" || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
