// ABOUTME: The physiological safety gate: pure code between the model's plan and any write.
// ABOUTME: Verifies SANITY, not just fidelity: a perfectly-formatted 3h VO2max workout dies here.

package safety

import (
	"fmt"
	"time"

	"github.com/maroffo/cadenza/internal/verdict"
	"github.com/maroffo/cadenza/internal/workout"
)

// Tier A ceilings: code constants, deploy-gated, immune to the data layer
// (decision 13). The Firestore-tunable layer can only tighten below these.
const (
	maxWorkoutSeconds   = 300 * 60 // nothing longer than 5h is ever auto-written
	maxHardStepSeconds  = 10 * 60  // no single Z4+ block past 10 minutes
	maxHardTotalSeconds = 40 * 60  // total time at Z4+ per workout
	warmupCooldownMaxZ  = 2
	maxDailyTSS         = 250
	writeWindowDays     = 14 // [today, today+14]
	hardZoneFloor       = 4
)

// ifByZone is the fixed intensity-factor table for TSS estimation. This is
// a GATE, not analytics: precision matters less than determinism.
var ifByZone = map[int]float64{1: 0.60, 2: 0.72, 3: 0.83, 4: 0.95, 5: 1.05}

type Violation struct {
	Bound    string
	Observed string
	Limit    string
}

type Action string

const (
	Pass   Action = "PASS"
	Reject Action = "REJECT" // model regenerates with the violations
	Block  Action = "BLOCK"  // never auto-resolvable: surface to the athlete
)

type Decision struct {
	Action     Action
	Violations []Violation
}

// EstimateTSS computes sum(minutes/60 * IF(zone)^2 * 100) over flattened
// steps. Ranges use the UPPER zone: the gate errs toward less load allowed.
func EstimateTSS(p workout.Plan) float64 {
	total := 0.0
	for _, s := range p.Flatten() {
		z := s.HR.Zone
		if z == 0 {
			z = s.HR.ZoneEnd
		}
		ifv := ifByZone[z]
		total += float64(s.DurationSeconds()) / 3600 * ifv * ifv * 100
	}
	return total
}

// Vet is pure: plan + today's verdict + today's date in, decision out.
// Block-class problems dominate Reject-class ones in the verdict.
func Vet(p workout.Plan, v verdict.Verdict, today string) Decision {
	d := Decision{Action: Pass}
	reject := func(bound, observed, limit string) {
		d.Violations = append(d.Violations, Violation{bound, observed, limit})
		if d.Action != Block {
			d.Action = Reject
		}
	}
	block := func(bound, observed, limit string) {
		d.Violations = append(d.Violations, Violation{bound, observed, limit})
		d.Action = Block
	}

	if err := p.Validate(); err != nil {
		reject("schema", err.Error(), "piano valido")
		return d
	}

	// Verdict coupling (decision 13): the model cannot out-prescribe the
	// deterministic verdict, today or any day it writes for.
	if v.Kind == verdict.Skip {
		for _, s := range p.Flatten() {
			if zoneTop(s) > 1 {
				block("verdetto SKIP", fmt.Sprintf("step a Z%d", zoneTop(s)), "solo Z1 in un giorno SKIP")
				break
			}
		}
	}
	if v.Caps.MaxZone != 0 {
		for _, s := range p.Flatten() {
			if zoneTop(s) > v.Caps.MaxZone {
				reject("cap zona del verdetto", fmt.Sprintf("Z%d", zoneTop(s)), fmt.Sprintf("max Z%d oggi", v.Caps.MaxZone))
				break
			}
		}
	}
	if v.Caps.MaxMinutes != 0 && p.TotalSeconds() > v.Caps.MaxMinutes*60 {
		reject("cap durata del verdetto",
			fmt.Sprintf("%d minuti", p.TotalSeconds()/60),
			fmt.Sprintf("max %d minuti oggi", v.Caps.MaxMinutes))
	}

	// Write window: no past writes, no far-future writes.
	planDay, err := time.Parse("2006-01-02", p.Date)
	todayDay, err2 := time.Parse("2006-01-02", today)
	if err != nil || err2 != nil {
		reject("data", p.Date, "yyyy-mm-dd")
	} else {
		diff := int(planDay.Sub(todayDay).Hours() / 24)
		if diff < 0 || diff > writeWindowDays {
			block("finestra di scrittura", p.Date, fmt.Sprintf("[%s, +%d giorni]", today, writeWindowDays))
		}
	}

	// Tier A ceilings.
	if total := p.TotalSeconds(); total > maxWorkoutSeconds {
		reject("durata totale", fmt.Sprintf("%dm", total/60), fmt.Sprintf("max %dm", maxWorkoutSeconds/60))
	}
	hardTotal := 0
	for _, s := range p.Flatten() {
		if zoneTop(s) >= hardZoneFloor {
			sec := s.DurationSeconds()
			hardTotal += sec
			if sec > maxHardStepSeconds {
				reject("singolo step duro", fmt.Sprintf("%dm a Z%d", sec/60, zoneTop(s)),
					fmt.Sprintf("max %dm per step Z%d+", maxHardStepSeconds/60, hardZoneFloor))
			}
		}
		if (s.Intensity == "warmup" || s.Intensity == "cooldown") && zoneTop(s) > warmupCooldownMaxZ {
			reject("zona warmup/cooldown", fmt.Sprintf("Z%d", zoneTop(s)), fmt.Sprintf("max Z%d", warmupCooldownMaxZ))
		}
	}
	if hardTotal > maxHardTotalSeconds {
		reject("minuti duri totali", fmt.Sprintf("%dm", hardTotal/60), fmt.Sprintf("max %dm", maxHardTotalSeconds/60))
	}
	if tss := EstimateTSS(p); tss > maxDailyTSS {
		reject("TSS stimato", fmt.Sprintf("%.0f", tss), fmt.Sprintf("max %d", maxDailyTSS))
	}

	return d
}

func zoneTop(s workout.Step) int {
	if s.HR.Zone != 0 {
		return s.HR.Zone
	}
	return s.HR.ZoneEnd
}
