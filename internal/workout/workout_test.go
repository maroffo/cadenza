// ABOUTME: Tests for the workout schema: trap unrepresentability, validation, doc shape.
// ABOUTME: The doc goldens pin the exact wire bytes the live spike verified.

package workout

import (
	"encoding/json"
	"strings"
	"testing"
)

func validPlan() Plan {
	return Plan{
		Date: "2026-06-20", Sport: SportRun, Title: "Intervalli corti",
		Items: []Item{
			{Step: &Step{Minutes: 10, HR: HRTarget{ZoneStart: 1, ZoneEnd: 2}, Intensity: "warmup"}},
			{Repeat: &Repeat{Count: 3, Steps: []Step{
				{Minutes: 4, HR: HRTarget{Zone: 4}},
				{Minutes: 2, HR: HRTarget{Zone: 1}, Intensity: "recovery"},
			}}},
			{Step: &Step{Minutes: 10, HR: HRTarget{Zone: 1}, Intensity: "cooldown"}},
		},
	}
}

func TestValidate_HappyPath(t *testing.T) {
	if err := validPlan().Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidate_RejectionTable(t *testing.T) {
	cases := map[string]func(*Plan){
		"bad date":          func(p *Plan) { p.Date = "20-06-2026" },
		"bad sport":         func(p *Plan) { p.Sport = "Swim" },
		"empty title":       func(p *Plan) { p.Title = "" },
		"no items":          func(p *Plan) { p.Items = nil },
		"zero duration":     func(p *Plan) { p.Items[0].Step.Minutes = 0; p.Items[0].Step.Seconds = 0 },
		"bad seconds":       func(p *Plan) { p.Items[0].Step.Seconds = 20 },
		"zone out of range": func(p *Plan) { p.Items[2].Step.HR = HRTarget{Zone: 6} },
		"inverted range":    func(p *Plan) { p.Items[0].Step.HR = HRTarget{ZoneStart: 3, ZoneEnd: 2} },
		"no hr target":      func(p *Plan) { p.Items[2].Step.HR = HRTarget{} },
		"warmup not first":  func(p *Plan) { p.Items[2].Step.Intensity = "warmup" },
		"cooldown not last": func(p *Plan) { p.Items[0].Step.Intensity = "cooldown" },
		"repeat count 1":    func(p *Plan) { p.Items[1].Repeat.Count = 1 },
		"warmup in repeat":  func(p *Plan) { p.Items[1].Repeat.Steps[0].Intensity = "warmup" },
		"both step and rep": func(p *Plan) {
			p.Items[0].Repeat = &Repeat{Count: 2, Steps: []Step{{Minutes: 1, HR: HRTarget{Zone: 1}}}}
		},
		"neither step nor rep": func(p *Plan) { p.Items[0] = Item{} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := validPlan()
			mutate(&p)
			if err := p.Validate(); err == nil {
				t.Errorf("accepted invalid plan (%s)", name)
			}
		})
	}
}

func TestUnmarshal_NestedRepeatImpossible(t *testing.T) {
	// A repeat inside a repeat decodes its inner items as plain steps and
	// then FAILS validation: nesting cannot survive the type system.
	raw := `{"date":"2026-06-20","sport":"Run","title":"x","items":[
		{"repeat":2,"steps":[{"repeat":2,"steps":[]}]}
	]}`
	var p Plan
	if err := json.Unmarshal([]byte(raw), &p); err == nil {
		if err := p.Validate(); err == nil {
			t.Fatal("nested repeat survived unmarshal+validate")
		}
	}
}

func TestFlattenAndTotals(t *testing.T) {
	p := validPlan()
	flat := p.Flatten()
	if len(flat) != 8 { // warmup + 3x(2) + cooldown
		t.Fatalf("flattened steps = %d, want 8", len(flat))
	}
	// 600 + 3*(240+120) + 600 = 2280: same total the live DSL spike produced.
	if got := p.TotalSeconds(); got != 2280 {
		t.Fatalf("TotalSeconds = %d, want 2280", got)
	}
}

func TestBuildDoc_WireShape(t *testing.T) {
	doc, err := validPlan().BuildDoc()
	if err != nil {
		t.Fatalf("BuildDoc: %v", err)
	}
	var parsed struct {
		Steps []map[string]any `json:"steps"`
	}
	if err := json.Unmarshal(doc, &parsed); err != nil {
		t.Fatalf("doc not valid JSON: %v", err)
	}
	if len(parsed.Steps) != 3 {
		t.Fatalf("top steps = %d, want 3", len(parsed.Steps))
	}
	raw := string(doc)
	for _, want := range []string{
		`"warmup":true`, `"cooldown":true`,
		`"units":"hr_zone"`, `"reps":3`, `"text":"3x"`,
		`"duration":600`, `"duration":240`, `"duration":120`,
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("doc missing %q:\n%s", want, raw)
		}
	}
	// The traps stay unrepresentable at the wire too: no DSL text, no
	// distance, no description.
	for _, forbidden := range []string{`"description"`, `"distance"`, "400m"} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("doc contains forbidden %q", forbidden)
		}
	}
}

func TestBuildDoc_RangeTarget(t *testing.T) {
	p := Plan{
		Date: "2026-06-20", Sport: SportRun, Title: "easy",
		Items: []Item{{Step: &Step{Minutes: 40, HR: HRTarget{ZoneStart: 1, ZoneEnd: 2}}}},
	}
	doc, err := p.BuildDoc()
	if err != nil {
		t.Fatalf("BuildDoc: %v", err)
	}
	if !strings.Contains(string(doc), `"start":1`) || !strings.Contains(string(doc), `"end":2`) {
		t.Errorf("range target malformed:\n%s", doc)
	}
}

func TestBuildDoc_InvalidPlanRefused(t *testing.T) {
	p := validPlan()
	p.Items[0].Step.HR = HRTarget{}
	if _, err := p.BuildDoc(); err == nil {
		t.Fatal("BuildDoc accepted an invalid plan")
	}
}

func TestToolSchema_IsValidJSON(t *testing.T) {
	var v any
	if err := json.Unmarshal([]byte(ToolSchema), &v); err != nil {
		t.Fatalf("ToolSchema not valid JSON: %v", err)
	}
}
