package client

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMicrovmSnapshotRoutesVerbsBodiesAndIdempotencyKey(t *testing.T) {
	type seen struct{ method, path, query, body, idem string }
	var got []seen
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		got = append(got, seen{r.Method, r.URL.EscapedPath(), r.URL.RawQuery, string(raw), r.Header.Get("Idempotency-Key")})
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"success":true,"snapshot":{"id":"snap-1","name":"before","status":"creating"}}`))
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"success":true,"snapshot":{"id":"snap-1","name":"before","status":"deleting"}}`))
		case r.URL.EscapedPath() == "/api/microvm/snapshots":
			_, _ = w.Write([]byte(`{"success":true,"snapshots":{"current_page":1,"last_page":1,"data":[{"id":"snap-1","status":"ready"}]}}`))
		default:
			_, _ = w.Write([]byte(`{"success":true,"snapshot":{"id":"snap-1","status":"ready","paused_ms":2300}}`))
		}
	}))
	defer server.Close()
	c := New(server.URL+"/api", "token", 0, false)
	ctx := context.Background()

	created, err := c.CreateMicrovmSnapshot(ctx, "vm/1", map[string]any{"name": "before", "kind": "disk"}, "key-1")
	if err != nil || created["status"] != "creating" || created["id"] != "snap-1" {
		t.Fatalf("create = %v, %v", created, err)
	}
	read, err := c.GetMicrovmSnapshot(ctx, "snap-1")
	if err != nil || read["status"] != "ready" || read["paused_ms"] != float64(2300) {
		t.Fatalf("get = %v, %v", read, err)
	}
	list, err := c.ListMicrovmSnapshots(ctx, "vm-9", "ready", "")
	if err != nil || len(list) != 1 || list[0]["id"] != "snap-1" {
		t.Fatalf("list = %v, %v", list, err)
	}
	if err := c.DeleteMicrovmSnapshot(ctx, "snap-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	want := []seen{
		{"POST", "/api/microvm/vm/vm%2F1/snapshots", "", `{"kind":"disk","name":"before"}`, "key-1"},
		{"GET", "/api/microvm/snapshot/snap-1", "", "", ""},
		{"GET", "/api/microvm/snapshots", "sandbox_id=vm-9&status=ready", "", ""},
		{"DELETE", "/api/microvm/snapshot/snap-1", "", "", ""},
	}
	if len(got) != len(want) {
		t.Fatalf("requests = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("request %d = %+v; want %+v", i, got[i], want[i])
		}
	}
}

func TestMicrovmSnapshotCreateSendsAFreshKeyWhenNoneIsGiven(t *testing.T) {
	var keys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"success":true,"snapshot":{"id":"s","status":"creating"}}`))
	}))
	defer server.Close()
	c := New(server.URL+"/api", "token", 0, false)
	for i := 0; i < 2; i++ {
		if _, err := c.CreateMicrovmSnapshot(context.Background(), "vm", map[string]any{"name": "n"}, ""); err != nil {
			t.Fatal(err)
		}
	}
	if keys[0] == "" || keys[1] == "" || keys[0] == keys[1] {
		t.Fatalf("keys = %v", keys)
	}
}

func TestMicrovmSnapshotTypedRefusalsAndNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"success":false,"reason":"snapshot_requires_pause","message":"pause first","pause_estimate_ms":14000,"pause_limit_ms":10000}`))
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"success":false,"reason":"snapshot_in_use","message":"in use"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"success":false,"reason":"snapshot_not_found","message":"Snapshot not found."}`))
		}
	}))
	defer server.Close()
	c := New(server.URL+"/api", "token", 0, false)
	ctx := context.Background()

	_, err := c.CreateMicrovmSnapshot(ctx, "vm", map[string]any{"name": "n"}, "k")
	var apiErr *APIError
	if !asAPIError(err, &apiErr) || apiErr.Status != 409 || apiErr.Reason != "snapshot_requires_pause" || !strings.Contains(err.Error(), "snapshot_requires_pause") {
		t.Fatalf("create err = %v", err)
	}
	if _, err := c.GetMicrovmSnapshot(ctx, "x"); !IsNotFound(err) {
		t.Fatalf("get err = %v; want not found", err)
	}
	err = c.DeleteMicrovmSnapshot(ctx, "x")
	if !asAPIError(err, &apiErr) || apiErr.Reason != "snapshot_in_use" {
		t.Fatalf("delete err = %v", err)
	}
	for name, call := range map[string]func() error{
		"create": func() error { _, e := c.CreateMicrovmSnapshot(ctx, "", nil, ""); return e },
		"get":    func() error { _, e := c.GetMicrovmSnapshot(ctx, ""); return e },
		"delete": func() error { return c.DeleteMicrovmSnapshot(ctx, "") },
	} {
		if call() == nil {
			t.Errorf("%s with an empty id must fail before any request", name)
		}
	}
}

func asAPIError(err error, target **APIError) bool {
	for err != nil {
		if e, ok := err.(*APIError); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
