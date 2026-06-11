// ABOUTME: Scenario tests for the deterministic go/modify/skip verdict engine.
// ABOUTME: Named scenarios from the plan; the anti-overreach case (green HRV, ramp over cap) is the core.

package verdict

import (
	"strings"
	"testing"
)

func f(v float64) *float64 { return &v }
func i(v int) *int         { return &v }

var baselines = Baselines{HRVMean: 68, HRVSD: 6, RestingHR: 47}

// day builds a wellness day with everything healthy unless overridden.
func greenDay(date string) Day {
	return Day{
		Date:      date,
		HRV:       f(69),
		RestingHR: i(47),
		SleepSecs: i(7 * 3600),
		RampRate:  f(2.0),
	}
}

func TestCompute_Scenarios(t *testing.T) {
	cases := []struct {
		name      string
		input     Input
		wantKind  Kind
		wantRules []string // RuleIDs that must fire
		wantGaps  int
	}{
		{
			name: "all_green",
			input: Input{
				Today:     greenDay("2026-06-10"),
				Window:    []Day{greenDay("2026-06-08"), greenDay("2026-06-09")},
				Baselines: baselines,
				RampCap:   4.0,
			},
			wantKind: Go,
		},
		{
			name: "hrv_low_single_day",
			input: Input{
				Today: func() Day {
					d := greenDay("2026-06-10")
					d.HRV = f(58) // 68 - 0.75*6 = 63.5 threshold
					return d
				}(),
				Window:    []Day{greenDay("2026-06-08"), greenDay("2026-06-09")},
				Baselines: baselines,
				RampCap:   4.0,
			},
			wantKind:  Modify,
			wantRules: []string{"hrv_low"},
		},
		{
			name: "hrv_degraded_3d",
			input: Input{
				Today: func() Day {
					d := greenDay("2026-06-10")
					d.HRV = f(58)
					return d
				}(),
				Window: []Day{
					func() Day { d := greenDay("2026-06-08"); d.HRV = f(60); return d }(),
					func() Day { d := greenDay("2026-06-09"); d.HRV = f(59); return d }(),
				},
				Baselines: baselines,
				RampCap:   4.0,
			},
			wantKind:  Skip,
			wantRules: []string{"hrv_low_3d"},
		},
		{
			// THE anti-overreach case: autonomic data green, structural load too hot.
			name: "green_hrv_ramp_over_cap",
			input: Input{
				Today: func() Day {
					d := greenDay("2026-06-10")
					d.HRV = f(75) // record HRV
					d.RampRate = f(5.1)
					return d
				}(),
				Window:    []Day{greenDay("2026-06-08"), greenDay("2026-06-09")},
				Baselines: baselines,
				RampCap:   4.0,
			},
			wantKind:  Modify,
			wantRules: []string{"ramp_over_cap"},
		},
		{
			name: "missing_hrv",
			input: Input{
				Today: func() Day {
					d := greenDay("2026-06-10")
					d.HRV = nil
					return d
				}(),
				Window:    []Day{greenDay("2026-06-08"), greenDay("2026-06-09")},
				Baselines: baselines,
				RampCap:   4.0,
			},
			wantKind:  Modify,
			wantRules: []string{"missing_data"},
			wantGaps:  1,
		},
		{
			name: "resting_hr_elevated",
			input: Input{
				Today: func() Day {
					d := greenDay("2026-06-10")
					d.RestingHR = i(52) // +5
					return d
				}(),
				Window:    []Day{greenDay("2026-06-08"), greenDay("2026-06-09")},
				Baselines: baselines,
				RampCap:   4.0,
			},
			wantKind:  Modify,
			wantRules: []string{"resting_hr_elevated"},
		},
		{
			name: "resting_hr_high",
			input: Input{
				Today: func() Day {
					d := greenDay("2026-06-10")
					d.RestingHR = i(55) // +8
					return d
				}(),
				Window:    []Day{greenDay("2026-06-08"), greenDay("2026-06-09")},
				Baselines: baselines,
				RampCap:   4.0,
			},
			wantKind:  Skip,
			wantRules: []string{"resting_hr_high"},
		},
		{
			name: "short_sleep",
			input: Input{
				Today: func() Day {
					d := greenDay("2026-06-10")
					d.SleepSecs = i(5 * 3600)
					return d
				}(),
				Window:    []Day{greenDay("2026-06-08"), greenDay("2026-06-09")},
				Baselines: baselines,
				RampCap:   4.0,
			},
			wantKind:  Modify,
			wantRules: []string{"short_sleep"},
		},
		{
			name: "injury_active_pain",
			input: Input{
				Today:     greenDay("2026-06-10"),
				Window:    []Day{greenDay("2026-06-08"), greenDay("2026-06-09")},
				Baselines: baselines,
				RampCap:   4.0,
				Injuries:  []ActiveInjury{{BodyPart: "knee", Pain: 5}},
			},
			wantKind:  Skip,
			wantRules: []string{"injury_active"},
		},
		{
			// Multiple rules fire; SKIP wins and ALL reasons are reported.
			name: "skip_beats_modify_reasons_accumulate",
			input: Input{
				Today: func() Day {
					d := greenDay("2026-06-10")
					d.HRV = f(58)
					d.RampRate = f(5.5)
					return d
				}(),
				Window: []Day{
					func() Day { d := greenDay("2026-06-08"); d.HRV = f(60); return d }(),
					func() Day { d := greenDay("2026-06-09"); d.HRV = f(59); return d }(),
				},
				Baselines: baselines,
				RampCap:   4.0,
			},
			wantKind:  Skip,
			wantRules: []string{"hrv_low_3d", "ramp_over_cap"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := Compute(tc.input, DefaultRules())
			if v.Kind != tc.wantKind {
				t.Errorf("Kind = %s, want %s (reasons: %+v)", v.Kind, tc.wantKind, v.Reasons)
			}
			for _, want := range tc.wantRules {
				found := false
				for _, r := range v.Reasons {
					if r.RuleID == want {
						found = true
						if r.Observed == "" || r.Threshold == "" {
							t.Errorf("rule %s missing observed/threshold (self-check requires both): %+v", want, r)
						}
					}
				}
				if !found {
					t.Errorf("rule %s did not fire; fired: %+v", want, v.Reasons)
				}
			}
			if len(v.DataGaps) < tc.wantGaps {
				t.Errorf("DataGaps = %v, want at least %d", v.DataGaps, tc.wantGaps)
			}
			if v.Kind == Go && len(v.Reasons) > 0 {
				t.Errorf("GO verdict must have no fired rules, got %+v", v.Reasons)
			}
		})
	}
}

