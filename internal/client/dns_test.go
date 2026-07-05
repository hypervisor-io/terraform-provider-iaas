package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// This file uses net/http/httptest directly (importing internal/acctest here
// would create an import cycle: acctest → provider → client).

// ── Zone CRUD ────────────────────────────────────────────────────────────────

// TestCreateDnsZone_Success verifies CreateDnsZone POSTs to the PLURAL /dns-zones,
// sends the prebuilt body verbatim (name, description, vpc_ids), and unwraps the
// "zone" envelope returning the id.
func TestCreateDnsZone_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"DNS zone created successfully.","zone":{"id":"z-1","name":"corp.internal","description":"d","status":"active"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.CreateDnsZone(context.Background(), map[string]any{
		"name":        "corp.internal",
		"description": "d",
		"vpc_ids":     []string{"v-1"},
	})
	if err != nil {
		t.Fatalf("CreateDnsZone returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/dns-zones" {
		t.Errorf("path = %s; want /api/dns-zones", gotPath)
	}
	if obj["id"] != "z-1" {
		t.Errorf("obj[id] = %v; want z-1", obj["id"])
	}
	if gotBody["name"] != "corp.internal" || gotBody["description"] != "d" {
		t.Errorf("create body = %v; missing name/description", gotBody)
	}
	if _, ok := gotBody["vpc_ids"]; !ok {
		t.Errorf("create body must include vpc_ids: %v", gotBody)
	}
}

// TestCreateDnsZone_SuccessFalse verifies a 200 success:false maps to an error
// (C3) - e.g. quota exceeded or VPC not owned.
func TestCreateDnsZone_SuccessFalse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"Maximum number of DNS zones reached."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if _, err := c.CreateDnsZone(context.Background(), map[string]any{"name": "x.internal"}); err == nil {
		t.Fatal("expected error on 200 success:false, got nil")
	}
}

// TestGetDnsZone_Success verifies GetDnsZone GETs the SINGULAR path and unwraps
// "zone", exposing the embedded vpcs[] and record_sets[].
func TestGetDnsZone_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"zone":{"id":"z-1","name":"corp.internal","status":"active","vpcs":[{"id":"v-1","name":"prod"}],"record_sets":[{"id":"rs-1","name":"www","type":"A","routing_policy":"simple","ttl":300,"records":[{"id":"r-1","value":"10.0.0.1","enabled":true,"health_check":null}]}]},"available_vpcs":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.GetDnsZone(context.Background(), "z-1")
	if err != nil {
		t.Fatalf("GetDnsZone returned error: %v", err)
	}
	if gotPath != "/api/dns-zone/z-1" {
		t.Errorf("path = %s; want /api/dns-zone/z-1", gotPath)
	}
	if obj["name"] != "corp.internal" {
		t.Errorf("obj[name] = %v; want corp.internal", obj["name"])
	}
	if _, ok := obj["record_sets"].([]any); !ok {
		t.Errorf("expected embedded record_sets array, got %T", obj["record_sets"])
	}
}

// TestGetDnsZone_NotFound verifies a 404 maps to IsNotFound.
func TestGetDnsZone_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"DNS Zone not found"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.GetDnsZone(context.Background(), "z-x")
	if err == nil || !IsNotFound(err) {
		t.Errorf("expected IsNotFound, got %v", err)
	}
}

// TestUpdateDnsZone_Success verifies UpdateDnsZone PATCHes the singular path and
// unwraps the fresh "zone".
func TestUpdateDnsZone_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"DNS zone updated successfully.","zone":{"id":"z-1","name":"corp.internal","description":"new"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.UpdateDnsZone(context.Background(), "z-1", map[string]any{"description": "new"})
	if err != nil {
		t.Fatalf("UpdateDnsZone returned error: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %s; want PATCH", gotMethod)
	}
	if gotPath != "/api/dns-zone/z-1" {
		t.Errorf("path = %s; want /api/dns-zone/z-1", gotPath)
	}
	if gotBody["description"] != "new" {
		t.Errorf("patch body = %v; want description=new", gotBody)
	}
	if obj["description"] != "new" {
		t.Errorf("obj[description] = %v; want new", obj["description"])
	}
}

