// ABOUTME: Unit tests for the week-context builder: the safety gate's input pipeline.
// ABOUTME: If aggregation is wrong, the gate vets garbage: every branch is pinned here.

package job

import (
	"context"
	"errors"
	"testing"

	"github.com/maroffo/cadenza/internal/icu"
	"github.com/maroffo/cadenza/internal/safety"
)

type stubPlans struct {
	plans map[string]string
	err   error
}

func (s stubPlans) LatestPlanFor(_ context.Context, extID string) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	return s.plans[extID], nil
}

func dayByDate(t *testing.T, w *safety.WeekContext, date string) safety.DayLoad {
	t.Helper()
	for _, d := range w.Days {
		if d.Date == date {
			return d
		}
	}
	t.Fatalf("day %s not in context", date)
	return safety.DayLoad{}
}

func intPtr(v int) *int { return &v }

func TestBuildWeekContext_AggregatesComponents(t *testing.T) {
	acts := stubActivities{acts: []icu.Activity{
		{ID: "i1", StartDateLocal: "2026-06-09T07:00:00", Type: "Run",
			TrainingLoad: intPtr(40), HRZoneTimes: []int{600, 600, 300, 200, 100, 50, 0}},
		{ID: "i2", StartDateLocal: "2026-06-09T18:00:00", Type: "Ride",
			TrainingLoad: intPtr(30), HRZoneTimes: []int{0, 0}},
		{ID: "i3", StartDateLocal: "garbage", Type: "Run", TrainingLoad: intPtr(99)},
	}}
	events := stubEvents{events: []icu.Event{
		{Category: "WORKOUT", StartDateLocal: "2026-06-11T00:00:00",
			Name: strPtr("Fondo"), ExternalID: strPtr("cadenza-2026-06-11-run")},
		{Category: "WORKOUT", StartDateLocal: "2026-06-12T00:00:00", Name: strPtr("Gruppo")},
		{Category: "RACE_A", StartDateLocal: "2026-06-13T00:00:00", Name: strPtr("non workout")},
		{Category: "WORKOUT", StartDateLocal: "2026-06-08T00:00:00",
			Name: strPtr("passato"), ExternalID: strPtr("cadenza-2026-06-08-run")},
	}}
	plans := stubPlans{plans: map[string]string{
		"cadenza-2026-06-11-run": `{"date":"2026-06-11","sport":"Run","title":"x",
			"items":[{"minutes":10,"hr":{"zone":4}},{"minutes":30,"hr":{"zone":2}}]}`,
	}}

	w := buildWeekContext(context.Background(), acts, events, plans, "2026-06-11", "2026-06-10")
	if w == nil {
		t.Fatal("context nil")
	}

	// Two activities on the 9th: TSS summed; hardness counts zones[3:] of the
	// 7-zone scheme (200+100+50+0=350) and ALL buckets of the 2-zone one (0).
	d9 := dayByDate(t, w, "2026-06-09")
	if d9.ExecutedTSS != 70 || d9.ExecutedHardSecs != 350 {
		t.Errorf("executed day = %+v, want TSS 70 hard 350", d9)
	}

	// Cadenza event resolved through the ledger: planned load with sport.
	d11 := dayByDate(t, w, "2026-06-11")
	if len(d11.Cadenza) != 1 || d11.Cadenza[0].Sport != "run" ||
		d11.Cadenza[0].HardSecs != 600 || d11.Cadenza[0].TSS <= 0 || d11.External {
		t.Errorf("cadenza day = %+v", d11)
	}

	// External workout flagged content-unknown; non-workout ignored.
	d12 := dayByDate(t, w, "2026-06-12")
	if !d12.External {
		t.Errorf("external not flagged: %+v", d12)
	}
	for _, d := range w.Days {
		if d.Date == "2026-06-13" {
			t.Errorf("non-workout category created a day: %+v", d)
		}
	}

	// Past planned events are skipped (executed activities represent them).
	for _, d := range w.Days {
		if d.Date == "2026-06-08" && len(d.Cadenza) > 0 {
			t.Errorf("past planned event counted: %+v", d)
		}
	}
}

func TestBuildWeekContext_LedgerMissMarksDayUnknown(t *testing.T) {
	events := stubEvents{events: []icu.Event{
		{Category: "WORKOUT", StartDateLocal: "2026-06-11T00:00:00",
			ExternalID: strPtr("cadenza-2026-06-11-run")},
	}}
	for name, plans := range map[string]PlanLookup{
		"nil lookup":   nil,
		"lookup error": stubPlans{err: errors.New("index missing")},
		"empty plan":   stubPlans{},
		"corrupt plan": stubPlans{plans: map[string]string{"cadenza-2026-06-11-run": "{broken"}},
	} {
		w := buildWeekContext(context.Background(), stubActivities{}, events, plans, "2026-06-11", "2026-06-10")
		if w == nil {
			t.Fatalf("%s: context nil", name)
		}
		d := dayByDate(t, w, "2026-06-11")
		if !d.External {
			t.Errorf("%s: unknowable cadenza load NOT marked External (silent undercount): %+v", name, d)
		}
	}
}

func TestBuildWeekContext_SourceFailuresReturnNil(t *testing.T) {
	if w := buildWeekContext(context.Background(),
		failingActivities{}, stubEvents{}, nil, "2026-06-11", "2026-06-10"); w != nil {
		t.Fatal("activities failure did not nil the context")
	}
	if w := buildWeekContext(context.Background(),
		stubActivities{}, stubEvents{err: errors.New("502")}, nil, "2026-06-11", "2026-06-10"); w != nil {
		t.Fatal("events failure did not nil the context")
	}
	if w := buildWeekContext(context.Background(),
		nil, stubEvents{}, nil, "2026-06-11", "2026-06-10"); w != nil {
		t.Fatal("nil activities source did not nil the context")
	}
}

type failingActivities struct{}

func (failingActivities) ActivitiesRange(context.Context, string, string) ([]icu.Activity, error) {
	return nil, errors.New("icu down")
}