func TestCompute_CapsAreRestrictive(t *testing.T) {
	in := Input{
		Today: func() Day {
			d := greenDay("2026-06-10")
			d.HRV = f(58)
			return d
		}(),
		Window:    []Day{greenDay("2026-06-08"), greenDay("2026-06-09")},
		Baselines: baselines,
		RampCap:   4.0,
	}
	v := Compute(in, DefaultRules())
	if v.Kind != Modify {
		t.Fatalf("Kind = %s, want MODIFY", v.Kind)
	}
	if v.Caps.MaxZone == 0 || v.Caps.MaxZone > 2 {
		t.Errorf("MaxZone = %d, want capped at Z2 for hrv_low", v.Caps.MaxZone)
	}
	if v.Caps.MaxMinutes == 0 {
		t.Error("MaxMinutes = 0 (uncapped), want a cap on a MODIFY day")
	}
}

func TestCompute_RampCapClampedToTierA(t *testing.T) {
	// A tunable ramp cap above the Tier A ceiling (6.0) must be clamped:
	// the data layer can only tighten safety bounds, never loosen them.
	in := Input{
		Today: func() Day {
			d := greenDay("2026-06-10")
			d.RampRate = f(7.0)
			return d
		}(),
		Window:    []Day{greenDay("2026-06-08"), greenDay("2026-06-09")},
		Baselines: baselines,
		RampCap:   12.0, // hostile tunable
	}
	v := Compute(in, DefaultRules())
	fired := false
	for _, r := range v.Reasons {
		if r.RuleID == "ramp_over_cap" {
			fired = true
		}
	}
	if !fired {
		t.Error("ramp 7.0 with hostile cap 12.0 must still fire ramp_over_cap (Tier A ceiling 6.0)")
	}
}

