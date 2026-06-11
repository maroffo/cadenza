// ABOUTME: Telegram webhook: thin enqueuer. Validate secret, filter sender, enqueue, ack fast.
// ABOUTME: No work after the ack: request-based billing kills post-response CPU (decision 7).

package server

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/maroffo/cadenza/internal/task"
)

type Webhook struct {
	Secret        string // X-Telegram-Bot-Api-Secret-Token set via setWebhook
	AllowedUserID int64
	Enqueue       task.Enqueuer
}

const maxUpdateBytes = 1 << 20

// webhookUpdate is the minimal pre-parse: enough to build the dedup id and
// apply the allowlist. The full payload travels in the envelope.
type webhookUpdate struct {
	UpdateID int64 `json:"update_id"`
	Message  *struct {
		From struct {
			ID int64 `json:"id"`
		} `json:"from"`
	} `json:"message"`
	CallbackQuery *struct {
		From struct {
			ID int64 `json:"id"`
		} `json:"from"`
	} `json:"callback_query"`
}

func (h *Webhook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.Secret == "" {
		// Fail closed, same doctrine as the executor.
		slog.Error("webhook: refusing to serve without a secret token configured")
		http.Error(w, "webhook not configured", http.StatusServiceUnavailable)
		return
	}
	got := r.Header.Get("X-Telegram-Bot-Api-Secret-Token")
	if subtle.ConstantTimeCompare([]byte(got), []byte(h.Secret)) != 1 {
		// Not Telegram: a 403 is fine, real deliveries always carry the secret.
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxUpdateBytes))
	if err != nil {
		// Oversized or broken stream: 200, or Telegram redelivers for 24h.
		slog.Error("webhook: body read failed", "err", err)
		w.WriteHeader(http.StatusOK)
		return
	}

	var u webhookUpdate
	if err := json.Unmarshal(body, &u); err != nil || u.UpdateID == 0 {
		slog.Error("webhook: malformed update dropped", "err", err)
		w.WriteHeader(http.StatusOK)
		return
	}

	// Allowlist: anyone can find a bot by username; only the athlete exists
	// here. Rejects MUST still return 200 or Telegram retries for 24h.
	if from, ok := senderID(&u); !ok || from != h.AllowedUserID {
		slog.Warn("webhook: update from non-allowed sender dropped", "update_id", u.UpdateID)
		w.WriteHeader(http.StatusOK)
		return
	}

	env := task.Envelope{
		V:       task.EnvelopeVersion,
		Type:    task.TypeTelegramUpdate,
		ID:      task.TelegramUpdateID(u.UpdateID),
		Payload: body,
	}
	if err := h.Enqueue.Enqueue(r.Context(), env); err != nil {
		// Durable path unavailable: non-200 makes Telegram redeliver later.
		slog.Error("webhook: enqueue failed", "update_id", u.UpdateID, "err", err)
		http.Error(w, "enqueue failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func senderID(u *webhookUpdate) (int64, bool) {
	switch {
	case u.Message != nil:
		return u.Message.From.ID, true
	case u.CallbackQuery != nil:
		return u.CallbackQuery.From.ID, true
	default:
		return 0, false
	}
}
