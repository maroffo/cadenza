// ABOUTME: Deterministic go/modify/skip verdict from wellness vs personal baselines. Pure: no I/O.
// ABOUTME: Computed before any model call, injected into the prompt, appended to messages by code.

package verdict

import "fmt"

type Kind string

const (
	Go     Kind = "GO"
	Modify Kind = "MODIFY"
	Skip   Kind = "SKIP"
)

// Version identifies the rule set in audit records and message footers.
const Version = "v1"

// tierARampCap is the absolute code ceiling: athlete-tunable ramp caps can
// only tighten below it, never loosen above it (decision 13).
const tierARampCap = 6.0

// Day is the verdict engine's own wellness view. It deliberately mirrors
// icu.Wellness without importing it: this package stays free of I/O deps.
// Pointers preserve the null-vs-zero distinction end to end.
type Day struct {
	Date      string
	HRV       *float64
	RestingHR *int
	SleepSecs *int
	RampRate  *float64
	CTL       *float64
	ATL       *float64
}

type Baselines struct {
	HRVMean   float64 // 30d mean
	HRVSD     float64 // 30d standard deviation
	RestingHR float64 // 30d baseline
}

type ActiveInjury struct {
	BodyPart string
	Pain     int // 0-10 athlete-reported
}

type Input struct {
	Today     Day
	Window    []Day // trailing days before today, oldest first
	Baselines Baselines
	RampCap   float64 // athlete-tunable (Tier B); clamped into tierARampCap
	Injuries  []ActiveInjury
}

// Rules holds the tunable thresholds with safe defaults. Tunables arrive from
// Firestore config in M5; until then DefaultRules() is the only source.
type Rules struct {
	HRVLowSDFactor float64 // threshold = mean - factor*SD
	HRVLowDays     int     // consecutive low days for escalation to SKIP
	RHRModifyDelta float64 // bpm above baseline -> MODIFY
	RHRSkipDelta   float64 // bpm above baseline -> SKIP
	SleepMinSecs   int
	InjurySkipPain int // pain >= this -> SKIP
}

func DefaultRules() Rules {
	return Rules{
		HRVLowSDFactor: 0.75,
		HRVLowDays:     3,
		RHRModifyDelta: 5,
		RHRSkipDelta:   8,
		SleepMinSecs:   6 * 3600,
		InjurySkipPain: 4,
	}
}

// Reason is one fired rule, with observed and threshold rendered so the
// athlete can self-check every number (spec: "thresholds the athlete can
// self-check").
type Reason struct {
	RuleID    string
	Message   string
	Observed  string
	Threshold string
}

// Caps bound today's session; 0 means no cap on that axis.
type Caps struct {
	MaxZone    int
	MaxMinutes int
}

type Verdict struct {
	Kind     Kind
	Reasons  []Reason
	Caps     Caps
	DataGaps []string
	Version  string
}

// capsFor maps each rule to the most conservative session it still allows.
var capsFor = map[string]Caps{
	"missing_data":        {MaxZone: 2, MaxMinutes: 60},
	"hrv_low":             {MaxZone: 2, MaxMinutes: 60},
	"hrv_low_3d":          {MaxZone: 1, MaxMinutes: 30},
	"resting_hr_elevated": {MaxZone: 2, MaxMinutes: 60},
	"resting_hr_high":     {MaxZone: 1, MaxMinutes: 30},
	"short_sleep":         {MaxZone: 2, MaxMinutes: 75},
	"ramp_over_cap":       {MaxZone: 3, MaxMinutes: 60},
	"injury_active":       {MaxZone: 1, MaxMinutes: 0},
}

// skipRules escalate the verdict to SKIP; everything else fired is MODIFY.
var skipRules = map[string]bool{
	"hrv_low_3d":      true,
	"resting_hr_high": true,
	"injury_active":   true,
}

