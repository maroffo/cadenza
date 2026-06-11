// ABOUTME: End-to-end interactive path: webhook (secret) -> Local dispatch -> Message -> Telegram fake.
// ABOUTME: Real dedup on the emulator: webhook replay of the same update_id sends exactly once.

//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/go-telegram/bot"

	"github.com/maroffo/cadenza/internal/job"
	"github.com/maroffo/cadenza/internal/server"
	"github.com/maroffo/cadenza/internal/store"
	"github.com/maroffo/cadenza/internal/task"
	"github.com/maroffo/cadenza/internal/telegram"
	"github.com/maroffo/cadenza/internal/verdict"
)

type fixedComposer struct{}

func (fixedComposer) Compose(context.Context) (string, verdict.Verdict, error) {
	return "☀️ <b>Check di prova</b>", verdict.Verdict{Kind: verdict.Go, Version: verdict.Version}, nil
}

func TestInteractivePath_EndToEnd(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		if os.Getenv("REQUIRE_EMULATOR") == "1" {
			t.Fatal("REQUIRE_EMULATOR=1 but FIRESTORE_EMULATOR_HOST is not set")
		}
		t.Skip("FIRESTORE_EMULATOR_HOST not set, skipping e2e interactive path")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const userID = int64(424242)
	const secret = "e2e-webhook-secret"

	sink := &telegramSink{}
	tgSrv := httptest.NewServer(http.HandlerFunc(sink.handler))
	defer tgSrv.Close()
	tgBot, err := bot.New("e2e-token", bot.WithServerURL(tgSrv.URL), bot.WithSkipGetMe())
	if err != nil {
		t.Fatalf("bot: %v", err)
	}

	fsClient, err := firestore.NewClient(ctx, fmt.Sprintf("cadenza-e2e-int-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("firestore: %v", err)
	}
	defer func() { _ = fsClient.Close() }()

	deps := job.Deps{
		Message: job.Message{
			AllowedUserID: userID,
			Dedup:         store.NewDedup(fsClient),
			Chats:         store.NewChats(fsClient),
			Out:           telegram.NewSender(tgBot, userID),
			Status:        fixedComposer{},
		},
	}
	webhook := &server.Webhook{
		Secret:        secret,
		AllowedUserID: userID,
		Enqueue:       task.Local{Dispatch: deps.Dispatch},
	}
	app := httptest.NewServer(server.New(server.Deps{Webhook: webhook}))
	defer app.Close()

	post := func(body string) int {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, app.URL+"/telegram/webhook", strings.NewReader(body))
		req.Header.Set("X-Telegram-Bot-Api-Secret-Token", secret)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	start := fmt.Sprintf(`{"update_id":9001,"message":{"text":"/start","from":{"id":%d},"chat":{"id":%d}}}`, userID, userID)
	if code := post(start); code != http.StatusOK {
		t.Fatalf("/start POST = %d", code)
	}
	if sink.count() != 1 {
		t.Fatalf("after /start: messages = %d, want 1 (welcome)", sink.count())
	}
	chatID, _, err := store.NewChats(fsClient).Get(ctx)
	if err != nil || chatID != userID {
		t.Fatalf("chat not persisted: %d, %v", chatID, err)
	}

	status := fmt.Sprintf(`{"update_id":9002,"message":{"text":"/status","from":{"id":%d},"chat":{"id":%d}}}`, userID, userID)
	if code := post(status); code != http.StatusOK {
		t.Fatalf("/status POST = %d", code)
	}
	if sink.count() != 2 {
		t.Fatalf("after /status: messages = %d, want 2", sink.count())
	}
	sink.mu.Lock()
	statusText := sink.texts[1]
	sink.mu.Unlock()
	if !strings.Contains(statusText, "VERDETTO") {
		t.Errorf("/status reply missing verdict block:\n%s", statusText)
	}

	// Telegram webhook replay of the same update: real dedup must no-op.
	if code := post(status); code != http.StatusOK {
		t.Fatalf("replay POST = %d", code)
	}
	if sink.count() != 2 {
		t.Fatalf("replay produced a duplicate send: messages = %d", sink.count())
	}

	// Callback round trip: answered (no stuck spinner) + ack message.
	cb := fmt.Sprintf(`{"update_id":9003,"callback_query":{"id":"cb1","data":"ping:1","from":{"id":%d}}}`, userID)
	if code := post(cb); code != http.StatusOK {
		t.Fatalf("callback POST = %d", code)
	}
	if sink.count() != 3 {
		t.Fatalf("after callback: messages = %d, want 3", sink.count())
	}
	// Foreign sender: 200 but nothing delivered.
	foreign := `{"update_id":9004,"message":{"text":"ciao","from":{"id":666},"chat":{"id":666}}}`
	if code := post(foreign); code != http.StatusOK {
		t.Fatalf("foreign POST = %d (must be 200, or Telegram retries 24h)", code)
	}
	if sink.count() != 3 {
		t.Fatalf("foreign sender reached the athlete channel: messages = %d", sink.count())
	}
}
