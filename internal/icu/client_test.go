// ABOUTME: Tests for HTTP client: Basic auth, retry on 429/5xx, context cancellation, APIError.
// ABOUTME: Uses httptest servers and injected sleep to keep tests fast and deterministic.

package icu

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newTestClient(t *testing.T, handler http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	c := New(ts.URL, "test-key", "0",
		WithMaxRetries(2),
		WithRetryBase(time.Millisecond),
		WithRateLimit(1000, 1000),
		withSleep(func(context.Context, time.Duration) error { return nil }),
	)
	return c, ts
}

func TestDo_BasicAuthHeader(t *testing.T) {
	var gotAuth string
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))

	body, err := c.Do(context.Background(), http.MethodGet, "/ping", nil, nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	// Decode the Authorization header instead of re-encoding the same inputs,
	// so the test asserts the observable contract (user == API_KEY, pass ==
	// configured key) rather than repeating the implementation's call to
	// base64.StdEncoding.EncodeToString.
	const prefix = "Basic "
	if !strings.HasPrefix(gotAuth, prefix) {
		t.Fatalf("Authorization = %q, want Basic scheme", gotAuth)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(gotAuth, prefix))
	if err != nil {
		t.Fatalf("decode Authorization: %v", err)
	}
	user, pass, ok := strings.Cut(string(raw), ":")
	if !ok {
		t.Fatalf("Authorization payload missing colon: %q", raw)
	}
	if user != "API_KEY" {
		t.Errorf("basic auth user = %q, want %q", user, "API_KEY")
	}
	if pass != "test-key" {
		t.Errorf("basic auth pass = %q, want %q", pass, "test-key")
	}
	if !strings.Contains(string(body), `"ok":true`) {
		t.Errorf("body = %s", body)
	}
}

func TestDo_JSONBodyAndQuery(t *testing.T) {
	var gotPath, gotQuery, gotCT string
	var gotBody []byte
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotCT = r.Header.Get("Content-Type")
		gotBody = readAll(r)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))

	q := url.Values{"foo": []string{"bar"}}
	body := map[string]string{"hello": "world"}
	_, err := c.Do(context.Background(), http.MethodPut, "/x", q, body)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if gotPath != "/x" {
		t.Errorf("path = %q", gotPath)
	}
	if gotQuery != "foo=bar" {
		t.Errorf("query = %q", gotQuery)
	}
	if gotCT != "application/json" {
		t.Errorf("content-type = %q", gotCT)
	}
	if !strings.Contains(string(gotBody), `"hello":"world"`) {
		t.Errorf("body = %s", gotBody)
	}
}

func TestDo_RetriesOn429ThenSucceeds(t *testing.T) {
	var hits atomic.Int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if n < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`slow down`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))

	_, err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if got := hits.Load(); got != 3 {
		t.Fatalf("hits = %d, want 3", got)
	}
}

func TestDo_RetriesOn500ThenGivesUp(t *testing.T) {
	var hits atomic.Int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`boom`))
	}))

	_, err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error type = %T, want *APIError: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d", apiErr.StatusCode)
	}
	if got := hits.Load(); got != 3 { // maxRetries=2 => 3 attempts
		t.Fatalf("hits = %d, want 3", got)
	}
}

func TestDo_NoRetryOn4xxExcept429(t *testing.T) {
	var hits atomic.Int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`bad`))
	}))

	_, err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil)
	if err == nil {
		t.Fatalf("expected error")
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("hits = %d, want 1 (no retry on 4xx)", got)
	}
}

func TestDo_ContextCanceled(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate slow server; the request should be canceled before receiving response.
		select {
		case <-r.Context().Done():
			return
		case <-time.After(2 * time.Second):
			w.WriteHeader(http.StatusOK)
		}
	}))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := c.Do(ctx, http.MethodGet, "/x", nil, nil)
	if err == nil {
		t.Fatalf("expected error on canceled context")
	}
}

func TestAPIError_Message(t *testing.T) {
	e := &APIError{Method: "GET", Path: "/foo", StatusCode: 404, Body: "not found"}
	got := e.Error()
	want := "intervals.icu GET /foo: 404 not found"
	if got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

func TestAPIError_BodyTruncated(t *testing.T) {
	long := strings.Repeat("x", 500)
	e := &APIError{Method: "POST", Path: "/y", StatusCode: 500, Body: long}
	got := e.Error()
	if !strings.Contains(got, "...") {
		t.Fatalf("expected truncation marker, got %q", got)
	}
}

func readAll(r *http.Request) []byte {
	defer func() { _ = r.Body.Close() }()
	buf := make([]byte, 0, 512)
	chunk := make([]byte, 256)
	for {
		n, err := r.Body.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
		}
		if err != nil {
			break
		}
	}
	return buf
}

// Sanity check: backoff honors Retry-After header when provided in seconds.
func TestBackoff_RetryAfterHeader(t *testing.T) {
	c := &Client{retryBase: 100 * time.Millisecond, now: time.Now}
	got := c.backoff(0, "2")
	if got != 2*time.Second {
		t.Fatalf("backoff with Retry-After = %v, want 2s", got)
	}
	got = c.backoff(2, "")
	want := 400 * time.Millisecond
	if got != want {
		t.Fatalf("backoff(2) = %v, want %v", got, want)
	}
}

func TestBackoff_MalformedRetryAfterFallsBack(t *testing.T) {
	c := &Client{retryBase: 50 * time.Millisecond, now: time.Now}
	got := c.backoff(1, "not-a-number")
	want := 100 * time.Millisecond
	if got != want {
		t.Fatalf("backoff fallback = %v, want %v", got, want)
	}
}