// Compute is pure: same input, same verdict. All arithmetic happens here,
// in code, never in the model.
func Compute(in Input, rules Rules) Verdict {
	v := Verdict{Kind: Go, Version: Version}

	hrvThreshold := in.Baselines.HRVMean - rules.HRVLowSDFactor*in.Baselines.HRVSD

	// HRV rules. Missing HRV is a data gap, not a zero.
	switch {
	case in.Today.HRV == nil:
		v.DataGaps = append(v.DataGaps, "HRV non sincronizzata")
		v.fire("missing_data",
			"dato HRV mancante: verdetto conservativo",
			"nessun valore", fmt.Sprintf("baseline %.0f", in.Baselines.HRVMean))
	case *in.Today.HRV < hrvThreshold:
		lowRun := 1
		for idx := len(in.Window) - 1; idx >= 0; idx-- {
			d := in.Window[idx]
			if d.HRV != nil && *d.HRV < hrvThreshold {
				lowRun++
			} else {
				break
			}
		}
		if lowRun >= rules.HRVLowDays {
			v.fire("hrv_low_3d",
				fmt.Sprintf("HRV sotto range da %d giorni consecutivi: recupero, e anomalia da segnalare", lowRun),
				fmt.Sprintf("%.0f", *in.Today.HRV),
				fmt.Sprintf("%.1f (baseline %.0f − %.2f×SD %.0f)", hrvThreshold, in.Baselines.HRVMean, rules.HRVLowSDFactor, in.Baselines.HRVSD))
		} else {
			v.fire("hrv_low",
				"HRV sotto il range personale: solo lavoro facile",
				fmt.Sprintf("%.0f", *in.Today.HRV),
				fmt.Sprintf("%.1f", hrvThreshold))
		}
	}

	// Resting HR rules.
	if in.Today.RestingHR == nil {
		v.DataGaps = append(v.DataGaps, "FC a riposo non sincronizzata")
	} else {
		delta := float64(*in.Today.RestingHR) - in.Baselines.RestingHR
		switch {
		case delta >= rules.RHRSkipDelta:
			v.fire("resting_hr_high",
				"FC a riposo molto elevata: solo recupero",
				fmt.Sprintf("%d (+%.0f)", *in.Today.RestingHR, delta),
				fmt.Sprintf("baseline %.0f +%.0f", in.Baselines.RestingHR, rules.RHRSkipDelta))
		case delta >= rules.RHRModifyDelta:
			v.fire("resting_hr_elevated",
				"FC a riposo elevata: ridurre intensità",
				fmt.Sprintf("%d (+%.0f)", *in.Today.RestingHR, delta),
				fmt.Sprintf("baseline %.0f +%.0f", in.Baselines.RestingHR, rules.RHRModifyDelta))
		}
	}

	// Sleep.
	if in.Today.SleepSecs == nil {
		v.DataGaps = append(v.DataGaps, "sonno non sincronizzato")
	} else if *in.Today.SleepSecs < rules.SleepMinSecs {
		v.fire("short_sleep",
			"sonno insufficiente: niente lavoro di qualità",
			fmt.Sprintf("%.1fh", float64(*in.Today.SleepSecs)/3600),
			fmt.Sprintf("%.1fh", float64(rules.SleepMinSecs)/3600))
	}

	// Ramp rate: fires even when every autonomic signal is green. There is
	// no HRV for tendons (spec, core principle 3).
	rampCap := in.RampCap
	if rampCap <= 0 || rampCap > tierARampCap {
		rampCap = tierARampCap
	}
	if in.Today.RampRate == nil {
		v.DataGaps = append(v.DataGaps, "ramp rate non disponibile")
	} else if *in.Today.RampRate > rampCap {
		v.fire("ramp_over_cap",
			"rampa CTL sopra il tetto: ridurre il carico anche se i segnali autonomici sono verdi",
			fmt.Sprintf("%.1f/settimana", *in.Today.RampRate),
			fmt.Sprintf("%.1f/settimana", rampCap))
	}

	// Injuries.
	for _, inj := range in.Injuries {
		if inj.Pain >= rules.InjurySkipPain {
			v.fire("injury_active",
				fmt.Sprintf("infortunio attivo (%s): nessun carico strutturato", inj.BodyPart),
				fmt.Sprintf("dolore %d/10", inj.Pain),
				fmt.Sprintf("soglia %d/10", rules.InjurySkipPain))
		}
	}

	return v
}

// fire records a reason, escalates the kind, and tightens caps.
func (v *Verdict) fire(ruleID, message, observed, threshold string) {
	v.Reasons = append(v.Reasons, Reason{
		RuleID:    ruleID,
		Message:   message,
		Observed:  observed,
		Threshold: threshold,
	})
	if skipRules[ruleID] {
		v.Kind = Skip
	} else if v.Kind != Skip {
		v.Kind = Modify
	}
	v.Caps = tighten(v.Caps, capsFor[ruleID])
}

// tighten merges caps keeping the most restrictive non-zero values.
func tighten(a, b Caps) Caps {
	out := a
	if b.MaxZone != 0 && (out.MaxZone == 0 || b.MaxZone < out.MaxZone) {
		out.MaxZone = b.MaxZone
	}
	if b.MaxMinutes != 0 && (out.MaxMinutes == 0 || b.MaxMinutes < out.MaxMinutes) {
		out.MaxMinutes = b.MaxMinutes
	}
	return out
}
