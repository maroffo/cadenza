// ABOUTME: Handles telegram_update envelopes: commands, callbacks, dedup, allowlist defense.
// ABOUTME: No LLM in M3: /status replays the deterministic morning, free text gets an honest notice.

package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/maroffo/cadenza/internal/store"
	"github.com/maroffo/cadenza/internal/task"
	"github.com/maroffo/cadenza/internal/verdict"
)

// DedupStore reserves side-effect keys; satisfied by store.Dedup. Release
// compensates failures after Reserve so redeliveries are not silently lost.
type DedupStore interface {
	Reserve(ctx context.Context, key string, ttl time.Duration) (bool, error)
	Release(ctx context.Context, key string) error
}

// ChatStore persists the chat id at /start; satisfied by store.Chats.
type ChatStore interface {
	Save(ctx context.Context, chatID, userID int64) error
}

// Interactor extends Messenger with the callback/button surface.
type Interactor interface {
	Messenger
	AnswerCallback(ctx context.Context, callbackID string) error
	SendWithButton(ctx context.Context, text, buttonLabel, callbackData string) error
}

// StatusComposer builds the deterministic daily picture without side
// effects; satisfied by Morning. The M4 conversational flows depend on this
// seam, never on the cron job itself.
type StatusComposer interface {
	Compose(ctx context.Context) (string, verdict.Verdict, error)
}

// MutationResolver flips proposals on athlete taps; satisfied by store.Mutations.
type MutationResolver interface {
	Resolve(ctx context.Context, id string, approve bool) (*store.Mutation, error)
}

type Message struct {
	AllowedUserID int64
	Dedup         DedupStore
	Chats         ChatStore
	Out           Interactor
	Status        StatusComposer
	// Coach handles free text (M5); nil keeps the honest not-yet notice.
	Coach *Coach
	// Muts resolves pm: confirmation callbacks; nil ignores them.
	Muts MutationResolver
	// InjuryFlow handles inj: check-in callbacks; nil ignores them.
	InjuryFlow *InjuryJob
	// Checkins records ci: morning taps; nil ignores them.
	Checkins CheckinRecorder
	// Keyboard chains the second check-in question.
	Keyboard KeyboardSender
	// WebLink mints dashboard magic links for /web; nil = feature off.
	WebLink func() string
}

const dedupTTL = 7 * 24 * time.Hour

// tgUpdate models only the fields cadenza reads. Two producers feed it: the
// webhook (raw Telegram body) and dev polling (marshaled go-telegram update);
// the contract test in message_test pins that both decode here.
type tgUpdate struct {
	UpdateID int64 `json:"update_id"`
	Message  *struct {
		Text string `json:"text"`
		From struct {
			ID int64 `json:"id"`
		} `json:"from"`
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
	} `json:"message"`
	CallbackQuery *struct {
		ID   string `json:"id"`
		Data string `json:"data"`
		From struct {
			ID int64 `json:"id"`
		} `json:"from"`
	} `json:"callback_query"`
}

func (m Message) Run(ctx context.Context, env task.Envelope) error {
	owned, err := m.Dedup.Reserve(ctx, env.ID, dedupTTL)
	if err != nil {
		return fmt.Errorf("message: dedup: %w", err)
	}
	if !owned {
		slog.Info("message: duplicate update, no-op", "id", env.ID)
		return nil
	}

	err = m.handle(ctx, env)
	if err != nil && !errors.Is(err, task.ErrPoison) {
		// Transient failure after the reservation: release it, or the
		// redelivery would no-op and the update would be lost forever.
		if rerr := m.Dedup.Release(ctx, env.ID); rerr != nil {
			slog.Error("message: release after failure", "id", env.ID, "err", rerr)
		}
	}
	return err
}

func (m Message) handle(ctx context.Context, env task.Envelope) error {
	var u tgUpdate
	if err := json.Unmarshal(env.Payload, &u); err != nil {
		return fmt.Errorf("message: unparseable update %s: %w", env.ID, task.ErrPoison)
	}

	switch {
	case u.CallbackQuery != nil:
		return m.handleCallback(ctx, &u)
	case u.Message != nil:
		return m.handleMessage(ctx, &u)
	default:
		// Update kinds we don't subscribe to (edits, reactions): ignore.
		slog.Info("message: ignoring update kind", "id", env.ID)
		return nil
	}
}

