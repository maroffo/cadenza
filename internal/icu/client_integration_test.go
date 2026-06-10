// ABOUTME: Integration tests against live intervals.icu API (opt-in via build tag).
// ABOUTME: Enable with: go test -tags=integration ./internal/icu/ -run Integration

//go:build integration

package icu

import (
	"context"
	"os"
	"testing"
	"time"
)

// integrationClient returns a client pointed at the live API, or skips the
// test if no API key is configured. All integration tests must be read-only.
func integrationClient(t *testing.T) *Client {
	t.Helper()
	apiKey := os.Getenv("INTERVALS_API_KEY")
	if apiKey == "" {
		t.Skip("INTERVALS_API_KEY not set, skipping integration")
	}
	athleteID := os.Getenv("INTERVALS_ATHLETE_ID")
	if athleteID == "" {
		athleteID = "0"
	}
	baseURL := os.Getenv("INTERVALS_BASE_URL")
	if baseURL == "" {
		baseURL = "https://intervals.icu/api/v1"
	}
	return New(baseURL, apiKey, athleteID)
}

func TestIntegration_GetAthlete(t *testing.T) {
	c := integrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	body, err := c.GetAthlete(ctx)
	if err != nil {
		t.Fatalf("GetAthlete: %v", err)
	}
	if len(body) == 0 {
		t.Fatal("empty response")
	}
	// sanity: response should be JSON object
	if body[0] != '{' {
		n := len(body)
		if n > 100 {
			n = 100
		}
		t.Fatalf("not a JSON object: %s", string(body[:n]))
	}
}

// TestIntegration_ListWellness_SmokeLast7Days hits the wellness range endpoint
// for the last 7 days. Read-only smoke test to catch regressions in the
// wellness list path + auth + rate-limit plumbing.
func TestIntegration_ListWellness_SmokeLast7Days(t *testing.T) {
	c := integrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	now := time.Now().UTC()
	oldest := now.AddDate(0, 0, -7).Format("2006-01-02")
	newest := now.Format("2006-01-02")

	body, err := c.ListWellness(ctx, GetWellnessRangeParams{
		Oldest: oldest,
		Newest: newest,
	})
	if err != nil {
		t.Fatalf("ListWellness: %v", err)
	}
	if len(body) == 0 {
		t.Fatal("empty wellness response")
	}
	// Response can legitimately be an empty JSON array if the athlete has no
	// wellness data for the range; either [ or { is acceptable.
	first := body[0]
	if first != '[' && first != '{' {
		n := len(body)
		if n > 100 {
			n = 100
		}
		t.Fatalf("not JSON: %s", string(body[:n]))
	}
}

// TestIntegration_ListEvents_UpcomingSmoke queries the next 30 days of calendar
// events. Read-only, does not create/update/delete anything.
func TestIntegration_ListEvents_UpcomingSmoke(t *testing.T) {
	c := integrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	now := time.Now().UTC()
	oldest := now.Format("2006-01-02")
	newest := now.AddDate(0, 0, 30).Format("2006-01-02")

	body, err := c.ListEvents(ctx, ListEventsParams{
		Oldest: oldest,
		Newest: newest,
	})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(body) == 0 {
		t.Fatal("empty events response")
	}
	first := body[0]
	if first != '[' && first != '{' {
		n := len(body)
		if n > 100 {
			n = 100
		}
		t.Fatalf("not JSON: %s", string(body[:n]))
	}
}