// TestDeleteDnsZone_Success verifies DeleteDnsZone DELETEs the singular path.
func TestDeleteDnsZone_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"DNS zone deletion queued."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DeleteDnsZone(context.Background(), "z-1"); err != nil {
		t.Fatalf("DeleteDnsZone returned error: %v", err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/api/dns-zone/z-1" {
		t.Errorf("got %s %s; want DELETE /api/dns-zone/z-1", gotMethod, gotPath)
	}
}

// ── Zone ↔ VPC attach/detach ─────────────────────────────────────────────────

// TestAttachDnsZoneVpc_Success verifies attach POSTs {vpc_id} (singular) to
// /dns-zone/{id}/attach-vpc.
func TestAttachDnsZoneVpc_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"VPC attached to DNS zone successfully."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.AttachDnsZoneVpc(context.Background(), "z-1", "v-1"); err != nil {
		t.Fatalf("AttachDnsZoneVpc returned error: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/dns-zone/z-1/attach-vpc" {
		t.Errorf("got %s %s; want POST /api/dns-zone/z-1/attach-vpc", gotMethod, gotPath)
	}
	if gotBody["vpc_id"] != "v-1" {
		t.Errorf("attach body = %v; want vpc_id=v-1 (singular)", gotBody)
	}
}

// TestAttachDnsZoneVpc_SuccessFalse verifies a 200 success:false maps to an error.
func TestAttachDnsZoneVpc_SuccessFalse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"VPC is already attached to this DNS zone."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.AttachDnsZoneVpc(context.Background(), "z-1", "v-1"); err == nil {
		t.Fatal("expected error on 200 success:false, got nil")
	}
}

// TestDetachDnsZoneVpc_Success verifies detach DELETEs /dns-zone/{id}/detach-vpc/{vpcId}
// (vpc id in the PATH, not the body).
func TestDetachDnsZoneVpc_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"VPC detached from DNS zone successfully."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DetachDnsZoneVpc(context.Background(), "z-1", "v-1"); err != nil {
		t.Fatalf("DetachDnsZoneVpc returned error: %v", err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/api/dns-zone/z-1/detach-vpc/v-1" {
		t.Errorf("got %s %s; want DELETE /api/dns-zone/z-1/detach-vpc/v-1", gotMethod, gotPath)
	}
}

// ── Record set CRUD ──────────────────────────────────────────────────────────

// TestCreateDnsRecordSet_Success verifies CreateDnsRecordSet POSTs to
// /dns-zone/{zoneId}/record-sets and unwraps "record_set".
func TestCreateDnsRecordSet_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Record set created successfully.","record_set":{"id":"rs-1","name":"www","type":"A","routing_policy":"simple","ttl":300}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.CreateDnsRecordSet(context.Background(), "z-1", map[string]any{
		"name": "www", "type": "A", "routing_policy": "simple", "ttl": 300,
	})
	if err != nil {
		t.Fatalf("CreateDnsRecordSet returned error: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/dns-zone/z-1/record-sets" {
		t.Errorf("got %s %s; want POST /api/dns-zone/z-1/record-sets", gotMethod, gotPath)
	}
	if obj["id"] != "rs-1" {
		t.Errorf("obj[id] = %v; want rs-1", obj["id"])
	}
	if gotBody["name"] != "www" || gotBody["type"] != "A" || gotBody["routing_policy"] != "simple" {
		t.Errorf("create body = %v; missing fields", gotBody)
	}
}

// TestCreateDnsRecordSet_SuccessFalse verifies a duplicate name+type maps to error.
func TestCreateDnsRecordSet_SuccessFalse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"A record set with this name and type already exists in this zone."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if _, err := c.CreateDnsRecordSet(context.Background(), "z-1", map[string]any{"name": "www"}); err == nil {
		t.Fatal("expected error on 200 success:false, got nil")
	}
}

