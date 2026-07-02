// ABOUTME: The deterministic morning check: wellness -> verdict -> Telegram, no LLM in M2.
// ABOUTME: Idempotent per date via the runs store; icu failures surface as errors so callers retry.

package job

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/maroffo/cadenza/internal/agent"
	"github.com/maroffo/cadenza/internal/exercises"
	"github.com/maroffo/cadenza/internal/icu"
	"github.com/maroffo/cadenza/internal/store"
	"github.com/maroffo/cadenza/internal/task"
	"github.com/maroffo/cadenza/internal/telegram"
	"github.com/maroffo/cadenza/internal/verdict"
)

// MorningNarrator produces the coaching prose; satisfied by agent.Narrator.
// Nil keeps the deterministic skeleton (and IS the degraded mode).
type MorningNarrator interface {
	MorningNarrative(ctx context.Context, in agent.NarrativeInput) (string, error)
}

// SessionStore persists exchanges in the cadenza-owned schema (decision 11).
type SessionStore interface {
	Create(ctx context.Context, mode string, now time.Time) (string, error)
	AppendTurn(ctx context.Context, sessionID string, seq int, role, content, model string) error
}

// WellnessSource provides typed wellness days for a date range (inclusive).
type WellnessSource interface {
	WellnessRange(ctx context.Context, oldest, newest string) ([]icu.Wellness, error)
}

// ProfileSource provides the athlete's baselines and tunable ramp cap.
type ProfileSource interface {
	Profile(ctx context.Context) (verdict.Baselines, float64, error)
	Identity(ctx context.Context) (store.Identity, error)
}

// Messenger delivers coaching messages; satisfied by telegram.Sender.
type Messenger interface {
	SendWithVerdict(ctx context.Context, body string, v verdict.Verdict) error
	Send(ctx context.Context, body string) error
}

// RunStore tracks per-date job state for idempotency and the watchdog.
type RunStore interface {
	// MorningCompleted is true only for a terminal run (message sent).
	MorningCompleted(ctx context.Context, date string) (bool, error)
	// MorningAlive is true when ANY run state exists, including a deferral:
	// the watchdog must stay quiet while a retry is in flight.
	MorningAlive(ctx context.Context, date string) (bool, error)
	MarkMorningCompleted(ctx context.Context, date, status string) error
	MarkMorningDeferred(ctx context.Context, date string, attempt int) error
}

const (
	// MaxMorningRetries bounds the HRV self-retry chain (decision 21):
	// attempts 0..MaxMorningRetries-1 may defer; the last always sends.
	MaxMorningRetries = 2
	MorningRetryDelay = 45 * time.Minute
)

// OpenInjuries feeds the injury_active verdict rule; nil = no registry yet.
type OpenInjuries interface {
	ListOpen(ctx context.Context) ([]store.Injury, error)
}

// CheckinSource reads the day's subjective taps; satisfied by store.Checkins.
type CheckinSource interface {
	Get(ctx context.Context, date string) (store.Checkin, error)
}

type Morning struct {
	Wellness WellnessSource
	Profiles ProfileSource
	Out      Messenger
	Runs     RunStore
	Injuries OpenInjuries
	// Events adds the "in programma oggi" line; nil skips it.
	Events EventsSource
	// Checkins + Keyboard power the 2-tap subjective check-in (M9.4).
	Checkins CheckinSource
	Keyboard KeyboardSender
	// Debrief piggybacks the morning run (M9.5); nil skips.
	Debrief *Debrief
	// Exercises powers the daily prevention/strength routine appended to the
	// morning message; nil skips the block entirely.
	Exercises *exercises.Catalog
	// Equipment restricts the routine to the athlete's standing home kit
	// (empty = full kit). Body weight is always allowed.
	Equipment []string
	// Retry schedules the +45min self-retry when today's HRV has not synced
	// yet. Nil disables deferral: the message goes out with data gaps.
	Retry task.DelayedEnqueuer
	// Narrator adds the M4 prose; nil = deterministic skeleton only.
	Narrator MorningNarrator
	// Sessions records the exchange; nil or failing never blocks the send.
	Sessions  SessionStore
	ModelName string
	Now       func() time.Time
	TZ        *time.Location
}

