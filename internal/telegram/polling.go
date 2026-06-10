// ABOUTME: Dev-only long polling: no tunnel, no webhook, same envelope path as prod.
// ABOUTME: Each update is wrapped exactly like the webhook would and fed to the dispatcher.

package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/maroffo/cadenza/internal/task"
)

// Poll runs getUpdates long polling until ctx is canceled, converting every
// update into the same envelope the webhook produces and dispatching it
// through enq (task.Local in dev). Prod never calls this.
func Poll(ctx context.Context, token string, enq task.Enqueuer) error {
	handler := func(hctx context.Context, _ *bot.Bot, update *models.Update) {
		raw, err := json.Marshal(update)
		if err != nil {
			slog.Error("poll: marshal update", "err", err)
			return
		}
		env := task.Envelope{
			V:       task.EnvelopeVersion,
			Type:    task.TypeTelegramUpdate,
			ID:      task.TelegramUpdateID(update.ID),
			Payload: raw,
		}
		if err := enq.Enqueue(hctx, env); err != nil {
			slog.Error("poll: handle update", "update_id", update.ID, "err", err)
		}
	}

	b, err := bot.New(token, bot.WithDefaultHandler(handler))
	if err != nil {
		return fmt.Errorf("poll: bot: %w", err)
	}
	slog.Info("polling started (dev mode)")
	b.Start(ctx) // blocks until ctx cancellation
	return nil
}
