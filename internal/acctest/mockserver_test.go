package acctest_test

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/iaas/terraform-provider-iaas/internal/acctest"
)

// TestMockServer_HandleJSON_RegisteredPath verifies that a HandleJSON registration
// responds with the configured status code and JSON body.
// The client calls Endpoint()+"/ssh-keys", which mirrors how the real client builds
// its request URL (baseURL + resource path).
func TestMockServer_HandleJSON_RegisteredPath(t *testing.T) {
	srv := acctest.NewMockServer(t)

	const body = `{"success":true,"ssh_key":{"id":"k1"}}`
	srv.HandleJSON("POST", "/ssh-keys", 201, body)

	resp, err := http.Post(srv.Endpoint()+"/ssh-keys", "application/json",
		strings.NewReader(`{"name":"test-key"}`))
	if err != nil {
		t.Fatalf("POST /ssh-keys: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 201 {
		t.Errorf("expected status 201; got %d", resp.StatusCode)
	}

	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading response body: %v", err)
	}
	if !strings.Contains(string(got), "k1") {
		t.Errorf("expected body to contain %q; got %q", "k1", string(got))
	}
}

// TestMockServer_UnregisteredPath verifies that requesting an unregistered
// path returns 404 with a legible JSON error message.
func TestMockServer_UnregisteredPath(t *testing.T) {
	srv := acctest.NewMockServer(t)

	resp, err := http.Get(srv.Endpoint() + "/no-such-resource")
	if err != nil {
		t.Fatalf("GET /no-such-resource: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 404 {
		t.Errorf("expected status 404 for unregistered path; got %d", resp.StatusCode)
	}

	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading response body: %v", err)
	}
	if !strings.Contains(string(got), "mock: no handler") {
		t.Errorf("expected body to contain %q; got %q", "mock: no handler", string(got))
	}
}

