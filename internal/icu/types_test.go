// ABOUTME: Tests for typed intervals.icu models: null-vs-zero safety via pointer fields.
// ABOUTME: A missing HRV must decode as nil, never as 0 (research pitfall 8).

package icu

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWellnessDecode_PresentFields(t *testing.T) {
	raw := []byte(`{
		"id": "2026-06-10",
		"ctl": 41.3,
		"atl": 47.9,
		"rampRate": 3.2,
		"hrv": 68.0,
		"restingHR": 47,
		"sleepSecs": 26100,
		"sleepScore": 81.0,
		"weight": 71.4,
		"soreness": 2,
		"fatigue": 3,
		"stress": 1,
		"readiness": 76.5
	}`)
	var w Wellness
	if err := json.Unmarshal(raw, &w); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if w.ID != "2026-06-10" {
		t.Errorf("ID = %q, want 2026-06-10", w.ID)
	}
	floatChecks := []struct {
		name string
		got  *float64
		want float64
	}{
		{"ctl", w.CTL, 41.3},
		{"atl", w.ATL, 47.9},
		{"rampRate", w.RampRate, 3.2},
		{"hrv", w.HRV, 68.0},
		{"sleepScore", w.SleepScore, 81.0},
		{"weight", w.Weight, 71.4},
		{"readiness", w.Readiness, 76.5},
	}
	for _, c := range floatChecks {
		if c.got == nil {
			t.Errorf("%s = nil, want %v", c.name, c.want)
		} else if *c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, *c.got, c.want)
		}
	}
	intChecks := []struct {
		name string
		got  *int
		want int
	}{
		{"restingHR", w.RestingHR, 47},
		{"sleepSecs", w.SleepSecs, 26100},
		{"soreness", w.Soreness, 2},
		{"fatigue", w.Fatigue, 3},
		{"stress", w.Stress, 1},
	}
	for _, c := range intChecks {
		if c.got == nil {
			t.Errorf("%s = nil, want %d", c.name, c.want)
		} else if *c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, *c.got, c.want)
		}
	}
}

func TestWellnessDecode_AbsentFieldsAreNil(t *testing.T) {
	// intervals.icu omits null fields entirely; absence must be nil, not zero.
	raw := []byte(`{"id": "2026-06-09", "ctl": 41.0}`)
	var w Wellness
	if err := json.Unmarshal(raw, &w); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	nilFloats := map[string]*float64{
		"hrv": w.HRV, "rampRate": w.RampRate, "sleepScore": w.SleepScore,
		"weight": w.Weight, "readiness": w.Readiness, "atl": w.ATL,
	}
	for name, p := range nilFloats {
		if p != nil {
			t.Errorf("missing %s decoded as %v, want nil", name, *p)
		}
	}
	nilInts := map[string]*int{
		"restingHR": w.RestingHR, "sleepSecs": w.SleepSecs,
		"soreness": w.Soreness, "fatigue": w.Fatigue, "stress": w.Stress,
	}
	for name, p := range nilInts {
		if p != nil {
			t.Errorf("missing %s decoded as %v, want nil", name, *p)
		}
	}
}

func TestWellnessDecode_ExplicitNullVsZero(t *testing.T) {
	// Defensive: an explicit JSON null must stay nil, an explicit 0 must
	// survive as a pointer to 0. This is the exact contract the verdict
	// engine relies on to tell "not synced" from "measured zero".
	raw := []byte(`{"id": "2026-06-08", "hrv": null, "soreness": 0}`)
	var w Wellness
	if err := json.Unmarshal(raw, &w); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if w.HRV != nil {
		t.Errorf("explicit null hrv decoded as %v, want nil", *w.HRV)
	}
	if w.Soreness == nil {
		t.Error("explicit 0 soreness decoded as nil, want pointer to 0")
	} else if *w.Soreness != 0 {
		t.Errorf("soreness = %d, want 0", *w.Soreness)
	}
}

