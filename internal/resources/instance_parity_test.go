package resources

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hypervisor-io/terraform-provider-iaas/client"
)

func TestInstancePolicyPartialTimezonePatch(t *testing.T) {
	prior := instanceBackupPolicyModel{
		Name: types.StringValue("nightly"), FullBackupFrequency: types.StringValue("daily"),
		FullBackupTime: types.StringValue("02:30"), FullBackupDay: types.Int64Null(),
		MaxIncrementalChain: types.Int64Value(0), RetentionCount: types.Int64Value(7),
		RetentionMode: types.StringValue("gfs"), BackupDevice: types.StringValue("all"),
		Timezone: types.StringValue("Europe/Amsterdam"), KeepLast: types.Int64Value(2),
	}
	for _, tc := range []struct {
		name string
		zone types.String
		want map[string]any
	}{
		{"unchanged", prior.Timezone, map[string]any{}},
		{"set", types.StringValue("Asia/Kolkata"), map[string]any{"timezone": "Asia/Kolkata", "full_backup_time": "02:30"}},
		{"clear", types.StringNull(), map[string]any{"timezone": nil, "full_backup_time": "02:30"}},
		{"unknown", types.StringUnknown(), map[string]any{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := prior
			plan.Timezone = tc.zone
			if got := ibpUpdateBody(plan, prior); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("PATCH = %#v, want %#v", got, tc.want)
			}
		})
	}
	plan := prior
	plan.KeepLast = types.Int64Null()
	plan.KeepDaily = types.Int64Value(0)
	got := ibpUpdateBody(plan, prior)
	if !reflect.DeepEqual(got, map[string]any{"keep_last": nil, "keep_daily": int64(0)}) {
		t.Fatalf("GFS PATCH = %#v", got)
	}
}

func TestInstancePolicyGFSCreateAndRead(t *testing.T) {
	plan := instanceBackupPolicyModel{
		Name: types.StringValue("gfs"), FullBackupFrequency: types.StringValue("daily"),
		FullBackupTime: types.StringValue("02:30"), FullBackupDay: types.Int64Null(),
		MaxIncrementalChain: types.Int64Value(0), RetentionCount: types.Int64Unknown(),
		RetentionMode: types.StringValue("gfs"), BackupDevice: types.StringValue("all"),
		Timezone: types.StringValue("Europe/Amsterdam"), KeepLast: types.Int64Value(2),
		KeepDaily: types.Int64Value(0), KeepWeekly: types.Int64Value(4),
	}
	body := ibpCreateBody(plan)
	if _, ok := body["retention_count"]; ok {
		t.Fatal("GFS must permit omitted retention_count")
	}
	if body["retention_mode"] != "gfs" || body["keep_last"] != int64(2) || body["keep_daily"] != int64(0) || body["timezone"] != "Europe/Amsterdam" {
		t.Fatalf("create body = %#v", body)
	}
	obj := map[string]any{"id": "p1", "retention_mode": "gfs", "retention_count": float64(7), "timezone": nil, "keep_last": float64(2), "keep_daily": nil, "keep_weekly": nil, "instances": []any{}}
	got, diags := ibpStateFromAPI(obj, plan)
	if diags.HasError() {
		t.Fatal(diags)
	}
	if !got.Timezone.IsNull() || got.KeepLast.ValueInt64() != 2 || got.KeepDaily.IsNull() || got.KeepDaily.ValueInt64() != 0 || !got.KeepWeekly.IsNull() {
		t.Fatalf("read lost nullable/zero semantics: %#v", got)
	}
}

func TestInstanceTagsApplyAndClear(t *testing.T) {
	for _, tags := range []string{"web,prod", "web;prod", " web,prod ", ""} {
		t.Run(tags, func(t *testing.T) {
			var calls int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/instance/i1/tags" && r.Method == http.MethodPost {
					calls++
					var body map[string]any
					_ = json.NewDecoder(r.Body).Decode(&body)
					if body["tags"] != tags {
						t.Errorf("tags body = %#v", body)
					}
					_, _ = w.Write([]byte(`{"success":true,"message":"Tags updated"}`))
					return
				}
				if r.URL.Path != "/api/instance/i1" || r.Method != http.MethodGet {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "i1", "tags": tags})
			}))
			defer srv.Close()
			r := &instanceResource{client: client.New(srv.URL+"/api", "token", time.Second, false)}
			if err := r.applyInstanceTags(context.Background(), "i1", tags); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("tag writes = %d", calls)
			}
			state := instanceStateFromAPI(map[string]any{"id": "i1", "tags": normalizedInstanceTags(tags)}, instanceModel{Tags: types.StringValue(tags)})
			if state.Tags.ValueString() != tags || state.Tags.IsNull() {
				t.Fatalf("tags state = %v", state.Tags)
			}
		})
	}
	if got := instanceTagsFromAPI(map[string]any{"tags": nil}, types.StringValue("old")); got.IsNull() || got.ValueString() != "" {
		t.Fatalf("null tags = %v", got)
	}
}

func TestInstanceTagsRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Error("must not read after refused tags")
		}
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"success":false,"message":"VM tags are a Proxmox-only feature."}`))
	}))
	defer srv.Close()
	r := &instanceResource{client: client.New(srv.URL+"/api", "token", time.Second, false)}
	if err := r.applyInstanceTags(context.Background(), "i1", "web"); err == nil || !strings.Contains(err.Error(), "Proxmox-only") {
		t.Fatalf("refusal = %v", err)
	}
}

func TestCloudCapacityDiagnosticIncludesReason(t *testing.T) {
	for _, reason := range []string{"ipv6_not_available", "storage_backend_not_offered", "no_capacity"} {
		t.Run(reason, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/cloud-service/instances" || r.Method != http.MethodPost {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(http.StatusUnprocessableEntity)
				_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "message": "Try another plan or location.", "reason": reason})
			}))
			defer srv.Close()
			cl := client.New(srv.URL+"/api", "token", time.Second, false)
			_, err := cl.CreateCSInstance(context.Background(), map[string]any{"location_id": "l1", "plan_id": "p1"})
			if err == nil {
				t.Fatal("expected capacity refusal")
			}
			diagnostic := diagFromErr("Error creating instance", err)
			if !strings.Contains(diagnostic.Detail(), reason) {
				t.Fatalf("diagnostic = %s", diagnostic.Detail())
			}
		})
	}
}
