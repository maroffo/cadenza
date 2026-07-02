// ABOUTME: End-to-end conversation: webhook -> coach -> reply, history, cache, mutation confirm.
// ABOUTME: The full M5 promise on real stores (emulator) with the scripted Anthropic fake.

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

	"github.com/maroffo/cadenza/internal/agent"
	"github.com/maroffo/cadenza/internal/fakes"
	"github.com/maroffo/cadenza/internal/icu"
	"github.com/maroffo/cadenza/internal/job"
	"github.com/maroffo/cadenza/internal/server"
	"github.com/maroffo/cadenza/internal/store"
	"github.com/maroffo/cadenza/internal/task"
	"github.com/maroffo/cadenza/internal/telegram"
	"github.com/maroffo/cadenza/internal/verdict"
)

// richSink also captures inline-keyboard callback data (for the confirm tap).
type richSink struct {
	mu        sync.Mutex
	texts     []string
	callbacks []string
}

func (s *richSink) handler(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/sendMessage") {
		// go-telegram posts multipart or JSON depending on payload shape.
		var text, markup string
		ct := r.Header.Get("Content-Type")
		if strings.HasPrefix(ct, "application/json") {
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			text, _ = payload["text"].(string)
			if rm, ok := payload["reply_markup"]; ok {
				raw, _ := json.Marshal(rm)
				markup = string(raw)
			}
		} else {
			_ = r.ParseMultipartForm(1 << 20)
			text = r.FormValue("text")
			markup = r.FormValue("reply_markup")
		}
		s.mu.Lock()
		s.texts = append(s.texts, text)
		if markup != "" {
			s.callbacks = append(s.callbacks, markup)
		}
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 1}})
		return
	}
	if strings.HasSuffix(r.URL.Path, "/answerCallbackQuery") {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": true})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 1}})
}