func (m Message) handleMessage(ctx context.Context, u *tgUpdate) error {
	// Defense in depth: the webhook already filters, but the queue could
	// carry anything that got enqueued before a config change. The chat id
	// must match too: in a private chat it equals the user id, and a forged
	// chat id on /start would redirect every future coaching message.
	if u.Message.From.ID != m.AllowedUserID || u.Message.Chat.ID != m.AllowedUserID {
		return fmt.Errorf("message: sender %d / chat %d not allowed: %w",
			u.Message.From.ID, u.Message.Chat.ID, task.ErrPoison)
	}

	switch u.Message.Text {
	case "":
		// Non-text content (photos, stickers): nothing to do, no noise.
		slog.Info("message: ignoring non-text message")
		return nil
	case "/start":
		if err := m.Chats.Save(ctx, u.Message.Chat.ID, u.Message.From.ID); err != nil {
			return fmt.Errorf("message: save chat: %w", err)
		}
		return m.Out.Send(ctx,
			"👋 <b>Cadenza attivo.</b>\n"+
				"Check mattutino automatico alle 07:00.\n"+
				"/status per i numeri di oggi, /test per provare i bottoni.")
	case "/status":
		body, v, err := m.Status.Compose(ctx)
		if err != nil {
			return fmt.Errorf("message: status: %w", err)
		}
		return m.Out.SendWithVerdict(ctx, body, v)
	case "/test":
		return m.Out.SendWithButton(ctx, "Prova di conferma:", "OK ✅", "ping:1")
	case "/web":
		if m.WebLink == nil {
			return m.Out.Send(ctx, "Dashboard non configurata su questo ambiente.")
		}
		// The link IS the credential: single-use, 10 minutes, this chat only.
		return m.Out.Send(ctx, "🔑 Il tuo accesso alla dashboard (singolo uso, vale 10 minuti):\n"+
			m.WebLink())
	default:
		if m.Coach != nil {
			return m.Coach.Converse(ctx, u.Message.Text)
		}
		return m.Out.Send(ctx,
			"Ricevuto. Le risposte del coach arrivano con la prossima versione; "+
				"per ora: /status per il quadro di oggi.")
	}
}

func (m Message) handleCallback(ctx context.Context, u *tgUpdate) error {
	if u.CallbackQuery.From.ID != m.AllowedUserID {
		return fmt.Errorf("message: callback from %d not allowed: %w", u.CallbackQuery.From.ID, task.ErrPoison)
	}
	// Always answer first: an unanswered callback shows a stuck spinner.
	if err := m.Out.AnswerCallback(ctx, u.CallbackQuery.ID); err != nil {
		return err
	}
	if u.CallbackQuery.Data == "ping:1" {
		return m.Out.Send(ctx, "✅ Bottone funzionante, callback chiusa.")
	}
	if id, approve, ok := parseMutationCallback(u.CallbackQuery.Data); ok && m.Muts != nil {
		mut, err := m.Muts.Resolve(ctx, id, approve)
		if err != nil {
			if errors.Is(err, store.ErrMutationInvalid) {
				// Terminal: stale button, wiped data, bad stored value.
				// Retrying the tap cannot fix it; tell the athlete instead.
				slog.Warn("message: invalid mutation callback", "id", id, "err", err)
				return m.Out.Send(ctx, "⚠️ Proposta non più valida (scaduta o non trovata).")
			}
			return fmt.Errorf("message: mutation resolve: %w", err)
		}
		switch mut.Status {
		case "confirmed":
			return m.Out.Send(ctx, "✅ Salvato nel profilo: "+describeMutation(mut)+
				"\nAttivo dalla prossima conversazione.")
		case "rejected":
			return m.Out.Send(ctx, "👍 Scartata, il profilo resta com'era.")
		case "expired":
			return m.Out.Send(ctx, "⏰ Proposta scaduta (oltre 48h): riproponila se serve ancora.")
		default:
			return m.Out.Send(ctx, "Già gestita in precedenza ("+mut.Status+").")
		}
	}
	if id, rev, action, ok := parseInjuryCallback(u.CallbackQuery.Data); ok && m.InjuryFlow != nil {
		// Stale-revision buttons (resolved/reopened meanwhile) die honestly.
		cur, err := m.InjuryFlow.Injuries.Get(ctx, id)
		if err != nil {
			return fmt.Errorf("message: injury get: %w", err)
		}
		if cur == nil || cur.Rev != rev {
			return m.Out.Send(ctx, "⚠️ Questo bottone si riferisce a uno stato superato dell'infortunio.")
		}
		reply, err := m.InjuryFlow.ReactToFeedback(ctx, id, action)
		if err != nil {
			if errors.Is(err, store.ErrInjuryNotFound) {
				return m.Out.Send(ctx, "⚠️ Infortunio non trovato (già rimosso?).")
			}
			return fmt.Errorf("message: injury action: %w", err)
		}
		return m.Out.Send(ctx, reply)
	}
	if date, field, value, ok := parseCheckinCallback(u.CallbackQuery.Data); ok && m.Checkins != nil {
		if err := m.Checkins.SetField(ctx, date, field, value); err != nil {
			return fmt.Errorf("message: checkin: %w", err)
		}
		if field == "feeling" {
			if value == "dolorante" {
				if err := m.Out.Send(ctx, "🤕 Registrato. Dimmi in chat DOVE ti fa male, così lo tracciamo e proteggo il piano."); err != nil {
					return err
				}
			}
			if m.Keyboard != nil {
				return m.Keyboard.SendKeyboard(ctx, "⏱ Quanto tempo hai per allenarti oggi?", checkinTimeButtons(date))
			}
			return nil
		}
		return m.Out.Send(ctx, "👍 Segnato. Il coach ne tiene conto per oggi.")
	}
	slog.Info("message: unhandled callback data", "data", u.CallbackQuery.Data)
	return nil
}

