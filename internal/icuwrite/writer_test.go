// ABOUTME: Writer tests against a scripted fake intervals.icu: verify, repair, surface.
// ABOUTME: The mangled-write scenario pins the entire reason this package exists.

package icuwrite

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/maroffo/cadenza/internal/icu"
	"github.com/maroffo/cadenza/internal/workout"
)

func testPlan() workout.Plan {
	return workout.Plan{
		Date: "2026-06-25", Sport: workout.SportRun, Title: "Intervalli",
		Items: []workout.Item{
			{Step: &workout.Step{Minutes: 10, HR: workout.HRTarget{ZoneStart: 1, ZoneEnd: 2}, Intensity: "warmup"}},
			{Repeat: &workout.Repeat{Count: 3, Steps: []workout.Step{
				{Minutes: 4, HR: workout.HRTarget{Zone: 4}},
				{Minutes: 2, HR: workout.HRTarget{Zone: 1}},
			}}},
			{Step: &workout.Step{Minutes: 10, HR: workout.HRTarget{Zone: 1}, Intensity: "cooldown"}},
		},
	}
}

// fakeICU stores the last upserted event and serves it back on list; the
// mangle hook corrupts the stored doc for N reads to exercise repair.
type fakeICU struct {
	upserts    int
	mangleNext int
	srv        *httptest.Server
	lastDoc    map[string]any
}

func newFakeICU(t *testing.T) *fakeICU {
	f := &fakeICU{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/events/bulk"):
			f.upserts++
			var events []map[string]any
			_ = json.NewDecoder(r.Body).Decode(&events)
			if len(events) == 1 {
				f.lastDoc, _ = events[0]["workout_doc"].(map[string]any)
			}
			_, _ = fmt.Fprint(w, `[{"id":555}]`)
		case strings.Contains(r.URL.Path, "/events"):
			doc := f.lastDoc
			if f.mangleNext > 0 {
				f.mangleNext--
				doc = map[string]any{"steps": []any{map[string]any{"duration": 60.0}}}
			}
			out, _ := json.Marshal([]map[string]any{{
				"id": 555.0, "external_id": r.URL.Query().Get("ext-marker"), "workout_doc": doc,
			}})
			// external_id round trip: serve whatever the upsert stored.
			var resp []map[string]any
			_ = json.Unmarshal(out, &resp)
			if len(resp) == 1 {
				resp[0]["external_id"] = f.lastExternalID()
			}
			_ = json.NewEncoder(w).Encode(resp)
		default:
			_, _ = fmt.Fprint(w, `{}`)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeICU) lastExternalID() string {
	// The writer computes it deterministically; recompute the same way.
	p := testPlan()
	doc, _ := p.BuildDoc()
	return ExternalID(p, doc)
}

func TestWriteVerified_CleanFirstAttempt(t *testing.T) {
	f := newFakeICU(t)
	w := &Writer{C: icu.New(f.srv.URL, "k", "0")}

	out, err := w.WriteVerified(context.Background(), testPlan())
	if err != nil {
		t.Fatalf("WriteVerified: %v", err)
	}
	if out.Status != Verified || out.Attempts != 1 || out.EventID != 555 {
		t.Fatalf("outcome = %+v, want verified on attempt 1", out)
	}
	if f.upserts != 1 {
		t.Errorf("upserts = %d, want 1", f.upserts)
	}
}

func TestWriteVerified_MangledOnceThenRepaired(t *testing.T) {
	f := newFakeICU(t)
	f.mangleNext = 1 // first read-back returns a gutted doc
	w := &Writer{C: icu.New(f.srv.URL, "k", "0")}

	out, err := w.WriteVerified(context.Background(), testPlan())
	if err != nil {
		t.Fatalf("WriteVerified: %v", err)
	}
	if out.Status != Verified || out.Attempts != 2 {
		t.Fatalf("outcome = %+v, want verified on attempt 2 (repair)", out)
	}
	if f.upserts != 2 {
		t.Errorf("upserts = %d, want 2 (original + repair)", f.upserts)
	}
}

func TestWriteVerified_PersistentMangleSurfacesHonestly(t *testing.T) {
	f := newFakeICU(t)
	f.mangleNext = 99 // every read-back is wrong
	w := &Writer{C: icu.New(f.srv.URL, "k", "0")}

	out, err := w.WriteVerified(context.Background(), testPlan())
	if err != nil {
		t.Fatalf("WriteVerified: %v (persistent mismatch is an OUTCOME, not an error)", err)
	}
	if out.Status != Unverified || out.Attempts != maxWriteAttempts {
		t.Fatalf("outcome = %+v, want unverified_surfaced after %d attempts", out, maxWriteAttempts)
	}
	if len(out.Diffs) == 0 {
		t.Fatal("no diffs surfaced for the athlete-facing message")
	}
}

func TestDiffDoc_Catches(t *testing.T) {
	p := testPlan()
	doc, _ := p.BuildDoc()
	var clean map[string]any
	_ = json.Unmarshal(doc, &clean)

	if diffs := DiffDoc(p, clean); len(diffs) != 0 {
		t.Fatalf("clean doc diffed: %v", diffs)
	}
	if diffs := DiffDoc(p, nil); len(diffs) == 0 {
		t.Fatal("nil doc not flagged")
	}
	// Duration tampering must be caught.
	var tampered map[string]any
	_ = json.Unmarshal(doc, &tampered)
	steps := tampered["steps"].([]any)
	steps[0].(map[string]any)["duration"] = 1.0
	if diffs := DiffDoc(p, tampered); len(diffs) == 0 {
		t.Fatal("duration tampering not flagged")
	}
}

func TestExternalID_DeterministicAndDateScoped(t *testing.T) {
	p := testPlan()
	doc, _ := p.BuildDoc()
	a, b := ExternalID(p, doc), ExternalID(p, doc)
	if a != b {
		t.Fatalf("not deterministic: %s vs %s", a, b)
	}
	if !strings.HasPrefix(a, "cadenza-2026-06-25-") {
		t.Errorf("id %q not date-scoped", a)
	}
	p2 := p
	p2.Title = "Altro"
	doc2, _ := p2.BuildDoc()
	if ExternalID(p2, doc2) == a {
		t.Error("different content, same id")
	}
}

// The payload must NEVER carry a description: the server would parse it
// into steps and clobber the structured doc (live spike lesson).
func TestWriteVerified_NoDescriptionField(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/events/bulk") {
			captured, _ = json.Marshal(func() any {
				var v any
				_ = json.NewDecoder(r.Body).Decode(&v)
				return v
			}())
			_, _ = fmt.Fprint(w, `[{"id":1}]`)
			return
		}
		_, _ = fmt.Fprint(w, `[]`)
	}))
	defer srv.Close()
	w := &Writer{C: icu.New(srv.URL, "k", "0")}
	_, _ = w.WriteVerified(context.Background(), testPlan())
	if strings.Contains(string(captured), `"description"`) {
		t.Fatalf("payload carries description:\n%s", captured)
	}
}