func TestConversation_EndToEnd(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		if os.Getenv("REQUIRE_EMULATOR") == "1" {
			t.Fatal("REQUIRE_EMULATOR=1 but FIRESTORE_EMULATOR_HOST is not set")
		}
		t.Skip("FIRESTORE_EMULATOR_HOST not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const userID = int64(424242)
	const secret = "e2e-secret"
	tz := time.FixedZone("Europe/Rome", 2*3600)
	now := func() time.Time { return time.Date(2031, 5, 6, 9, 0, 0, 0, tz) }
	today := "2031-05-06"

	icuSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/wellness"):
			fmt.Fprintf(w, `[{"id":"%s","hrv":70,"restingHR":46,"sleepSecs":27000,"ctl":41.0,"atl":45.0,"rampRate":2.0}]`, today)
		default:
			fmt.Fprint(w, `[]`)
		}
	}))
	defer icuSrv.Close()

	sink := &richSink{}
	tgSrv := httptest.NewServer(http.HandlerFunc(sink.handler))
	defer tgSrv.Close()
	llm := fakes.NewAnthropic(
		fakes.Text{S: "Sei fresco, oggi puoi osare un filo di più."},
		fakes.Text{S: "Come dicevo: resta dentro i range e goditela."},
		fakes.Call("tu_rule", "propose_profile_update",
			`{"kind":"rule","new_value":"Niente qualita il giorno dopo un volo","rationale":"pattern","source_quote":"dopo i voli sono a pezzi"}`),
		fakes.Text{S: "Proposta inviata, confermala col bottone."},
		fakes.Text{S: "Regola attiva, ne terrò conto."},
	)
	defer llm.Close()

	fsClient, err := firestore.NewClient(ctx, fmt.Sprintf("cadenza-e2e-conv-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("firestore: %v", err)
	}
	defer func() { _ = fsClient.Close() }()
	profiles := store.NewProfiles(fsClient)
	if err := profiles.Seed(ctx, verdict.Baselines{HRVMean: 68, HRVSD: 6, RestingHR: 47}, 4.0); err != nil {
		t.Fatalf("seed: %v", err)
	}

	tgBot, err := bot.New("e2e", bot.WithServerURL(tgSrv.URL), bot.WithSkipGetMe())
	if err != nil {
		t.Fatalf("bot.New: %v", err)
	}
	sender := telegram.NewSender(tgBot, userID)
	icuClient := icu.New(icuSrv.URL, "k", "0")
	chats := store.NewChats(fsClient)

	morning := job.Morning{
		Wellness: job.ICU{C: icuClient}, Profiles: profiles,
		Out: sender, Runs: store.NewRuns(fsClient), Now: now, TZ: tz,
	}
	llmClient := agent.NewClient("k", llm.URL())
	message := job.Message{
		AllowedUserID: userID,
		Dedup:         store.NewDedup(fsClient),
		Chats:         chats,
		Out:           sender,
		Status:        morning,
		Muts:          store.NewMutations(fsClient),
		Coach: &job.Coach{
			Agent:      agent.Coach{Client: llmClient, Model: "claude-opus-test"},
			Wellness:   job.ICU{C: icuClient},
			Activities: job.ICU{C: icuClient},
			Profiles:   profiles,
			Rules:      store.NewRules(fsClient),
			Muts:       store.NewMutations(fsClient),
			Sessions:   store.NewSessions(fsClient),
			Chats:      chats,
			Status:     morning,
			Out:        sender,
			Confirm:    sender,
			Now:        now,
			TZ:         tz,
		},
	}
	deps := job.Deps{Message: message}
	webhook := &server.Webhook{Secret: secret, AllowedUserID: userID, Enqueue: task.Local{Dispatch: deps.Dispatch}}
	app := httptest.NewServer(server.New(server.Deps{Webhook: webhook}))
	defer app.Close()

	post := func(body string) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, app.URL+"/telegram/webhook", strings.NewReader(body))
		req.Header.Set("X-Telegram-Bot-Api-Secret-Token", secret)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("webhook status = %d (dispatch error)", resp.StatusCode)
		}

	}
	msg := func(updateID int64, text string) string {
		return fmt.Sprintf(`{"update_id":%d,"message":{"text":%q,"from":{"id":%d},"chat":{"id":%d}}}`,
			updateID, text, userID, userID)
	}

	// 1) First free-text message: coach replies WITHOUT the verdict footer
	// (that block is morning-only now; the verdict still reaches the model).
	post(msg(8001, "come sto messo oggi?"))
	if len(sink.texts) != 1 || !strings.Contains(sink.texts[0], "Sei fresco") {
		t.Fatalf("first reply wrong: %v", sink.texts)
	}
	if strings.Contains(sink.texts[0], "VERDETTO") {
		t.Fatalf("conversational reply leaked the verdict footer: %v", sink.texts)
	}

	// 2) Second message: history replayed, cache breakpoint stable.
	post(msg(8002, "quindi posso spingere?"))
	if len(sink.texts) != 2 {
		t.Fatalf("second reply missing: %v", sink.texts)
	}
	second := llm.Requests[1]
	if len(second.Messages) != 3 {
		t.Fatalf("second request messages = %d, want 3 (history + new)", len(second.Messages))
	}
	if n := strings.Count(string(second.Raw), "cache_control"); n != 1 {
		t.Errorf("cache_control on 2nd request = %d, want 1", n)
	}
	if !strings.Contains(string(second.Raw), "come sto messo oggi?") {
		t.Error("first user message not in history")
	}
	// Byte-stability is the actual cache precondition: same system+tools
	// prefix bytes on both requests, or every read becomes a write.
	var req1, req2 struct {
		System json.RawMessage   `json:"system"`
		Tools  []json.RawMessage `json:"tools"`
	}
	_ = json.Unmarshal(llm.Requests[0].Raw, &req1)
	_ = json.Unmarshal(second.Raw, &req2)
	t1, _ := json.Marshal(req1.Tools)
	t2, _ := json.Marshal(req2.Tools)
	if string(req1.System) != string(req2.System) || string(t1) != string(t2) {
		t.Error("cached prefix not byte-stable across requests (cache would never hit)")
	}

	// 3) Pattern statement: the coach proposes a rule, confirm button arrives.
	post(msg(8003, "dopo i voli sono a pezzi"))
	if len(sink.callbacks) == 0 {
		t.Fatal("no confirm keyboard sent")
	}
	cb := sink.callbacks[len(sink.callbacks)-1]
	var kb struct {
		InlineKeyboard [][]struct {
			CallbackData string `json:"callback_data"`
		} `json:"inline_keyboard"`
	}
	if err := json.Unmarshal([]byte(cb), &kb); err != nil || len(kb.InlineKeyboard) == 0 {
		t.Fatalf("keyboard decode: %v %s", err, cb)
	}
	yesData := kb.InlineKeyboard[0][0].CallbackData
	if !strings.HasPrefix(yesData, "pm:") || !strings.HasSuffix(yesData, ":y") {
		t.Fatalf("yes callback = %q", yesData)
	}

	// 4) Athlete taps Confirm: mutation applies, rule becomes active.
	post(fmt.Sprintf(`{"update_id":8004,"callback_query":{"id":"cbE","data":%q,"from":{"id":%d}}}`, yesData, userID))
	rules, err := store.NewRules(fsClient).ActiveTexts(ctx)
	if err != nil || len(rules) != 1 {
		t.Fatalf("active rules = %v, %v; want the confirmed one", rules, err)
	}
	sink.mu.Lock()
	lastText := sink.texts[len(sink.texts)-1]
	sink.mu.Unlock()
	if !strings.Contains(lastText, "Salvato nel profilo") {
		t.Errorf("athlete feedback missing after confirm: %q", lastText)
	}

	// 5) Next message: the confirmed rule is in the cached profile prefix.
	post(msg(8005, "ok grazie"))
	last := llm.Requests[len(llm.Requests)-1]
	if !strings.Contains(string(last.Raw), "Niente qualita il giorno dopo un volo") {
		t.Error("confirmed rule missing from the profile prefix")
	}
}