// morningPayload travels in retry envelopes; the Scheduler's static body has
// no payload, which decodes as attempt 0.
type morningPayload struct {
	Attempt int `json:"attempt"`
}

const dateLayout = "2006-01-02"

func (m Morning) Run(ctx context.Context) error {
	return m.RunAttempt(ctx, 0)
}

func (m Morning) RunAttempt(ctx context.Context, attempt int) error {
	today := m.Now().In(m.TZ).Format(dateLayout)

	done, err := m.Runs.MorningCompleted(ctx, today)
	if err != nil {
		return fmt.Errorf("morning: completion check: %w", err)
	}
	if done {
		slog.Info("morning: already completed, no-op", "date", today)
		return nil
	}

	data, in, err := m.prepare(ctx, today)
	if err != nil {
		// No degraded message here: the caller's retry policy gets its shot
		// first; the watchdog covers persistent absence (decision 16).
		return err
	}

	// HRV not synced yet: defer once or twice rather than coaching on gaps
	// at 09:00 sharp. The deferred run doc keeps the watchdog quiet; the
	// terminal attempt always sends, so the worst case is a late message,
	// never a silent morning.
	if in.Today.HRV == nil && attempt < MaxMorningRetries && m.Retry != nil {
		next := attempt + 1
		if err := m.Runs.MarkMorningDeferred(ctx, today, next); err != nil {
			return fmt.Errorf("morning: mark deferred: %w", err)
		}
		payload, err := json.Marshal(morningPayload{Attempt: next})
		if err != nil {
			return fmt.Errorf("morning: marshal retry payload: %w", err)
		}
		env := task.Envelope{
			V:       task.EnvelopeVersion,
			Type:    task.TypeMorningCheck,
			ID:      fmt.Sprintf("morning-%s-r%d", today, next),
			Payload: payload,
		}
		if err := m.Retry.EnqueueAt(ctx, env, m.Now().Add(MorningRetryDelay)); err != nil {
			return fmt.Errorf("morning: schedule retry: %w", err)
		}
		slog.Info("morning: HRV not synced, deferred", "date", today, "attempt", next)
		return nil
	}

	v := verdict.Compute(in, verdict.DefaultRules())
	body := telegram.MorningBody(data)
	full, narrative, status := m.narrate(ctx, today, body, v)

	// The prevention/strength routine is a deterministic appendix: it rides
	// after the coach prose but before the verdict footer, and is deliberately
	// NOT fed to the narrator (its M4 contract is numbers + verdict, not sets).
	if routine := m.routineBlock(today); routine != "" {
		full += "\n\n" + routine
	}

	if err := m.Out.SendWithVerdict(ctx, full, v); err != nil {
		return fmt.Errorf("morning: send: %w", err)
	}
	// Persist only what was actually delivered: undelivered narratives must
	// never surface as conversation history in M5.
	if narrative != "" {
		m.persistExchange(ctx, body, narrative)
	}
	if err := m.Runs.MarkMorningCompleted(ctx, today, status); err != nil {
		// The message went out; each retry resends until the mark sticks,
		// bounded by the caller's max-attempts (accepted trade-off).
		return fmt.Errorf("morning: mark completed: %w", err)
	}
	// 2-tap check-in (M9.4): best-effort, after the completion mark so a
	// keyboard failure can never resend the morning message on retry.
	if m.Keyboard != nil {
		if err := m.Keyboard.SendKeyboard(ctx, "🙋 Come ti senti stamattina?", checkinFeelButtons(today)); err != nil {
			slog.Warn("morning: checkin keyboard failed", "err", err)
		}
	}
	// Debrief piggyback (M9.5): yesterday's evening workouts get their
	// debrief now; the sweep is Create-once so the 19:30 run never repeats.
	if m.Debrief != nil {
		if err := m.Debrief.Sweep(ctx); err != nil {
			slog.Warn("morning: debrief sweep failed", "err", err)
		}
	}
	return nil
}