func TestDecodeWellnessRange(t *testing.T) {
	raw := json.RawMessage(`[
		{"id": "2026-06-09", "ctl": 41.0, "hrv": 65.5},
		{"id": "2026-06-10", "ctl": 41.3}
	]`)
	days, err := DecodeWellnessRange(raw)
	if err != nil {
		t.Fatalf("DecodeWellnessRange: %v", err)
	}
	if len(days) != 2 {
		t.Fatalf("len = %d, want 2", len(days))
	}
	if days[0].HRV == nil || *days[0].HRV != 65.5 {
		t.Errorf("day0 hrv = %v, want 65.5", days[0].HRV)
	}
	if days[1].HRV != nil {
		t.Errorf("day1 hrv = %v, want nil", *days[1].HRV)
	}
}

func TestDecodeWellnessRange_Malformed(t *testing.T) {
	cases := map[string]string{
		"object not array": `{"not":"an array"}`,
		"truncated":        `[{"id":"2026-06-09"`,
		"wrong field type": `[{"id":"2026-06-09","ctl":"high"}]`,
		"wrong element":    `["just-a-string"]`,
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeWellnessRange(json.RawMessage(payload)); err == nil {
				t.Errorf("DecodeWellnessRange(%s) = nil error, want decode failure", payload)
			}
		})
	}
}

func TestEventDecode(t *testing.T) {
	raw := []byte(`{
		"id": 12345,
		"start_date_local": "2026-06-12T00:00:00",
		"category": "WORKOUT",
		"name": "Easy run",
		"description": "- 40m Z2",
		"external_id": "cadenza-2026-06-12-abc123"
	}`)
	var e Event
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if e.ID != 12345 {
		t.Errorf("ID = %d, want 12345", e.ID)
	}
	if e.Category != "WORKOUT" {
		t.Errorf("Category = %q, want WORKOUT", e.Category)
	}
	if e.Name == nil || *e.Name != "Easy run" {
		t.Errorf("Name = %v, want 'Easy run'", e.Name)
	}
	if e.Description == nil || *e.Description != "- 40m Z2" {
		t.Errorf("Description = %v, want '- 40m Z2'", e.Description)
	}
	if e.ExternalID == nil || *e.ExternalID != "cadenza-2026-06-12-abc123" {
		t.Errorf("ExternalID = %v", e.ExternalID)
	}
}

func TestDecodeEvents(t *testing.T) {
	raw := json.RawMessage(`[
		{
			"id": 1,
			"start_date_local": "2026-06-12T00:00:00",
			"category": "WORKOUT",
			"name": "Intervals",
			"workout_doc": {"steps": [{"duration": 600}]}
		},
		{
			"id": 2,
			"start_date_local": "2026-06-13T00:00:00",
			"category": "NOTE"
		}
	]`)
	events, err := DecodeEvents(raw)
	if err != nil {
		t.Fatalf("DecodeEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("len = %d, want 2", len(events))
	}
	if events[0].WorkoutDoc == nil {
		t.Error("workout_doc = nil, want raw JSON passthrough")
	} else if !json.Valid(events[0].WorkoutDoc) {
		t.Errorf("workout_doc not valid JSON: %s", events[0].WorkoutDoc)
	}
	if events[1].Name != nil || events[1].Description != nil ||
		events[1].ExternalID != nil || events[1].WorkoutDoc != nil {
		t.Errorf("absent optionals must be nil: %+v", events[1])
	}
}

func TestDecodeEvents_Malformed(t *testing.T) {
	cases := map[string]string{
		"object not array": `{"id": 1}`,
		"wrong id type":    `[{"id": "abc"}]`,
		"truncated":        `[{"id": 1`,
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeEvents(json.RawMessage(payload)); err == nil {
				t.Errorf("DecodeEvents(%s) = nil error, want decode failure", payload)
			}
		})
	}
}