func TestBackoff_SecondsCappedAt60s(t *testing.T) {
	c := &Client{retryBase: 100 * time.Millisecond, now: time.Now}
	got := c.backoff(0, "86400")
	if got != 60*time.Second {
		t.Fatalf("backoff with Retry-After=86400 = %v, want 60s cap", got)
	}
}

func TestBackoff_NegativeOrZero(t *testing.T) {
	c := &Client{retryBase: 100 * time.Millisecond, now: time.Now}
	// "0" and "-5" should fall back to exponential, not parse as valid delay.
	got := c.backoff(0, "0")
	if got != 100*time.Millisecond {
		t.Fatalf("backoff with Retry-After=0 = %v, want fallback 100ms", got)
	}
	got = c.backoff(2, "-5")
	if got != 400*time.Millisecond {
		t.Fatalf("backoff with Retry-After=-5 attempt=2 = %v, want fallback 400ms", got)
	}
}

func TestBackoff_HTTPDate(t *testing.T) {
	now := time.Date(2026, 10, 21, 7, 28, 0, 0, time.UTC)
	c := &Client{retryBase: 100 * time.Millisecond, now: func() time.Time { return now }}
	// 30 seconds in the future.
	target := now.Add(30 * time.Second)
	header := target.Format(http.TimeFormat)
	got := c.backoff(0, header)
	// Allow small rounding tolerance (http.TimeFormat has second precision).
	if got < 29*time.Second || got > 31*time.Second {
		t.Fatalf("backoff HTTP-date = %v, want ~30s", got)
	}
}

func TestBackoff_HTTPDateCappedAt60s(t *testing.T) {
	now := time.Date(2026, 10, 21, 7, 28, 0, 0, time.UTC)
	c := &Client{retryBase: 100 * time.Millisecond, now: func() time.Time { return now }}
	// 24 hours in the future.
	target := now.Add(24 * time.Hour)
	header := target.Format(http.TimeFormat)
	got := c.backoff(0, header)
	if got != 60*time.Second {
		t.Fatalf("backoff HTTP-date far-future = %v, want 60s cap", got)
	}
}

func TestBackoff_HTTPDateInPastFallsBack(t *testing.T) {
	now := time.Date(2026, 10, 21, 7, 28, 0, 0, time.UTC)
	c := &Client{retryBase: 100 * time.Millisecond, now: func() time.Time { return now }}
	// 60 seconds in the past -> invalid, fall back to exponential.
	target := now.Add(-60 * time.Second)
	header := target.Format(http.TimeFormat)
	got := c.backoff(1, header)
	if got != 200*time.Millisecond {
		t.Fatalf("backoff HTTP-date past attempt=1 = %v, want fallback 200ms", got)
	}
}

func TestIsRetryableErr_DNSNotFound(t *testing.T) {
	err := &net.DNSError{Err: "no such host", Name: "nope.invalid", IsNotFound: true}
	if isRetryableErr(err) {
		t.Fatalf("expected DNS IsNotFound error to be non-retryable")
	}
}

func TestIsRetryableErr_DNSTimeoutIsRetryable(t *testing.T) {
	err := &net.DNSError{Err: "i/o timeout", Name: "slow.example.com", IsTimeout: true}
	if !isRetryableErr(err) {
		t.Fatalf("expected DNS timeout to remain retryable")
	}
}

func TestIsRetryableErr_TLSError(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"CertificateVerificationError", &tls.CertificateVerificationError{}},
		{"UnknownAuthorityError", x509.UnknownAuthorityError{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if isRetryableErr(tc.err) {
				t.Fatalf("expected %s to be non-retryable", tc.name)
			}
		})
	}
}

// Helper: ensure the generated URL contains expected baseURL (smoke test for trimming).
func TestNew_TrimsTrailingSlash(t *testing.T) {
	c := New("https://example.com/api/", "k", "0")
	if !strings.HasSuffix(c.baseURL, "/api") {
		t.Fatalf("baseURL = %q, want trimmed trailing slash", c.baseURL)
	}
}

func TestDo_RespectsRetryAfterDuration(t *testing.T) {
	var hits atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	var (
		mu    sync.Mutex
		slept []time.Duration
	)
	recordSleep := func(_ context.Context, d time.Duration) error {
		mu.Lock()
		slept = append(slept, d)
		mu.Unlock()
		return nil
	}

	c := New(ts.URL, "test-key", "0",
		WithMaxRetries(2),
		WithRetryBase(50*time.Millisecond),
		WithRateLimit(1000, 1000),
		withSleep(recordSleep),
	)

	if _, err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil); err != nil {
		t.Fatalf("Do: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(slept) != 1 {
		t.Fatalf("sleep calls = %d, want 1; got %v", len(slept), slept)
	}
	if slept[0] != 1*time.Second {
		t.Fatalf("sleep duration = %v, want 1s (from Retry-After, not exponential backoff)", slept[0])
	}
}

func TestDo_ContextDeadlineMidRetry(t *testing.T) {
	// Server always returns 429 to force retries.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	// Real ctxSleep (not injected) so deadline-aware sleep applies.
	c := New(ts.URL, "test-key", "0",
		WithMaxRetries(5),
		WithRetryBase(100*time.Millisecond),
		WithRateLimit(1000, 1000),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := c.Do(ctx, http.MethodGet, "/x", nil, nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected deadline error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded via errors.Is", err)
	}
	// Generous budget: first attempt + one aborted sleep should be well under 1s.
	if elapsed > 1*time.Second {
		t.Fatalf("elapsed = %v, want <1s (aborted mid-retry)", elapsed)
	}
}