func TestRenderBlock(t *testing.T) {
	in := Input{
		Today: func() Day {
			d := greenDay("2026-06-10")
			d.HRV = f(58)
			return d
		}(),
		Window:    []Day{greenDay("2026-06-08"), greenDay("2026-06-09")},
		Baselines: baselines,
		RampCap:   4.0,
	}
	v := Compute(in, DefaultRules())
	block := RenderBlock(v)

	if !strings.Contains(block, "MODIFY") {
		t.Errorf("block missing verdict kind:\n%s", block)
	}
	if !strings.Contains(block, "<b>") {
		t.Errorf("block must use Telegram HTML mode:\n%s", block)
	}
	// Self-check contract: observed and threshold visible to the athlete.
	if !strings.Contains(block, "58") {
		t.Errorf("block missing observed value:\n%s", block)
	}
	for _, forbidden := range []string{"<table", "<div", "<span"} {
		if strings.Contains(block, forbidden) {
			t.Errorf("unsupported HTML tag %s in Telegram block:\n%s", forbidden, block)
		}
	}
}

func TestRenderBlock_GoIsCompact(t *testing.T) {
	in := Input{
		Today:     greenDay("2026-06-10"),
		Window:    []Day{greenDay("2026-06-08"), greenDay("2026-06-09")},
		Baselines: baselines,
		RampCap:   4.0,
	}
	v := Compute(in, DefaultRules())
	block := RenderBlock(v)
	if !strings.Contains(block, "GO") {
		t.Errorf("block missing GO:\n%s", block)
	}
	if len(block) > 400 {
		t.Errorf("GO block should be compact, got %d chars:\n%s", len(block), block)
	}
}

func TestCompute_ThresholdBoundariesDoNotFire(t *testing.T) {
	// Exact-threshold values must NOT fire (< and > comparisons, not <=/>=).
	d := greenDay("2026-06-10")
	d.HRV = f(63.5)           // exactly mean - 0.75*SD
	d.SleepSecs = i(6 * 3600) // exactly the minimum
	d.RampRate = f(4.0)       // exactly the cap
	v := Compute(Input{
		Today:     d,
		Window:    []Day{greenDay("2026-06-08"), greenDay("2026-06-09")},
		Baselines: baselines,
		RampCap:   4.0,
	}, DefaultRules())
	if v.Kind != Go {
		t.Errorf("Kind = %s with boundary values, want GO (reasons %+v)", v.Kind, v.Reasons)
	}
}

func TestCompute_ZeroRampCapClampsToTierA(t *testing.T) {
	// Unseeded cap (0) must default to the Tier A ceiling, not disable the rule.
	d := greenDay("2026-06-10")
	d.RampRate = f(6.5)
	v := Compute(Input{
		Today: d, Window: []Day{greenDay("2026-06-09")},
		Baselines: baselines, RampCap: 0,
	}, DefaultRules())
	fired := false
	for _, r := range v.Reasons {
		if r.RuleID == "ramp_over_cap" {
			fired = true
		}
	}
	if !fired {
		t.Error("ramp 6.5 with cap 0 must fire against the Tier A ceiling 6.0")
	}

	d.RampRate = f(5.5)
	v = Compute(Input{
		Today: d, Window: []Day{greenDay("2026-06-09")},
		Baselines: baselines, RampCap: 0,
	}, DefaultRules())
	for _, r := range v.Reasons {
		if r.RuleID == "ramp_over_cap" {
			t.Error("ramp 5.5 under the Tier A ceiling must not fire with cap 0")
		}
	}
}

func TestCompute_CapsMergeToMostRestrictive(t *testing.T) {
	// hrv_low_3d (Z1/30) + ramp_over_cap (Z3/60) must merge to Z1/30.
	d := greenDay("2026-06-10")
	d.HRV = f(58)
	d.RampRate = f(5.5)
	v := Compute(Input{
		Today: d,
		Window: []Day{
			func() Day { w := greenDay("2026-06-08"); w.HRV = f(60); return w }(),
			func() Day { w := greenDay("2026-06-09"); w.HRV = f(59); return w }(),
		},
		Baselines: baselines, RampCap: 4.0,
	}, DefaultRules())
	if v.Caps.MaxZone != 1 || v.Caps.MaxMinutes != 30 {
		t.Errorf("Caps = %+v, want {1 30} (most restrictive wins)", v.Caps)
	}
}

