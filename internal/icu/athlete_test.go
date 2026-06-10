// ABOUTME: Unit tests for athlete domain: GetAthlete, GetFitness, ListFolders against httptest.
// ABOUTME: Verifies path, Basic auth, query params and APIError mapping on 401.

package icu

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func newAthleteClient(t *testing.T, handler http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	c := New(ts.URL, "k", "0",
		WithMaxRetries(0),
		WithRateLimit(1000, 1000),
		WithRetryBase(time.Millisecond),
		withSleep(func(context.Context, time.Duration) error { return nil }),
	)
	return c, ts
}

func TestGetAthlete_PathAndAuth(t *testing.T) {
	var gotPath, gotAuth string
	c, _ := newAthleteClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"id":"0","name":"Max","timezone":"Europe/Rome"}`))
	}))

	body, err := c.GetAthlete(context.Background())
	if err != nil {
		t.Fatalf("GetAthlete: %v", err)
	}
	if gotPath != "/athlete/0" {
		t.Errorf("path = %q, want /athlete/0", gotPath)
	}
	// Decode the Authorization header and compare fields separately, instead
	// of encoding the same literal twice on both sides (would be a tautology).
	if !strings.HasPrefix(gotAuth, "Basic ") {
		t.Fatalf("Authorization = %q, want Basic scheme", gotAuth)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(gotAuth, "Basic "))
	if err != nil {
		t.Fatalf("Authorization decode: %v", err)
	}
	gotUser, gotPass, ok := strings.Cut(string(decoded), ":")
	if !ok {
		t.Fatalf("Authorization payload not user:pass: %q", decoded)
	}
	if gotUser != "API_KEY" {
		t.Errorf("basic user = %q, want API_KEY", gotUser)
	}
	if gotPass != "k" {
		t.Errorf("basic pass = %q, want %q", gotPass, "k")
	}
	if !strings.Contains(string(body), `"name":"Max"`) {
		t.Errorf("body = %s", body)
	}
}

func TestGetFitness_AllFilters(t *testing.T) {
	var gotPath, gotQuery string
	c, _ := newAthleteClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`[{"date":"2024-01-01","ctl":50.0,"atl":40.0,"tsb":10.0}]`))
	}))

	body, err := c.GetFitness(context.Background(), GetFitnessParams{
		Oldest: "2024-01-01",
		Newest: "2024-01-31",
		Sport:  "Ride",
	})
	if err != nil {
		t.Fatalf("GetFitness: %v", err)
	}
	if gotPath != "/athlete/0/fitness" {
		t.Errorf("path = %q", gotPath)
	}
	// Parse the query string and assert on key/value pairs: the alphabetical
	// ordering that url.Values.Encode happens to produce is not part of our
	// contract, only the presence of each filter.
	q, err := url.ParseQuery(gotQuery)
	if err != nil {
		t.Fatalf("parse query %q: %v", gotQuery, err)
	}
	for k, want := range map[string]string{
		"oldest": "2024-01-01",
		"newest": "2024-01-31",
		"sport":  "Ride",
	} {
		if got := q.Get(k); got != want {
			t.Errorf("query %s = %q, want %q", k, got, want)
		}
	}
	// Nothing else should have been smuggled in.
	if len(q) != 3 {
		t.Errorf("query has unexpected extra keys: %v", q)
	}
	if !strings.Contains(string(body), `"ctl":50`) {
		t.Errorf("body = %s", body)
	}
}

func TestGetFitness_NoFilters(t *testing.T) {
	var gotPath, gotQuery string
	c, _ := newAthleteClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`[]`))
	}))

	if _, err := c.GetFitness(context.Background(), GetFitnessParams{}); err != nil {
		t.Fatalf("GetFitness: %v", err)
	}
	if gotPath != "/athlete/0/fitness" {
		t.Errorf("path = %q", gotPath)
	}
	if gotQuery != "" {
		t.Errorf("query = %q, want empty", gotQuery)
	}
}

func TestGetFitness_PartialFilters(t *testing.T) {
	var gotQuery string
	c, _ := newAthleteClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`[]`))
	}))

	_, err := c.GetFitness(context.Background(), GetFitnessParams{Oldest: "2024-01-01"})
	if err != nil {
		t.Fatalf("GetFitness: %v", err)
	}
	if gotQuery != "oldest=2024-01-01" {
		t.Errorf("query = %q, want oldest=2024-01-01", gotQuery)
	}
}

func TestListFolders_Path(t *testing.T) {
	var gotPath string
	c, _ := newAthleteClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`[{"id":"f1","name":"Base"}]`))
	}))

	body, err := c.ListFolders(context.Background())
	if err != nil {
		t.Fatalf("ListFolders: %v", err)
	}
	if gotPath != "/athlete/0/folders" {
		t.Errorf("path = %q, want /athlete/0/folders", gotPath)
	}
	if !strings.Contains(string(body), `"name":"Base"`) {
		t.Errorf("body = %s", body)
	}
}

func TestGetAthlete_Unauthorized(t *testing.T) {
	c, _ := newAthleteClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`unauthorized`))
	}))

	_, err := c.GetAthlete(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error type = %T, want *APIError: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", apiErr.StatusCode)
	}
}
