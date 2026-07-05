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
// rather than internal/acctest.MockServer. acctest imports internal/provider
// which imports internal/client, so importing acctest here would create an
// import cycle.

// ---------------------------------------------------------------------------
// ListNotificationChannels
// ---------------------------------------------------------------------------

func TestListNotificationChannels_Success(t *testing.T) {
	var gotMethod, gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"channels":{"current_page":1,"data":[{"id":"nc-uuid-1","name":"Ops Slack","type":"slack","enabled":1,"auto_disabled":false},{"id":"nc-uuid-2","name":"Dev Webhook","type":"webhook","enabled":1,"auto_disabled":false}],"per_page":25,"total":2}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	items, err := c.ListNotificationChannels(context.Background())
	if err != nil {
		t.Fatalf("ListNotificationChannels returned error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %s; want GET", gotMethod)
	}
	if gotPath != "/api/notification-channels" {
		t.Errorf("path = %s; want /api/notification-channels (plural)", gotPath)
	}
	if len(items) != 2 {
		t.Errorf("len(items) = %d; want 2", len(items))
	}
	if items[0]["id"] != "nc-uuid-1" {
		t.Errorf("items[0][id] = %v; want nc-uuid-1", items[0]["id"])
	}
}

func TestListNotificationChannels_Empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"channels":{"current_page":1,"data":[],"per_page":25,"total":0}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	items, err := c.ListNotificationChannels(context.Background())
	if err != nil {
		t.Fatalf("ListNotificationChannels returned error: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("len(items) = %d; want 0", len(items))
	}
}

// ---------------------------------------------------------------------------
// CreateNotificationChannel
// ---------------------------------------------------------------------------

func TestCreateNotificationChannel_Slack_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"channel":{"id":"nc-uuid-1","name":"Ops Slack","type":"slack","enabled":1,"auto_disabled":false,"config":{"webhook_url":"https://hooks.slack.com/services/T000/B000/XYZ"}}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	body := map[string]any{
		"name":   "Ops Slack",
		"type":   "slack",
		"config": map[string]any{"webhook_url": "https://hooks.slack.com/services/T000/B000/XYZ"},
	}
	obj, err := c.CreateNotificationChannel(context.Background(), body)
	if err != nil {
		t.Fatalf("CreateNotificationChannel returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/api/notification-channels" {
		t.Errorf("path = %s; want /api/notification-channels (plural)", gotPath)
	}
	if obj["id"] != "nc-uuid-1" {
		t.Errorf("obj[id] = %v; want nc-uuid-1", obj["id"])
	}
	if gotBody["name"] != "Ops Slack" {
		t.Errorf("body[name] = %v; want Ops Slack", gotBody["name"])
	}
	if gotBody["type"] != "slack" {
		t.Errorf("body[type] = %v; want slack", gotBody["type"])
	}
	cfg, _ := gotBody["config"].(map[string]any)
	if cfg["webhook_url"] != "https://hooks.slack.com/services/T000/B000/XYZ" {
		t.Errorf("body[config][webhook_url] = %v; want slack URL", cfg["webhook_url"])
	}
}

func TestCreateNotificationChannel_Webhook_Success(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"channel":{"id":"nc-uuid-3","name":"Dev Webhook","type":"webhook","enabled":1,"auto_disabled":false,"config":{"url":"https://example.com/hooks/alerts","method":"POST","verify_ssl":true}}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	body := map[string]any{
		"name": "Dev Webhook",
		"type": "webhook",
		"config": map[string]any{
			"url":        "https://example.com/hooks/alerts",
			"method":     "POST",
			"verify_ssl": true,
		},
	}
	obj, err := c.CreateNotificationChannel(context.Background(), body)
	if err != nil {
		t.Fatalf("CreateNotificationChannel returned error: %v", err)
	}
	if obj["id"] != "nc-uuid-3" {
		t.Errorf("obj[id] = %v; want nc-uuid-3", obj["id"])
	}
	cfg, _ := gotBody["config"].(map[string]any)
	if cfg["url"] != "https://example.com/hooks/alerts" {
		t.Errorf("body[config][url] = %v; want webhook URL", cfg["url"])
	}
}

func TestCreateNotificationChannel_Failure_ValidationError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"errors":{"name":["The name field is required."],"type":["The type field is required."]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.CreateNotificationChannel(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("CreateNotificationChannel: expected error for 422, got nil")
	}
	if !contains(err.Error(), "name field is required") {
		t.Errorf("error = %q; want it to contain %q", err.Error(), "name field is required")
	}
}

func TestCreateNotificationChannel_Failure_SuccessFalse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"Type is invalid."}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.CreateNotificationChannel(context.Background(), map[string]any{"name": "x", "type": "fax"})
	if err == nil {
		t.Fatal("CreateNotificationChannel: expected error for success:false, got nil")
	}
}

// ---------------------------------------------------------------------------
// GetNotificationChannel
// ---------------------------------------------------------------------------

