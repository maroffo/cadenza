// ABOUTME: Wellness domain methods for intervals.icu: get single day, list range, update entry.
// ABOUTME: Tolerant parsing via json.RawMessage; fields map is free-form to track backend volatility.

package icu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Typical wellness fields accepted by intervals.icu (non-exhaustive, backend schema evolves):
// weight, restingHR, hrv, sleepSecs, sleepScore, fatigue, soreness, stress, mood, motivation,
// injury, sickness, readiness, bloodGlucose, steps, bodyFat, vo2max, spO2, systolic, diastolic,
// hydration, hydrationVolume, food, caffeine, alcohol.
// The UpdateWellness API accepts any subset; unset fields are left untouched server-side.

// validateDate enforces the yyyy-MM-dd format with strict calendar ranges.
// Uses time.Parse so that impossible values like 2026-13-40 are rejected locally
// before hitting the server.
func validateDate(date string) error {
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return fmt.Errorf("invalid date %q: expected yyyy-MM-dd (got parse error: %v)", date, err)
	}
	return nil
}

// GetWellnessRangeParams filters ListWellness by date range (inclusive, yyyy-MM-dd).
// Both fields are optional: empty strings are omitted from the query.
type GetWellnessRangeParams struct {
	Oldest string
	Newest string
}

// GetWellness fetches wellness data for a single day.
// GET /athlete/{id}/wellness/{date}
func (c *Client) GetWellness(ctx context.Context, date string) (json.RawMessage, error) {
	if err := validateDate(date); err != nil {
		return nil, err
	}
	path := fmt.Sprintf("/athlete/%s/wellness/%s", url.PathEscape(c.athleteID), date)
	body, err := c.Do(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(body), nil
}

// ListWellness fetches wellness entries in a date range.
// GET /athlete/{id}/wellness?oldest=...&newest=...
func (c *Client) ListWellness(ctx context.Context, p GetWellnessRangeParams) (json.RawMessage, error) {
	q := url.Values{}
	if p.Oldest != "" {
		q.Set("oldest", p.Oldest)
	}
	if p.Newest != "" {
		q.Set("newest", p.Newest)
	}
	path := fmt.Sprintf("/athlete/%s/wellness", url.PathEscape(c.athleteID))
	body, err := c.Do(ctx, http.MethodGet, path, q, nil)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(body), nil
}

// UpdateWellness performs a partial update of a wellness entry for a given day.
// PUT /athlete/{id}/wellness/{date}
// Only the keys present in fields are modified server-side.
func (c *Client) UpdateWellness(ctx context.Context, date string, fields map[string]any) (json.RawMessage, error) {
	if err := validateDate(date); err != nil {
		return nil, err
	}
	if fields == nil {
		return nil, errors.New("fields must not be nil")
	}
	if len(fields) == 0 {
		return nil, errors.New("fields must not be empty")
	}
	path := fmt.Sprintf("/athlete/%s/wellness/%s", url.PathEscape(c.athleteID), date)
	body, err := c.Do(ctx, http.MethodPut, path, nil, fields)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(body), nil
}
