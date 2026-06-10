// ABOUTME: Athlete domain methods for intervals.icu API (profile, fitness trend, folders).
// ABOUTME: Returns raw JSON (json.RawMessage) for tolerant parsing and MCP pass-through.

package icu

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
)

// GetFitnessParams are the optional filters accepted by GetFitness.
type GetFitnessParams struct {
	Oldest string // yyyy-MM-dd
	Newest string // yyyy-MM-dd
	Sport  string // optional sport filter (e.g. "Ride", "Run")
}

// GetAthlete calls GET /athlete/{id} and returns the athlete profile as raw JSON.
// The payload contains info such as name, timezone, sportSettings and icu_* zones.
func (c *Client) GetAthlete(ctx context.Context) (json.RawMessage, error) {
	path := "/athlete/" + url.PathEscape(c.athleteID)
	body, err := c.Do(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(body), nil
}

// GetFitness calls GET /athlete/{id}/fitness and returns the CTL/ATL/TSB/rampRate
// time series as raw JSON. Only non-empty parameters are sent as query string.
func (c *Client) GetFitness(ctx context.Context, p GetFitnessParams) (json.RawMessage, error) {
	path := "/athlete/" + url.PathEscape(c.athleteID) + "/fitness"
	q := url.Values{}
	if p.Oldest != "" {
		q.Set("oldest", p.Oldest)
	}
	if p.Newest != "" {
		q.Set("newest", p.Newest)
	}
	if p.Sport != "" {
		q.Set("sport", p.Sport)
	}
	body, err := c.Do(ctx, http.MethodGet, path, q, nil)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(body), nil
}

// ListFolders calls GET /athlete/{id}/folders and returns the athlete's workout
// library folders as raw JSON.
func (c *Client) ListFolders(ctx context.Context) (json.RawMessage, error) {
	path := "/athlete/" + url.PathEscape(c.athleteID) + "/folders"
	body, err := c.Do(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(body), nil
}
