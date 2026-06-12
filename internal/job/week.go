// ABOUTME: Week context builder: executed + planned load around a date for the cumulative gate.
// ABOUTME: Degrades to nil on source failures: per-workout Tier A always still applies.

package job

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/maroffo/cadenza/internal/icu"
	"github.com/maroffo/cadenza/internal/safety"
	"github.com/maroffo/cadenza/internal/workout"
)

// EventsSource reads planned calendar events; satisfied by job.ICU.
type EventsSource interface {
	EventsRange(ctx context.Context, oldest, newest string) ([]icu.Event, error)
}

// PlanLookup resolves a cadenza external id to its stored plan JSON;
// satisfied by store.Ledger.
type PlanLookup interface {
	LatestPlanFor(ctx context.Context, externalID string) (string, error)
}

// buildWeekContext assembles [date-6, date+6]: the cumulative gate examines
// every 7-day window containing the plan date (suffix-only was evadable by
// writing a week in reverse order).
//
// Executed hardness counts zone 4 and above of the athlete's scheme
// (HRZoneTimes[3:]): our written Z4 resolves onto athlete zone 4, so the
// planned and executed definitions of "hard" agree on any scheme length;
// schemes shorter than 4 zones count everything (tightens, never loosens).
func buildWeekContext(ctx context.Context, acts ActivitiesSource, events EventsSource, plans PlanLookup, date string, today string) *safety.WeekContext {
	d, err := time.Parse("2006-01-02", date)
	if err != nil || acts == nil || events == nil {
		return nil
	}
	oldest := d.AddDate(0, 0, -6).Format("2006-01-02")
	newest := d.AddDate(0, 0, 6).Format("2006-01-02")

	loads := map[string]*safety.DayLoad{}
	day := func(dt string) *safety.DayLoad {
		if loads[dt] == nil {
			loads[dt] = &safety.DayLoad{Date: dt}
		}
		return loads[dt]
	}

	executed, err := acts.ActivitiesRange(ctx, oldest, newest)
	if err != nil {
		slog.Warn("week context: activities unavailable, cumulative gate degraded", "err", err)
		return nil
	}
	for _, a := range executed {
		if len(a.StartDateLocal) < 10 {
			continue
		}
		dl := day(a.StartDateLocal[:10])
		if a.TrainingLoad != nil {
			dl.ExecutedTSS += float64(*a.TrainingLoad)
		}
		if n := len(a.HRZoneTimes); n >= 4 {
			for _, secs := range a.HRZoneTimes[3:] {
				dl.ExecutedHardSecs += secs
			}
		} else {
			for _, secs := range a.HRZoneTimes {
				dl.ExecutedHardSecs += secs
			}
		}
	}

	planned, err := events.EventsRange(ctx, oldest, newest)
	if err != nil {
		slog.Warn("week context: events unavailable, cumulative gate degraded", "err", err)
		return nil
	}
	for _, e := range planned {
		if e.Category != "WORKOUT" || len(e.StartDateLocal) < 10 {
			continue
		}
		evDate := e.StartDateLocal[:10]
		if evDate < today {
			continue // past planned events are represented by executed activities
		}
		dl := day(evDate)
		extID := ""
		if e.ExternalID != nil {
			extID = *e.ExternalID
		}
		if !strings.HasPrefix(extID, "cadenza-") {
			dl.External = true
			continue
		}
		sport := cadenzaSlotSport(extID)
		if plans == nil {
			// Content unknowable: treat as external so the gate refuses to
			// stack on it (silent zero-load would UNDERCOUNT: review MAJOR).
			dl.External = true
			continue
		}
		planJSON, err := plans.LatestPlanFor(ctx, extID)
		if err != nil || planJSON == "" {
			slog.Warn("week context: ledger plan missing, day marked unknown", "external_id", extID, "err", err)
			dl.External = true
			continue
		}
		var p workout.Plan
		if err := json.Unmarshal([]byte(planJSON), &p); err != nil {
			dl.External = true
			continue
		}
		dl.Cadenza = append(dl.Cadenza, safety.PlannedLoad{
			Sport: sport, TSS: safety.EstimateTSS(p), HardSecs: safety.HardSeconds(p),
		})
	}

	out := &safety.WeekContext{}
	for _, dl := range loads {
		out.Days = append(out.Days, *dl)
	}
	return out
}

// cadenzaSlotSport recovers the sport from a slot id cadenza-YYYY-MM-DD-<sport>.
func cadenzaSlotSport(extID string) string {
	parts := strings.Split(extID, "-")
	if len(parts) < 5 {
		return ""
	}
	return parts[len(parts)-1]
}
