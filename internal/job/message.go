// ABOUTME: Handles telegram_update envelopes: commands, callbacks, dedup, allowlist defense.
// ABOUTME: No LLM in M3: /status replays the deterministic morning, free text gets an honest notice.

package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

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

type Message struct {
	AllowedUserID int64
	Dedup         DedupStore
	Chats         ChatStore
	Out           Interactor
	Status        StatusComposer
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
	default:
		// Honest placeholder: conversational coaching arrives with M4/M5.
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
	slog.Info("message: unhandled callback data", "data", u.CallbackQuery.Data)
	return nil
}
