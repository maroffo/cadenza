// ABOUTME: Tests for wellness domain methods: GET single, list range, PUT partial update, validation.
// ABOUTME: httptest-based, no network; verifies paths, query string, HTTP method and request body.

package icu

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newWellnessClient(t *testing.T, handler http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	c := New(ts.URL, "k", "0",
		WithMaxRetries(0),
		WithRetryBase(time.Millisecond),
		WithRateLimit(1000, 1000),
		withSleep(func(context.Context, time.Duration) error { return nil }),
	)
	return c, ts
}

func TestGetWellness_PathAndMethod(t *testing.T) {
	var gotPath, gotMethod string
	c, _ := newWellnessClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		_, _ = w.Write([]byte(`{"id":"2026-04-21","weight":72.5}`))
	}))

	out, err := c.GetWellness(context.Background(), "2026-04-21")
	if err != nil {
		t.Fatalf("GetWellness: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if gotPath != "/athlete/0/wellness/2026-04-21" {
		t.Errorf("path = %q", gotPath)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("decode: %v (raw=%s)", err, out)
	}
	if m["weight"].(float64) != 72.5 {
		t.Errorf("weight = %v", m["weight"])
	}
}

func TestGetWellness_InvalidDate_NoHTTPCall(t *testing.T) {
	var hits int
	c, _ := newWellnessClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))

	cases := []string{"", "2026-4-21", "2026/04/21", "2026-04-21T00:00:00Z", "not-a-date"}
	for _, d := range cases {
		if _, err := c.GetWellness(context.Background(), d); err == nil {
			t.Errorf("GetWellness(%q): expected error", d)
		}
	}
	if hits != 0 {
		t.Fatalf("server was called %d times, expected 0", hits)
	}
}

func TestListWellness_WithRange(t *testing.T) {
	var gotPath, gotQuery string
	c, _ := newWellnessClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`[]`))
	}))

	_, err := c.ListWellness(context.Background(), GetWellnessRangeParams{
		Oldest: "2026-04-01",
		Newest: "2026-04-21",
	})
	if err != nil {
		t.Fatalf("ListWellness: %v", err)
	}
	if gotPath != "/athlete/0/wellness" {
		t.Errorf("path = %q", gotPath)
	}
	// query order is deterministic via url.Values.Encode (alphabetical).
	if !strings.Contains(gotQuery, "oldest=2026-04-01") || !strings.Contains(gotQuery, "newest=2026-04-21") {
		t.Errorf("query = %q", gotQuery)
	}
}

func TestListWellness_NoFilters_NoQuery(t *testing.T) {
	var gotQuery string
	c, _ := newWellnessClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`[]`))
	}))

	if _, err := c.ListWellness(context.Background(), GetWellnessRangeParams{}); err != nil {
		t.Fatalf("ListWellness: %v", err)
	}
	if gotQuery != "" {
		t.Errorf("query = %q, want empty", gotQuery)
	}
}

func TestUpdateWellness_PutBodyAndResponse(t *testing.T) {
	var gotPath, gotMethod, gotCT string
	var gotBody map[string]any
	c, _ := newWellnessClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotCT = r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_, _ = w.Write([]byte(`{"updated":true,"weight":73.1}`))
	}))

	fields := map[string]any{
		"weight":    73.1,
		"fatigue":   3,
		"mood":      4,
		"sleepSecs": 27000,
	}
	out, err := c.UpdateWellness(context.Background(), "2026-04-21", fields)
	if err != nil {
		t.Fatalf("UpdateWellness: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	if gotPath != "/athlete/0/wellness/2026-04-21" {
		t.Errorf("path = %q", gotPath)
	}
	if gotCT != "application/json" {
		t.Errorf("content-type = %q", gotCT)
	}
	if gotBody["weight"].(float64) != 73.1 {
		t.Errorf("body.weight = %v", gotBody["weight"])
	}
	if gotBody["fatigue"].(float64) != 3 {
		t.Errorf("body.fatigue = %v", gotBody["fatigue"])
	}
	var resp map[string]any
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["updated"] != true {
		t.Errorf("response.updated = %v", resp["updated"])
	}
}

func TestUpdateWellness_NilFields(t *testing.T) {
	var hits int
	c, _ := newWellnessClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
	}))
	if _, err := c.UpdateWellness(context.Background(), "2026-04-21", nil); err == nil {
		t.Fatal("expected error on nil fields")
	}
	if hits != 0 {
		t.Fatalf("server was called %d times, expected 0", hits)
	}
}

func TestUpdateWellness_EmptyFields(t *testing.T) {
	var hits int
	c, _ := newWellnessClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
	}))
	if _, err := c.UpdateWellness(context.Background(), "2026-04-21", map[string]any{}); err == nil {
		t.Fatal("expected error on empty fields")
	}
	if hits != 0 {
		t.Fatalf("server was called %d times, expected 0", hits)
	}
}

func TestUpdateWellness_InvalidDate(t *testing.T) {
	var hits int
	c, _ := newWellnessClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
	}))
	if _, err := c.UpdateWellness(context.Background(), "bad-date", map[string]any{"weight": 70}); err == nil {
		t.Fatal("expected error on invalid date")
	}
	if hits != 0 {
		t.Fatalf("server was called %d times, expected 0", hits)
	}
}
