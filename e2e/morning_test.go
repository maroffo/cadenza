// ABOUTME: End-to-end morning path: executor -> dispatch -> icu fake -> emulator stores -> Telegram fake.
// ABOUTME: Emulator-gated; REQUIRE_EMULATOR=1 in CI turns the skip into a failure.

//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/go-telegram/bot"

	"github.com/maroffo/cadenza/internal/icu"
	"github.com/maroffo/cadenza/internal/job"
	"github.com/maroffo/cadenza/internal/server"
	"github.com/maroffo/cadenza/internal/store"
	"github.com/maroffo/cadenza/internal/telegram"
	"github.com/maroffo/cadenza/internal/verdict"
)

type passValidator struct{ email string }

func (p passValidator) Validate(context.Context, string, string) (string, error) {
	return p.email, nil
}

// telegramSink records sendMessage texts from the real go-telegram/bot client.
type telegramSink struct {
	mu    sync.Mutex
	texts []string
}

func (s *telegramSink) handler(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/sendMessage"):
		var text string
		ct := r.Header.Get("Content-Type")
		switch {
		case strings.HasPrefix(ct, "application/json"):
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			text, _ = payload["text"].(string)
		default:
			_ = r.ParseMultipartForm(1 << 20)
			text = r.FormValue("text")
		}
		s.mu.Lock()
		s.texts = append(s.texts, text)
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 1}})
	case strings.HasSuffix(r.URL.Path, "/answerCallbackQuery"):
		// The Bot API returns a bare bool here, not a message object.
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": true})
	default:
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 1}})
	}
}

func (s *telegramSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.texts)
}

func TestMorningPath_EndToEnd(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		if os.Getenv("REQUIRE_EMULATOR") == "1" {
			t.Fatal("REQUIRE_EMULATOR=1 but FIRESTORE_EMULATOR_HOST is not set")
		}
		t.Skip("FIRESTORE_EMULATOR_HOST not set, skipping e2e morning path")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tz := time.FixedZone("Europe/Rome", 2*3600)
	now := func() time.Time { return time.Date(2031, 3, 5, 7, 0, 0, 0, tz) }
	today := "2031-03-05"

	// Fake intervals.icu: serves a wellness range with all-green numbers.
	icuSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/wellness") {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `[
			{"id":"2031-03-03","hrv":69,"restingHR":47,"sleepSecs":25200,"ctl":41.0,"atl":45.0,"rampRate":2.0},
			{"id":"2031-03-04","hrv":70,"restingHR":46,"sleepSecs":26100,"ctl":41.2,"atl":45.5,"rampRate":2.1},
			{"id":"%s","hrv":71,"restingHR":47,"sleepSecs":27000,"ctl":41.3,"atl":45.8,"rampRate":2.2}
		]`, today)
	}))
	defer icuSrv.Close()

	sink := &telegramSink{}
	tgSrv := httptest.NewServer(http.HandlerFunc(sink.handler))
	defer tgSrv.Close()

	fsClient, err := firestore.NewClient(ctx, fmt.Sprintf("cadenza-e2e-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("firestore: %v", err)
	}
	defer func() { _ = fsClient.Close() }()

	profiles := store.NewProfiles(fsClient)
	if err := profiles.Seed(ctx, verdict.Baselines{HRVMean: 68, HRVSD: 6, RestingHR: 47}, 4.0); err != nil {
		t.Fatalf("seed profile: %v", err)
	}

	tgBot, err := bot.New("e2e-token", bot.WithServerURL(tgSrv.URL), bot.WithSkipGetMe())
	if err != nil {
		t.Fatalf("bot: %v", err)
	}

	runs := store.NewRuns(fsClient)
	deps := job.Deps{
		Morning: job.Morning{
			Wellness: job.ICU{C: icu.New(icuSrv.URL, "e2e-key", "0")},
			Profiles: profiles,
			Out:      telegram.NewSender(tgBot, 424242),
			Runs:     runs,
			Now:      now,
			TZ:       tz,
		},
		Watchdog: job.Watchdog{Runs: runs, Out: telegram.NewSender(tgBot, 424242), Now: now, TZ: tz},
	}

	executor := &server.Executor{
		Validator:    passValidator{email: "invoker@e2e.test"},
		Audience:     "https://cadenza.e2e.test",
		InvokerEmail: "invoker@e2e.test",
		Dispatch:     deps.Dispatch,
	}
	app := httptest.NewServer(server.New(server.Deps{Executor: executor}))
	defer app.Close()

	envelope := `{"v":1,"type":"morning_check","id":"morning-scheduler"}`
	postEnvelope := func() int {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, app.URL+"/internal/execute", strings.NewReader(envelope))
		req.Header.Set("Authorization", "Bearer e2e")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	// First delivery: full path, one Telegram message with body + verdict.
	if code := postEnvelope(); code != http.StatusOK {
		t.Fatalf("first POST = %d, want 200", code)
	}
	if sink.count() != 1 {
		t.Fatalf("telegram messages = %d, want 1", sink.count())
	}
	sink.mu.Lock()
	text := sink.texts[0]
	sink.mu.Unlock()
	if !strings.Contains(text, "VERDETTO") {
		t.Errorf("message missing verdict block:\n%s", text)
	}
	if !strings.Contains(text, "71") {
		t.Errorf("message missing today's HRV:\n%s", text)
	}

	done, err := runs.MorningCompleted(ctx, today)
	if err != nil || !done {
		t.Fatalf("run doc completed = %v, %v; want true", done, err)
	}

	// Replay (Scheduler retry): idempotent no-op, no duplicate message.
	if code := postEnvelope(); code != http.StatusOK {
		t.Fatalf("second POST = %d, want 200", code)
	}
	if sink.count() != 1 {
		t.Fatalf("after replay: telegram messages = %d, want still 1 (idempotency)", sink.count())
	}

	// Watchdog after completion stays quiet.
	wd := `{"v":1,"type":"watchdog","id":"watchdog-scheduler"}`
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, app.URL+"/internal/execute", strings.NewReader(wd))
	req.Header.Set("Authorization", "Bearer e2e")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("watchdog POST: %v", err)
	}
	_ = resp.Body.Close()
	if sink.count() != 1 {
		t.Fatalf("watchdog sent despite completed morning: messages = %d", sink.count())
	}
}
