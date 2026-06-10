// ABOUTME: Tests for events domain methods: path shape, query filters, body propagation, errors.
// ABOUTME: Uses httptest servers; verifies required-field validation happens before HTTP call.

package icu

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestListEvents_PathAndAllFilters(t *testing.T) {
	var gotPath, gotQuery string
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`[{"id":"EVT1"}]`))
	}))

	raw, err := c.ListEvents(context.Background(), ListEventsParams{
		Oldest:   "2026-01-01",
		Newest:   "2026-01-31",
		Category: "WORKOUT",
		Resolve:  true,
	})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if gotPath != "/athlete/0/events" {
		t.Errorf("path = %q", gotPath)
	}
	for _, want := range []string{"oldest=2026-01-01", "newest=2026-01-31", "category=WORKOUT", "resolve=true"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("query = %q missing %q", gotQuery, want)
		}
	}
	if !strings.Contains(string(raw), "EVT1") {
		t.Errorf("raw body = %s", raw)
	}
}

func TestListEvents_PartialFilters(t *testing.T) {
	var gotQuery string
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`[]`))
	}))

	_, err := c.ListEvents(context.Background(), ListEventsParams{
		Oldest: "2026-02-01",
	})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if gotQuery != "oldest=2026-02-01" {
		t.Errorf("query = %q, want only oldest", gotQuery)
	}
}

func TestListEvents_NoFilters(t *testing.T) {
	var gotQuery string
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`[]`))
	}))

	if _, err := c.ListEvents(context.Background(), ListEventsParams{}); err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if gotQuery != "" {
		t.Errorf("query = %q, want empty", gotQuery)
	}
}

func TestListEvents_ResolveOnlyWhenTrue(t *testing.T) {
	var gotQuery string
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`[]`))
	}))

	_, err := c.ListEvents(context.Background(), ListEventsParams{Resolve: false})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if strings.Contains(gotQuery, "resolve") {
		t.Errorf("query = %q, should not include resolve", gotQuery)
	}
}

func TestCreateEvent_PostsJSONBody(t *testing.T) {
	var gotMethod, gotPath, gotCT string
	var gotBody map[string]any
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotCT = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"id":"EVT42"}`))
	}))

	payload := map[string]any{
		"start_date_local": "2026-03-01T07:00:00",
		"category":         "WORKOUT",
		"name":             "Tempo run",
		"type":             "Run",
		"moving_time":      3600,
	}
	raw, err := c.CreateEvent(context.Background(), payload)
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q", gotMethod)
	}
	if gotPath != "/athlete/0/events" {
		t.Errorf("path = %q", gotPath)
	}
	if gotCT != "application/json" {
		t.Errorf("content-type = %q", gotCT)
	}
	if gotBody["name"] != "Tempo run" || gotBody["category"] != "WORKOUT" {
		t.Errorf("body decoded = %v", gotBody)
	}
	if !strings.Contains(string(raw), "EVT42") {
		t.Errorf("raw = %s", raw)
	}
}

func TestCreateEvent_MissingStartDateLocal(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("should not hit server, got %s %s", r.Method, r.URL.Path)
	}))
	_, err := c.CreateEvent(context.Background(), map[string]any{"category": "WORKOUT"})
	if err == nil || !strings.Contains(err.Error(), "start_date_local") {
		t.Fatalf("err = %v, want missing start_date_local", err)
	}
}

func TestCreateEvent_MissingCategory(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("should not hit server, got %s %s", r.Method, r.URL.Path)
	}))
	_, err := c.CreateEvent(context.Background(), map[string]any{"start_date_local": "2026-03-01T07:00:00"})
	if err == nil || !strings.Contains(err.Error(), "category") {
		t.Fatalf("err = %v, want missing category", err)
	}
}

func TestCreateEvent_EmptyPayload(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("should not hit server")
	}))
	_, err := c.CreateEvent(context.Background(), nil)
	if err == nil {
		t.Fatalf("expected error for nil payload")
	}
	_, err = c.CreateEvent(context.Background(), map[string]any{})
	if err == nil {
		t.Fatalf("expected error for empty payload")
	}
}

func TestUpdateEvent_PutWithEscapedID(t *testing.T) {
	var gotMethod, gotEscapedPath string
	var gotBody map[string]any
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotEscapedPath = r.URL.EscapedPath()
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"id":"evt with space","name":"updated"}`))
	}))

	raw, err := c.UpdateEvent(context.Background(), "evt with space", map[string]any{"name": "updated"})
	if err != nil {
		t.Fatalf("UpdateEvent: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q", gotMethod)
	}
	// url.PathEscape encodes spaces as %20 on the wire.
	if gotEscapedPath != "/athlete/0/events/evt%20with%20space" {
		t.Errorf("escaped path = %q", gotEscapedPath)
	}
	if gotBody["name"] != "updated" {
		t.Errorf("body = %v", gotBody)
	}
	if !strings.Contains(string(raw), "updated") {
		t.Errorf("raw = %s", raw)
	}
}

func TestUpdateEvent_EmptyID(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("should not hit server")
	}))
	_, err := c.UpdateEvent(context.Background(), "", map[string]any{"name": "x"})
	if err == nil || !strings.Contains(err.Error(), "event id") {
		t.Fatalf("err = %v, want event id required", err)
	}
}

func TestUpdateEvent_EmptyPayload(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("should not hit server")
	}))
	_, err := c.UpdateEvent(context.Background(), "EVT1", nil)
	if err == nil {
		t.Fatalf("expected error for nil payload")
	}
}

func TestDeleteEvent_DeleteWithPath(t *testing.T) {
	var gotMethod, gotPath string
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))

	if err := c.DeleteEvent(context.Background(), "EVT123"); err != nil {
		t.Fatalf("DeleteEvent: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %q", gotMethod)
	}
	if gotPath != "/athlete/0/events/EVT123" {
		t.Errorf("path = %q", gotPath)
	}
}

func TestDeleteEvent_NotFoundAPIError(t *testing.T) {
	// 404 is not a retryable status: the APIError bubbles up directly.
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
	}))

	err := c.DeleteEvent(context.Background(), "GHOST")
	if err == nil {
		t.Fatalf("expected error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error type = %T, want *APIError: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d", apiErr.StatusCode)
	}
}

func TestDeleteEvent_EmptyID(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("should not hit server")
	}))
	if err := c.DeleteEvent(context.Background(), ""); err == nil {
		t.Fatalf("expected error for empty id")
	}
}