// TestMockServer_MethodMismatch verifies that a GET request to a path only
// registered for POST returns 404.
func TestMockServer_MethodMismatch(t *testing.T) {
	srv := acctest.NewMockServer(t)
	srv.HandleJSON("POST", "/ssh-keys", 201, `{"success":true}`)

	resp, err := http.Get(srv.Endpoint() + "/ssh-keys")
	if err != nil {
		t.Fatalf("GET /ssh-keys (registered as POST only): %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 404 {
		t.Errorf("expected status 404 for method mismatch; got %d", resp.StatusCode)
	}
}

// TestMockServer_ConcurrentRequests is a thread-safety smoke test.
// It fires several concurrent requests to a registered path and asserts that
// all succeed. Run the suite with -race to detect data races.
func TestMockServer_ConcurrentRequests(t *testing.T) {
	srv := acctest.NewMockServer(t)
	srv.HandleJSON("GET", "/instances", 200, `{"instances":[]}`)

	const workers = 20
	var wg sync.WaitGroup
	errs := make(chan error, workers)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			resp, err := http.Get(srv.Endpoint() + "/instances")
			if err != nil {
				errs <- fmt.Errorf("worker %d GET: %w", n, err)
				return
			}
			resp.Body.Close()
			if resp.StatusCode != 200 {
				errs <- fmt.Errorf("worker %d: expected 200; got %d", n, resp.StatusCode)
			}
		}(i)
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// TestMockServer_RequestCapture_BodyAndMetadata verifies that a POST request
// body is fully captured and accessible via Requests, and that the captured
// Method and Path fields are correct.
func TestMockServer_RequestCapture_BodyAndMetadata(t *testing.T) {
	srv := acctest.NewMockServer(t)

	const sentBody = `{"name":"my-key","public_key":"ssh-ed25519 AAAA"}`
	srv.HandleJSON("POST", "/ssh-keys", 201, `{"id":"k1"}`)

	resp, err := http.Post(srv.Endpoint()+"/ssh-keys", "application/json",
		strings.NewReader(sentBody))
	if err != nil {
		t.Fatalf("POST /ssh-keys: %v", err)
	}
	resp.Body.Close()

	recs := srv.Requests("POST", "/ssh-keys")
	if len(recs) != 1 {
		t.Fatalf("expected 1 recorded request; got %d", len(recs))
	}

	r := recs[0]
	if r.Method != "POST" {
		t.Errorf("expected Method %q; got %q", "POST", r.Method)
	}
	if r.Path != "/api/ssh-keys" {
		t.Errorf("expected Path %q; got %q", "/api/ssh-keys", r.Path)
	}
	if string(r.Body) != sentBody {
		t.Errorf("expected Body %q; got %q", sentBody, string(r.Body))
	}
}

// TestMockServer_RequestCapture_UnmatchedAlsoCaptured verifies that requests
// to unregistered paths (404 responses) are still captured in AllRequests.
func TestMockServer_RequestCapture_UnmatchedAlsoCaptured(t *testing.T) {
	srv := acctest.NewMockServer(t)

	resp, err := http.Get(srv.Endpoint() + "/ghost")
	if err != nil {
		t.Fatalf("GET /ghost: %v", err)
	}
	resp.Body.Close()

	all := srv.AllRequests()
	if len(all) != 1 {
		t.Fatalf("expected 1 recorded request after unmatched GET; got %d", len(all))
	}
	if all[0].Path != "/api/ghost" {
		t.Errorf("expected captured path %q; got %q", "/api/ghost", all[0].Path)
	}
}

// TestMockServer_RequestCapture_BodyRestoration verifies that a handler which
// reads r.Body still sees the complete body even though the capture logic has
// already consumed it. The handler echoes the request body back; the test
// asserts the client receives the full echo.
func TestMockServer_RequestCapture_BodyRestoration(t *testing.T) {
	srv := acctest.NewMockServer(t)

	// Echo handler: reads r.Body and writes it back to the response.
	srv.Handle("POST", "/echo", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "handler: read body: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	})

	const payload = `{"ping":"pong"}`
	resp, err := http.Post(srv.Endpoint()+"/echo", "application/json",
		strings.NewReader(payload))
	if err != nil {
		t.Fatalf("POST /echo: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200; got %d", resp.StatusCode)
	}

	echo, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading echo response: %v", err)
	}
	if string(echo) != payload {
		t.Errorf("body restoration failed: handler saw %q; expected %q", string(echo), payload)
	}

	// Capture should also have the full body.
	recs := srv.Requests("POST", "/echo")
	if len(recs) != 1 {
		t.Fatalf("expected 1 captured request; got %d", len(recs))
	}
	if string(recs[0].Body) != payload {
		t.Errorf("captured body %q; expected %q", string(recs[0].Body), payload)
	}
}

// TestMockServer_RequestCapture_Concurrent exercises the capture path under
// concurrent load to detect data races (run with -race). It fires many
// goroutines posting to a registered path and verifies all were captured.
func TestMockServer_RequestCapture_Concurrent(t *testing.T) {
	srv := acctest.NewMockServer(t)
	srv.HandleJSON("POST", "/items", 201, `{"ok":true}`)

	const workers = 30
	var wg sync.WaitGroup
	errs := make(chan error, workers)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			body := fmt.Sprintf(`{"n":%d}`, n)
			resp, err := http.Post(srv.Endpoint()+"/items", "application/json",
				strings.NewReader(body))
			if err != nil {
				errs <- fmt.Errorf("worker %d POST: %w", n, err)
				return
			}
			resp.Body.Close()
			if resp.StatusCode != 201 {
				errs <- fmt.Errorf("worker %d: expected 201; got %d", n, resp.StatusCode)
			}
		}(i)
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	recs := srv.Requests("POST", "/items")
	if len(recs) != workers {
		t.Errorf("expected %d captured requests; got %d", workers, len(recs))
	}
}
