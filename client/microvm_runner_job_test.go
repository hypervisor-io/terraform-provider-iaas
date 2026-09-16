package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestListRunnerJobs_HitsJobsWithPoolFilter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/microvm/runners/jobs" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("pool_id") != "p1" {
			t.Fatalf("missing pool_id filter: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"j1"},{"id":"j2"}],"current_page":1,"last_page":1}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	jobs, err := c.ListRunnerJobs(context.Background(), "p1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(jobs))
	}
}

func TestGetRunnerJob_EmptyIdGuard(t *testing.T) {
	c := New("http://127.0.0.1:1", "tok", time.Second, false)
	if _, err := c.GetRunnerJob(context.Background(), ""); err == nil {
		t.Fatal("expected an error for an empty id")
	}
}
