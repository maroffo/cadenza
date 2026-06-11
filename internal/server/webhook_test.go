// ABOUTME: Tests for the webhook enqueuer: secret gate, allowlist-with-200, enqueue contract.
// ABOUTME: Telegram retries on non-200 for 24h, so every drop path must still ack.

package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/maroffo/cadenza/internal/task"
)

type captureEnqueuer struct {
	envs []task.Envelope
	err  error
}

func (c *captureEnqueuer) Enqueue(_ context.Context, e task.Envelope) error {
	if c.err != nil {
		return c.err
	}
	c.envs = append(c.envs, e)
	return nil
}

func newWebhook(enq task.Enqueuer) *Webhook {
	return &Webhook{Secret: "s3cret", AllowedUserID: 424242, Enqueue: enq}
}

func postUpdate(t *testing.T, h *Webhook, secret, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/telegram/webhook", strings.NewReader(body))
	if secret != "" {
		req.Header.Set("X-Telegram-Bot-Api-Secret-Token", secret)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

const validUpdate = `{"update_id":777,"message":{"text":"/status","from":{"id":424242},"chat":{"id":424242}}}`

func TestWebhook_NoSecretConfiguredFailsClosed(t *testing.T) {
	h := &Webhook{AllowedUserID: 424242, Enqueue: &captureEnqueuer{}}
	if rec := postUpdate(t, h, "anything", validUpdate); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", rec.Code)
	}
}

func TestWebhook_WrongSecretIs403(t *testing.T) {
	enq := &captureEnqueuer{}
	h := newWebhook(enq)
	if rec := postUpdate(t, h, "wrong", validUpdate); rec.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403", rec.Code)
	}
	if len(enq.envs) != 0 {
		t.Fatal("enqueued despite wrong secret")
	}
}

func TestWebhook_ValidUpdateEnqueued(t *testing.T) {
	enq := &captureEnqueuer{}
	h := newWebhook(enq)
	if rec := postUpdate(t, h, "s3cret", validUpdate); rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	if len(enq.envs) != 1 {
		t.Fatalf("enqueued = %d, want 1", len(enq.envs))
	}
	e := enq.envs[0]
	if e.ID != "tg-update-777" || e.Type != task.TypeTelegramUpdate {
		t.Errorf("envelope = %+v", e)
	}
	if !strings.Contains(string(e.Payload), `"/status"`) {
		t.Errorf("payload must carry the full update: %s", e.Payload)
	}
}

func TestWebhook_ForeignSenderDroppedWith200(t *testing.T) {
	enq := &captureEnqueuer{}
	h := newWebhook(enq)
	foreign := `{"update_id":778,"message":{"text":"hi","from":{"id":666},"chat":{"id":666}}}`
	if rec := postUpdate(t, h, "s3cret", foreign); rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (non-200 means 24h of Telegram retries)", rec.Code)
	}
	if len(enq.envs) != 0 {
		t.Fatal("foreign sender enqueued")
	}
}

func TestWebhook_CallbackSenderChecked(t *testing.T) {
	enq := &captureEnqueuer{}
	h := newWebhook(enq)
	cb := `{"update_id":779,"callback_query":{"id":"abc","data":"ping:1","from":{"id":424242}}}`
	if rec := postUpdate(t, h, "s3cret", cb); rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	if len(enq.envs) != 1 {
		t.Fatalf("callback from allowed user must enqueue")
	}
	foreignCb := `{"update_id":780,"callback_query":{"id":"abc","data":"x","from":{"id":666}}}`
	_ = postUpdate(t, h, "s3cret", foreignCb)
	if len(enq.envs) != 1 {
		t.Fatal("foreign callback enqueued")
	}
}

func TestWebhook_MalformedDroppedWith200(t *testing.T) {
	enq := &captureEnqueuer{}
	h := newWebhook(enq)
	for _, body := range []string{`{not json`, `{"no_update_id":true}`, `{"update_id":781}`} {
		if rec := postUpdate(t, h, "s3cret", body); rec.Code != http.StatusOK {
			t.Errorf("body %q: code = %d, want 200", body, rec.Code)
		}
	}
	if len(enq.envs) != 0 {
		t.Fatal("malformed update enqueued")
	}
}

func TestWebhook_EnqueueFailureIs500ForRedelivery(t *testing.T) {
	enq := &captureEnqueuer{err: errors.New("tasks unavailable")}
	h := newWebhook(enq)
	if rec := postUpdate(t, h, "s3cret", validUpdate); rec.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, want 500 (Telegram must redeliver later)", rec.Code)
	}
}

func TestWebhook_OversizedAndEmptyBodiesDropped(t *testing.T) {
	enq := &captureEnqueuer{}
	h := newWebhook(enq)

	big := `{"update_id":1,"message":{"text":"` + strings.Repeat("a", maxUpdateBytes) + `"}}`
	for name, body := range map[string]string{"oversized": big, "empty": ""} {
		if rec := postUpdate(t, h, "s3cret", body); rec.Code != http.StatusOK {
			t.Errorf("%s: code = %d, want 200 (drop, no 24h retries)", name, rec.Code)
		}
	}
	if len(enq.envs) != 0 {
		t.Fatal("bad bodies enqueued")
	}
}

func TestWebhook_MissingSecretHeaderIs403(t *testing.T) {
	enq := &captureEnqueuer{}
	h := newWebhook(enq)
	if rec := postUpdate(t, h, "", validUpdate); rec.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403", rec.Code)
	}
}

func TestWebhookRoute_GETIs405(t *testing.T) {
	h := New(Deps{Webhook: newWebhook(&captureEnqueuer{})})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/telegram/webhook", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("code = %d, want 405 (POST-only route)", rec.Code)
	}
}
