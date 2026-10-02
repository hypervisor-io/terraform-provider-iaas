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

func TestManagedDatabaseBackup_FailedResponseIsNotSuccess(t *testing.T) {
	for _, tc := range []struct {
		name, body, message string
		incomplete          bool
	}{
		{"user-dispatch-failure", `{"success":false,"message":"Backup dispatch acknowledgement is uncertain. Await the node callback before retrying."}`, "uncertain", false},
		{"invalid-json", `{`, "decoding response", false},
		{"interrupted-response", `{`, "reading response body", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if tc.incomplete {
					w.Header().Set("Content-Length", "100")
				}
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			obj, err := New(srv.URL+"/api", "tok", time.Second, false).BackupManagedDatabase(context.Background(), "db-1")
			if obj != nil || err == nil || !strings.Contains(err.Error(), tc.message) || calls != 1 {
				t.Fatalf("failed backup returned success/lost error/replayed: obj=%v err=%v calls=%d", obj, err, calls)
			}
		})
	}
}

// A retry after an uncertain dispatch can start a second non-idempotent backup.
func TestManagedDatabaseBackup_NoReplayOnDispatchError(t *testing.T) {
	for _, backupType := range []string{"", "full", "incremental"} {
		for _, status := range []int{http.StatusConflict, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusServiceUnavailable} {
			t.Run(backupType+"/"+http.StatusText(status), func(t *testing.T) {
				calls := 0
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					if r.Method != http.MethodPost || r.URL.Path != "/api/database/db-1/backup" {
						t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					}
					w.WriteHeader(status)
					_, _ = w.Write([]byte(`{"success":false,"reason":"backup_in_progress","message":"Inspect backup and task state before retrying."}`))
				}))
				defer srv.Close()
				c := New(srv.URL+"/api", "tok", time.Second, false)
				c.retryBaseDelay = time.Millisecond
				obj, err := c.BackupManagedDatabaseOfType(context.Background(), "db-1", backupType)
				var apiErr *APIError
				if obj != nil || !errors.As(err, &apiErr) || apiErr.Status != status || apiErr.Message != "Inspect backup and task state before retrying." {
					t.Fatalf("want status/message error and no successful object: obj=%v err=%v", obj, err)
				}
				if calls != 1 {
					t.Fatalf("non-idempotent backup replayed: got %d requests; want 1", calls)
				}
			})
		}
	}
}

// Guards must propagate through real client methods without a successful object/password.
func TestManagedDatabaseLifecycle_RestoreNotActivated(t *testing.T) {
	actions := []struct {
		name, method, path string
		call               func(*Client) error
	}{
		{"restart", "POST", "/restart", func(c *Client) error { return c.RestartManagedDatabase(context.Background(), "db-1") }},
		{"reset-password", "POST", "/reset-password", func(c *Client) error {
			obj, err := c.ResetManagedDatabasePassword(context.Background(), "db-1")
			if obj != nil {
				t.Errorf("refusal returned password object: %v", obj)
			}
			return err
		}},
		{"resize", "PATCH", "/resize", func(c *Client) error {
			obj, err := c.ResizeManagedDatabase(context.Background(), "db-1", map[string]any{"db_plan_id": "plan-2"})
			if obj != nil {
				t.Errorf("refusal returned database object: %v", obj)
			}
			return err
		}},
		{"parameter-group", "PATCH", "/parameter-group", func(c *Client) error {
			return c.ApplyDatabaseParameterGroup(context.Background(), "db-1", map[string]any{"parameter_group_id": "pg-2"})
		}},
	}
	for _, action := range actions {
		t.Run(action.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != action.method || r.URL.Path != "/api/database/db-1"+action.path {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "reason": "restore_not_activated", "message": "This database is still being restored (or its restore did not finish)."})
			}))
			defer srv.Close()
			var apiErr *APIError
			err := action.call(New(srv.URL+"/api", "tok", time.Second, false))
			if !errors.As(err, &apiErr) || apiErr.Status != 409 || apiErr.Message != "This database is still being restored (or its restore did not finish)." || calls != 1 {
				t.Fatalf("refusal lost/replayed: err=%v calls=%d", err, calls)
			}
		})
	}
}