// TestUpdateDnsRecordSet_Success verifies PATCH /dns-zone/{zoneId}/record-set/{rsId}.
func TestUpdateDnsRecordSet_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Record set updated successfully.","record_set":{"id":"rs-1","name":"www","type":"A","routing_policy":"weighted","ttl":600}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.UpdateDnsRecordSet(context.Background(), "z-1", "rs-1", map[string]any{"ttl": 600})
	if err != nil {
		t.Fatalf("UpdateDnsRecordSet returned error: %v", err)
	}
	if gotMethod != http.MethodPatch || gotPath != "/api/dns-zone/z-1/record-set/rs-1" {
		t.Errorf("got %s %s; want PATCH /api/dns-zone/z-1/record-set/rs-1", gotMethod, gotPath)
	}
	if obj["ttl"].(float64) != 600 {
		t.Errorf("obj[ttl] = %v; want 600", obj["ttl"])
	}
}

// TestDeleteDnsRecordSet_Success verifies DELETE /dns-zone/{zoneId}/record-set/{rsId}.
func TestDeleteDnsRecordSet_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Record set deleted successfully."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DeleteDnsRecordSet(context.Background(), "z-1", "rs-1"); err != nil {
		t.Fatalf("DeleteDnsRecordSet returned error: %v", err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/api/dns-zone/z-1/record-set/rs-1" {
		t.Errorf("got %s %s; want DELETE", gotMethod, gotPath)
	}
}

// TestGetDnsRecordSet_ScanFound verifies the read-by-scan resolves a record set
// from the zone SHOW embedded record_sets[].
func TestGetDnsRecordSet_ScanFound(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"zone":{"id":"z-1","record_sets":[{"id":"rs-1","name":"www","type":"A"},{"id":"rs-2","name":"api","type":"AAAA"}]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.GetDnsRecordSet(context.Background(), "z-1", "rs-2")
	if err != nil {
		t.Fatalf("GetDnsRecordSet returned error: %v", err)
	}
	if gotPath != "/api/dns-zone/z-1" {
		t.Errorf("scan must call zone SHOW; path = %s", gotPath)
	}
	if obj["name"] != "api" {
		t.Errorf("obj[name] = %v; want api", obj["name"])
	}
}

// TestGetDnsRecordSet_ScanAbsent verifies an absent record-set id yields IsNotFound.
func TestGetDnsRecordSet_ScanAbsent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"zone":{"id":"z-1","record_sets":[]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.GetDnsRecordSet(context.Background(), "z-1", "missing")
	if err == nil || !IsNotFound(err) {
		t.Errorf("expected IsNotFound for absent record set, got %v", err)
	}
}

// TestGetDnsRecordSet_ParentNotFound verifies a 404 on the parent zone propagates.
func TestGetDnsRecordSet_ParentNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"DNS Zone not found"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.GetDnsRecordSet(context.Background(), "z-x", "rs-1")
	if err == nil || !IsNotFound(err) {
		t.Errorf("expected IsNotFound from parent 404, got %v", err)
	}
}

// ── Record CRUD ──────────────────────────────────────────────────────────────

// TestCreateDnsRecord_Success verifies POST .../record-set/{rsId}/records and
// unwraps "record".
func TestCreateDnsRecord_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Record created successfully.","record":{"id":"r-1","value":"10.0.0.1","enabled":true}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.CreateDnsRecord(context.Background(), "z-1", "rs-1", map[string]any{"value": "10.0.0.1"})
	if err != nil {
		t.Fatalf("CreateDnsRecord returned error: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/dns-zone/z-1/record-set/rs-1/records" {
		t.Errorf("got %s %s; want POST .../record-set/rs-1/records", gotMethod, gotPath)
	}
	if obj["id"] != "r-1" {
		t.Errorf("obj[id] = %v; want r-1", obj["id"])
	}
	if gotBody["value"] != "10.0.0.1" {
		t.Errorf("create body = %v; want value=10.0.0.1", gotBody)
	}
}

// TestCreateDnsRecord_SuccessFalse verifies type-specific value validation failure.
func TestCreateDnsRecord_SuccessFalse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"A records must contain a valid IPv4 address."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if _, err := c.CreateDnsRecord(context.Background(), "z-1", "rs-1", map[string]any{"value": "nope"}); err == nil {
		t.Fatal("expected error on 200 success:false, got nil")
	}
}

