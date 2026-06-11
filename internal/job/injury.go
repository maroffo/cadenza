// ABOUTME: Injury wake-ups and escalation: deterministic check-ins at day 2/5/7 (decision 17).
// ABOUTME: The firm physio referral is code, not model: it must fire even with the LLM down.

package job

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/maroffo/cadenza/internal/store"
	"github.com/maroffo/cadenza/internal/task"
	"github.com/maroffo/cadenza/internal/telegram"
)

// injuryCheckDays are the wake-up checkpoints after opening (spec: structural
// pain not improving in 5-7 days goes to the physio, firmly).
var injuryCheckDays = []int{2, 5, 7}

// KeyboardSender renders multi-choice check-ins; satisfied by telegram.Sender.
type KeyboardSender interface {
	SendKeyboard(ctx context.Context, text string, buttons [][2]string) error
}

// InjuryStore is the Firestore truth; satisfied by store.Injuries.
type InjuryStore interface {
	Get(ctx context.Context, id string) (*store.Injury, error)
	ListOpen(ctx context.Context) ([]store.Injury, error)
	RecordFeedback(ctx context.Context, id, feedback string) error
	Resolve(ctx context.Context, id string) error
	AppendLog(ctx context.Context, id, kind, note string) error
}

// feedbackFreshness bounds how long a "Migliora" tap shields the firm
// rungs: a better from day 2 must not silence the day-7 stop.
const feedbackFreshness = 72 * time.Hour

type InjuryJob struct {
	Injuries InjuryStore
	Out      Interactor
	Keyboard KeyboardSender
	Retry    task.DelayedEnqueuer
	Now      func() time.Time
	TZ       *time.Location
}

type injuryPayload struct {
	InjuryID string `json:"injury_id"`
	Day      int    `json:"day"`
	Rev      int    `json:"rev"`
}

// WakeupID names the task: deterministic per injury/day/revision, so lost
// timers heal idempotently and stale revisions die quietly (decision 17).
func WakeupID(injuryID string, day, rev int) string {
	return fmt.Sprintf("%s-day%d-r%d", injuryID, day, rev)
}

// ScheduleWakeup enqueues the day-N check at 08:30 athlete time (after the
// morning verdict, before the day's session).
func (j InjuryJob) ScheduleWakeup(ctx context.Context, inj store.Injury, day int) error {
	if j.Retry == nil {
		slog.Warn("injury: no scheduler wired, wakeup skipped", "injury", inj.ID, "day", day)
		return nil
	}
	payload, err := json.Marshal(injuryPayload{InjuryID: inj.ID, Day: day, Rev: inj.Rev})
	if err != nil {
		return fmt.Errorf("injury: marshal wakeup: %w", err)
	}
	opened := inj.OpenedAt.In(j.TZ)
	at := time.Date(opened.Year(), opened.Month(), opened.Day(), 8, 30, 0, 0, j.TZ).
		AddDate(0, 0, day)
	env := task.Envelope{
		V:       task.EnvelopeVersion,
		Type:    task.TypeInjuryWakeup,
		ID:      WakeupID(inj.ID, day, inj.Rev),
		Payload: payload,
	}
	return j.Retry.EnqueueAt(ctx, env, at)
}