func TestGetNotificationChannel_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"channel":{"id":"nc-uuid-1","name":"Ops Slack","type":"slack","enabled":1,"auto_disabled":false,"failure_count":0,"config":{"webhook_url":"https://hooks.slack.com/services/T000/B000/XYZ"}}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	obj, err := c.GetNotificationChannel(context.Background(), "nc-uuid-1")
	if err != nil {
		t.Fatalf("GetNotificationChannel returned error: %v", err)
	}
	if gotPath != "/api/notification-channel/nc-uuid-1" {
		t.Errorf("path = %s; want /api/notification-channel/nc-uuid-1 (singular)", gotPath)
	}
	if obj["id"] != "nc-uuid-1" {
		t.Errorf("obj[id] = %v; want nc-uuid-1", obj["id"])
	}
	cfg, _ := obj["config"].(map[string]any)
	if cfg["webhook_url"] != "https://hooks.slack.com/services/T000/B000/XYZ" {
		t.Errorf("obj[config][webhook_url] = %v; want slack URL", cfg["webhook_url"])
	}
}

func TestGetNotificationChannel_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"Notification Channel not found"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	_, err := c.GetNotificationChannel(context.Background(), "nc-missing")
	if err == nil {
		t.Fatal("GetNotificationChannel: expected error for 404, got nil")
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound = false; want true for 404 response, err = %v", err)
	}
}

func TestGetNotificationChannel_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	_, err := c.GetNotificationChannel(context.Background(), "")
	if err == nil {
		t.Fatal("GetNotificationChannel: expected error for empty id, got nil")
	}
	if !contains(err.Error(), "empty id") {
		t.Errorf("error = %q; want it to mention empty id", err.Error())
	}
}

// ---------------------------------------------------------------------------
// UpdateNotificationChannel
// ---------------------------------------------------------------------------

func TestUpdateNotificationChannel_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"channel":{"id":"nc-uuid-1","name":"Ops Slack (prod)","type":"slack","enabled":1,"auto_disabled":false,"config":{"webhook_url":"https://hooks.slack.com/services/T000/B000/XYZ"}}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	body := map[string]any{
		"name":    "Ops Slack (prod)",
		"type":    "slack",
		"enabled": true,
		"config":  map[string]any{"webhook_url": "https://hooks.slack.com/services/T000/B000/XYZ"},
	}
	obj, err := c.UpdateNotificationChannel(context.Background(), "nc-uuid-1", body)
	if err != nil {
		t.Fatalf("UpdateNotificationChannel returned error: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %s; want PATCH", gotMethod)
	}
	if gotPath != "/api/notification-channel/nc-uuid-1" {
		t.Errorf("path = %s; want /api/notification-channel/nc-uuid-1 (singular)", gotPath)
	}
	if obj["id"] != "nc-uuid-1" {
		t.Errorf("obj[id] = %v; want nc-uuid-1", obj["id"])
	}
	if gotBody["name"] != "Ops Slack (prod)" {
		t.Errorf("body[name] = %v; want Ops Slack (prod)", gotBody["name"])
	}
}

func TestUpdateNotificationChannel_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	_, err := c.UpdateNotificationChannel(context.Background(), "", map[string]any{"name": "x", "type": "slack"})
	if err == nil {
		t.Fatal("UpdateNotificationChannel: expected error for empty id, got nil")
	}
	if !contains(err.Error(), "empty id") {
		t.Errorf("error = %q; want it to mention empty id", err.Error())
	}
}

// ---------------------------------------------------------------------------
// DeleteNotificationChannel
// ---------------------------------------------------------------------------

func TestDeleteNotificationChannel_Success(t *testing.T) {
	var gotMethod, gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	err := c.DeleteNotificationChannel(context.Background(), "nc-uuid-1")
	if err != nil {
		t.Fatalf("DeleteNotificationChannel returned error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s; want DELETE", gotMethod)
	}
	if gotPath != "/api/notification-channel/nc-uuid-1" {
		t.Errorf("path = %s; want /api/notification-channel/nc-uuid-1 (singular)", gotPath)
	}
}

func TestDeleteNotificationChannel_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"Notification Channel not found"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api", "tok", 10*time.Second, false)
	err := c.DeleteNotificationChannel(context.Background(), "nc-missing")
	if err == nil {
		t.Fatal("DeleteNotificationChannel: expected error for 404, got nil")
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound = false; want true for 404 response, err = %v", err)
	}
}

func TestDeleteNotificationChannel_EmptyID(t *testing.T) {
	c := New("http://localhost/api", "tok", 10*time.Second, false)
	err := c.DeleteNotificationChannel(context.Background(), "")
	if err == nil {
		t.Fatal("DeleteNotificationChannel: expected error for empty id, got nil")
	}
	if !contains(err.Error(), "empty id") {
		t.Errorf("error = %q; want it to mention empty id", err.Error())
	}
}
