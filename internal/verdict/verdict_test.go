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