// Wakeup handles a day-N check-in. Stale revisions and resolved injuries
// drop silently (poison: a retry cannot un-resolve an injury).
func (j InjuryJob) Wakeup(ctx context.Context, env task.Envelope) error {
	var p injuryPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return fmt.Errorf("injury: bad wakeup payload: %w", task.ErrPoison)
	}
	inj, err := j.Injuries.Get(ctx, p.InjuryID)
	if err != nil {
		return fmt.Errorf("injury: get: %w", err)
	}
	if inj == nil || inj.Status != "open" || inj.Rev != p.Rev {
		slog.Info("injury: stale wakeup dropped", "id", p.InjuryID, "day", p.Day, "rev", p.Rev)
		return nil
	}

	// Recency-gated: a stale "better" must not shield the firm rungs.
	improving := inj.LastFeedback == "better" &&
		j.Now().Sub(inj.LastFeedbackAt) < feedbackFreshness
	part := telegram.Escape(inj.BodyPart)
	switch {
	case p.Day >= 7 && improving:
		// Positive close-out: the ladder ends here, the exit stays in hand.
		if err := j.Keyboard.SendKeyboard(ctx, fmt.Sprintf(
			"🟢 <b>Giorno %d, l'infortunio (%s) sta migliorando.</b>\n"+
				"Continuiamo il rientro graduale. Quando ti senti a posto, chiudilo qui:",
			p.Day, part), resolveOnlyButtons(inj.ID, inj.Rev)); err != nil {
			return fmt.Errorf("injury: notify: %w", err)
		}
		if err := j.Injuries.AppendLog(ctx, inj.ID, "checkin", fmt.Sprintf("day%d improving", p.Day)); err != nil {
			slog.Warn("injury: log failed", "err", err)
		}
		return nil
	case p.Day >= 7 && !improving:
		// Firm stop: deterministic, fires even with the LLM down. The
		// resolve exit stays available: a post-day-7 injury must never
		// become an unresolvable forever-SKIP.
		if err := j.Keyboard.SendKeyboard(ctx, fmt.Sprintf(
			"🔴 <b>Giorno %d con l'infortunio (%s) e non migliora.</b>\n"+
				"Qui mi fermo io per te: stop al carico sulla zona e "+
				"<b>prenota un fisioterapista questa settimana</b>. Non è negoziabile: "+
				"continuare ad allenarsi su un dolore strutturale che non migliora "+
				"in 5-7 giorni rischia di trasformare 2 settimane di stop in 2 mesi.",
			p.Day, part), resolveOnlyButtons(inj.ID, inj.Rev)); err != nil {
			return fmt.Errorf("injury: notify: %w", err)
		}
		if err := j.Injuries.AppendLog(ctx, inj.ID, "escalation", fmt.Sprintf("day%d fermo+fisio", p.Day)); err != nil {
			slog.Warn("injury: log failed", "err", err)
		}
		return nil
	case p.Day >= 5 && !improving:
		if err := j.Keyboard.SendKeyboard(ctx, fmt.Sprintf(
			"🟡 <b>Giorno %d con l'infortunio (%s).</b>\n"+
				"Se non sta migliorando in modo netto, il protocollo è chiaro: "+
				"<b>fisioterapista</b>, senza aspettare oltre. Come va oggi?",
			p.Day, part), injuryButtons(inj.ID, inj.Rev)); err != nil {
			return fmt.Errorf("injury: notify: %w", err)
		}
	default:
		if err := j.Keyboard.SendKeyboard(ctx, fmt.Sprintf(
			"🩹 <b>Check infortunio (%s), giorno %d.</b>\nCome va rispetto a quando l'abbiamo registrato?",
			part, p.Day), injuryButtons(inj.ID, inj.Rev)); err != nil {
			return fmt.Errorf("injury: notify: %w", err)
		}
	}
	if err := j.Injuries.AppendLog(ctx, inj.ID, "checkin", fmt.Sprintf("day%d", p.Day)); err != nil {
		slog.Warn("injury: log failed", "err", err)
	}
	// Chain the next checkpoint. Tolerate-and-warn: the message already
	// went out, and a hard error would re-send it on retry; the daily
	// reconcile heals a lost chain within a day.
	for _, d := range injuryCheckDays {
		if d > p.Day {
			if err := j.ScheduleWakeup(ctx, *inj, d); err != nil {
				slog.Warn("injury: chain schedule failed, reconcile will heal", "day", d, "err", err)
			}
			break
		}
	}
	return nil
}

