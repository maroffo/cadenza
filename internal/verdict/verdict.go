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
	// Conservative-only signals (D32): when present they can DOWNGRADE the
	// verdict, never upgrade it; when absent they are silent (sparse logging
	// is normal, nil is no-signal here, unlike the four core metrics).
	Readiness  *float64 // device composite 0-100
	SleepScore *float64 // device composite 0-100
	SpO2       *float64 // percent; low = illness signal
	Soreness   *int     // athlete-entered, 1 good .. 4 worst
	Fatigue    *int     // athlete-entered, 1 good .. 4 worst
	InjuryFeel *int     // athlete-entered, 1 none .. 4 worst
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

	// D32 conservative-only thresholds (calibrated on live ranges).
	ReadinessModify  float64 // below -> MODIFY (observed 60d range: 47-94)
	ReadinessSkip    float64 // below -> SKIP (autonomic composite floor)
	SleepScoreModify float64 // below -> MODIFY (observed: 61-93)
	SpO2Modify       float64 // below -> MODIFY (observed: 95-98)
	SpO2Skip         float64 // below -> SKIP + see-how-you-feel
	SubjectiveModify int     // soreness/fatigue >= this -> MODIFY (1..4)
	InjuryFeelModify int     // athlete injury feel >= this -> MODIFY w/ caps
}

