package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// NOTE: like the other client tests, this file uses net/http/httptest directly
// rather than internal/acctest.MockServer (acctest → provider → client would be
// an import cycle).

func s3TestClient(url string) *Client {
	return New(url+"/api", "tok", 10*time.Second, false)
}

// ─── Buckets ────────────────────────────────────────────────────────────────

// TestCreateS3Bucket_Success verifies CreateS3Bucket POSTs the prebuilt body to
// the PLURAL /object-storage/buckets path and tolerates the id-less
// {success,message} response (returning the bare envelope so the resource can
// do its C4 readback).
func TestCreateS3Bucket_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"Bucket created successfully."}`))
	}))
	defer srv.Close()

	body := map[string]any{"name": "my-bucket", "s3_plan_id": "plan-1", "s3_server_id": "srv-1"}
	if _, err := s3TestClient(srv.URL).CreateS3Bucket(context.Background(), body); err != nil {
		t.Fatalf("CreateS3Bucket returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/object-storage/buckets" {
		t.Errorf("path = %s; want /api/object-storage/buckets", gotPath)
	}
	if gotBody["name"] != "my-bucket" || gotBody["s3_plan_id"] != "plan-1" || gotBody["s3_server_id"] != "srv-1" {
		t.Errorf("body = %v; missing required fields", gotBody)
	}
}

// TestCreateS3Bucket_Failure422 verifies a 422 {success:false,message} (quota /
// plan disabled) is surfaced as an error.
func TestCreateS3Bucket_Failure422(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"success":false,"message":"Selected plan is not available."}`))
	}))
	defer srv.Close()
	if _, err := s3TestClient(srv.URL).CreateS3Bucket(context.Background(), map[string]any{"name": "x"}); err == nil {
		t.Fatal("expected error for 422 plan-disabled response")
	}
}

// TestGetS3Bucket_Success verifies the SHOW envelope is returned bare (key="")
// so the resource can read the nested bucket object AND the top-level
// access_key/secret_key/endpoint.
func TestGetS3Bucket_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"bucket":{"id":"b1","name":"my-bucket","default_access":"private","s3_plan_id":"plan-1","s3_server_id":"srv-1","suspended":0,"quota":1073741824},"endpoint":"https://s3.example.com","access_key":"ak_bucketown","secret_key":"sk_bucketown"}`))
	}))
	defer srv.Close()

	obj, err := s3TestClient(srv.URL).GetS3Bucket(context.Background(), "b1")
	if err != nil {
		t.Fatalf("GetS3Bucket error: %v", err)
	}
	if gotPath != "/api/object-storage/bucket/b1" {
		t.Errorf("path = %s; want singular /api/object-storage/bucket/b1", gotPath)
	}
	if obj["access_key"] != "ak_bucketown" || obj["secret_key"] != "sk_bucketown" || obj["endpoint"] != "https://s3.example.com" {
		t.Errorf("top-level envelope keys missing: %v", obj)
	}
	bucket, _ := obj["bucket"].(map[string]any)
	if bucket == nil || bucket["id"] != "b1" {
		t.Errorf("nested bucket object missing/wrong: %v", obj["bucket"])
	}
}

// TestGetS3Bucket_NotFound verifies a 404 is an IsNotFound *APIError.
func TestGetS3Bucket_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"S3 Bucket not found"}`))
	}))
	defer srv.Close()
	_, err := s3TestClient(srv.URL).GetS3Bucket(context.Background(), "missing")
	if err == nil || !IsNotFound(err) {
		t.Fatalf("expected IsNotFound error; got %v", err)
	}
}

// TestGetS3Bucket_EmptyID verifies the empty-id guard.
func TestGetS3Bucket_EmptyID(t *testing.T) {
	if _, err := (&Client{}).GetS3Bucket(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty id")
	}
}