// TestUpdateDnsRecord_Success verifies PATCH .../record/{recId}.
func TestUpdateDnsRecord_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Record updated successfully.","record":{"id":"r-1","value":"10.0.0.2","enabled":false}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.UpdateDnsRecord(context.Background(), "z-1", "rs-1", "r-1", map[string]any{"value": "10.0.0.2", "enabled": false})
	if err != nil {
		t.Fatalf("UpdateDnsRecord returned error: %v", err)
	}
	if gotMethod != http.MethodPatch || gotPath != "/api/dns-zone/z-1/record-set/rs-1/record/r-1" {
		t.Errorf("got %s %s; want PATCH .../record/r-1", gotMethod, gotPath)
	}
	if obj["value"] != "10.0.0.2" {
		t.Errorf("obj[value] = %v; want 10.0.0.2", obj["value"])
	}
	if gotBody["enabled"] != false {
		t.Errorf("patch body = %v; want enabled=false", gotBody)
	}
}

// TestDeleteDnsRecord_Success verifies DELETE .../record/{recId}.
func TestDeleteDnsRecord_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Record deleted successfully."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DeleteDnsRecord(context.Background(), "z-1", "rs-1", "r-1"); err != nil {
		t.Fatalf("DeleteDnsRecord returned error: %v", err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/api/dns-zone/z-1/record-set/rs-1/record/r-1" {
		t.Errorf("got %s %s; want DELETE .../record/r-1", gotMethod, gotPath)
	}
}

