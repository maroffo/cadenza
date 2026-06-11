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
	lastEvent  map[string]any
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
				f.lastEvent = events[0]
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
			if len(resp) == 1 && f.lastEvent != nil {
				resp[0]["external_id"] = f.lastEvent["external_id"]
			}
			_ = json.NewEncoder(w).Encode(resp)
		default:
			_, _ = fmt.Fprint(w, `{}`)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
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

func TestExternalID_SlotKeyedGolden(t *testing.T) {
	// Golden literal: the id is the upsert slot. A regenerated plan for the
	// same day/sport MUST reuse it (overwrite, never orphan a second event).
	if got := ExternalID(testPlan()); got != "cadenza-2026-06-25-run" {
		t.Fatalf("ExternalID = %q, want cadenza-2026-06-25-run", got)
	}
	p2 := testPlan()
	p2.Title = "Rigenerato più corto"
	if ExternalID(p2) != ExternalID(testPlan()) {
		t.Fatal("regenerated plan got a new slot id (orphan event on the calendar)")
	}
}

func TestBuildDoc_DeterministicBytes(t *testing.T) {
	// ContentHash (ledger) and upsert idempotency depend on stable bytes.
	p := testPlan()
	a, err := p.BuildDoc()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := p.BuildDoc()
	if string(a) != string(b) {
		t.Fatalf("BuildDoc not byte-deterministic:\n%s\n%s", a, b)
	}
	if ContentHash(a) != ContentHash(b) {
		t.Fatal("ContentHash diverges on identical plans")
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

func TestDiffDoc_ZoneAndRangeTampering(t *testing.T) {
	p := testPlan()
	doc, _ := p.BuildDoc()

	tamper := func(mutate func(map[string]any)) map[string]any {
		var m map[string]any
		_ = json.Unmarshal(doc, &m)
		mutate(m)
		return m
	}
	// Flat zone value changed (repeat sub-step Z4 -> Z2).
	zoneTampered := tamper(func(m map[string]any) {
		rep := m["steps"].([]any)[1].(map[string]any)
		rep["steps"].([]any)[0].(map[string]any)["hr"].(map[string]any)["value"] = 2.0
	})
	if diffs := DiffDoc(p, zoneTampered); len(diffs) == 0 {
		t.Fatal("zone tampering not flagged")
	}
	// Range lower bound lost (warmup Z1-2 flattened to start=2).
	startTampered := tamper(func(m map[string]any) {
		m["steps"].([]any)[0].(map[string]any)["hr"].(map[string]any)["start"] = 2.0
	})
	if diffs := DiffDoc(p, startTampered); len(diffs) == 0 {
		t.Fatal("range lower-bound tampering not flagged")
	}
}

func TestWriteVerified_VerifiedOnExactlyLastAttempt(t *testing.T) {
	f := newFakeICU(t)
	f.mangleNext = maxWriteAttempts - 1
	w := &Writer{C: icu.New(f.srv.URL, "k", "0")}

	out, err := w.WriteVerified(context.Background(), testPlan())
	if err != nil {
		t.Fatalf("WriteVerified: %v", err)
	}
	if out.Status != Verified || out.Attempts != maxWriteAttempts {
		t.Fatalf("outcome = %+v, want verified on the LAST attempt", out)
	}
}

func TestWriteVerified_UpstreamFailuresSurfaceAsErrors(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"upsert 500": func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		},
		"readback fails": func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "/bulk") {
				_, _ = fmt.Fprint(w, `[{"id":1}]`)
				return
			}
			http.Error(w, "boom", http.StatusInternalServerError)
		},
		"event vanished": func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "/bulk") {
				_, _ = fmt.Fprint(w, `[{"id":1}]`)
				return
			}
			_, _ = fmt.Fprint(w, `[]`)
		},
	}
	for name, handler := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(handler)
			defer srv.Close()
			w := &Writer{C: icu.New(srv.URL, "k", "0", icu.WithMaxRetries(0))}
			out, err := w.WriteVerified(context.Background(), testPlan())
			if err == nil {
				t.Fatalf("%s: error swallowed (outcome %+v)", name, out)
			}
			if out.Status == Verified {
				t.Fatalf("%s: verified on a failing upstream", name)
			}
		})
	}
}