// CheckinRecorder persists morning taps; satisfied by store.Checkins.
type CheckinRecorder interface {
	SetField(ctx context.Context, date, field, value string) error
}

// parseCheckinCallback decodes "ci:<date>:feel|time:<value>".
func parseCheckinCallback(data string) (date, field, value string, ok bool) {
	rest, found := strings.CutPrefix(data, "ci:")
	if !found {
		return "", "", "", false
	}
	parts := strings.Split(rest, ":")
	if len(parts) != 3 {
		return "", "", "", false
	}
	date = parts[0]
	switch parts[1] {
	case "feel":
		field = "feeling"
		switch parts[2] {
		case "bene", "cosi", "stanco", "dolorante":
			return date, field, parts[2], true
		}
	case "time":
		field = "time_budget"
		switch parts[2] {
		case "full", "short", "none":
			return date, field, parts[2], true
		}
	}
	return "", "", "", false
}

// parseInjuryCallback decodes "inj:<id>:<rev>:better|same|worse|resolve".
// The revision rides along so buttons from a superseded state die honestly.
func parseInjuryCallback(data string) (id string, rev int, action string, ok bool) {
	rest, found := strings.CutPrefix(data, "inj:")
	if !found {
		return "", 0, "", false
	}
	lastIdx := strings.LastIndex(rest, ":")
	if lastIdx <= 0 {
		return "", 0, "", false
	}
	action = rest[lastIdx+1:]
	switch action {
	case "better", "same", "worse", "resolve":
	default:
		return "", 0, "", false
	}
	rest = rest[:lastIdx]
	revIdx := strings.LastIndex(rest, ":")
	if revIdx <= 0 {
		return "", 0, "", false
	}
	r, err := strconv.Atoi(rest[revIdx+1:])
	if err != nil || r < 1 {
		return "", 0, "", false
	}
	return rest[:revIdx], r, action, true
}

// parseMutationCallback decodes "pm:<id>:y|n".
func parseMutationCallback(data string) (id string, approve, ok bool) {
	rest, found := strings.CutPrefix(data, "pm:")
	if !found {
		return "", false, false
	}
	id, verdict, found := strings.Cut(rest, ":")
	if !found || id == "" || (verdict != "y" && verdict != "n") {
		return "", false, false
	}
	return id, verdict == "y", true
}

func describeMutation(m *store.Mutation) string {
	switch m.Kind {
	case store.MutationRampCap:
		return "tetto rampa CTL → " + m.NewValue + "/settimana"
	case store.MutationRule:
		return "regola: «" + m.NewValue + "»"
	default:
		return m.Kind + " → " + m.NewValue
	}
}