// TestGetS3BucketByName_Match verifies the C4 readback finds a bucket by its
// unique name in the LIST paginator.
func TestGetS3BucketByName_Match(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"current_page":1,"last_page":1,"data":[{"id":"b1","name":"other"},{"id":"b2","name":"my-bucket"}]}`))
	}))
	defer srv.Close()
	obj, err := s3TestClient(srv.URL).GetS3BucketByName(context.Background(), "my-bucket")
	if err != nil {
		t.Fatalf("GetS3BucketByName error: %v", err)
	}
	if obj["id"] != "b2" {
		t.Errorf("matched id = %v; want b2", obj["id"])
	}
}

// TestGetS3BucketByName_NotFound verifies a missing name yields IsNotFound.
func TestGetS3BucketByName_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"current_page":1,"last_page":1,"data":[]}`))
	}))
	defer srv.Close()
	_, err := s3TestClient(srv.URL).GetS3BucketByName(context.Background(), "nope")
	if err == nil || !IsNotFound(err) {
		t.Fatalf("expected IsNotFound; got %v", err)
	}
}

// TestSetS3BucketACL_Success verifies the action is placed in the path.
func TestSetS3BucketACL_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"ok"}`))
	}))
	defer srv.Close()
	if err := s3TestClient(srv.URL).SetS3BucketACL(context.Background(), "b1", "public"); err != nil {
		t.Fatalf("SetS3BucketACL error: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %s; want PATCH", gotMethod)
	}
	if gotPath != "/api/object-storage/bucket/b1/acl/public" {
		t.Errorf("path = %s; want .../bucket/b1/acl/public", gotPath)
	}
}

// TestSetS3BucketACL_EmptyArgs verifies guards on id + action.
func TestSetS3BucketACL_EmptyArgs(t *testing.T) {
	if err := (&Client{}).SetS3BucketACL(context.Background(), "", "public"); err == nil {
		t.Error("expected error for empty id")
	}
	if err := (&Client{}).SetS3BucketACL(context.Background(), "b1", ""); err == nil {
		t.Error("expected error for empty action")
	}
}

// TestDeleteS3Bucket_Success / Failure / EmptyID.
func TestDeleteS3Bucket_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"queued for deletion"}`))
	}))
	defer srv.Close()
	if err := s3TestClient(srv.URL).DeleteS3Bucket(context.Background(), "b1"); err != nil {
		t.Fatalf("DeleteS3Bucket error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s; want DELETE", gotMethod)
	}
	if gotPath != "/api/object-storage/bucket/b1" {
		t.Errorf("path = %s; want singular .../bucket/b1", gotPath)
	}
}

func TestDeleteS3Bucket_Failure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"could not delete"}`))
	}))
	defer srv.Close()
	if err := s3TestClient(srv.URL).DeleteS3Bucket(context.Background(), "b1"); err == nil {
		t.Fatal("expected error for success:false delete")
	}
}

func TestDeleteS3Bucket_EmptyID(t *testing.T) {
	if err := (&Client{}).DeleteS3Bucket(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty id")
	}
}

// ─── Bucket ↔ key attachments ────────────────────────────────────────────────

// TestListS3BucketKeys_Success verifies the attached-keys listing carries the
// pivot.permission.
func TestListS3BucketKeys_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"current_page":1,"last_page":1,"data":[{"id":"k1","name":"key-one","access_key":"ak_one","pivot":{"s3_bucket_id":"b1","user_s3_access_key_id":"k1","permission":"readwrite"}}]}`))
	}))
	defer srv.Close()
	items, err := s3TestClient(srv.URL).ListS3BucketKeys(context.Background(), "b1")
	if err != nil {
		t.Fatalf("ListS3BucketKeys error: %v", err)
	}
	if gotPath != "/api/object-storage/bucket/b1/keys" {
		t.Errorf("path = %s; want .../bucket/b1/keys", gotPath)
	}
	if len(items) != 1 {
		t.Fatalf("got %d keys; want 1", len(items))
	}
	pivot, _ := items[0]["pivot"].(map[string]any)
	if pivot == nil || pivot["permission"] != "readwrite" {
		t.Errorf("pivot permission missing: %v", items[0])
	}
}

