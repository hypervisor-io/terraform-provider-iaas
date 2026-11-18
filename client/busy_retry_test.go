package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// busyServer answers the first `busyFor` calls with a 409 of the given reason
// (Retry-After: 1) and then 200. calls counts every request.
func busyServer(t *testing.T, reason string, busyFor int32, calls *int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(calls, 1)
		w.Header().Set("Content-Type", "application/json")
		if n <= busyFor {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"success":false,"reason":"` + reason + `","message":"Another request is in progress, repeat it."}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"thing":{"id":"x"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func fastBusyClient(url string) *Client {
	c := New(url+"/api", "tok", 10*time.Second, false)
	c.retryBaseDelay = time.Millisecond
	c.busyDefaultWait = time.Millisecond
	c.busyWaitCap = 5 * time.Millisecond // shrinks the 1 s Retry-After
	return c
}

// A busy 409 on a create and on a delete is waited out and the call succeeds.
func TestBusyRetry_SucceedsAfterBusyAnswers(t *testing.T) {
	for _, reason := range []string{ReasonAddressReservationBusy, ReasonDatabaseBusy} {
		for _, once := range []bool{false, true} {
			var calls int32
			srv := busyServer(t, reason, 2, &calls)
			c := fastBusyClient(srv.URL)
			var err error
			if once {
				_, err = c.doItemOnce(context.Background(), "POST", "/things", map[string]any{"a": 1}, "thing")
			} else {
				err = c.doVoid(context.Background(), "DELETE", "/thing/x", nil)
			}
			if err != nil {
				t.Fatalf("%s once=%v: %v", reason, once, err)
			}
			if got := atomic.LoadInt32(&calls); got != 3 {
				t.Errorf("%s once=%v: calls = %d, want 3", reason, once, got)
			}
		}
	}
}

// After maxBusyAttempts the last 409 is returned with the server's message,
// reason and Retry-After; the number of requests is bounded.
func TestBusyRetry_GivesUpWithServerMessage(t *testing.T) {
	var calls int32
	srv := busyServer(t, ReasonAddressReservationBusy, 1000, &calls)
	c := fastBusyClient(srv.URL)
	_, err := c.doItem(context.Background(), "POST", "/things", nil, "thing")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if apiErr.Status != 409 || apiErr.Reason != ReasonAddressReservationBusy ||
		apiErr.Message != "Another request is in progress, repeat it." || apiErr.RetryAfter != time.Second {
		t.Errorf("apiErr = %+v", apiErr)
	}
	if got := atomic.LoadInt32(&calls); got != maxBusyAttempts {
		t.Errorf("calls = %d, want %d", got, maxBusyAttempts)
	}
}

// The total wait is bounded: a tiny total cap stops the retry early.
func TestBusyRetry_TotalWaitIsCapped(t *testing.T) {
	var calls int32
	srv := busyServer(t, ReasonDatabaseBusy, 1000, &calls)
	c := fastBusyClient(srv.URL)
	c.busyTotalCap = 7 * time.Millisecond // one 5 ms wait fits, a second does not
	start := time.Now()
	if err := c.doVoid(context.Background(), "DELETE", "/thing/x", nil); err == nil {
		t.Fatal("want an error")
	}
	if got := atomic.LoadInt32(&calls); got >= maxBusyAttempts {
		t.Errorf("calls = %d, the total cap did not stop the retry", got)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("took %v", time.Since(start))
	}
}

// A real conflict (any other reason, or none) is never retried.
func TestBusyRetry_OtherConflictsSurfaceAtOnce(t *testing.T) {
	for _, reason := range []string{"plan_in_use", "load_balancer_destroy_failed", "vpc_delete_incomplete", "public_ip_operation_in_flight", ""} {
		var calls int32
		srv := busyServer(t, reason, 1000, &calls)
		c := fastBusyClient(srv.URL)
		err := c.doVoid(context.Background(), "DELETE", "/thing/x", nil)
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Status != 409 {
			t.Fatalf("%q: err = %v", reason, err)
		}
		if got := atomic.LoadInt32(&calls); got != 1 {
			t.Errorf("%q: calls = %d, want 1 (no retry)", reason, got)
		}
	}
}

// The reason database_busy is also the managed-database admission refusal
// ("another operation is running on this database"), which carries no
// Retry-After: it is a real conflict and must never be repeated. The same holds
// for address_reservation_busy without the header.
func TestBusyRetry_NoRetryAfterMeansNoRetry(t *testing.T) {
	for _, reason := range []string{ReasonDatabaseBusy, ReasonAddressReservationBusy} {
		var calls int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&calls, 1)
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"success":false,"reason":"` + reason + `","message":"Another operation is running on this database."}`))
		}))
		c := fastBusyClient(srv.URL)
		if err := c.doVoid(context.Background(), "POST", "/database/x/backup", nil); err == nil {
			t.Fatal("want an error")
		}
		srv.Close()
		if got := atomic.LoadInt32(&calls); got != 1 {
			t.Errorf("%s without Retry-After: calls = %d, want 1", reason, got)
		}
	}
}

// A cancelled context ends the wait at once.
func TestBusyRetry_ContextCancelEndsWait(t *testing.T) {
	var calls int32
	srv := busyServer(t, ReasonDatabaseBusy, 1000, &calls)
	c := fastBusyClient(srv.URL)
	c.busyWaitCap = time.Hour
	c.busyTotalCap = 2 * time.Hour
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := c.doVoid(ctx, "DELETE", "/thing/x", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
}

// A persistent 500 database_error is bounded by the existing 5xx retry (4
// attempts, short back-off) and ends with the server's fixed sentence and its
// reason; the single-attempt actions send it once.
func TestDatabaseError500_IsBounded(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"success":false,"reason":"database_error","message":"A database error occurred. Try again later."}`))
	}))
	defer srv.Close()
	c := fastBusyClient(srv.URL)
	err := c.doVoid(context.Background(), "DELETE", "/thing/x", nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 500 || apiErr.Reason != ReasonDatabaseError ||
		apiErr.Message != "A database error occurred. Try again later." {
		t.Fatalf("err = %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != maxRetryAttempts {
		t.Errorf("calls = %d, want %d", got, maxRetryAttempts)
	}
	atomic.StoreInt32(&calls, 0)
	if err := c.doVoidOnce(context.Background(), "DELETE", "/thing/x", nil); err == nil {
		t.Fatal("want an error")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("once: calls = %d, want 1", got)
	}
	// The worst-case sleep of the 5xx loop at the production base delay is
	// 0.5+1+2 s plus 10 % jitter: a persistent database_error never hangs.
	worst := (defaultRetryBaseDelay + 2*defaultRetryBaseDelay + 4*defaultRetryBaseDelay) * 11 / 10
	if worst > 10*time.Second {
		t.Errorf("worst-case 5xx wait %v is not bounded", worst)
	}
}

// WithBusyRetry gives interactive callers a short budget: 3 tries in all.
func TestBusyRetry_WithBusyRetryShortBudget(t *testing.T) {
	var calls int32
	srv := busyServer(t, ReasonDatabaseBusy, 1000, &calls)
	c := fastBusyClient(srv.URL).WithBusyRetry(3, 5*time.Millisecond, time.Second)
	if err := c.doVoid(context.Background(), "DELETE", "/thing/x", nil); err == nil {
		t.Fatal("want an error")
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("calls = %d, want 3", got)
	}
	atomic.StoreInt32(&calls, 0)
	c = fastBusyClient(srv.URL).WithBusyRetry(1, 0, 0)
	_ = c.doVoid(context.Background(), "DELETE", "/thing/x", nil)
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("attempts=1: calls = %d, want 1", got)
	}
}
