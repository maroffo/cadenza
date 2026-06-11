// ABOUTME: Verified workout writer: upsert by external_id, read back resolved, diff, repair.
// ABOUTME: "Write-and-trust is how silent step drops happen": every write is checked, bounded retries.

package icuwrite

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"

	"github.com/maroffo/cadenza/internal/icu"
	"github.com/maroffo/cadenza/internal/workout"
)

// maxWriteAttempts bounds the write→verify→repair loop (decision 12).
const maxWriteAttempts = 3

type Status string

const (
	Verified   Status = "verified"
	Unverified Status = "unverified_surfaced"
)

type Outcome struct {
	Status     Status
	EventID    int64
	ExternalID string
	Attempts   int
	Diffs      []string
}

type Writer struct {
	C *icu.Client
}

// ExternalID is deterministic on date+content: a task retry upserts the same
// event instead of duplicating it (spike-verified under API-key auth).
func ExternalID(p workout.Plan, doc json.RawMessage) string {
	sum := sha256.Sum256(append([]byte(p.Date+"|"+p.Title+"|"), doc...))
	return fmt.Sprintf("cadenza-%s-%x", p.Date, sum[:4])
}

// WriteVerified writes the plan and verifies what intervals.icu actually
// stored. Never trusts the write: reads back with resolve=true and diffs
// structure; repairs by re-upserting; after maxWriteAttempts surfaces
// honestly so the athlete gets the plan in chat instead of a broken calendar.
func (w *Writer) WriteVerified(ctx context.Context, p workout.Plan) (Outcome, error) {
	doc, err := p.BuildDoc()
	if err != nil {
		return Outcome{}, fmt.Errorf("icuwrite: build: %w", err)
	}
	extID := ExternalID(p, doc)
	var docObj map[string]any
	if err := json.Unmarshal(doc, &docObj); err != nil {
		return Outcome{}, fmt.Errorf("icuwrite: doc decode: %w", err)
	}
	// NO description field, ever: the server parses description text into
	// steps and would clobber the structured doc (live spike).
	event := map[string]any{
		"category":         "WORKOUT",
		"start_date_local": p.Date + "T00:00:00",
		"type":             p.Sport,
		"name":             p.Title,
		"external_id":      extID,
		"workout_doc":      docObj,
	}

	out := Outcome{Status: Unverified, ExternalID: extID}
	for attempt := 1; attempt <= maxWriteAttempts; attempt++ {
		out.Attempts = attempt
		if _, err := w.C.Do(ctx, "POST",
			fmt.Sprintf("/athlete/%s/events/bulk", url.PathEscape(w.C.AthleteID())),
			url.Values{"upsert": []string{"true"}}, []any{event}); err != nil {
			return out, fmt.Errorf("icuwrite: upsert (attempt %d): %w", attempt, err)
		}

		stored, eventID, err := w.readBack(ctx, p.Date, extID)
		if err != nil {
			return out, fmt.Errorf("icuwrite: read back (attempt %d): %w", attempt, err)
		}
		out.EventID = eventID
		diffs := DiffDoc(p, stored)
		if len(diffs) == 0 {
			out.Status = Verified
			out.Diffs = nil
			return out, nil
		}
		out.Diffs = diffs
		slog.Warn("icuwrite: verification diff, repairing", "attempt", attempt, "diffs", diffs)
	}
	// Honest failure: caller surfaces the plan in chat (decision 12).
	return out, nil
}

func (w *Writer) readBack(ctx context.Context, date, extID string) (map[string]any, int64, error) {
	raw, err := w.C.ListEvents(ctx, icu.ListEventsParams{Oldest: date, Newest: date, Resolve: true})
	if err != nil {
		return nil, 0, err
	}
	var events []map[string]any
	if err := json.Unmarshal(raw, &events); err != nil {
		return nil, 0, fmt.Errorf("events decode: %w", err)
	}
	for _, e := range events {
		if id, _ := e["external_id"].(string); id == extID {
			eventID := int64(0)
			if n, ok := e["id"].(float64); ok {
				eventID = int64(n)
			}
			docAny, _ := e["workout_doc"].(map[string]any)
			return docAny, eventID, nil
		}
	}
	return nil, 0, fmt.Errorf("event %s assente dopo la scrittura", extID)
}

// DiffDoc compares the intended plan with the stored resolved doc. Checks
// structure (step count, durations, zone targets); labels are cosmetic and
// not diffed. A missing doc or missing steps is a total mismatch.
func DiffDoc(p workout.Plan, stored map[string]any) []string {
	if stored == nil {
		return []string{"workout_doc assente nell'evento salvato"}
	}
	gotSteps := flattenStored(stored)
	want := p.Flatten()
	if len(gotSteps) != len(want) {
		return []string{fmt.Sprintf("step: attesi %d, trovati %d", len(want), len(gotSteps))}
	}
	var diffs []string
	for i, ws := range want {
		gs := gotSteps[i]
		if int(gs.duration) != ws.DurationSeconds() {
			diffs = append(diffs, fmt.Sprintf("step %d: durata attesa %ds, trovata %ds", i+1, ws.DurationSeconds(), int(gs.duration)))
		}
		wantZone := ws.HR.Zone
		if wantZone == 0 {
			wantZone = ws.HR.ZoneEnd
		}
		if gs.zoneTop != wantZone {
			diffs = append(diffs, fmt.Sprintf("step %d: zona attesa Z%d, trovata Z%d", i+1, wantZone, gs.zoneTop))
		}
	}
	return diffs
}

type storedStep struct {
	duration float64
	zoneTop  int
}

func flattenStored(doc map[string]any) []storedStep {
	rawSteps, _ := doc["steps"].([]any)
	var out []storedStep
	for _, rs := range rawSteps {
		m, ok := rs.(map[string]any)
		if !ok {
			continue
		}
		if reps, isRep := m["reps"].(float64); isRep {
			sub, _ := m["steps"].([]any)
			for range int(reps) {
				for _, ss := range sub {
					if sm, ok := ss.(map[string]any); ok {
						out = append(out, parseStored(sm))
					}
				}
			}
			continue
		}
		out = append(out, parseStored(m))
	}
	return out
}

func parseStored(m map[string]any) storedStep {
	s := storedStep{}
	if d, ok := m["duration"].(float64); ok {
		s.duration = d
	}
	if hr, ok := m["hr"].(map[string]any); ok {
		if v, ok := hr["value"].(float64); ok {
			s.zoneTop = int(v)
		} else if e, ok := hr["end"].(float64); ok {
			s.zoneTop = int(e)
		}
	}
	return s
}
