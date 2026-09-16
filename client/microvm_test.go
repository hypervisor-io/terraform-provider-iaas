package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMicrovmUnifiedClientRoutes(t *testing.T) {
	requests := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/microvm/images"},
		{http.MethodGet, "/api/microvm/image/image%2F1"},
		{http.MethodDelete, "/api/microvm/image/image%2F1"},
		{http.MethodPost, "/api/microvm/vms"},
		{http.MethodPut, "/api/microvm/vm/vm%2F1/env"},
		{http.MethodPost, "/api/microvm/vm/vm%2F1/domain"},
		{http.MethodDelete, "/api/microvm/vm/vm%2F1/domain/domain%2F1"},
		{http.MethodDelete, "/api/microvm/vm/vm%2F1"},
		{http.MethodPost, "/api/microvm/connectors/github"},
		{http.MethodPost, "/api/microvm/connectors/gitlab"},
		{http.MethodPut, "/api/microvm/connector/connector%2F1"},
		{http.MethodDelete, "/api/microvm/connector/connector%2F1"},
		{http.MethodGet, "/api/microvm/catalog"},
	}
	index := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if index >= len(requests) {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		want := requests[index]
		index++
		if r.Method != want.method || r.URL.EscapedPath() != want.path {
			t.Errorf("request = %s %s; want %s %s", r.Method, r.URL.EscapedPath(), want.method, want.path)
		}
		w.Header().Set("Content-Type", "application/json")
		switch index {
		case 1, 2:
			_, _ = w.Write([]byte(`{"success":true,"image":{"id":"image/1"}}`))
		case 4:
			_, _ = w.Write([]byte(`{"success":true,"microvm":{"id":"vm/1"}}`))
		case 6:
			_, _ = w.Write([]byte(`{"success":true,"domain":{"id":"domain/1"}}`))
		case 9, 10, 11:
			_, _ = w.Write([]byte(`{"success":true,"connector":{"id":"connector/1"}}`))
		case 13:
			_, _ = w.Write([]byte(`{"locations":[],"images":[],"security_groups":[],"vpc_subnets":[],"limits":{}}`))
		default:
			_, _ = w.Write([]byte(`{"success":true}`))
		}
	}))
	defer server.Close()

	c := New(server.URL+"/api", "token", 0, false)
	ctx := context.Background()
	_, _ = c.CreateMicrovmImage(ctx, map[string]any{"name": "image"})
	_, _ = c.GetMicrovmImage(ctx, "image/1")
	_ = c.DeleteMicrovmImage(ctx, "image/1")
	_, _ = c.CreateMicrovm(ctx, map[string]any{"name": "vm"})
	_, _ = c.SetMicrovmEnv(ctx, "vm/1", map[string]string{"MODE": "test"})
	_, _ = c.AddMicrovmDomain(ctx, "vm/1", "vm.example.test")
	_ = c.RemoveMicrovmDomain(ctx, "vm/1", "domain/1")
	_ = c.DeleteMicrovm(ctx, "vm/1")
	_, _ = c.CreateGithubMicrovmConnector(ctx, map[string]any{"name": "github"})
	_, _ = c.CreateGitlabMicrovmConnector(ctx, map[string]any{"name": "gitlab"})
	_, _ = c.UpdateMicrovmConnector(ctx, "connector/1", map[string]any{"enabled": true})
	_ = c.DeleteMicrovmConnector(ctx, "connector/1")
	_, _ = c.GetMicrovmCatalog(ctx)

	if index != len(requests) {
		t.Fatalf("received %d requests; want %d", index, len(requests))
	}
}

func TestListMicrovmImagesPaginatesNamedEnvelope(t *testing.T) {
	var pages []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages = append(pages, r.URL.Query().Get("page"))
		page := 1
		if r.URL.Query().Get("page") == "2" {
			page = 2
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"images": map[string]any{
				"data":         []any{map[string]any{"id": page}},
				"current_page": page,
				"last_page":    2,
			},
		})
	}))
	defer server.Close()

	c := New(server.URL+"/api", "token", 0, false)
	images, err := c.ListMicrovmImages(context.Background(), "", "", "")
	if err != nil {
		t.Fatalf("ListMicrovmImages: %v", err)
	}
	if len(images) != 2 || len(pages) != 2 || pages[0] != "" || pages[1] != "2" {
		t.Fatalf("images/pages = %v/%v; want two pages", images, pages)
	}
}

func TestGetMicrovmKeepsDomains(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"microvm":{"id":"vm-1"},"domains":[{"id":"domain-1","hostname":"vm.example.test"}]}`))
	}))
	defer server.Close()

	c := New(server.URL+"/api", "token", 0, false)
	envelope, err := c.GetMicrovm(context.Background(), "vm-1")
	if err != nil {
		t.Fatalf("GetMicrovm: %v", err)
	}
	microvm, _ := envelope["microvm"].(map[string]any)
	domains := objSlice(envelope["domains"])
	if microvm["id"] != "vm-1" || len(domains) != 1 || domains[0]["id"] != "domain-1" {
		t.Fatalf("envelope = %#v", envelope)
	}
}