func checkinFeelButtons(date string) [][2]string {
	prefix := "ci:" + date + ":feel:"
	return [][2]string{
		{"💪 Bene", prefix + "bene"},
		{"😐 Così così", prefix + "cosi"},
		{"🥱 Stanco", prefix + "stanco"},
		{"🤕 Dolorante", prefix + "dolorante"},
	}
}

func checkinTimeButtons(date string) [][2]string {
	prefix := "ci:" + date + ":time:"
	return [][2]string{
		{"⏱ 60-75'", prefix + "full"},
		{"⏳ 30-45'", prefix + "short"},
		{"🚫 Niente oggi", prefix + "none"},
	}
}

// narrate wraps the deterministic body with the coach prose. Narrative
// failure NEVER fails the morning: the athlete gets the raw numbers and the
// verdict with an honest notice (decision 16), and the run records degraded.
func (m Morning) narrate(ctx context.Context, today, body string, v verdict.Verdict) (full, narrative, status string) {
	if m.Narrator == nil {
		return body, "", string(v.Kind)
	}
	raw, err := m.Narrator.MorningNarrative(ctx, agent.NarrativeInput{
		Date: today, Body: body, Verdict: v,
	})
	if err != nil {
		// Warn, not Error: the athlete still gets a message; the email
		// alert (severity>=ERROR) is reserved for silent mornings.
		slog.Warn("morning: narrative failed, sending degraded", "err", err)
		return telegram.DegradedLLMDown() + "\n\n" + body, "", string(v.Kind) + "-degraded"
	}
	// Markup contract enforced in code: prompts are wishes (decision 15 doctrine).
	narrative = telegram.SanitizeNarrative(raw)
	return narrative + "\n\n" + body, narrative, string(v.Kind)
}

// routinePerGroup is how many exercises each muscle group contributes to the
// morning routine (arms/legs/back/core -> 8 total).
const routinePerGroup = 2

// routineBlock renders the day's prevention/strength routine, or "" when no
// catalog is wired. The seed is the local epoch-day so the picks rotate once
// per calendar day and are identical across restarts and the +45min retries.
func (m Morning) routineBlock(today string) string {
	if m.Exercises == nil {
		return ""
	}
	seed := 0
	if day, err := time.ParseInLocation(dateLayout, today, m.TZ); err == nil {
		seed = int(day.Unix() / 86400)
	}
	picks := m.Exercises.DailyRoutine(seed, routinePerGroup, m.Equipment)
	groups := make([]telegram.RoutineGroup, 0, len(picks))
	for _, p := range picks {
		exs := make([]telegram.RoutineExercise, 0, len(p.Exercises))
		for _, e := range p.Exercises {
			exs = append(exs, telegram.RoutineExercise{Name: e.Name, Equipment: e.Equipment})
		}
		groups = append(groups, telegram.RoutineGroup{Label: p.Label, Exercises: exs})
	}
	return telegram.MorningRoutine(groups)
}

// persistExchange records the morning as a session; best-effort by design.
func (m Morning) persistExchange(ctx context.Context, userContext, narrative string) {
	if m.Sessions == nil {
		return
	}
	id, err := m.Sessions.Create(ctx, "morning", m.Now())
	if err != nil {
		slog.Warn("morning: session create failed", "err", err)
		return
	}
	if err := m.Sessions.AppendTurn(ctx, id, 1, "user", userContext, ""); err != nil {
		slog.Warn("morning: session turn failed", "err", err)
		return
	}
	if err := m.Sessions.AppendTurn(ctx, id, 2, "assistant", narrative, m.ModelName); err != nil {
		slog.Warn("morning: session turn failed", "err", err)
	}
}

