// ABOUTME: The deterministic morning check: wellness -> verdict -> Telegram, no LLM in M2.
// ABOUTME: Idempotent per date via the runs store; icu failures surface as errors so callers retry.

package job

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/maroffo/cadenza/internal/icu"
	"github.com/maroffo/cadenza/internal/telegram"
	"github.com/maroffo/cadenza/internal/verdict"
)

// WellnessSource provides typed wellness days for a date range (inclusive).
type WellnessSource interface {
	WellnessRange(ctx context.Context, oldest, newest string) ([]icu.Wellness, error)
}

// ProfileSource provides the athlete's baselines and tunable ramp cap.
type ProfileSource interface {
	Profile(ctx context.Context) (verdict.Baselines, float64, error)
}

// Messenger delivers coaching messages; satisfied by telegram.Sender.
type Messenger interface {
	SendWithVerdict(ctx context.Context, body string, v verdict.Verdict) error
	Send(ctx context.Context, body string) error
}

// RunStore tracks per-date job completion for idempotency.
type RunStore interface {
	MorningCompleted(ctx context.Context, date string) (bool, error)
	MarkMorningCompleted(ctx context.Context, date, status string) error
}

type Morning struct {
	Wellness WellnessSource
	Profiles ProfileSource
	Out      Messenger
	Runs     RunStore
	Now      func() time.Time
	TZ       *time.Location
}

const dateLayout = "2006-01-02"

func (m Morning) Run(ctx context.Context) error {
	today := m.Now().In(m.TZ).Format(dateLayout)

	done, err := m.Runs.MorningCompleted(ctx, today)
	if err != nil {
		return fmt.Errorf("morning: completion check: %w", err)
	}
	if done {
		slog.Info("morning: already completed, no-op", "date", today)
		return nil
	}

	body, v, err := m.composeFor(ctx, today)
	if err != nil {
		// No degraded message here: the caller's retry policy gets its shot
		// first; the watchdog covers persistent absence (decision 16).
		return err
	}

	if err := m.Out.SendWithVerdict(ctx, body, v); err != nil {
		return fmt.Errorf("morning: send: %w", err)
	}
	if err := m.Runs.MarkMorningCompleted(ctx, today, string(v.Kind)); err != nil {
		// The message went out; each retry resends until the mark sticks,
		// bounded by the caller's max-attempts (accepted trade-off).
		return fmt.Errorf("morning: mark completed: %w", err)
	}
	return nil
}

// Compose builds the morning body and verdict without side effects. The
// morning job sends and marks; /status (M3) sends without marking.
func (m Morning) Compose(ctx context.Context) (string, verdict.Verdict, error) {
	return m.composeFor(ctx, m.Now().In(m.TZ).Format(dateLayout))
}

// composeFor takes the date from the caller so Run marks exactly the date
// the message describes, even across a midnight boundary.
func (m Morning) composeFor(ctx context.Context, today string) (string, verdict.Verdict, error) {
	baselines, rampCap, err := m.Profiles.Profile(ctx)
	if err != nil {
		return "", verdict.Verdict{}, fmt.Errorf("morning: profile: %w", err)
	}

	day, err := time.ParseInLocation(dateLayout, today, m.TZ)
	if err != nil {
		return "", verdict.Verdict{}, fmt.Errorf("morning: bad date %q: %w", today, err)
	}
	oldest := day.AddDate(0, 0, -7).Format(dateLayout)
	days, err := m.Wellness.WellnessRange(ctx, oldest, today)
	if err != nil {
		return "", verdict.Verdict{}, fmt.Errorf("morning: wellness fetch: %w", err)
	}

	data, in := assemble(today, days, baselines, rampCap)
	v := verdict.Compute(in, verdict.DefaultRules())
	return telegram.MorningBody(data), v, nil
}

// assemble maps wellness days onto the message data and the verdict input.
// If today's record is absent, the freshest older day is shown, labeled
// stale, and the verdict input keeps today empty so missing-data rules fire.
// The window is sorted and deduplicated here: the consecutive-low-HRV SKIP
// escalation in verdict.Compute assumes chronological order, and the API's
// response ordering is not a documented contract we may lean on.
func assemble(today string, days []icu.Wellness, baselines verdict.Baselines, rampCap float64) (telegram.MorningData, verdict.Input) {
	var todayW *icu.Wellness
	var window []verdict.Day
	var latest *icu.Wellness
	seen := make(map[string]bool, len(days))

	for i := range days {
		d := &days[i]
		switch {
		case d.ID == today:
			todayW = d
		case d.ID < today && !seen[d.ID]:
			seen[d.ID] = true
			window = append(window, toVerdictDay(*d))
			if latest == nil || d.ID > latest.ID {
				latest = d
			}
		}
	}
	slices.SortFunc(window, func(a, b verdict.Day) int {
		return strings.Compare(a.Date, b.Date)
	})

	in := verdict.Input{
		Today:     verdict.Day{Date: today},
		Window:    window,
		Baselines: baselines,
		RampCap:   rampCap,
	}
	data := telegram.MorningData{Date: today}

	display := todayW
	if todayW != nil {
		in.Today = toVerdictDay(*todayW)
	} else if latest != nil {
		display = latest
		data.Stale = true
		data.StaleAsOf = latest.ID
	}
	if display != nil {
		data.HRV = display.HRV
		data.RestingHR = display.RestingHR
		data.SleepSecs = display.SleepSecs
		data.CTL = display.CTL
		data.ATL = display.ATL
		data.RampRate = display.RampRate
	}
	return data, in
}

func toVerdictDay(w icu.Wellness) verdict.Day {
	return verdict.Day{
		Date:      w.ID,
		HRV:       w.HRV,
		RestingHR: w.RestingHR,
		SleepSecs: w.SleepSecs,
		RampRate:  w.RampRate,
		CTL:       w.CTL,
		ATL:       w.ATL,
	}
}
