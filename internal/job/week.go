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

// buildWeekContext assembles the rolling window [date-6, date+1] the
// cumulative gate inspects (the +1 covers next-day hard adjacency).
//
// Executed hardness uses the TOP THREE zones of the athlete's HR scheme:
// on a 7-zone scheme that is SuperThreshold and above; on shorter schemes
// it over-counts toward MORE hard seconds, which only ever tightens.
func buildWeekContext(ctx context.Context, acts ActivitiesSource, events EventsSource, plans PlanLookup, date string, today string) *safety.WeekContext {
	d, err := time.Parse("2006-01-02", date)
	if err != nil {
		return nil
	}
	oldest := d.AddDate(0, 0, -6).Format("2006-01-02")
	newest := d.AddDate(0, 0, 1).Format("2006-01-02")

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
			dl.TSS += float64(*a.TrainingLoad)
		}
		if n := len(a.HRZoneTimes); n >= 3 {
			for _, secs := range a.HRZoneTimes[n-3:] {
				dl.HardSecs += secs
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
		dl.Planned = true
		extID := ""
		if e.ExternalID != nil {
			extID = *e.ExternalID
		}
		if !strings.HasPrefix(extID, "cadenza-") {
			dl.External = true
			continue
		}
		if plans == nil {
			continue
		}
		planJSON, err := plans.LatestPlanFor(ctx, extID)
		if err != nil || planJSON == "" {
			slog.Warn("week context: ledger plan missing", "external_id", extID, "err", err)
			continue
		}
		var p workout.Plan
		if err := json.Unmarshal([]byte(planJSON), &p); err != nil {
			continue
		}
		dl.TSS += safety.EstimateTSS(p)
		dl.HardSecs += safety.HardSeconds(p)
	}

	out := &safety.WeekContext{}
	for _, dl := range loads {
		out.Days = append(out.Days, *dl)
	}
	return out
}