func TestTighten(t *testing.T) {
	cases := []struct {
		name       string
		a, b, want Caps
	}{
		{"zero left", Caps{}, Caps{MaxZone: 2, MaxMinutes: 60}, Caps{MaxZone: 2, MaxMinutes: 60}},
		{"zero right", Caps{MaxZone: 2, MaxMinutes: 60}, Caps{}, Caps{MaxZone: 2, MaxMinutes: 60}},
		{"both set", Caps{MaxZone: 3, MaxMinutes: 30}, Caps{MaxZone: 1, MaxMinutes: 60}, Caps{MaxZone: 1, MaxMinutes: 30}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tighten(tc.a, tc.b); got != tc.want {
				t.Errorf("tighten(%+v, %+v) = %+v, want %+v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestCompute_InjuryBelowThresholdIsGo(t *testing.T) {
	v := Compute(Input{
		Today: greenDay("2026-06-10"), Window: []Day{greenDay("2026-06-09")},
		Baselines: baselines, RampCap: 4.0,
		Injuries: []ActiveInjury{{BodyPart: "knee", Pain: 3}},
	}, DefaultRules())
	if v.Kind != Go {
		t.Errorf("Kind = %s with pain 3 (< threshold 4), want GO", v.Kind)
	}
}

func TestCompute_MultipleInjuriesAllReported(t *testing.T) {
	v := Compute(Input{
		Today: greenDay("2026-06-10"), Window: []Day{greenDay("2026-06-09")},
		Baselines: baselines, RampCap: 4.0,
		Injuries: []ActiveInjury{{BodyPart: "knee", Pain: 5}, {BodyPart: "achilles", Pain: 6}},
	}, DefaultRules())
	if v.Kind != Skip {
		t.Fatalf("Kind = %s, want SKIP", v.Kind)
	}
	count := 0
	for _, r := range v.Reasons {
		if r.RuleID == "injury_active" {
			count++
		}
	}
	if count != 2 {
		t.Errorf("injury_active reasons = %d, want 2", count)
	}
	if v.Caps.MaxMinutes == 0 {
		t.Error("injury SKIP must bound minutes too (0 means uncapped)")
	}
}

func TestRenderBlock_CapsGapsAndSkip(t *testing.T) {
	d := greenDay("2026-06-10")
	d.HRV = nil // missing_data: gap + caps
	v := Compute(Input{
		Today: d, Window: []Day{greenDay("2026-06-09")},
		Baselines: baselines, RampCap: 4.0,
		Injuries: []ActiveInjury{{BodyPart: "knee", Pain: 5}},
	}, DefaultRules())
	block := RenderBlock(v)
	if !strings.Contains(block, "SKIP") || !strings.Contains(block, "🔴") {
		t.Errorf("SKIP render missing kind/emoji:\n%s", block)
	}
	if !strings.Contains(block, "Limiti oggi") {
		t.Errorf("caps line missing:\n%s", block)
	}
	if !strings.Contains(block, "Dati mancanti") {
		t.Errorf("data gaps line missing:\n%s", block)
	}
}

func TestCompute_ChecksCarryMarginsOnGo(t *testing.T) {
	// Spec: "thresholds the athlete can self-check". A silent GO is not
	// self-checkable: every evaluated bound must surface, passed or not.
	d := greenDay("2026-06-10")
	d.RestingHR = i(51) // +4 on baseline 47: passes, close to the +5 line
	v := Compute(Input{
		Today: d, Window: []Day{greenDay("2026-06-09")},
		Baselines: baselines, RampCap: 4.0,
	}, DefaultRules())
	if v.Kind != Go {
		t.Fatalf("Kind = %s, want GO", v.Kind)
	}
	if len(v.Checks) < 4 {
		t.Fatalf("Checks = %d, want at least 4 (hrv, rhr, sleep, ramp)", len(v.Checks))
	}
	for _, c := range v.Checks {
		if !c.Passed {
			t.Errorf("GO verdict with failed check: %+v", c)
		}
		if c.Observed == "" || c.Limit == "" {
			t.Errorf("check missing observed/limit: %+v", c)
		}
	}
}

func TestCompute_ChecksMarkFailures(t *testing.T) {
	d := greenDay("2026-06-10")
	d.SleepSecs = i(5 * 3600)
	v := Compute(Input{
		Today: d, Window: []Day{greenDay("2026-06-09")},
		Baselines: baselines, RampCap: 4.0,
	}, DefaultRules())
	found := false
	for _, c := range v.Checks {
		if c.Label == "sonno" && !c.Passed {
			found = true
		}
	}
	if !found {
		t.Error("failed sleep check not marked in Checks")
	}
}

func TestRenderBlock_GoShowsMargins(t *testing.T) {
	v := Compute(Input{
		Today: greenDay("2026-06-10"), Window: []Day{greenDay("2026-06-09")},
		Baselines: baselines, RampCap: 4.0,
	}, DefaultRules())
	block := RenderBlock(v)
	for _, want := range []string{"GO", "69", "min 64", "max +5", "6.0h", "max 4.0"} {
		if !strings.Contains(block, want) {
			t.Errorf("GO block missing %q (self-check thresholds):\n%s", want, block)
		}
	}
}

func fp(v float64) *float64 { return &v }

func TestCompute_D32ConservativeSignals(t *testing.T) {
	base := func() Day {
		d := greenDay("2026-06-10")
		return d
	}
	in := func(d Day) Input {
		return Input{Today: d, Window: []Day{greenDay("2026-06-09")}, Baselines: baselines, RampCap: 4.0}
	}

	t.Run("all green signals stay GO with margins", func(t *testing.T) {
		d := base()
		d.Readiness, d.SleepScore, d.SpO2 = fp(86), fp(82), fp(96)
		v := Compute(in(d), DefaultRules())
		if v.Kind != Go {
			t.Fatalf("Kind = %s (%+v)", v.Kind, v.Reasons)
		}
		labels := map[string]bool{}
		for _, c := range v.Checks {
			labels[c.Label] = true
		}
		for _, want := range []string{"readiness", "sleep score", "spO2"} {
			if !labels[want] {
				t.Errorf("margin %q missing from checks", want)
			}
		}
	})

	t.Run("low readiness modifies with zone cap", func(t *testing.T) {
		d := base()
		d.Readiness = fp(47) // his real May 15th
		v := Compute(in(d), DefaultRules())
		if v.Kind != Modify || v.Caps.MaxZone != 3 {
			t.Fatalf("Kind=%s Caps=%+v, want MODIFY maxZ3", v.Kind, v.Caps)
		}
	})

	t.Run("very low readiness skips", func(t *testing.T) {
		d := base()
		d.Readiness = fp(35)
		if v := Compute(in(d), DefaultRules()); v.Kind != Skip {
			t.Fatalf("Kind = %s, want SKIP", v.Kind)
		}
	})

	t.Run("low spO2 escalates by severity", func(t *testing.T) {
		d := base()
		d.SpO2 = fp(91)
		if v := Compute(in(d), DefaultRules()); v.Kind != Modify {
			t.Fatalf("spO2 91 = %s, want MODIFY", v.Kind)
		}
		d.SpO2 = fp(88)
		if v := Compute(in(d), DefaultRules()); v.Kind != Skip {
			t.Fatalf("spO2 88 = %s, want SKIP (illness signal)", v.Kind)
		}
	})

	t.Run("athlete-reported soreness and injury bite", func(t *testing.T) {
		d := base()
		so, inj := 3, 2
		d.Soreness, d.InjuryFeel = &so, &inj
		v := Compute(in(d), DefaultRules())
		if v.Kind != Modify {
			t.Fatalf("Kind = %s", v.Kind)
		}
		if v.Caps.MaxZone != 2 || v.Caps.MaxMinutes != 60 {
			t.Fatalf("injury_feel caps = %+v, want Z2/60m", v.Caps)
		}
	})

	t.Run("absent signals are silent, not gaps", func(t *testing.T) {
		v := Compute(in(base()), DefaultRules())
		if v.Kind != Go {
			t.Fatalf("Kind = %s", v.Kind)
		}
		for _, g := range v.DataGaps {
			for _, forbidden := range []string{"readiness", "spO2", "soreness"} {
				if strings.Contains(g, forbidden) {
					t.Errorf("sparse signal reported as gap: %q", g)
				}
			}
		}
	})

	t.Run("conservative-only: signals never upgrade a bad verdict", func(t *testing.T) {
		d := base()
		low := 20.0
		d.HRV = &low // way below threshold: MODIFY from the core rules
		d.Readiness, d.SleepScore, d.SpO2 = fp(94), fp(93), fp(98)
		v := Compute(in(d), DefaultRules())
		if v.Kind == Go {
			t.Fatal("perfect D32 signals upgraded a low-HRV day to GO")
		}
	})
}