// ReactToFeedback owns the escalation policy for athlete taps: one place
// for wording, logging and thresholds (the wakeup ladder is the other rung
// of the same policy, in this same file).
func (j InjuryJob) ReactToFeedback(ctx context.Context, id, action string) (string, error) {
	switch action {
	case "resolve":
		if err := j.Injuries.Resolve(ctx, id); err != nil {
			return "", err
		}
		return "💪 Segnato come risolto: bentornato. Riprendiamo gradualmente, non da dove avevamo lasciato.", nil
	case "worse":
		if err := j.Injuries.RecordFeedback(ctx, id, action); err != nil {
			return "", err
		}
		if err := j.Injuries.AppendLog(ctx, id, "escalation", "peggioramento riferito"); err != nil {
			slog.Warn("injury: log failed", "err", err)
		}
		return "🔴 Se peggiora, si cambia piano: stop al carico sulla zona e " +
			"<b>fisioterapista</b>. Mai allenarsi attraverso un dolore strutturale che peggiora.", nil
	case "better":
		if err := j.Injuries.RecordFeedback(ctx, id, action); err != nil {
			return "", err
		}
		return "📈 Bene. Continuiamo a monitorare: prudenza ancora per qualche giorno.", nil
	case "same":
		if err := j.Injuries.RecordFeedback(ctx, id, action); err != nil {
			return "", err
		}
		return "😐 Registrato. Se al prossimo check non migliora, fisioterapista senza aspettare.", nil
	}
	return "", fmt.Errorf("azione %q sconosciuta: %w", action, task.ErrPoison)
}

func injuryButtons(id string, rev int) [][2]string {
	prefix := fmt.Sprintf("inj:%s:%d:", id, rev)
	return [][2]string{
		{"📈 Migliora", prefix + "better"},
		{"😐 Uguale", prefix + "same"},
		{"📉 Peggio", prefix + "worse"},
		{"✅ Risolto", prefix + "resolve"},
	}
}

func resolveOnlyButtons(id string, rev int) [][2]string {
	return [][2]string{{"✅ Risolto", fmt.Sprintf("inj:%s:%d:resolve", id, rev)}}
}

// Reconcile heals lost timers daily: future checkpoints are re-enqueued
// (named tasks make duplicates impossible), and an OVERDUE final checkpoint
// fires as an immediate catch-up under a distinct name: a hand-deleted
// day-7 task must not silently cancel the firm stop. One broken injury
// never blocks healing for the others.
func (j InjuryJob) Reconcile(ctx context.Context) error {
	open, err := j.Injuries.ListOpen(ctx)
	if err != nil {
		return fmt.Errorf("injury reconcile: %w", err)
	}
	now := j.Now()
	var failed []string
	for _, inj := range open {
		lastDay := injuryCheckDays[len(injuryCheckDays)-1]
		for _, d := range injuryCheckDays {
			due := inj.OpenedAt.Add(time.Duration(d) * 24 * time.Hour)
			switch {
			case due.After(now):
				if err := j.ScheduleWakeup(ctx, inj, d); err != nil {
					slog.Warn("injury reconcile: schedule failed", "injury", inj.ID, "day", d, "err", err)
					failed = append(failed, inj.ID)
				}
			case d == lastDay && j.Retry != nil:
				// Final checkpoint overdue and the injury is still open:
				// catch up NOW. Distinct per-day name: executed task names
				// cannot be reused, and dedup still holds per calendar day.
				payload, _ := json.Marshal(injuryPayload{InjuryID: inj.ID, Day: d, Rev: inj.Rev})
				env := task.Envelope{
					V: task.EnvelopeVersion, Type: task.TypeInjuryWakeup,
					ID:      fmt.Sprintf("%s-catchup-%s", WakeupID(inj.ID, d, inj.Rev), now.In(j.TZ).Format("2006-01-02")),
					Payload: payload,
				}
				if err := j.Retry.EnqueueAt(ctx, env, now); err != nil {
					slog.Warn("injury reconcile: catchup failed", "injury", inj.ID, "err", err)
					failed = append(failed, inj.ID)
				}
			}
		}
	}
	slog.Info("injury reconcile: done", "open", len(open), "failed", len(failed))
	if len(failed) > 0 {
		return fmt.Errorf("injury reconcile: %d injuries failed scheduling", len(failed))
	}
	return nil
}