func TestListS3BucketKeys_EmptyID(t *testing.T) {
	if _, err := (&Client{}).ListS3BucketKeys(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty bucket id")
	}
}

// TestAttachS3BucketKey_Success verifies POST attach with permission body.
func TestAttachS3BucketKey_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"attached"}`))
	}))
	defer srv.Close()
	if err := s3TestClient(srv.URL).AttachS3BucketKey(context.Background(), "b1", "k1", "read"); err != nil {
		t.Fatalf("AttachS3BucketKey error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/object-storage/bucket/b1/attach/k1" {
		t.Errorf("path = %s; want .../bucket/b1/attach/k1", gotPath)
	}
	if gotBody["permission"] != "read" {
		t.Errorf("body permission = %v; want read", gotBody["permission"])
	}
}

// TestUpdateS3BucketKey_Success verifies PATCH update with permission body
// (in-place change, not delete+add).
func TestUpdateS3BucketKey_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"updated"}`))
	}))
	defer srv.Close()
	if err := s3TestClient(srv.URL).UpdateS3BucketKey(context.Background(), "b1", "k1", "readwrite"); err != nil {
		t.Fatalf("UpdateS3BucketKey error: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %s; want PATCH", gotMethod)
	}
	if gotPath != "/api/object-storage/bucket/b1/update/k1" {
		t.Errorf("path = %s; want .../bucket/b1/update/k1", gotPath)
	}
	if gotBody["permission"] != "readwrite" {
		t.Errorf("body permission = %v; want readwrite", gotBody["permission"])
	}
}

// TestDetachS3BucketKey_Success verifies POST detach (not DELETE).
func TestDetachS3BucketKey_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"detached"}`))
	}))
	defer srv.Close()
	if err := s3TestClient(srv.URL).DetachS3BucketKey(context.Background(), "b1", "k1"); err != nil {
		t.Fatalf("DetachS3BucketKey error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST (detach is POST not DELETE)", gotMethod)
	}
	if gotPath != "/api/object-storage/bucket/b1/detach/k1" {
		t.Errorf("path = %s; want .../bucket/b1/detach/k1", gotPath)
	}
}

func TestS3BucketKeyOps_EmptyArgs(t *testing.T) {
	c := &Client{}
	if err := c.AttachS3BucketKey(context.Background(), "", "k1", "read"); err == nil {
		t.Error("attach: expected error for empty bucket id")
	}
	if err := c.AttachS3BucketKey(context.Background(), "b1", "", "read"); err == nil {
		t.Error("attach: expected error for empty key id")
	}
	if err := c.UpdateS3BucketKey(context.Background(), "", "k1", "read"); err == nil {
		t.Error("update: expected error for empty bucket id")
	}
	if err := c.DetachS3BucketKey(context.Background(), "b1", ""); err == nil {
		t.Error("detach: expected error for empty key id")
	}
}

// ─── Access keys ─────────────────────────────────────────────────────────────

// TestCreateS3AccessKey_Success verifies the secret-key-shown-once create:
//   - POSTs name to the PLURAL /object-storage/access-keys path
//   - unwraps the "data" sub-object exposing access_key + secret_key
func TestCreateS3AccessKey_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"created","data":{"access_key":"ak_secret123","secret_key":"sk_shown_once"}}`))
	}))
	defer srv.Close()
	obj, err := s3TestClient(srv.URL).CreateS3AccessKey(context.Background(), map[string]any{"name": "my-key"})
	if err != nil {
		t.Fatalf("CreateS3AccessKey error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/object-storage/access-keys" {
		t.Errorf("path = %s; want /api/object-storage/access-keys", gotPath)
	}
	if gotBody["name"] != "my-key" {
		t.Errorf("body name = %v; want my-key", gotBody["name"])
	}
	if obj["access_key"] != "ak_secret123" {
		t.Errorf("access_key = %v; want ak_secret123", obj["access_key"])
	}
	if obj["secret_key"] != "sk_shown_once" {
		t.Errorf("secret_key = %v; want sk_shown_once (shown once on create)", obj["secret_key"])
	}
}

// TestCreateS3AccessKey_Failure verifies success:false surfaces as an error.
func TestCreateS3AccessKey_Failure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"success":false,"message":"The name has already been taken."}`))
	}))
	defer srv.Close()
	if _, err := s3TestClient(srv.URL).CreateS3AccessKey(context.Background(), map[string]any{"name": "dup"}); err == nil {
		t.Fatal("expected error for duplicate-name 422")
	}
}

// TestGetS3AccessKey_MatchById verifies list-and-scan by record id (no SHOW
// route) and that secret_key is absent from the listing.
func TestGetS3AccessKey_MatchById(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"current_page":1,"last_page":1,"data":[{"id":"k1","name":"key-one","access_key":"ak_one","active":1},{"id":"k2","name":"key-two","access_key":"ak_two","active":0}]}`))
	}))
	defer srv.Close()
	obj, err := s3TestClient(srv.URL).GetS3AccessKey(context.Background(), "k2")
	if err != nil {
		t.Fatalf("GetS3AccessKey error: %v", err)
	}
	if obj["id"] != "k2" || obj["name"] != "key-two" {
		t.Errorf("matched wrong key: %v", obj)
	}
	if _, present := obj["secret_key"]; present {
		t.Error("secret_key must NOT be present in the listing ($hidden)")
	}
}

