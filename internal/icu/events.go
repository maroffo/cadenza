// ABOUTME: Calendar events domain methods for intervals.icu: list/create/update/delete events.
// ABOUTME: Events cover WORKOUT, RACE_A/B/C, NOTE, FITNESS and TARGET categories on the athlete calendar.

package icu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

// EventCategory enumerates the calendar event kinds recognised by intervals.icu.
// ListEventsParams.Category stays string for backward compatibility; use these
// constants (or AllEventCategories) to populate schema enums without drift.
type EventCategory string

const (
	EventCategoryWorkout EventCategory = "WORKOUT"
	EventCategoryRaceA   EventCategory = "RACE_A"
	EventCategoryRaceB   EventCategory = "RACE_B"
	EventCategoryRaceC   EventCategory = "RACE_C"
	EventCategoryNote    EventCategory = "NOTE"
	EventCategoryFitness EventCategory = "FITNESS"
	EventCategoryTarget  EventCategory = "TARGET"
)

// AllEventCategories returns every EventCategory as a string slice, in the order
// used by the intervals.icu UI. Suitable for MCP enum/schema generation.
func AllEventCategories() []string {
	return []string{
		string(EventCategoryWorkout),
		string(EventCategoryRaceA),
		string(EventCategoryRaceB),
		string(EventCategoryRaceC),
		string(EventCategoryNote),
		string(EventCategoryFitness),
		string(EventCategoryTarget),
	}
}

// ListEventsParams filters the calendar events query. All fields are optional;
// only set values are forwarded as query parameters to intervals.icu.
type ListEventsParams struct {
	Oldest   string // yyyy-MM-dd (inclusive)
	Newest   string // yyyy-MM-dd (inclusive)
	Category string // One of EventCategory values (kept as string for API stability)
	Resolve  bool   // if true, expand referenced workouts inline
}

// ListEvents returns calendar events for the configured athlete.
// GET /athlete/{id}/events
func (c *Client) ListEvents(ctx context.Context, p ListEventsParams) (json.RawMessage, error) {
	q := url.Values{}
	if p.Oldest != "" {
		q.Set("oldest", p.Oldest)
	}
	if p.Newest != "" {
		q.Set("newest", p.Newest)
	}
	if p.Category != "" {
		q.Set("category", p.Category)
	}
	if p.Resolve {
		q.Set("resolve", "true")
	}

	path := fmt.Sprintf("/athlete/%s/events", url.PathEscape(c.athleteID))
	raw, err := c.Do(ctx, http.MethodGet, path, q, nil)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(raw), nil
}

// CreateEvent creates a new calendar event. The payload is passed through as-is,
// so callers may set any field accepted by intervals.icu. Required fields are
// start_date_local (ISO datetime) and category.
// POST /athlete/{id}/events
func (c *Client) CreateEvent(ctx context.Context, payload map[string]any) (json.RawMessage, error) {
	if len(payload) == 0 {
		return nil, errors.New("create event: payload is empty")
	}
	if _, ok := payload["start_date_local"]; !ok {
		return nil, errors.New("create event: missing required field 'start_date_local'")
	}
	if _, ok := payload["category"]; !ok {
		return nil, errors.New("create event: missing required field 'category'")
	}

	path := fmt.Sprintf("/athlete/%s/events", url.PathEscape(c.athleteID))
	raw, err := c.Do(ctx, http.MethodPost, path, nil, payload)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(raw), nil
}

// UpdateEvent updates an existing calendar event. The payload may contain any
// subset of the event fields; only the provided fields are modified server-side.
// PUT /athlete/{id}/events/{eventID}
func (c *Client) UpdateEvent(ctx context.Context, eventID string, payload map[string]any) (json.RawMessage, error) {
	if eventID == "" {
		return nil, errors.New("update event: event id is required")
	}
	if len(payload) == 0 {
		return nil, errors.New("update event: payload is empty")
	}

	path := fmt.Sprintf("/athlete/%s/events/%s", url.PathEscape(c.athleteID), url.PathEscape(eventID))
	raw, err := c.Do(ctx, http.MethodPut, path, nil, payload)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(raw), nil
}

// DeleteEvent removes a calendar event by id.
// DELETE /athlete/{id}/events/{eventID}
func (c *Client) DeleteEvent(ctx context.Context, eventID string) error {
	if eventID == "" {
		return errors.New("delete event: event id is required")
	}
	path := fmt.Sprintf("/athlete/%s/events/%s", url.PathEscape(c.athleteID), url.PathEscape(eventID))
	if _, err := c.Do(ctx, http.MethodDelete, path, nil, nil); err != nil {
		return err
	}
	return nil
}
