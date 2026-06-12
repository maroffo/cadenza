// ABOUTME: Debrief sweep tests: idempotency, settle window, planned-vs-executed, missed sessions.
// ABOUTME: The athlete hears each thing exactly once; numbers come from code, never the model.

package job

import (
	"context"
	"strings"
	"testing"

	"github.com/maroffo/cadenza/internal/agent"
	"github.com/maroffo/cadenza/internal/fakes"
	"github.com/maroffo/cadenza/internal/icu"
)

type memMarks struct{ seen map[string]bool }

func newMemMarks() *memMarks { return &memMarks{seen: map[string]bool{}} }

func (m *memMarks) MarkOnce(_ context.Context, key string) (bool, error) {
	if m.seen[key] {
		return false, nil
	}
	m.seen[key] = true
	return true, nil
}

// settledActivity ended well before fixedNow (2026-06-10 07:00).
func settledActivity(id, dateLocal, sport string, tss int) icu.Activity {
	return icu.Activity{
		ID: id, StartDateLocal: dateLocal, Type: sport,
		TrainingLoad: &tss, MovingTime: intPtr(3600),
		HRZoneTimes: []int{600, 1800, 600, 300, 300, 0, 0},
	}
}

func newDebrief(llm *fakes.Anthropic, acts []icu.Activity, events []icu.Event) (Debrief, *stubMessenger, *memMarks) {
	out := &stubMessenger{}
	marks := newMemMarks()
	d := Debrief{
		Activities: stubActivities{acts: acts},
		Events:     stubEvents{events: events},
		Plans:      stubPlans{plans: map[string]string{}},
		Marks:      marks,
		Out:        out,
		Now:        fixedNow,
		TZ:         testTZ,
	}
	if llm != nil {
		d.Narrator = agent.Debriefer{Client: agent.NewClient("k", llm.URL()), Model: "claude-haiku-test"}
	}
	return d, out, marks
}

func TestDebrief_SweepIsCreateOnce(t *testing.T) {
	llm := fakes.NewAnthropic(fakes.Text{S: "Buon fondo, distribuzione corretta."})
	defer llm.Close()
	d, out, _ := newDebrief(llm, []icu.Activity{
		settledActivity("i1", "2026-06-09T18:00:00", "Run", 55),
	}, nil)

	if err := d.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(out.plain) != 1 || !strings.Contains(out.plain[0], "Debrief") {
		t.Fatalf("debrief missing: %v", out.plain)
	}
	if !strings.Contains(out.plain[0], "Buon fondo") || !strings.Contains(out.plain[0], "TSS 55") {
		t.Errorf("narrative+data block expected:\n%s", out.plain[0])
	}
	// Second sweep (the 19:30 run, or a task retry): silence.
	if err := d.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep 2: %v", err)
	}
	if len(out.plain) != 1 {
		t.Fatalf("debrief repeated: %v", out.plain)
	}
}

func TestDebrief_SettleWindowAndNoiseFilter(t *testing.T) {
	d, out, marks := newDebrief(nil, []icu.Activity{
		// Ended 10 minutes before fixedNow: NOT settled.
		settledActivity("fresh", "2026-06-10T05:50:00", "Run", 60),
		// Walk-level load: filtered, and NOT marked (late TSS recompute may upgrade).
		settledActivity("walk", "2026-06-09T10:00:00", "Walk", 8),
	}, nil)

	if err := d.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(out.plain) != 0 {
		t.Fatalf("messages for unsettled/noise: %v", out.plain)
	}
	if marks.seen["act-fresh"] || marks.seen["act-walk"] {
		t.Fatal("unsettled or noise activity consumed its mark")
	}
}

func TestDebrief_PrescribedComparisonFromLedger(t *testing.T) {
	d, out, _ := newDebrief(nil, []icu.Activity{
		settledActivity("i2", "2026-06-09T18:00:00", "Run", 70),
	}, nil)
	d.Plans = stubPlans{plans: map[string]string{
		"cadenza-2026-06-09-run": `{"date":"2026-06-09","sport":"Run","title":"Fondo Z2",
			"items":[{"minutes":50,"hr":{"zone":2}}]}`,
	}}

	if err := d.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(out.plain) != 1 {
		t.Fatalf("plain = %v", out.plain)
	}
	msg := out.plain[0]
	for _, want := range []string{`Prescritto: "Fondo Z2"`, "50 min", "Distribuzione:"} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in:\n%s", want, msg)
		}
	}
}

func TestDebrief_MissedSessionFiresOnce(t *testing.T) {
	events := []icu.Event{{
		Category: "WORKOUT", StartDateLocal: "2026-06-09T00:00:00",
		Name: strPtr("Fondo Z2 50min"), ExternalID: strPtr("cadenza-2026-06-09-run"),
	}}
	// A RIDE was executed yesterday, but the planned RUN was not.
	d, out, _ := newDebrief(nil, []icu.Activity{
		settledActivity("ride", "2026-06-09T08:00:00", "Ride", 40),
	}, events)

	if err := d.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	missed := 0
	for _, p := range out.plain {
		if strings.Contains(p, "non risulta nessuna attività") {
			missed++
			if !strings.Contains(p, "Fondo Z2 50min") {
				t.Errorf("missed message without the workout name: %s", p)
			}
		}
	}
	if missed != 1 {
		t.Fatalf("missed notices = %d, want 1", missed)
	}
	// Re-sweep: silence.
	before := len(out.plain)
	_ = d.Sweep(context.Background())
	if len(out.plain) != before {
		t.Fatal("missed-session notice repeated")
	}
}

func TestDebrief_ExecutedSameSportSilencesMissed(t *testing.T) {
	events := []icu.Event{{
		Category: "WORKOUT", StartDateLocal: "2026-06-09T00:00:00",
		Name: strPtr("Fondo"), ExternalID: strPtr("cadenza-2026-06-09-run"),
	}}
	d, out, _ := newDebrief(nil, []icu.Activity{
		settledActivity("run", "2026-06-09T18:00:00", "Run", 55),
	}, events)

	if err := d.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	for _, p := range out.plain {
		if strings.Contains(p, "non risulta") {
			t.Fatalf("missed fired despite execution: %s", p)
		}
	}
}

func TestDebrief_NarrativeDownDegradesToData(t *testing.T) {
	llm := fakes.NewAnthropic(fakes.HTTPErr{Status: 400})
	defer llm.Close()
	d, out, _ := newDebrief(llm, []icu.Activity{
		settledActivity("i3", "2026-06-09T18:00:00", "Run", 60),
	}, nil)

	if err := d.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(out.plain) != 1 || !strings.Contains(out.plain[0], "TSS 60") {
		t.Fatalf("deterministic debrief missing on LLM failure: %v", out.plain)
	}
}