func TestDecodeActivities_RealisticPayload(t *testing.T) {
	// Activity ids are strings with an "i" prefix in the live API: the
	// coach's get_recent_activities tool died on int64 here (live bug).
	raw := json.RawMessage(`[{
		"id": "i86384921", "start_date_local": "2026-06-09T07:12:00",
		"type": "Run", "name": "Morning Run",
		"moving_time": 2700, "distance": 8400.5,
		"icu_training_load": 35, "average_heartrate": 142
	}, {
		"id": "i86384950", "start_date_local": "2026-06-10T18:00:00",
		"type": "Ride", "name": null,
		"moving_time": null, "distance": null,
		"icu_training_load": null, "average_heartrate": null
	}]`)
	acts, err := DecodeActivities(raw)
	if err != nil {
		t.Fatalf("DecodeActivities: %v", err)
	}
	if len(acts) != 2 || acts[0].ID != "i86384921" {
		t.Fatalf("acts = %+v", acts)
	}
	if acts[0].TrainingLoad == nil || *acts[0].TrainingLoad != 35 {
		t.Errorf("training load lost: %+v", acts[0])
	}
	if acts[1].Name != nil || acts[1].MovingTime != nil {
		t.Errorf("nulls must stay nil, not zero: %+v", acts[1])
	}
}

func TestWellness_FullSurfaceDecodeAndCompactMarshal(t *testing.T) {
	// D31: full read, compact write. Nulls must vanish from the marshaled
	// tool payload, not flood the model context as "field": null.
	raw := json.RawMessage(`[{
		"id": "2026-06-11", "hrv": 42, "restingHR": 59, "sleepSecs": 24480,
		"kcalConsumed": 2350, "protein": 132.5, "carbohydrates": 240.0,
		"fatTotal": 78.2, "hydration": 2.1, "bloodGlucose": 92.5,
		"steps": 8421, "vo2max": 52.3, "mood": 2, "comments": "gambe pesanti",
		"REMSleep": 1.4, "DeepSleep": 0.9, "respiration": 14.2,
		"tempWeight": 71.3,
		"weight": null, "spO2": null, "lactate": null
	}]`)
	days, err := DecodeWellnessRange(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	d := days[0]
	if d.KcalConsumed == nil || *d.KcalConsumed != 2350 {
		t.Errorf("kcal lost: %+v", d)
	}
	if d.BloodGlucose == nil || *d.BloodGlucose != 92.5 {
		t.Errorf("glucose lost")
	}
	if d.Protein == nil || *d.Protein != 132.5 || d.Carbohydrates == nil || d.FatTotal == nil {
		t.Errorf("macros lost: %+v", d)
	}
	if d.REMSleep == nil || d.TempWeight == nil {
		t.Errorf("sleep phases / interpolated weight lost")
	}
	if d.Comments == nil || *d.Comments != "gambe pesanti" {
		t.Errorf("athlete comments lost")
	}
	if d.Weight != nil || d.SpO2 != nil {
		t.Errorf("nulls must stay nil")
	}

	out, _ := json.Marshal(d)
	for _, forbidden := range []string{"weight", "spO2", "lactate", "null"} {
		if strings.Contains(string(out), forbidden) {
			t.Errorf("compact marshal leaked %q:\n%s", forbidden, out)
		}
	}
	for _, want := range []string{"kcalConsumed", "protein", "carbohydrates", "hydration", "comments"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("marshal missing %q", want)
		}
	}
}

func TestActivity_AnalyticsDecode(t *testing.T) {
	raw := json.RawMessage(`[{
		"id": "i1", "start_date_local": "2026-06-09T07:00:00", "type": "Run",
		"icu_intensity": 0.71, "icu_efficiency_factor": 1.42,
		"decoupling": 3.8, "icu_hr_zone_times": [1200, 1500, 300, 0, 0],
		"calories": 540
	}]`)
	acts, err := DecodeActivities(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	a := acts[0]
	if a.Intensity == nil || *a.Intensity != 0.71 || len(a.HRZoneTimes) != 5 {
		t.Errorf("analytics lost: %+v", a)
	}
	if a.HRZoneTimes[1] != 1500 {
		t.Errorf("zone times wrong: %v", a.HRZoneTimes)
	}
}