// Compose builds the morning body and verdict without side effects. The
// morning job sends and marks; /status (M3) sends without marking.
func (m Morning) Compose(ctx context.Context) (string, verdict.Verdict, error) {
	today := m.Now().In(m.TZ).Format(dateLayout)
	data, in, err := m.prepare(ctx, today)
	if err != nil {
		return "", verdict.Verdict{}, err
	}
	v := verdict.Compute(in, verdict.DefaultRules())
	return telegram.MorningBody(data), v, nil
}

// prepare fetches and assembles; the date comes from the caller so Run marks
// exactly the date the message describes, even across a midnight boundary.
func (m Morning) prepare(ctx context.Context, today string) (telegram.MorningData, verdict.Input, error) {
	baselines, rampCap, err := m.Profiles.Profile(ctx)
	if err != nil {
		return telegram.MorningData{}, verdict.Input{}, fmt.Errorf("morning: profile: %w", err)
	}

	day, err := time.ParseInLocation(dateLayout, today, m.TZ)
	if err != nil {
		return telegram.MorningData{}, verdict.Input{}, fmt.Errorf("morning: bad date %q: %w", today, err)
	}
	oldest := day.AddDate(0, 0, -7).Format(dateLayout)
	days, err := m.Wellness.WellnessRange(ctx, oldest, today)
	if err != nil {
		return telegram.MorningData{}, verdict.Input{}, fmt.Errorf("morning: wellness fetch: %w", err)
	}

	data, in := assemble(today, days, baselines, rampCap)
	if m.Events != nil {
		if events, err := m.Events.EventsRange(ctx, today, today); err != nil {
			slog.Warn("morning: events unavailable", "err", err)
		} else {
			for _, e := range events {
				if e.Category == "WORKOUT" && e.Name != nil {
					data.PlannedToday = append(data.PlannedToday, *e.Name)
				}
			}
		}
	}
	if m.Checkins != nil {
		ci, err := m.Checkins.Get(ctx, today)
		if err != nil {
			slog.Warn("morning: checkin unavailable", "err", err)
		} else {
			applyCheckin(&data, &in, ci)
		}
	}
	if m.Injuries != nil {
		open, err := m.Injuries.ListOpen(ctx)
		if err != nil {
			// Conservative degrade: missing registry must not hide an injury.
			slog.Warn("morning: injuries unavailable", "err", err)
			in.DataGapInjuries = true
		}
		for _, inj := range open {
			in.Injuries = append(in.Injuries, verdict.ActiveInjury{BodyPart: inj.BodyPart, Pain: inj.Pain})
		}
	}
	return data, in, nil
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

// applyCheckin folds the athlete's taps into the day: gap-fill ONLY (device
// data wins when present) and conservative-only via the D32 rules.
func applyCheckin(data *telegram.MorningData, in *verdict.Input, ci store.Checkin) {
	data.CheckinFeeling = ci.Feeling
	data.CheckinTime = ci.TimeBudget
	three := 3
	switch ci.Feeling {
	case "stanco":
		if in.Today.Fatigue == nil {
			in.Today.Fatigue = &three
		}
	case "dolorante":
		if in.Today.Soreness == nil {
			in.Today.Soreness = &three
		}
	}
}

func toVerdictDay(w icu.Wellness) verdict.Day {
	return verdict.Day{
		Date:       w.ID,
		HRV:        w.HRV,
		RestingHR:  w.RestingHR,
		SleepSecs:  w.SleepSecs,
		RampRate:   w.RampRate,
		CTL:        w.CTL,
		ATL:        w.ATL,
		Readiness:  w.Readiness,
		SleepScore: w.SleepScore,
		SpO2:       w.SpO2,
		Soreness:   w.Soreness,
		Fatigue:    w.Fatigue,
		InjuryFeel: w.Injury,
	}
}
