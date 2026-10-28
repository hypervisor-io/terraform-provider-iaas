package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMicrovmSecretsRealEnvelopeAndUntrimmedValue(t *testing.T) {
	const value = "  preserve value\n"
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch calls {
		case 1:
			if r.Method != "POST" || r.URL.Path != "/api/microvm/secrets" {
				t.Errorf("wrong create route")
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["value"] != value {
				t.Error("secret bytes changed")
			}
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"success":true,"secret":{"id":"s1","name":"KEY","kind":"env","registry":null,"username":null,"preview":"masked","used_by":0,"images_count":0,"last_used_at":null}}`))
		case 2:
			if r.Method != "GET" || r.URL.Query().Get("kind") != "all" {
				t.Error("read must request kind=all")
			}
			_, _ = w.Write([]byte(`{"success":true,"secrets":[{"id":"s1","kind":"registry"}]}`))
		case 3:
			if r.Method != "PUT" || r.RequestURI != "/api/microvm/secret/a%2Fb" {
				t.Error("wrong escaped update route")
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if _, ok := body["value"]; ok {
				t.Error("metadata edit sent value")
			}
			_, _ = w.Write([]byte(`{"success":true,"secret":{"id":"s1"}}`))
		case 4:
			if r.Method != "DELETE" {
				t.Error("wrong delete verb")
			}
			_, _ = w.Write([]byte(`{"success":true}`))
		}
	}))
	defer server.Close()
	c := New(server.URL+"/api", "token", time.Second, false)
	row, err := c.CreateMicrovmSecret(context.Background(), map[string]any{"name": "KEY", "value": value})
	if err != nil || row["id"] != "s1" {
		t.Fatalf("create failed: %v", err)
	}
	rows, err := c.ListMicrovmSecrets(context.Background(), "all")
	if err != nil || len(rows) != 1 {
		t.Fatalf("list failed: %v", err)
	}
	if _, err = c.UpdateMicrovmSecret(context.Background(), "a/b", map[string]any{"name": "RENAMED"}); err != nil {
		t.Fatal(err)
	}
	if err = c.DeleteMicrovmSecret(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}
}
func TestMicrovmSecretMalformedListFailsClosed(t *testing.T) {
	for _, payload := range []string{`{"success":true}`, `{"success":true,"secrets":null}`, `{"success":true,"secrets":[{}]}`, `{"success":true,"secrets":[{"id":5}]}`, `{"success":true,"secrets":[false]}`} {
		t.Run(payload, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(payload)) }))
			defer server.Close()
			_, err := New(server.URL, "token", time.Second, false).ListMicrovmSecrets(context.Background(), "all")
			if err == nil {
				t.Fatal("malformed response must not look like empty inventory")
			}
		})
	}
}
func TestMicrovmSecretErrorSanitizesTypedFieldsAndDoesNotReplay(t *testing.T) {
	const secret = "sentinel-secret"
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("X-Request-Id", secret)
		w.WriteHeader(503)
		_ = json.NewEncoder(w).Encode(map[string]any{"reason": "secret_exists", "message": secret, "errors": map[string]any{"value": []string{secret}}})
	}))
	defer server.Close()
	_, err := New(server.URL, "token", time.Second, false).CreateMicrovmSecret(context.Background(), map[string]any{"name": "KEY", "value": secret})
	var api *APIError
	if !errors.As(err, &api) || api.Status != 503 || api.Reason != "secret_exists" {
		t.Fatalf("typed refusal lost: %v", err)
	}
	if strings.Contains(api.Error(), secret) || api.RequestID != "" || len(api.FieldErrors) != 0 {
		t.Fatal("typed error leaked secret")
	}
	if calls != 1 {
		t.Fatalf("write replayed %d times", calls)
	}
}
func TestMicrovmSecretResponseNeverReturnsValue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"secret":{"id":"s1","value":"sentinel","nested":{"static_token":"sentinel","message":"sentinel"}}}`))
	}))
	defer server.Close()
	row, err := New(server.URL, "token", time.Second, false).CreateMicrovmSecret(context.Background(), map[string]any{"value": "sentinel"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(row)
	if strings.Contains(string(raw), "sentinel") {
		t.Fatal("response leaked credential")
	}
}

func TestMicrovmShortCredentialPreservesMetadataAndDropsUntrustedText(t *testing.T) {
	for _, token := range []string{"a", "sentinel-secret"} {
		t.Run(token, func(t *testing.T) {
			row := map[string]any{"id": "aaa-uuid", "name": "alpha", "provider": "catalog", "label": "Alpha", "value": token, "message": token, "unknown": map[string]any{"name": token}, "preview": token, "revision": map[string]any{"id": "aaa-revision", "url": "https://alpha.example/api", "auth_kind": "bearer", "static_token": token, "static_token_preview": token, "message": token}}
			safe, err := microvmSafeResult(row, nil, token)
			if err != nil {
				t.Fatal(err)
			}
			if safe["id"] != "aaa-uuid" || safe["name"] != "alpha" || safe["provider"] != "catalog" {
				t.Fatal("credential corrupted identity")
			}
			revision := safe["revision"].(map[string]any)
			if revision["id"] != "aaa-revision" || revision["url"] != "https://alpha.example/api" || revision["auth_kind"] != "bearer" {
				t.Fatal("credential corrupted revision")
			}
			for _, key := range []string{"value", "message", "unknown", "preview"} {
				if _, present := safe[key]; present {
					t.Fatalf("unsafe field retained: %s", key)
				}
			}
			for _, key := range []string{"static_token", "static_token_preview", "message"} {
				if _, present := revision[key]; present {
					t.Fatalf("unsafe revision field retained: %s", key)
				}
			}
		})
	}
}

func TestMicrovmShortCredentialPreservesKnownReasonOnly(t *testing.T) {
	for _, reason := range []string{"validation_failed", "mcp_auth_kind_invalid", "mcp_server_limit", "not_found", "untrusted a response"} {
		_, err := microvmSafeResult(nil, &APIError{Status: 422, Reason: reason, Message: "a"}, "a")
		var api *APIError
		if !errors.As(err, &api) {
			t.Fatal("typed error lost")
		}
		if reason == "untrusted a response" {
			if api.Reason != "" {
				t.Fatal("untrusted reason retained")
			}
		} else if api.Reason != reason {
			t.Fatal("known reason corrupted by short credential")
		}
	}
}
