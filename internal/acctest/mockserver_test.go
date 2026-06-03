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
