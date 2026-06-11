// ABOUTME: Gate matrix tests: every Tier A ceiling, verdict coupling, TSS arithmetic.
// ABOUTME: The gate verifies sanity; these tests verify the gate, bound by bound.

package safety

import (
	"math"
	"strings"
	"testing"

	"github.com/maroffo/cadenza/internal/verdict"
	"github.com/maroffo/cadenza/internal/workout"
)

const today = "2026-06-20"

func goVerdict() verdict.Verdict { return verdict.Verdict{Kind: verdict.Go} }

type Item = workout.Item

func plan(date string, items ...workout.Item) workout.Plan {
	return workout.Plan{Date: date, Sport: workout.SportRun, Title: "t", Items: items}
}

func step(min, zone int) workout.Item {
	return workout.Item{Step: &workout.Step{Minutes: min, HR: workout.HRTarget{Zone: zone}}}
}

func TestVet_CleanPlanPasses(t *testing.T) {
	p := plan(today,
		workout.Item{Step: &workout.Step{Minutes: 10, HR: workout.HRTarget{ZoneStart: 1, ZoneEnd: 2}, Intensity: "warmup"}},
		step(40, 2),
		workout.Item{Step: &workout.Step{Minutes: 10, HR: workout.HRTarget{Zone: 1}, Intensity: "cooldown"}},
	)
	d := Vet(p, goVerdict(), today)
	if d.Action != Pass || len(d.Violations) != 0 {
		t.Fatalf("decision = %+v, want clean PASS", d)
	}
}

func TestVet_TierACeilings(t *testing.T) {
	cases := map[string]struct {
		p     workout.Plan
		bound string
	}{
		"total duration": {plan(today, step(180, 2), step(180, 2)), "durata totale"},
		"hard step":      {plan(today, step(12, 4)), "singolo step duro"},
		"hard total": {plan(today,
			step(10, 4), step(10, 1), step(10, 4), step(10, 1), step(10, 4),
			step(10, 1), step(10, 4), step(10, 1), step(10, 5)), "minuti duri totali"},
		"warmup zone": {plan(today,
			workout.Item{Step: &workout.Step{Minutes: 10, HR: workout.HRTarget{Zone: 3}, Intensity: "warmup"}},
			step(20, 2)), "zona warmup/cooldown"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			d := Vet(tc.p, goVerdict(), today)
			if d.Action != Reject {
				t.Fatalf("Action = %s, want REJECT (%+v)", d.Action, d.Violations)
			}
			found := false
			for _, v := range d.Violations {
				if strings.Contains(v.Bound, tc.bound) {
					found = true
				}
			}
			if !found {
				t.Errorf("bound %q not in violations: %+v", tc.bound, d.Violations)
			}
		})
	}
}

func TestVet_VerdictCoupling(t *testing.T) {
	// SKIP day: anything above Z1 is BLOCK, not merely reject: the model
	// cannot out-prescribe the deterministic verdict.
	d := Vet(plan(today, step(30, 2)), verdict.Verdict{Kind: verdict.Skip}, today)
	if d.Action != Block {
		t.Fatalf("SKIP-day Z2 = %s, want BLOCK", d.Action)
	}

	v := verdict.Verdict{Kind: verdict.Modify, Caps: verdict.Caps{MaxZone: 2, MaxMinutes: 60}}
	d = Vet(plan(today, step(30, 3)), v, today)
	if d.Action != Reject {
		t.Fatalf("zone over verdict cap = %s, want REJECT", d.Action)
	}
	d = Vet(plan(today, step(90, 2)), v, today)
	if d.Action != Reject {
		t.Fatalf("duration over verdict cap = %s, want REJECT", d.Action)
	}
	d = Vet(plan(today, step(45, 2)), v, today)
	if d.Action != Pass {
		t.Fatalf("within caps = %s, want PASS (%+v)", d.Action, d.Violations)
	}
}

func TestVet_WriteWindow(t *testing.T) {
	if d := Vet(plan("2026-06-19", step(30, 2)), goVerdict(), today); d.Action != Block {
		t.Errorf("past date = %s, want BLOCK", d.Action)
	}
	if d := Vet(plan("2026-07-05", step(30, 2)), goVerdict(), today); d.Action != Block {
		t.Errorf("past window = %s, want BLOCK", d.Action)
	}
	if d := Vet(plan("2026-07-04", step(30, 2)), goVerdict(), today); d.Action != Pass {
		t.Errorf("window edge = %s, want PASS", d.Action)
	}
}

func TestVet_InvalidSchemaRejects(t *testing.T) {
	p := plan(today) // no items
	if d := Vet(p, goVerdict(), today); d.Action != Reject {
		t.Fatalf("invalid plan = %s, want REJECT", d.Action)
	}
}

func TestEstimateTSS_HandComputed(t *testing.T) {
	// 60' Z2: 1h * 0.72^2 * 100 = 51.84
	got := EstimateTSS(plan(today, step(60, 2)))
	if math.Abs(got-51.84) > 1e-9 {
		t.Fatalf("TSS = %v, want 51.84", got)
	}
	// Range targets use the UPPER zone (gate errs toward less load).
	p := plan(today, workout.Item{Step: &workout.Step{Minutes: 60, HR: workout.HRTarget{ZoneStart: 1, ZoneEnd: 3}}})
	got = EstimateTSS(p)
	if math.Abs(got-68.89) > 0.01 { // 0.83^2 * 100
		t.Fatalf("range TSS = %v, want ~68.89 (upper zone)", got)
	}
}

func TestVet_TSSCeiling(t *testing.T) {
	// 5h at Z5 would smash TSS, but duration trips first; use 4h Z3:
	// 4 * 0.83^2 * 100 = 275.6 > 250 while duration 240m < 300m.
	d := Vet(plan(today, step(120, 3), step(120, 3)), goVerdict(), today)
	if d.Action != Reject {
		t.Fatalf("Action = %s, want REJECT for TSS", d.Action)
	}
	found := false
	for _, v := range d.Violations {
		if strings.Contains(v.Bound, "TSS") {
			found = true
		}
	}
	if !found {
		t.Errorf("TSS violation missing: %+v", d.Violations)
	}
}
