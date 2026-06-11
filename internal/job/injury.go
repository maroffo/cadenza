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

// ScheduleWakeup enqueues the day-N check for an injury.
func (j InjuryJob) ScheduleWakeup(ctx context.Context, inj store.Injury, day int) error {
	if j.Retry == nil {
		return nil
	}
	payload, err := json.Marshal(injuryPayload{InjuryID: inj.ID, Day: day, Rev: inj.Rev})
	if err != nil {
		return fmt.Errorf("injury: marshal wakeup: %w", err)
	}
	at := inj.OpenedAt.Add(time.Duration(day) * 24 * time.Hour)
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

	improving := inj.LastFeedback == "better"
	switch {
	case p.Day >= 7 && !improving:
		// Firm stop: deterministic, fires even with the LLM down.
		if err := j.Out.Send(ctx, fmt.Sprintf(
			"🔴 <b>Giorno %d con l'infortunio (%s) e non migliora.</b>\n"+
				"Qui mi fermo io per te: stop al carico sulla zona e "+
				"<b>prenota un fisioterapista questa settimana</b>. Non è negoziabile: "+
				"continuare ad allenarsi su un dolore strutturale che non migliora "+
				"in 5-7 giorni è il modo classico per trasformare 2 settimane di stop in 2 mesi.",
			p.Day, inj.BodyPart)); err != nil {
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
			p.Day, inj.BodyPart), injuryButtons(inj.ID)); err != nil {
			return fmt.Errorf("injury: notify: %w", err)
		}
	default:
		if err := j.Keyboard.SendKeyboard(ctx, fmt.Sprintf(
			"🩹 <b>Check infortunio (%s), giorno %d.</b>\nCome va rispetto a quando l'abbiamo registrato?",
			inj.BodyPart, p.Day), injuryButtons(inj.ID)); err != nil {
			return fmt.Errorf("injury: notify: %w", err)
		}
	}
	if err := j.Injuries.AppendLog(ctx, inj.ID, "checkin", fmt.Sprintf("day%d", p.Day)); err != nil {
		slog.Warn("injury: log failed", "err", err)
	}
	// Chain the next checkpoint.
	for _, d := range injuryCheckDays {
		if d > p.Day {
			if err := j.ScheduleWakeup(ctx, *inj, d); err != nil {
				return fmt.Errorf("injury: schedule next: %w", err)
			}
			break
		}
	}
	return nil
}

func injuryButtons(id string) [][2]string {
	return [][2]string{
		{"📈 Migliora", "inj:" + id + ":better"},
		{"😐 Uguale", "inj:" + id + ":same"},
		{"📉 Peggio", "inj:" + id + ":worse"},
		{"✅ Risolto", "inj:" + id + ":resolve"},
	}
}

// Reconcile heals lost timers daily: for every open injury it re-enqueues
// every future checkpoint; named tasks make duplicates impossible
// (AlreadyExists is success). A hand-deleted task comes back within a day.
func (j InjuryJob) Reconcile(ctx context.Context) error {
	open, err := j.Injuries.ListOpen(ctx)
	if err != nil {
		return fmt.Errorf("injury reconcile: %w", err)
	}
	now := j.Now()
	for _, inj := range open {
		for _, d := range injuryCheckDays {
			due := inj.OpenedAt.Add(time.Duration(d) * 24 * time.Hour)
			if due.After(now) {
				if err := j.ScheduleWakeup(ctx, inj, d); err != nil {
					return fmt.Errorf("injury reconcile %s day%d: %w", inj.ID, d, err)
				}
			}
		}
	}
	slog.Info("injury reconcile: done", "open", len(open))
	return nil
}
