package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMicrovmVolumeRoutesVerbsAndBodies(t *testing.T) {
	var got []string
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.Path)
		raw, _ := io.ReadAll(r.Body)
		var b map[string]any
		_ = json.Unmarshal(raw, &b)
		bodies = append(bodies, b)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/microvm/vm/vm1/volumes" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"success":true,"volumes":[{"id":"v1","slot":0},{"id":"v2","slot":1}]}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"success":true,"volume":{"id":"v1","status":"available"}}`))
		}
	}))
	defer srv.Close()
	c := New(srv.URL+"/api", "token", 0, false)
	ctx := context.Background()

	if v, err := c.CreateSandboxVolume(ctx, map[string]any{"for": "sandbox", "name": "d", "size_gib": 5}); err != nil || v["id"] != "v1" {
		t.Fatalf("create: %v %v", v, err)
	}
	list, err := c.ListMicrovmVolumes(ctx, "vm1")
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %v %v", list, err)
	}
	if _, err := c.AttachMicrovmVolume(ctx, "vm1", map[string]any{"volume_id": "v1"}, "k1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DetachMicrovmVolume(ctx, "vm1", "v1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DeleteSandboxVolume(ctx, "v1"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"POST /api/storage/volumes", "GET /api/microvm/vm/vm1/volumes", "POST /api/microvm/vm/vm1/volumes",
		"DELETE /api/microvm/vm/vm1/volume/v1", "DELETE /api/storage/volume/v1",
	}
	for i, w := range want {
		if i >= len(got) || got[i] != w {
			t.Fatalf("call %d = %v, want %q (all: %v)", i, got, w, got)
		}
	}
	if bodies[0]["for"] != "sandbox" || bodies[2]["volume_id"] != "v1" {
		t.Fatalf("bodies: %v", bodies)
	}
}

func TestMicrovmVolumeEmptyIDsAreRefusedBeforeTheNetwork(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"volume":{"id":"x"},"volumes":[]}`))
	}))
	defer srv.Close()
	defer func() {
		if hit {
			t.Fatal("a call with an empty id or body reached the network")
		}
	}()
	c := New(srv.URL+"/api", "token", 0, false)
	ctx := context.Background()
	if _, err := c.DeleteSandboxVolume(ctx, ""); err == nil {
		t.Fatal("empty volume id")
	}
	if _, err := c.ListMicrovmVolumes(ctx, ""); err == nil {
		t.Fatal("empty sandbox id")
	}
	if _, err := c.AttachMicrovmVolume(ctx, "", nil, ""); err == nil {
		t.Fatal("empty sandbox id")
	}
	if _, err := c.DetachMicrovmVolume(ctx, "vm", ""); err == nil {
		t.Fatal("empty volume id")
	}
	if _, err := c.CreateSandboxVolume(ctx, nil); err == nil {
		t.Fatal("nil body")
	}
}

func TestMicrovmVolumeTypedRefusalKeepsStatusAndReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"success":false,"reason":"volume_in_use","message":"The volume is attached to a sandbox."}`))
	}))
	defer srv.Close()
	c := New(srv.URL+"/api", "token", 0, false)
	_, err := c.DeleteSandboxVolume(context.Background(), "v1")
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.Status != 409 || apiErr.Reason != "volume_in_use" {
		t.Fatalf("err = %#v", err)
	}
}

func TestListSandboxVolumesAsksForTheSandboxKindAndFiltersByStatus(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.RequestURI()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"current_page":1,"last_page":1,"data":[{"id":"a","status":"available"},{"id":"b","status":"attached"}]}`))
	}))
	defer srv.Close()
	c := New(srv.URL+"/api", "token", 0, false)
	rows, err := c.ListSandboxVolumes(context.Background(), "attached")
	if err != nil || len(rows) != 1 || rows[0]["id"] != "b" {
		t.Fatalf("rows = %v, %v", rows, err)
	}
	if path != "/api/storage/volumes?for=sandbox" {
		t.Fatalf("path = %s", path)
	}
}
