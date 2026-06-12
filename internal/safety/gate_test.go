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
	d := Vet(p, goVerdict(), today, nil)
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
		"hard step":      {plan(today, step(12, 4)), "blocco duro continuo"},
		"hard total": {plan(today,
			step(10, 4), step(10, 1), step(10, 4), step(10, 1), step(10, 4),
			step(10, 1), step(10, 4), step(10, 1), step(10, 5)), "minuti duri totali"},
		"warmup zone": {plan(today,
			workout.Item{Step: &workout.Step{Minutes: 10, HR: workout.HRTarget{Zone: 3}, Intensity: "warmup"}},
			step(20, 2)), "zona warmup/cooldown"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			d := Vet(tc.p, goVerdict(), today, nil)
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
	d := Vet(plan(today, step(30, 2)), verdict.Verdict{Kind: verdict.Skip}, today, nil)
	if d.Action != Block {
		t.Fatalf("SKIP-day Z2 = %s, want BLOCK", d.Action)
	}

	v := verdict.Verdict{Kind: verdict.Modify, Caps: verdict.Caps{MaxZone: 2, MaxMinutes: 60}}
	d = Vet(plan(today, step(30, 3)), v, today, nil)
	if d.Action != Reject {
		t.Fatalf("zone over verdict cap = %s, want REJECT", d.Action)
	}
	d = Vet(plan(today, step(90, 2)), v, today, nil)
	if d.Action != Reject {
		t.Fatalf("duration over verdict cap = %s, want REJECT", d.Action)
	}
	d = Vet(plan(today, step(45, 2)), v, today, nil)
	if d.Action != Pass {
		t.Fatalf("within caps = %s, want PASS (%+v)", d.Action, d.Violations)
	}
}

func TestVet_WriteWindow(t *testing.T) {
	if d := Vet(plan("2026-06-19", step(30, 2)), goVerdict(), today, nil); d.Action != Block {
		t.Errorf("past date = %s, want BLOCK", d.Action)
	}
	if d := Vet(plan("2026-07-05", step(30, 2)), goVerdict(), today, nil); d.Action != Block {
		t.Errorf("past window = %s, want BLOCK", d.Action)
	}
	if d := Vet(plan("2026-07-04", step(30, 2)), goVerdict(), today, nil); d.Action != Pass {
		t.Errorf("window edge = %s, want PASS", d.Action)
	}
}

func TestVet_InvalidSchemaRejects(t *testing.T) {
	p := plan(today) // no items
	if d := Vet(p, goVerdict(), today, nil); d.Action != Reject {
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
	d := Vet(plan(today, step(120, 3), step(120, 3)), goVerdict(), today, nil)
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

func TestVet_StepSplittingCannotDefeatHardCeiling(t *testing.T) {
	// 4 consecutive 10' Z5 steps = 40 continuous hard minutes: each passes
	// the per-step check alone, the COALESCED run must not.
	d := Vet(plan(today, step(10, 5), step(10, 5), step(10, 5), step(10, 5)), goVerdict(), today, nil)
	if d.Action != Reject {
		t.Fatalf("Action = %s, want REJECT (split steps = continuous hard block)", d.Action)
	}
	found := false
	for _, v := range d.Violations {
		if strings.Contains(v.Bound, "blocco duro continuo") {
			found = true
		}
	}
	if !found {
		t.Errorf("coalesced hard-run violation missing: %+v", d.Violations)
	}
}

func TestVet_RepeatExpansionFeedsTheGate(t *testing.T) {
	// 12x(10' Z4 + 2' Z1): per-step fine, recovery breaks the runs, but the
	// TOTAL hard time (120') must trip. A Flatten regression would hide it.
	p := plan(today, workout.Item{Repeat: &workout.Repeat{Count: 12, Steps: []workout.Step{
		{Minutes: 10, HR: workout.HRTarget{Zone: 4}},
		{Minutes: 2, HR: workout.HRTarget{Zone: 1}},
	}}})
	d := Vet(p, goVerdict(), today, nil)
	if d.Action != Reject {
		t.Fatalf("Action = %s, want REJECT", d.Action)
	}
	found := false
	for _, v := range d.Violations {
		if strings.Contains(v.Bound, "minuti duri totali") {
			found = true
		}
	}
	if !found {
		t.Errorf("hard-total violation missing: %+v", d.Violations)
	}
}

func TestVet_BlockDominatesReject(t *testing.T) {
	// SKIP-day BLOCK plus a Tier A violation in the same plan: the verdict
	// must stay BLOCK (decision 13: never invite a regen on a SKIP day) and
	// report BOTH problems.
	d := Vet(plan(today, step(12, 4)), verdict.Verdict{Kind: verdict.Skip}, today, nil)
	if d.Action != Block {
		t.Fatalf("Action = %s, want BLOCK to dominate", d.Action)
	}
	if len(d.Violations) < 2 {
		t.Fatalf("violations = %+v, want both the SKIP block and the hard-step reject", d.Violations)
	}
}

func TestVet_CooldownZoneBound(t *testing.T) {
	p := plan(today,
		step(20, 2),
		workout.Item{Step: &workout.Step{Minutes: 10, HR: workout.HRTarget{Zone: 3}, Intensity: "cooldown"}},
	)
	d := Vet(p, goVerdict(), today, nil)
	if d.Action != Reject {
		t.Fatalf("Z3 cooldown = %s, want REJECT", d.Action)
	}
}

func TestVet_EqualityBoundariesPass(t *testing.T) {
	// Exactly AT the limit must pass: the gate rejects beyond, not at.
	cases := map[string]workout.Plan{
		"hard step exactly 10m": plan(today, step(10, 4)),
		"total exactly 300m":    plan(today, step(150, 1), step(150, 1)),
		"hard total exactly 40m": plan(today,
			step(10, 4), step(5, 1), step(10, 4), step(5, 1),
			step(10, 4), step(5, 1), step(10, 4)),
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			d := Vet(p, goVerdict(), today, nil)
			if d.Action != Pass {
				t.Fatalf("%s = %s (%+v), want PASS", name, d.Action, d.Violations)
			}
		})
	}
}

