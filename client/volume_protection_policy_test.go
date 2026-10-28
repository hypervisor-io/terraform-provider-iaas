package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestVolumeProtectionPoliciesExactContracts(t *testing.T) {
	var paths []string
	var patches []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /storage/volume-policies":
			if r.URL.Query().Get("page") == "2" {
				_, _ = w.Write([]byte(`{"current_page":2,"last_page":2,"data":[{"id":"p2"}]}`))
			} else {
				_, _ = w.Write([]byte(`{"current_page":1,"last_page":2,"data":[{"id":"p1"}]}`))
			}
		case "GET /storage/volume-policy/p1":
			_, _ = w.Write([]byte(`{"success":true,"policy":{"id":"p1","schedule_time":"02:30","timezone":null,"effective_timezone":"Asia/Kolkata","volumes":[{"id":"v1"}],"recent_runs":[]},"attachable_volumes":[{"id":"v2"}]}`))
		case "POST /storage/volume-policies", "PATCH /storage/volume-policy/p1":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			patches = append(patches, body)
			if r.Method == "POST" {
				w.WriteHeader(201)
			}
			_, _ = w.Write([]byte(`{"success":true,"policy":{"id":"p1","schedule_time":"02:30","timezone":null,"backup_enabled":false,"snapshot_enabled":true}}`))
		case "POST /storage/volume-policy/p1/reset-failures":
			_, _ = w.Write([]byte(`{"success":true,"policy":{"id":"p1","status":"active"}}`))
		default:
			_, _ = w.Write([]byte(`{"success":true,"message":"done"}`))
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "token", time.Second, false)
	ctx := context.Background()
	items, err := c.ListVolumeProtectionPolicies(ctx)
	if err != nil || len(items) != 2 {
		t.Fatalf("list=%v err=%v", items, err)
	}
	body := map[string]any{"schedule_time": "02:30", "timezone": nil, "backup_enabled": false, "snapshot_enabled": true, "keep_last": 0}
	obj, err := c.CreateVolumeProtectionPolicy(ctx, body)
	if err != nil || obj["id"] != "p1" {
		t.Fatalf("create=%v err=%v", obj, err)
	}
	obj, err = c.UpdateVolumeProtectionPolicy(ctx, "p1", body)
	if err != nil || obj["id"] != "p1" {
		t.Fatalf("patch=%v err=%v", obj, err)
	}
	for _, got := range patches {
		want := map[string]any{"schedule_time": "02:30", "timezone": nil, "backup_enabled": false, "snapshot_enabled": true, "keep_last": float64(0)}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("payload=%#v", got)
		}
	}
	obj, err = c.GetVolumeProtectionPolicy(ctx, "p1")
	if err != nil || obj["volumes"] == nil {
		t.Fatalf("get=%v err=%v", obj, err)
	}
	obj, err = c.GetVolumeProtectionPolicyDetails(ctx, "p1")
	if err != nil || obj["attachable_volumes"] == nil {
		t.Fatalf("detail=%v err=%v", obj, err)
	}
	if err = c.AttachVolumeToProtectionPolicy(ctx, "p1", "v1"); err != nil {
		t.Fatal(err)
	}
	if err = c.DetachVolumeFromProtectionPolicy(ctx, "p1", "v1"); err != nil {
		t.Fatal(err)
	}
	obj, err = c.ResetVolumeProtectionPolicyFailures(ctx, "p1")
	if err != nil || obj["status"] != "active" {
		t.Fatalf("reset=%v err=%v", obj, err)
	}
	if err = c.DeleteVolumeProtectionPolicy(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 10 {
		t.Fatalf("calls=%v", paths)
	}
}
func TestVolumeProtectionWritesSentOnceAndTypedRefusals(t *testing.T) {
	for _, tc := range []struct {
		status int
		reason string
	}{{503, "dispatch_unknown"}, {409, "volume_already_protected"}, {422, "volume_protection_unsupported"}, {404, "not_found"}} {
		t.Run(tc.reason, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "reason": tc.reason, "message": "refused"})
			}))
			defer srv.Close()
			c := New(srv.URL, "token", time.Second, false)
			err := c.AttachVolumeToProtectionPolicy(context.Background(), "p1", "v1")
			var api *APIError
			if !errors.As(err, &api) || api.Reason != tc.reason || calls != 1 {
				t.Fatalf("calls=%d error=%v", calls, err)
			}
			if tc.status == 503 {
				calls = 0
				_, err = c.CreateVolumeProtectionPolicy(context.Background(), map[string]any{"name": "policy"})
				if err == nil || calls != 1 {
					t.Fatalf("create calls=%d error=%v", calls, err)
				}
			}
		})
	}
}
