// ABOUTME: Tests for activities domain API: paths, query params, input validation, raw decode.
// ABOUTME: Uses httptest.Server and an in-package client with retries disabled.

package icu

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newActivitiesClient(t *testing.T, h http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	c := New(ts.URL, "k", "42",
		WithMaxRetries(0),
		WithRetryBase(time.Millisecond),
		WithRateLimit(1000, 1000),
	)
	return c, ts
}

func TestListActivities_PathAndAllQueryParams(t *testing.T) {
	var gotPath, gotQuery string
	c, _ := newActivitiesClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`[{"id":"abc"}]`))
	}))

	raw, err := c.ListActivities(context.Background(), ListActivitiesParams{
		Oldest: "2025-01-01",
		Newest: "2025-04-30",
		Limit:  42,
	})
	if err != nil {
		t.Fatalf("ListActivities: %v", err)
	}
	if gotPath != "/athlete/42/activities" {
		t.Fatalf("path = %q", gotPath)
	}
	// query param order is deterministic (alphabetical) with url.Values.Encode.
	for _, want := range []string{"oldest=2025-01-01", "newest=2025-04-30", "limit=42"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("query %q missing %q", gotQuery, want)
		}
	}
	var decoded []map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	if len(decoded) != 1 || decoded[0]["id"] != "abc" {
		t.Fatalf("unexpected decoded payload: %v", decoded)
	}
}

func TestListActivities_OmitsEmptyFilters(t *testing.T) {
	var gotQuery string
	c, _ := newActivitiesClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`[]`))
	}))

	if _, err := c.ListActivities(context.Background(), ListActivitiesParams{}); err != nil {
		t.Fatalf("ListActivities: %v", err)
	}
	if gotQuery != "" {
		t.Fatalf("query should be empty when no filters set, got %q", gotQuery)
	}
}

func TestListActivities_LimitZeroOmitted(t *testing.T) {
	var gotQuery string
	c, _ := newActivitiesClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`[]`))
	}))

	if _, err := c.ListActivities(context.Background(), ListActivitiesParams{Oldest: "2025-01-01", Limit: 0}); err != nil {
		t.Fatalf("ListActivities: %v", err)
	}
	if strings.Contains(gotQuery, "limit=") {
		t.Fatalf("limit=0 should not be sent, query=%q", gotQuery)
	}
	if !strings.Contains(gotQuery, "oldest=2025-01-01") {
		t.Fatalf("oldest should still be present, query=%q", gotQuery)
	}
}

func TestListActivities_TrimsWhitespaceFilters(t *testing.T) {
	var gotQuery string
	c, _ := newActivitiesClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`[]`))
	}))

	if _, err := c.ListActivities(context.Background(), ListActivitiesParams{Oldest: "   ", Newest: "\t"}); err != nil {
		t.Fatalf("ListActivities: %v", err)
	}
	if gotQuery != "" {
		t.Fatalf("whitespace-only filters should be omitted, query=%q", gotQuery)
	}
}

func TestGetActivity_PathEscapeAndDecode(t *testing.T) {
	var gotPath, gotEscaped string
	c, _ := newActivitiesClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotEscaped = r.URL.EscapedPath()
		_, _ = w.Write([]byte(`{"id":"i 123","type":"Ride"}`))
	}))

	raw, err := c.GetActivity(context.Background(), "i 123")
	if err != nil {
		t.Fatalf("GetActivity: %v", err)
	}
	// Decoded path has the literal space; escaped path proves the client sent %20.
	if gotPath != "/activity/i 123" {
		t.Fatalf("decoded path = %q", gotPath)
	}
	if gotEscaped != "/activity/i%20123" {
		t.Fatalf("escaped path = %q, want /activity/i%%20123", gotEscaped)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	if decoded["id"] != "i 123" {
		t.Fatalf("decoded id = %v", decoded["id"])
	}
}

func TestGetActivity_EmptyIDRejected(t *testing.T) {
	c, _ := newActivitiesClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("server should not be called, got %s %s", r.Method, r.URL.Path)
	}))
	for _, id := range []string{"", "   ", "\t"} {
		if _, err := c.GetActivity(context.Background(), id); err == nil {
			t.Fatalf("expected error for id=%q", id)
		}
	}
}

func TestGetActivityStreams_WithTypes(t *testing.T) {
	var gotPath, gotQuery string
	c, _ := newActivitiesClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`[{"type":"watts","data":[1,2,3]}]`))
	}))

	types := []string{"watts", "heartrate", "cadence"}
	raw, err := c.GetActivityStreams(context.Background(), "99", types)
	if err != nil {
		t.Fatalf("GetActivityStreams: %v", err)
	}
	if gotPath != "/activity/99/streams" {
		t.Fatalf("path = %q", gotPath)
	}
	// url.Values.Encode escapes commas to %2C.
	if gotQuery != "types=watts%2Cheartrate%2Ccadence" {
		t.Fatalf("query = %q", gotQuery)
	}
	if !strings.Contains(string(raw), `"watts"`) {
		t.Fatalf("raw body missing expected stream, got %s", raw)
	}
}

func TestGetActivityStreams_WithoutTypes(t *testing.T) {
	var gotQuery string
	c, _ := newActivitiesClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`[]`))
	}))

	if _, err := c.GetActivityStreams(context.Background(), "99", nil); err != nil {
		t.Fatalf("GetActivityStreams: %v", err)
	}
	if gotQuery != "" {
		t.Fatalf("query should be empty when types is nil, got %q", gotQuery)
	}

	// Empty slice should also omit the param.
	if _, err := c.GetActivityStreams(context.Background(), "99", []string{}); err != nil {
		t.Fatalf("GetActivityStreams (empty slice): %v", err)
	}
	if gotQuery != "" {
		t.Fatalf("query should be empty when types is [], got %q", gotQuery)
	}
}

func TestGetActivityStreams_EmptyIDRejected(t *testing.T) {
	c, _ := newActivitiesClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("server should not be called")
	}))
	if _, err := c.GetActivityStreams(context.Background(), "  ", []string{"watts"}); err == nil {
		t.Fatal("expected error for blank activity id")
	}
}

func TestGetActivityIntervals_Path(t *testing.T) {
	var gotPath string
	c, _ := newActivitiesClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"id":"7","intervals":[]}`))
	}))

	raw, err := c.GetActivityIntervals(context.Background(), "7")
	if err != nil {
		t.Fatalf("GetActivityIntervals: %v", err)
	}
	if gotPath != "/activity/7/intervals" {
		t.Fatalf("path = %q", gotPath)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded["id"] != "7" {
		t.Fatalf("unexpected decoded payload: %v", decoded)
	}
}

func TestGetActivityIntervals_EmptyIDRejected(t *testing.T) {
	c, _ := newActivitiesClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("server should not be called")
	}))
	if _, err := c.GetActivityIntervals(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty activity id")
	}
}