func DefaultRules() Rules {
	return Rules{
		HRVLowSDFactor:   0.75,
		HRVLowDays:       3,
		ReadinessModify:  60,
		ReadinessSkip:    40,
		SleepScoreModify: 55,
		SpO2Modify:       92,
		SpO2Skip:         90,
		SubjectiveModify: 3,
		InjuryFeelModify: 2,
		RHRModifyDelta:   5,
		RHRSkipDelta:     8,
		SleepMinSecs:     6 * 3600,
		InjurySkipPain:   4,
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
	// Checks lists every evaluated bound, passed or not: the spec requires
	// thresholds the athlete can self-check, and a silent GO checks nothing.
	Checks []Check
}

// Check is one evaluated bound with its margin, shown on GO days and fed to
// the M4 prompt as deterministic context.
type Check struct {
	Label    string
	Observed string
	Limit    string
	Passed   bool
}

// capsFor maps each rule to the most conservative session it still allows.
// Caps semantics: 0 means "no cap on that axis", so every SKIP-class rule
// must set BOTH axes (an injury day allows bounded easy movement, never
// unlimited anything).
var capsFor = map[string]Caps{
	"missing_data":        {MaxZone: 2, MaxMinutes: 60},
	"hrv_low":             {MaxZone: 2, MaxMinutes: 60},
	"hrv_low_3d":          {MaxZone: 1, MaxMinutes: 30},
	"resting_hr_elevated": {MaxZone: 2, MaxMinutes: 60},
	"resting_hr_high":     {MaxZone: 1, MaxMinutes: 30},
	"short_sleep":         {MaxZone: 2, MaxMinutes: 75},
	"ramp_over_cap":       {MaxZone: 3, MaxMinutes: 60},
	"injury_active":       {MaxZone: 1, MaxMinutes: 45},
	"injury_feel":         {MaxZone: 2, MaxMinutes: 60},
	"readiness_low":       {MaxZone: 3},
}

// skipRules escalate the verdict to SKIP; everything else fired is MODIFY.
var skipRules = map[string]bool{
	"hrv_low_3d":         true,
	"resting_hr_high":    true,
	"injury_active":      true,
	"readiness_very_low": true,
	"spo2_very_low":      true,
}

// Compute is pure: same input, same verdict. All arithmetic happens here,
// in code, never in the model.
func Compute(in Input, rules Rules) Verdict {
	v := Verdict{Kind: Go, Version: Version}

	hrvThreshold := in.Baselines.HRVMean - rules.HRVLowSDFactor*in.Baselines.HRVSD

	// HRV rules. Missing HRV is a data gap, not a zero.
	if in.Today.HRV == nil {
		v.DataGaps = append(v.DataGaps, "HRV non sincronizzata")
		v.fire("missing_data",
			"dato HRV mancante: verdetto conservativo",
			"nessun valore", fmt.Sprintf("baseline %.0f", in.Baselines.HRVMean))
	} else {
		v.check("HRV", fmt.Sprintf("%.0f", *in.Today.HRV),
			fmt.Sprintf("min %.0f", hrvThreshold), *in.Today.HRV >= hrvThreshold)
		if *in.Today.HRV < hrvThreshold {
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
	}

	// Resting HR rules.
	if in.Today.RestingHR == nil {
		v.DataGaps = append(v.DataGaps, "FC a riposo non sincronizzata")
	} else {
		delta := float64(*in.Today.RestingHR) - in.Baselines.RestingHR
		v.check("FC riposo", fmt.Sprintf("%+.1f", delta),
			fmt.Sprintf("max +%.0f", rules.RHRModifyDelta), delta < rules.RHRModifyDelta)
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
	} else {
		v.check("sonno", fmt.Sprintf("%.1fh", float64(*in.Today.SleepSecs)/3600),
			fmt.Sprintf("min %.1fh", float64(rules.SleepMinSecs)/3600),
			*in.Today.SleepSecs >= rules.SleepMinSecs)
	}
	if in.Today.SleepSecs != nil && *in.Today.SleepSecs < rules.SleepMinSecs {
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
	} else {
		v.check("rampa", fmt.Sprintf("%.1f", *in.Today.RampRate),
			fmt.Sprintf("max %.1f", rampCap), *in.Today.RampRate <= rampCap)
	}
	if in.Today.RampRate != nil && *in.Today.RampRate > rampCap {
		v.fire("ramp_over_cap",
			"rampa CTL sopra il tetto: ridurre il carico anche se i segnali autonomici sono verdi",
			fmt.Sprintf("%.1f/settimana", *in.Today.RampRate),
			fmt.Sprintf("%.1f/settimana", rampCap))
	}

	// D32 conservative-only signals: each can only push the verdict down.
	if r := in.Today.Readiness; r != nil {
		v.check("readiness", fmt.Sprintf("%.0f", *r), fmt.Sprintf("min %.0f", rules.ReadinessModify), *r >= rules.ReadinessModify)
		switch {
		case *r < rules.ReadinessSkip:
			v.fire("readiness_very_low",
				"readiness molto bassa: il composito del device segnala recupero assente, oggi recupero",
				fmt.Sprintf("%.0f", *r), fmt.Sprintf("%.0f", rules.ReadinessSkip))
		case *r < rules.ReadinessModify:
			v.fire("readiness_low",
				"readiness sotto soglia: recupero autonomico incompleto, tieni il giorno facile",
				fmt.Sprintf("%.0f", *r), fmt.Sprintf("%.0f", rules.ReadinessModify))
		}
	}
	if sc := in.Today.SleepScore; sc != nil {
		v.check("sleep score", fmt.Sprintf("%.0f", *sc), fmt.Sprintf("min %.0f", rules.SleepScoreModify), *sc >= rules.SleepScoreModify)
		if *sc < rules.SleepScoreModify {
			v.fire("sleep_score_low",
				"qualità del sonno scarsa: niente intensità oggi",
				fmt.Sprintf("%.0f", *sc), fmt.Sprintf("%.0f", rules.SleepScoreModify))
		}
	}
	if o2 := in.Today.SpO2; o2 != nil {
		v.check("spO2", fmt.Sprintf("%.0f%%", *o2), fmt.Sprintf("min %.0f%%", rules.SpO2Modify), *o2 >= rules.SpO2Modify)
		switch {
		case *o2 < rules.SpO2Skip:
			v.fire("spo2_very_low",
				"saturazione molto bassa per i tuoi standard: possibile malattia, oggi fermo e ascoltati",
				fmt.Sprintf("%.0f%%", *o2), fmt.Sprintf("%.0f%%", rules.SpO2Skip))
		case *o2 < rules.SpO2Modify:
			v.fire("spo2_low",
				"saturazione sotto il tuo range abituale: solo lavoro facile e osserva come ti senti",
				fmt.Sprintf("%.0f%%", *o2), fmt.Sprintf("%.0f%%", rules.SpO2Modify))
		}
	}
	if so := in.Today.Soreness; so != nil && *so >= rules.SubjectiveModify {
		v.fire("soreness_high",
			"indolenzimento dichiarato alto: il tuo segnale conta più dei numeri, giorno facile",
			fmt.Sprintf("%d/4", *so), fmt.Sprintf("max %d", rules.SubjectiveModify-1))
	}
	if fa := in.Today.Fatigue; fa != nil && *fa >= rules.SubjectiveModify {
		v.fire("fatigue_high",
			"fatica dichiarata alta: riduci, oggi non è giornata da spingere",
			fmt.Sprintf("%d/4", *fa), fmt.Sprintf("max %d", rules.SubjectiveModify-1))
	}
	if inj := in.Today.InjuryFeel; inj != nil && *inj >= rules.InjuryFeelModify {
		v.fire("injury_feel",
			"hai segnalato fastidio/infortunio nel diario: protezione attiva",
			fmt.Sprintf("%d/4", *inj), fmt.Sprintf("max %d", rules.InjuryFeelModify-1))
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

// check records an evaluated bound for athlete self-verification.
func (v *Verdict) check(label, observed, limit string, passed bool) {
	v.Checks = append(v.Checks, Check{Label: label, Observed: observed, Limit: limit, Passed: passed})
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