// TestGetDnsRecord_ScanFound verifies the two-level read-by-scan resolves a record
// from zone.record_sets[].records[].
func TestGetDnsRecord_ScanFound(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"zone":{"id":"z-1","record_sets":[{"id":"rs-1","records":[{"id":"r-1","value":"10.0.0.1"},{"id":"r-2","value":"10.0.0.2"}]},{"id":"rs-2","records":[{"id":"r-9","value":"x"}]}]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.GetDnsRecord(context.Background(), "z-1", "rs-1", "r-2")
	if err != nil {
		t.Fatalf("GetDnsRecord returned error: %v", err)
	}
	if gotPath != "/api/dns-zone/z-1" {
		t.Errorf("scan must call zone SHOW; path = %s", gotPath)
	}
	if obj["value"] != "10.0.0.2" {
		t.Errorf("obj[value] = %v; want 10.0.0.2", obj["value"])
	}
}

// TestGetDnsRecord_ScanAbsent verifies an absent record id yields IsNotFound, even
// when the parent record set exists.
func TestGetDnsRecord_ScanAbsent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"zone":{"id":"z-1","record_sets":[{"id":"rs-1","records":[]}]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.GetDnsRecord(context.Background(), "z-1", "rs-1", "missing")
	if err == nil || !IsNotFound(err) {
		t.Errorf("expected IsNotFound for absent record, got %v", err)
	}
}

// ── Health check ─────────────────────────────────────────────────────────────

// TestStoreDnsHealthCheck_Success verifies POST .../record/{recId}/health-check
// sends the body and unwraps "health_check".
func TestStoreDnsHealthCheck_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Health check created successfully.","health_check":{"id":"hc-1","type":"http","port":80,"path":"/health","interval":30}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.StoreDnsHealthCheck(context.Background(), "z-1", "rs-1", "r-1", map[string]any{
		"type": "http", "port": 80, "path": "/health",
	})
	if err != nil {
		t.Fatalf("StoreDnsHealthCheck returned error: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/dns-zone/z-1/record-set/rs-1/record/r-1/health-check" {
		t.Errorf("got %s %s; want POST .../health-check", gotMethod, gotPath)
	}
	if obj["id"] != "hc-1" {
		t.Errorf("obj[id] = %v; want hc-1", obj["id"])
	}
	if gotBody["type"] != "http" || gotBody["path"] != "/health" {
		t.Errorf("hc body = %v; missing type/path", gotBody)
	}
}

// TestStoreDnsHealthCheck_SuccessFalse verifies a validation failure maps to error.
func TestStoreDnsHealthCheck_SuccessFalse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(422)
		_, _ = w.Write([]byte(`{"message":"The type field is required.","errors":{"type":["required"]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if _, err := c.StoreDnsHealthCheck(context.Background(), "z-1", "rs-1", "r-1", map[string]any{}); err == nil {
		t.Fatal("expected error on 422, got nil")
	}
}

// TestDeleteDnsHealthCheck_Success verifies DELETE .../record/{recId}/health-check.
func TestDeleteDnsHealthCheck_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Health check deleted successfully."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	if err := c.DeleteDnsHealthCheck(context.Background(), "z-1", "rs-1", "r-1"); err != nil {
		t.Fatalf("DeleteDnsHealthCheck returned error: %v", err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/api/dns-zone/z-1/record-set/rs-1/record/r-1/health-check" {
		t.Errorf("got %s %s; want DELETE .../health-check", gotMethod, gotPath)
	}
}

// ── Empty-id guards ──────────────────────────────────────────────────────────

func TestDns_EmptyIDGuards(t *testing.T) {
	c := New("http://unused/api", "tok", time.Second, false)
	ctx := context.Background()

	if _, err := c.GetDnsZone(ctx, ""); err == nil {
		t.Error("GetDnsZone: expected empty-id error")
	}
	if _, err := c.UpdateDnsZone(ctx, "", nil); err == nil {
		t.Error("UpdateDnsZone: expected empty-id error")
	}
	if err := c.DeleteDnsZone(ctx, ""); err == nil {
		t.Error("DeleteDnsZone: expected empty-id error")
	}
	if err := c.AttachDnsZoneVpc(ctx, "", "v"); err == nil {
		t.Error("AttachDnsZoneVpc: expected empty-zoneID error")
	}
	if err := c.AttachDnsZoneVpc(ctx, "z", ""); err == nil {
		t.Error("AttachDnsZoneVpc: expected empty-vpcID error")
	}
	if err := c.DetachDnsZoneVpc(ctx, "z", ""); err == nil {
		t.Error("DetachDnsZoneVpc: expected empty-vpcID error")
	}
	if _, err := c.CreateDnsRecordSet(ctx, "", nil); err == nil {
		t.Error("CreateDnsRecordSet: expected empty-zoneID error")
	}
	if _, err := c.UpdateDnsRecordSet(ctx, "z", "", nil); err == nil {
		t.Error("UpdateDnsRecordSet: expected empty-rsID error")
	}
	if err := c.DeleteDnsRecordSet(ctx, "z", ""); err == nil {
		t.Error("DeleteDnsRecordSet: expected empty-rsID error")
	}
	if _, err := c.GetDnsRecordSet(ctx, "z", ""); err == nil {
		t.Error("GetDnsRecordSet: expected empty-rsID error")
	}
	if _, err := c.CreateDnsRecord(ctx, "z", "", nil); err == nil {
		t.Error("CreateDnsRecord: expected empty-rsID error")
	}
	if _, err := c.UpdateDnsRecord(ctx, "z", "rs", "", nil); err == nil {
		t.Error("UpdateDnsRecord: expected empty-recID error")
	}
	if err := c.DeleteDnsRecord(ctx, "z", "rs", ""); err == nil {
		t.Error("DeleteDnsRecord: expected empty-recID error")
	}
	if _, err := c.GetDnsRecord(ctx, "z", "rs", ""); err == nil {
		t.Error("GetDnsRecord: expected empty-recID error")
	}
	if _, err := c.StoreDnsHealthCheck(ctx, "z", "rs", "", nil); err == nil {
		t.Error("StoreDnsHealthCheck: expected empty-recID error")
	}
	if err := c.DeleteDnsHealthCheck(ctx, "z", "rs", ""); err == nil {
		t.Error("DeleteDnsHealthCheck: expected empty-recID error")
	}
}