func TestVet_MultiViolationAccumulatesAcrossRules(t *testing.T) {
	// One monster plan: continuous hard block + hard total + TSS must all
	// be reported in a single decision (targeted regen needs the full list).
	// 60'Z4+60'Z4+120'Z3: continuous hard 120m, hard total 120m, TSS 318.
	p := plan(today, step(60, 4), step(60, 4), step(120, 3))
	d := Vet(p, goVerdict(), today, nil)
	if d.Action != Reject {
		t.Fatalf("Action = %s", d.Action)
	}
	bounds := map[string]bool{}
	for _, v := range d.Violations {
		bounds[v.Bound] = true
	}
	for _, want := range []string{"blocco duro continuo", "minuti duri totali", "TSS stimato"} {
		if !bounds[want] {
			t.Errorf("bound %q missing from %v", want, bounds)
		}
	}
}

func TestVet_FutureDateExemptFromTodayVerdict(t *testing.T) {
	// D29: today's SKIP must not block planning an easy week for Tuesday;
	// Tier A still applies to any date.
	skip := verdict.Verdict{Kind: verdict.Skip, Caps: verdict.Caps{MaxZone: 1, MaxMinutes: 45}}
	if d := Vet(plan("2026-06-24", step(40, 2)), skip, today, nil); d.Action != Pass {
		t.Fatalf("future Z2 on SKIP day = %s (%+v), want PASS (D29)", d.Action, d.Violations)
	}
	if d := Vet(plan("2026-06-24", step(120, 5)), skip, today, nil); d.Action != Reject {
		t.Fatalf("future Tier A violation = %s, want REJECT regardless of date", d.Action)
	}
	if d := Vet(plan(today, step(40, 2)), skip, today, nil); d.Action != Block {
		t.Fatalf("today on SKIP = %s, want BLOCK", d.Action)
	}
}

func FuzzVet_NeverPassesTierAViolation(f *testing.F) {
	f.Add(10, 4, 3, 2, 60, 2)
	f.Add(180, 5, 12, 4, 1, 1)
	f.Fuzz(func(t *testing.T, m1, z1, reps, m2, m3, z3 int) {
		clamp := func(v, lo, hi int) int {
			if v < lo {
				return lo
			}
			if v > hi {
				return hi
			}
			return v
		}
		p := plan(today,
			step(clamp(m1, 1, 180), clamp(z1, 1, 5)),
			workout.Item{Repeat: &workout.Repeat{Count: clamp(reps, 2, 12), Steps: []workout.Step{
				{Minutes: clamp(m2, 1, 180), HR: workout.HRTarget{Zone: clamp(z3, 1, 5)}},
			}}},
			step(clamp(m3, 1, 180), 1),
		)
		d := Vet(p, goVerdict(), today, nil)
		if d.Action != Pass {
			return
		}
		// Oracle: a PASS must satisfy every Tier A bound recomputed here.
		total, hard, run := 0, 0, 0
		for _, s := range p.Flatten() {
			sec := s.DurationSeconds()
			total += sec
			z := s.HR.Zone
			if z >= 4 {
				hard += sec
				run += sec
				if run > 10*60 {
					t.Fatalf("PASS with continuous hard run %ds: %+v", run, p)
				}
			} else {
				run = 0
			}
		}
		if total > 300*60 {
			t.Fatalf("PASS with total %ds", total)
		}
		if hard > 40*60 {
			t.Fatalf("PASS with hard total %ds", hard)
		}
		if EstimateTSS(p) > 250 {
			t.Fatalf("PASS with TSS %.1f", EstimateTSS(p))
		}
	})
}

