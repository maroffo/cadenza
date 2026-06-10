// ABOUTME: intervals.icu activities domain: list, get, streams, intervals.
// ABOUTME: Thin wrappers over Client.Do that return json.RawMessage for pass-through.

package icu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// ListActivitiesParams holds the filters supported by the list endpoint.
type ListActivitiesParams struct {
	Oldest string // yyyy-MM-dd (optional)
	Newest string // yyyy-MM-dd (optional)
	Limit  int    // optional; when <= 0 the server default is used
}

// ListActivities returns the raw JSON array of activities for the authenticated
// athlete. Filtering is done server-side via the `oldest`, `newest` and `limit`
// query parameters. Empty filters are omitted from the request.
func (c *Client) ListActivities(ctx context.Context, p ListActivitiesParams) (json.RawMessage, error) {
	q := url.Values{}
	if s := strings.TrimSpace(p.Oldest); s != "" {
		q.Set("oldest", s)
	}
	if s := strings.TrimSpace(p.Newest); s != "" {
		q.Set("newest", s)
	}
	if p.Limit > 0 {
		q.Set("limit", strconv.Itoa(p.Limit))
	}

	path := "/athlete/" + url.PathEscape(c.athleteID) + "/activities"
	raw, err := c.Do(ctx, "GET", path, q, nil)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(raw), nil
}

// GetActivity returns the full activity document for the given id.
func (c *Client) GetActivity(ctx context.Context, activityID string) (json.RawMessage, error) {
	id, err := requireActivityID(activityID)
	if err != nil {
		return nil, err
	}
	path := "/activity/" + url.PathEscape(id)
	raw, err := c.Do(ctx, "GET", path, nil, nil)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(raw), nil
}

// GetActivityStreams returns the per-second data streams for an activity.
// If types is empty, no `types` query param is sent and the server returns all
// available streams.
func (c *Client) GetActivityStreams(ctx context.Context, activityID string, types []string) (json.RawMessage, error) {
	id, err := requireActivityID(activityID)
	if err != nil {
		return nil, err
	}

	q := url.Values{}
	if len(types) > 0 {
		q.Set("types", strings.Join(types, ","))
	}

	path := "/activity/" + url.PathEscape(id) + "/streams"
	raw, err := c.Do(ctx, "GET", path, q, nil)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(raw), nil
}

// GetActivityIntervals returns the detected or manual intervals for an activity.
func (c *Client) GetActivityIntervals(ctx context.Context, activityID string) (json.RawMessage, error) {
	id, err := requireActivityID(activityID)
	if err != nil {
		return nil, err
	}
	path := "/activity/" + url.PathEscape(id) + "/intervals"
	raw, err := c.Do(ctx, "GET", path, nil, nil)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(raw), nil
}

func requireActivityID(id string) (string, error) {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return "", fmt.Errorf("activity_id is required")
	}
	return trimmed, nil
}
