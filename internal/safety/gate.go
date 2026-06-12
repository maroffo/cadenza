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

// Vet is pure: plan + today's verdict + today's date + week context in,
// decision out. Block-class problems dominate Reject-class ones. A nil week
// skips the cumulative rules (per-workout Tier A always applies).
func Vet(p workout.Plan, v verdict.Verdict, today string, week *WeekContext) Decision {
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

	// Verdict coupling (decision 13) applies to TODAY's plan only: the
	// calendar is a plan, the morning verdict is the execution gate, and
	// tomorrow's readiness is unknowable today (D29). Future dates remain
	// bounded by Tier A.
	if p.Date == today && v.Kind == verdict.Skip {
		for _, s := range p.Flatten() {
			if zoneTop(s) > 1 {
				block("verdetto SKIP", fmt.Sprintf("step a Z%d", zoneTop(s)), "solo Z1 in un giorno SKIP")
				break
			}
		}
	}
	if p.Date == today && v.Caps.MaxZone != 0 {
		for _, s := range p.Flatten() {
			if zoneTop(s) > v.Caps.MaxZone {
				reject("cap zona del verdetto", fmt.Sprintf("Z%d", zoneTop(s)), fmt.Sprintf("max Z%d oggi", v.Caps.MaxZone))
				break
			}
		}
	}
	if p.Date == today && v.Caps.MaxMinutes != 0 && p.TotalSeconds() > v.Caps.MaxMinutes*60 {
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
	// Hard work is measured over CONSECUTIVE Z4+ runs: splitting a 40'
	// block into 4x10' without recovery is still 40 continuous hard minutes
	// (step-splitting must not defeat the ceiling).
	hardTotal, hardRun := 0, 0
	flat := p.Flatten()
	for i, s := range flat {
		if zoneTop(s) >= hardZoneFloor {
			sec := s.DurationSeconds()
			hardTotal += sec
			hardRun += sec
			lastOrBroken := i == len(flat)-1 || zoneTop(flat[i+1]) < hardZoneFloor
			if lastOrBroken {
				if hardRun > maxHardStepSeconds {
					reject("blocco duro continuo", fmt.Sprintf("%dm a Z%d+", hardRun/60, hardZoneFloor),
						fmt.Sprintf("max %dm continui Z%d+", maxHardStepSeconds/60, hardZoneFloor))
				}
				hardRun = 0
			}
		} else {
			hardRun = 0
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

	vetWeek(&d, p, week)

	return d
}

func zoneTop(s workout.Step) int {
	if s.HR.Zone != 0 {
		return s.HR.Zone
	}
	return s.HR.ZoneEnd
}

// --- Cumulative weekly rules (D34, M9.3) -----------------------------------
// The per-workout ceilings bound ONE write; nothing stopped three hard days
// in a row across separate writes (review finding). These rules see the
// rolling week around the plan date. All Tier A: code, immutable.

const (
	maxWeeklyTSS    = 600.0 // planned+executed, rolling 7 days ending at plan date
	maxHardDaysPer7 = 3
	hardDayMinSecs  = 8 * 60 // a day with >=8 hard minutes counts as a hard day
)

// DayLoad summarizes one day the gate can see: executed (from icu) or
// planned (from our ledger / icu events).
type DayLoad struct {
	Date     string
	TSS      float64
	HardSecs int
	Planned  bool // a planned event exists this day
	External bool // planned by the athlete outside cadenza (content unknown)
}

// WeekContext is the rolling window the coach assembles; nil means the
// cumulative rules are skipped (per-workout Tier A still applies).
type WeekContext struct {
	Days []DayLoad
}

// vetWeek applies the cumulative rules for a plan landing on date.
func vetWeek(d *Decision, p workout.Plan, week *WeekContext) {
	if week == nil {
		return
	}
	reject := func(bound, observed, limit string) {
		d.Violations = append(d.Violations, Violation{bound, observed, limit})
		if d.Action != Block {
			d.Action = Reject
		}
	}
	planHard := HardSeconds(p)
	planTSS := EstimateTSS(p)

	loads := map[string]DayLoad{}
	for _, dl := range week.Days {
		loads[dl.Date] = dl
	}

	// Same-day external planning: never stack on the athlete's own event.
	if dl, ok := loads[p.Date]; ok && dl.External {
		reject("giorno già pianificato",
			"evento esterno presente su "+p.Date,
			"un solo workout al giorno: chiedi all'atleta se sostituirlo")
	}

	// Rolling 7 days ending at the plan date.
	weekTSS := planTSS
	hardDays := 0
	if planHard >= hardDayMinSecs {
		hardDays = 1
	}
	for _, dl := range week.Days {
		if dl.Date == p.Date {
			continue // replaced by the new plan (slot-keyed upsert)
		}
		if withinWindow(dl.Date, p.Date, 6) {
			weekTSS += dl.TSS
			if dl.HardSecs >= hardDayMinSecs {
				hardDays++
			}
		}
	}
	if weekTSS > maxWeeklyTSS {
		reject("TSS settimanale cumulato",
			fmt.Sprintf("%.0f nei 7 giorni fino a %s", weekTSS, p.Date),
			fmt.Sprintf("max %.0f (pianificato+eseguito)", maxWeeklyTSS))
	}
	if hardDays > maxHardDaysPer7 {
		reject("giorni duri nella settimana",
			fmt.Sprintf("%d", hardDays), fmt.Sprintf("max %d su 7 giorni", maxHardDaysPer7))
	}

	// Consecutive hard days: the reviewers' exact scenario.
	if planHard >= hardDayMinSecs {
		for _, adj := range []string{addDays(p.Date, -1), addDays(p.Date, 1)} {
			if dl, ok := loads[adj]; ok && dl.HardSecs >= hardDayMinSecs {
				reject("giorni duri consecutivi",
					"giorno duro adiacente: "+adj,
					"mai due giorni duri di fila (recupero strutturale master)")
			}
		}
	}
}

// HardSeconds is the total Z4+ time of a plan (exported for the week builder).
func HardSeconds(p workout.Plan) int {
	total := 0
	for _, s := range p.Flatten() {
		if zoneTop(s) >= hardZoneFloor {
			total += s.DurationSeconds()
		}
	}
	return total
}

func withinWindow(date, end string, days int) bool {
	d, err1 := time.Parse("2006-01-02", date)
	e, err2 := time.Parse("2006-01-02", end)
	if err1 != nil || err2 != nil {
		return false
	}
	diff := int(e.Sub(d).Hours() / 24)
	return diff >= 0 && diff <= days
}

func addDays(date string, n int) string {
	d, err := time.Parse("2006-01-02", date)
	if err != nil {
		return ""
	}
	return d.AddDate(0, 0, n).Format("2006-01-02")
}
