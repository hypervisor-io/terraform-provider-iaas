package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAdminGetEmailLogByStatus(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		if r.URL.Path != "/api/v1/system/email-log" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"current_page":1,"last_page":1,"data":[` +
			`{"id":"1","to":"a@example.com","subject":"s","html_body":"","user_id":null,"mailer":"smtp","status":"failed","error":"Connection refused"}]}`))
	}))
	defer srv.Close()
	c := New(srv.URL+"/api", "tok", 10*time.Second, false)

	rows, err := c.AdminGetEmailLogByStatus(context.Background(), "failed")
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %v, err = %v", rows, err)
	}
	if gotQuery != "status=failed" {
		t.Errorf("query = %q", gotQuery)
	}
	if rows[0]["error"] != "Connection refused" || rows[0]["user_id"] != nil {
		t.Errorf("row = %v", rows[0])
	}
	if _, err := c.AdminGetEmailLogByStatus(context.Background(), ""); err != nil || gotQuery != "" {
		t.Errorf("empty status: query = %q, err = %v", gotQuery, err)
	}
}