func TestVetWeek_CumulativeRules(t *testing.T) {
	hardDay := func(date string) DayLoad {
		return DayLoad{Date: date, TSS: 80, HardSecs: 20 * 60, Planned: true}
	}
	easyDay := func(date string, tss float64) DayLoad {
		return DayLoad{Date: date, TSS: tss}
	}
	hardPlan := plan("2026-06-25", step(10, 4), step(30, 2)) // 10' hard = hard day
	easyPlan := plan("2026-06-25", step(40, 2))

	t.Run("nil week skips cumulative, per-workout still applies", func(t *testing.T) {
		if d := Vet(easyPlan, goVerdict(), today, nil); d.Action != Pass {
			t.Fatalf("Action = %s", d.Action)
		}
	})

	t.Run("consecutive hard days rejected (the review scenario)", func(t *testing.T) {
		week := &WeekContext{Days: []DayLoad{hardDay("2026-06-24")}}
		d := Vet(hardPlan, goVerdict(), today, week)
		if d.Action != Reject {
			t.Fatalf("Action = %s, want REJECT", d.Action)
		}
		found := false
		for _, v := range d.Violations {
			if strings.Contains(v.Bound, "consecutivi") {
				found = true
			}
		}
		if !found {
			t.Fatalf("violations = %+v", d.Violations)
		}
		// Next-day adjacency too.
		week2 := &WeekContext{Days: []DayLoad{hardDay("2026-06-26")}}
		if d := Vet(hardPlan, goVerdict(), today, week2); d.Action != Reject {
			t.Fatal("hard day AFTER the plan not caught")
		}
		// Easy plan next to a hard day is fine.
		if d := Vet(easyPlan, goVerdict(), today, week); d.Action != Pass {
			t.Fatalf("easy next to hard = %s (%+v)", d.Action, d.Violations)
		}
	})

	t.Run("fourth hard day in rolling 7 rejected", func(t *testing.T) {
		week := &WeekContext{Days: []DayLoad{
			hardDay("2026-06-19"), hardDay("2026-06-21"), hardDay("2026-06-23"),
		}}
		d := Vet(hardPlan, goVerdict(), today, week)
		found := false
		for _, v := range d.Violations {
			if strings.Contains(v.Bound, "giorni duri nella settimana") {
				found = true
			}
		}
		if d.Action != Reject || !found {
			t.Fatalf("4th hard day passed: %s %+v", d.Action, d.Violations)
		}
	})

	t.Run("weekly TSS cumulates planned and executed", func(t *testing.T) {
		week := &WeekContext{Days: []DayLoad{
			easyDay("2026-06-20", 200), easyDay("2026-06-22", 200), easyDay("2026-06-24", 180),
		}}
		d := Vet(easyPlan, goVerdict(), today, week) // ~29 TSS pushes past 600
		found := false
		for _, v := range d.Violations {
			if strings.Contains(v.Bound, "TSS settimanale") {
				found = true
			}
		}
		if d.Action != Reject || !found {
			t.Fatalf("weekly TSS overload passed: %s %+v", d.Action, d.Violations)
		}
		// Outside the window it does not count.
		old := &WeekContext{Days: []DayLoad{easyDay("2026-06-10", 580)}}
		if d := Vet(easyPlan, goVerdict(), today, old); d.Action != Pass {
			t.Fatalf("stale load counted: %s", d.Action)
		}
	})

	t.Run("same-day external event rejects, own slot replaces", func(t *testing.T) {
		ext := &WeekContext{Days: []DayLoad{{Date: "2026-06-25", Planned: true, External: true}}}
		if d := Vet(easyPlan, goVerdict(), today, ext); d.Action != Reject {
			t.Fatal("stacking on athlete's own event allowed")
		}
		ours := &WeekContext{Days: []DayLoad{{Date: "2026-06-25", Planned: true, External: false, TSS: 50}}}
		if d := Vet(easyPlan, goVerdict(), today, ours); d.Action != Pass {
			t.Fatalf("own slot replacement rejected: %+v", d.Violations)
		}
	})

	t.Run("same-day own load excluded from weekly sum (upsert replaces)", func(t *testing.T) {
		week := &WeekContext{Days: []DayLoad{
			{Date: "2026-06-25", TSS: 590, Planned: true}, // our own huge plan being replaced
		}}
		if d := Vet(easyPlan, goVerdict(), today, week); d.Action != Pass {
			t.Fatalf("replaced plan double-counted: %s %+v", d.Action, d.Violations)
		}
	})
}
