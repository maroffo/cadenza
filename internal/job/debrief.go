// ABOUTME: Post-workout debrief + missed-session detection: the planned-vs-executed diff (M9.5).
// ABOUTME: Idempotent sweep, runs morning and evening; numbers computed here, Haiku only comments.

package job

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/maroffo/cadenza/internal/agent"
	"github.com/maroffo/cadenza/internal/icu"
	"github.com/maroffo/cadenza/internal/safety"
	"github.com/maroffo/cadenza/internal/telegram"
	"github.com/maroffo/cadenza/internal/workout"
)

const (
	// debriefMinTSS filters noise: walks and strolls get no debrief.
	debriefMinTSS = 20
	// debriefSettle lets the athlete rename/annotate (RPE!) before analysis.
	debriefSettle = 45 * time.Minute
)

// DebriefMarker is the processed-set; satisfied by store.Debriefs.
type DebriefMarker interface {
	MarkOnce(ctx context.Context, key string) (bool, error)
}

type Debrief struct {
	Activities ActivitiesSource
	Events     EventsSource
	Plans      PlanLookup
	Marks      DebriefMarker
	Narrator   agent.Debriefer
	Out        Messenger
	Now        func() time.Time
	TZ         *time.Location
}

// Sweep debriefs settled activities of yesterday+today and flags yesterday's
// planned-but-missing sessions. Re-runnable: every outcome is Create-once.
func (d Debrief) Sweep(ctx context.Context) error {
	now := d.Now().In(d.TZ)
	today := now.Format(dateOnly)
	yesterday := now.AddDate(0, 0, -1).Format(dateOnly)

	acts, err := d.Activities.ActivitiesRange(ctx, yesterday, today)
	if err != nil {
		return fmt.Errorf("debrief: activities: %w", err)
	}
	for _, a := range acts {
		if err := d.debriefOne(ctx, a, now); err != nil {
			slog.Warn("debrief: activity failed", "id", a.ID, "err", err)
		}
	}

	// Missed sessions: only YESTERDAY (a complete day) is judged.
	if err := d.missedSessions(ctx, yesterday, acts); err != nil {
		slog.Warn("debrief: missed-session check failed", "err", err)
	}
	return nil
}

func (d Debrief) debriefOne(ctx context.Context, a icu.Activity, now time.Time) error {
	if a.ID == "" || len(a.StartDateLocal) < 10 {
		return nil
	}
	tss := 0
	if a.TrainingLoad != nil {
		tss = *a.TrainingLoad
	}
	if tss < debriefMinTSS {
		return nil // noise: no message, no mark (a late TSS recompute may upgrade it)
	}
	// Settle window: started+duration+45min must be in the past, so RPE and
	// renames have landed before we speak.
	start, err := time.ParseInLocation("2006-01-02T15:04:05", a.StartDateLocal, d.TZ)
	if err == nil {
		dur := 0
		if a.MovingTime != nil {
			dur = *a.MovingTime
		}
		if now.Before(start.Add(time.Duration(dur)*time.Second + debriefSettle)) {
			return nil // not settled yet; the next sweep picks it up
		}
	}
	fresh, err := d.Marks.MarkOnce(ctx, "act-"+a.ID)
	if err != nil {
		return err
	}
	if !fresh {
		return nil
	}

	block := d.dataBlock(ctx, a)
	text := "🏁 <b>Debrief</b>\n" + block
	if narrative, err := d.Narrator.Narrate(ctx, block); err != nil {
		slog.Warn("debrief: narrative failed, deterministic only", "err", err)
	} else {
		text = "🏁 <b>Debrief</b>\n" + telegram.SanitizeNarrative(narrative) + "\n\n" + block
	}
	return d.Out.Send(ctx, text)
}

// dataBlock computes the prescribed-vs-executed comparison: all numbers
// here, none in the model.
func (d Debrief) dataBlock(ctx context.Context, a icu.Activity) string {
	var b strings.Builder
	date := a.StartDateLocal[:10]
	name := a.Type
	if a.Name != nil {
		name = *a.Name
	}
	fmt.Fprintf(&b, "%s · %s\n", telegram.Escape(name), date)
	if a.MovingTime != nil {
		fmt.Fprintf(&b, "Durata: %d min", *a.MovingTime/60)
	}
	if a.TrainingLoad != nil {
		fmt.Fprintf(&b, " · TSS %d", *a.TrainingLoad)
	}
	if a.Intensity != nil {
		fmt.Fprintf(&b, " · IF %.2f", *a.Intensity)
	}
	if a.AverageHR != nil {
		fmt.Fprintf(&b, " · FC media %d", *a.AverageHR)
	}
	b.WriteString("\n")
	if n := len(a.HRZoneTimes); n >= 4 {
		easy, hard := 0, 0
		for i, secs := range a.HRZoneTimes {
			if i < 3 {
				easy += secs
			} else {
				hard += secs
			}
		}
		if easy+hard > 0 {
			fmt.Fprintf(&b, "Distribuzione: %d%% facile / %d%% duro (Z4+)\n",
				easy*100/(easy+hard), hard*100/(easy+hard))
		}
	}
	if a.RPE != nil {
		fmt.Fprintf(&b, "RPE dichiarato: %d/10", *a.RPE)
		if a.Intensity != nil {
			fmt.Fprintf(&b, " (IF oggettivo %.2f: confronta percepito e oggettivo)", *a.Intensity)
		}
		b.WriteString("\n")
	}

	// Prescribed, when this day+sport had a cadenza plan.
	if d.Plans != nil {
		slot := fmt.Sprintf("cadenza-%s-%s", date, strings.ToLower(a.Type))
		if planJSON, err := d.Plans.LatestPlanFor(ctx, slot); err == nil && planJSON != "" {
			var p workout.Plan
			if json.Unmarshal([]byte(planJSON), &p) == nil {
				fmt.Fprintf(&b, "Prescritto: %q, %d min, TSS stimato %.0f, %d min duri\n",
					p.Title, p.TotalSeconds()/60, safety.EstimateTSS(p), safety.HardSeconds(p)/60)
			}
		}
	}
	return b.String()
}

// missedSessions flags yesterday's cadenza plans with no executed activity
// of the same sport: the thing a real coach notices.
func (d Debrief) missedSessions(ctx context.Context, yesterday string, acts []icu.Activity) error {
	if d.Events == nil {
		return nil
	}
	events, err := d.Events.EventsRange(ctx, yesterday, yesterday)
	if err != nil {
		return err
	}
	executedSports := map[string]bool{}
	for _, a := range acts {
		if len(a.StartDateLocal) >= 10 && a.StartDateLocal[:10] == yesterday {
			executedSports[strings.ToLower(a.Type)] = true
		}
	}
	for _, e := range events {
		if e.Category != "WORKOUT" || e.ExternalID == nil ||
			!strings.HasPrefix(*e.ExternalID, "cadenza-") {
			continue
		}
		sport := cadenzaSlotSport(*e.ExternalID)
		if executedSports[sport] {
			continue
		}
		fresh, err := d.Marks.MarkOnce(ctx, "missed-"+*e.ExternalID)
		if err != nil {
			return err
		}
		if !fresh {
			continue
		}
		name := "allenamento"
		if e.Name != nil {
			name = *e.Name
		}
		if err := d.Out.Send(ctx, fmt.Sprintf(
			"🤔 Ieri avevi in programma <b>%s</b> ma non risulta nessuna attività. "+
				"Tutto ok? Se è saltato dimmelo in chat: meglio riprogrammare che recuperare in fretta.",
			telegram.Escape(name))); err != nil {
			return err
		}
	}
	return nil
}