func TestGetS3AccessKey_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"current_page":1,"last_page":1,"data":[]}`))
	}))
	defer srv.Close()
	_, err := s3TestClient(srv.URL).GetS3AccessKey(context.Background(), "missing")
	if err == nil || !IsNotFound(err) {
		t.Fatalf("expected IsNotFound; got %v", err)
	}
}

func TestGetS3AccessKey_EmptyID(t *testing.T) {
	if _, err := (&Client{}).GetS3AccessKey(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty id")
	}
}

// TestGetS3AccessKeyByAccessKey_Match verifies the C4 readback by the public
// access_key string.
func TestGetS3AccessKeyByAccessKey_Match(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"current_page":1,"last_page":1,"data":[{"id":"k1","name":"key-one","access_key":"ak_one"},{"id":"k2","name":"key-two","access_key":"ak_secret123"}]}`))
	}))
	defer srv.Close()
	obj, err := s3TestClient(srv.URL).GetS3AccessKeyByAccessKey(context.Background(), "ak_secret123")
	if err != nil {
		t.Fatalf("GetS3AccessKeyByAccessKey error: %v", err)
	}
	if obj["id"] != "k2" {
		t.Errorf("matched id = %v; want k2", obj["id"])
	}
}

func TestGetS3AccessKeyByAccessKey_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"current_page":1,"last_page":1,"data":[]}`))
	}))
	defer srv.Close()
	_, err := s3TestClient(srv.URL).GetS3AccessKeyByAccessKey(context.Background(), "ak_nope")
	if err == nil || !IsNotFound(err) {
		t.Fatalf("expected IsNotFound; got %v", err)
	}
}

// TestUpdateS3AccessKey_Success verifies the PATCH on the SINGULAR path with the
// partial body.
func TestUpdateS3AccessKey_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"message":"updated"}`))
	}))
	defer srv.Close()
	err := s3TestClient(srv.URL).UpdateS3AccessKey(context.Background(), "k1", map[string]any{"name": "renamed", "active": false})
	if err != nil {
		t.Fatalf("UpdateS3AccessKey error: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %s; want PATCH", gotMethod)
	}
	if gotPath != "/api/object-storage/access-key/k1" {
		t.Errorf("path = %s; want singular .../access-key/k1", gotPath)
	}
	if gotBody["name"] != "renamed" || gotBody["active"] != false {
		t.Errorf("body = %v; want name=renamed active=false", gotBody)
	}
}

func TestUpdateS3AccessKey_EmptyID(t *testing.T) {
	if err := (&Client{}).UpdateS3AccessKey(context.Background(), "", map[string]any{"name": "x"}); err == nil {
		t.Fatal("expected error for empty id")
	}
}
