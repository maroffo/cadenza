// ABOUTME: HTTP client for intervals.icu API v1 with Basic auth, rate limiting and retry.
// ABOUTME: Shared transport for all domain methods (activities, wellness, events, athlete).

package icu

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"
)

const (
	basicAuthUsername = "API_KEY"

	defaultTimeout    = 30 * time.Second
	defaultRatePerSec = 3
	defaultBurst      = 3
	defaultMaxRetries = 3
	defaultRetryBase  = 500 * time.Millisecond

	// maxRetryAfter caps Retry-After values (seconds or HTTP-date) to avoid
	// blocking for unreasonable durations if upstream sends very large values.
	maxRetryAfter = 60 * time.Second
)

type Client struct {
	baseURL    string
	apiKey     string
	athleteID  string
	httpClient *http.Client
	limiter    *rate.Limiter
	maxRetries int
	retryBase  time.Duration
	now        func() time.Time
	sleep      func(context.Context, time.Duration) error
}

type Option func(*Client)

func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.httpClient = h }
}

func WithRateLimit(ratePerSec float64, burst int) Option {
	return func(c *Client) { c.limiter = rate.NewLimiter(rate.Limit(ratePerSec), burst) }
}

func WithMaxRetries(n int) Option {
	return func(c *Client) { c.maxRetries = n }
}

func WithRetryBase(d time.Duration) Option {
	return func(c *Client) { c.retryBase = d }
}

func withSleep(fn func(context.Context, time.Duration) error) Option {
	return func(c *Client) { c.sleep = fn }
}

func New(baseURL, apiKey, athleteID string, opts ...Option) *Client {
	c := &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		athleteID:  athleteID,
		httpClient: &http.Client{Timeout: defaultTimeout},
		limiter:    rate.NewLimiter(rate.Limit(defaultRatePerSec), defaultBurst),
		maxRetries: defaultMaxRetries,
		retryBase:  defaultRetryBase,
		now:        time.Now,
		sleep:      ctxSleep,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func (c *Client) AthleteID() string { return c.athleteID }

// APIError describes a non-2xx response from intervals.icu.
//
// Error() truncates Body to 200 characters to keep log lines compact and avoid
// leaking large upstream payloads into error messages. The full response body
// remains available via the Body field for callers that need it for debugging.
// This trade-off favors safe default logging over maximum debuggability.
type APIError struct {
	Method     string
	Path       string
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	snippet := e.Body
	if len(snippet) > 200 {
		snippet = snippet[:200] + "..."
	}
	return fmt.Sprintf("intervals.icu %s %s: %d %s", e.Method, e.Path, e.StatusCode, snippet)
}

// Do performs an authenticated request with rate limiting and retry on 429/5xx.
// Returns the response body as a byte slice. Callers decode as needed.
func (c *Client) Do(ctx context.Context, method, path string, query url.Values, body any) ([]byte, error) {
	var bodyBytes []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		bodyBytes = b
	}

	fullURL := c.baseURL + path
	if len(query) > 0 {
		fullURL += "?" + query.Encode()
	}

	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if err := c.limiter.Wait(ctx); err != nil {
			return nil, fmt.Errorf("rate limiter: %w", err)
		}

		req, err := http.NewRequestWithContext(ctx, method, fullURL, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
		req.SetBasicAuth(basicAuthUsername, c.apiKey)
		req.Header.Set("Accept", "application/json")
		if bodyBytes != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			if !isRetryableErr(err) || attempt == c.maxRetries {
				return nil, fmt.Errorf("http %s %s: %w", method, path, err)
			}
			if err := c.sleep(ctx, c.backoff(attempt, "")); err != nil {
				return nil, err
			}
			continue
		}

		respBody, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			if attempt == c.maxRetries {
				return nil, fmt.Errorf("read body %s %s: %w", method, path, readErr)
			}
			if err := c.sleep(ctx, c.backoff(attempt, "")); err != nil {
				return nil, err
			}
			continue
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return respBody, nil
		}

		apiErr := &APIError{Method: method, Path: path, StatusCode: resp.StatusCode, Body: string(respBody)}
		if !isRetryableStatus(resp.StatusCode) || attempt == c.maxRetries {
			return respBody, apiErr
		}
		lastErr = apiErr
		retryAfter := resp.Header.Get("Retry-After")
		if err := c.sleep(ctx, c.backoff(attempt, retryAfter)); err != nil {
			return nil, err
		}
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("unreachable: retry loop exited without result")
}

// parseRetryAfter parses the Retry-After header per RFC 7231 (seconds or HTTP-date).
// Returns (duration, true) on a valid positive value, else (0, false).
// The returned duration is always capped at maxRetryAfter to protect callers
// from pathological upstream values (e.g. 86400 seconds).
func parseRetryAfter(header string, now time.Time) (time.Duration, bool) {
	h := strings.TrimSpace(header)
	if h == "" {
		return 0, false
	}
	// Form 1: delta-seconds (non-negative integer).
	if secs, err := strconv.Atoi(h); err == nil {
		if secs <= 0 {
			return 0, false
		}
		d := time.Duration(secs) * time.Second
		if d > maxRetryAfter {
			d = maxRetryAfter
		}
		return d, true
	}
	// Form 2: HTTP-date (RFC 7231 IMF-fixdate / obsolete formats).
	if t, err := http.ParseTime(h); err == nil {
		d := t.Sub(now)
		if d <= 0 {
			return 0, false
		}
		if d > maxRetryAfter {
			d = maxRetryAfter
		}
		return d, true
	}
	return 0, false
}

func (c *Client) backoff(attempt int, retryAfter string) time.Duration {
	if d, ok := parseRetryAfter(retryAfter, c.now()); ok {
		return d
	}
	d := c.retryBase
	for i := 0; i < attempt; i++ {
		d *= 2
	}
	return d
}

func isRetryableStatus(code int) bool {
	return code == http.StatusTooManyRequests || (code >= 500 && code < 600)
}

func isRetryableErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	// TLS certificate verification failures are permanent for the current chain.
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return false
	}
	// Unknown authority in the cert chain: configuration problem, not transient.
	var unknownAuth x509.UnknownAuthorityError
	if errors.As(err, &unknownAuth) {
		return false
	}
	// DNS NXDOMAIN (IsNotFound) is a permanent lookup failure; other DNS
	// errors (IsTimeout / IsTemporary) remain retryable by default.
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return false
	}
	return true
}

func ctxSleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
