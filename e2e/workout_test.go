// ABOUTME: End-to-end M6 gauntlet: coach -> gate -> verified write against a fake intervals.icu.
// ABOUTME: Real Writer, real gate, real ledger on the emulator; only the model is scripted.

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
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/go-telegram/bot"

	"github.com/maroffo/cadenza/internal/agent"
	"github.com/maroffo/cadenza/internal/fakes"
	"github.com/maroffo/cadenza/internal/icu"
	"github.com/maroffo/cadenza/internal/icuwrite"
	"github.com/maroffo/cadenza/internal/job"
	"github.com/maroffo/cadenza/internal/store"
	"github.com/maroffo/cadenza/internal/telegram"
	"github.com/maroffo/cadenza/internal/verdict"
)

func TestWorkoutWrite_EndToEnd(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		if os.Getenv("REQUIRE_EMULATOR") == "1" {
			t.Fatal("REQUIRE_EMULATOR=1 but FIRESTORE_EMULATOR_HOST is not set")
		}
		t.Skip("FIRESTORE_EMULATOR_HOST not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	tz := time.FixedZone("Europe/Rome", 2*3600)
	now := func() time.Time { return time.Date(2031, 5, 6, 9, 0, 0, 0, tz) }
	today := "2031-05-06"

	// Fake intervals.icu: wellness for the verdict, bulk upsert storage,
	// resolved read-back of whatever was stored.
	var storedEvent map[string]any
	icuSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/wellness"):
			fmt.Fprintf(w, `[{"id":%q,"hrv":70,"restingHR":46,"sleepSecs":27000,"ctl":41.0,"atl":45.0,"rampRate":2.0}]`, today)
		case strings.Contains(r.URL.Path, "/events/bulk"):
			var events []map[string]any
			_ = json.NewDecoder(r.Body).Decode(&events)
			if len(events) == 1 {
				storedEvent = events[0]
				storedEvent["id"] = 777.0
			}
			fmt.Fprint(w, `[{"id":777}]`)
		case strings.Contains(r.URL.Path, "/events"):
			if storedEvent == nil {
				fmt.Fprint(w, `[]`)
				return
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{storedEvent})
		default:
			fmt.Fprint(w, `[]`)
		}
	}))
	defer icuSrv.Close()

	sink := &richSink{}
	tgSrv := httptest.NewServer(http.HandlerFunc(sink.handler))
	defer tgSrv.Close()
	planJSON := fmt.Sprintf(`{"date":%q,"sport":"Run","title":"Progressivo","items":[
		{"minutes":10,"hr":{"zone_start":1,"zone_end":2},"intensity":"warmup"},
		{"minutes":30,"hr":{"zone":2}},
		{"minutes":10,"hr":{"zone":1},"intensity":"cooldown"}]}`, today)
	llm := fakes.NewAnthropic(
		fakes.Call("tu_wr", "write_workout", planJSON),
		fakes.Text{S: "Scritto: progressivo da 50 minuti, lo trovi sull'orologio."},
	)
	defer llm.Close()

	fsClient, err := firestore.NewClient(ctx, fmt.Sprintf("cadenza-e2e-wo-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("firestore: %v", err)
	}
	defer func() { _ = fsClient.Close() }()
	profiles := store.NewProfiles(fsClient)
	if err := profiles.Seed(ctx, verdict.Baselines{HRVMean: 68, HRVSD: 6, RestingHR: 47}, 4.0); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tgBot, _ := bot.New("e2e", bot.WithServerURL(tgSrv.URL), bot.WithSkipGetMe())
	sender := telegram.NewSender(tgBot, 1)
	icuClient := icu.New(icuSrv.URL, "k", "0")
	morning := job.Morning{
		Wellness: job.ICU{C: icuClient}, Profiles: profiles,
		Out: sender, Runs: store.NewRuns(fsClient), Now: now, TZ: tz,
	}
	coach := &job.Coach{
		Agent:      agent.Coach{Client: agent.NewClient("k", llm.URL()), Model: "claude-opus-test"},
		Wellness:   job.ICU{C: icuClient},
		Activities: job.ICU{C: icuClient},
		Profiles:   profiles,
		Rules:      store.NewRules(fsClient),
		Muts:       store.NewMutations(fsClient),
		Sessions:   store.NewSessions(fsClient),
		Chats:      store.NewChats(fsClient),
		Status:     morning,
		Out:        sender,
		Confirm:    sender,
		Writer:     &icuwrite.Writer{C: icuClient},
		Ledger:     store.NewLedger(fsClient),
		Now:        now,
		TZ:         tz,
	}

	if err := coach.Converse(ctx, "scrivimi un progressivo per oggi"); err != nil {
		t.Fatalf("Converse: %v", err)
	}

	// The event reached the fake calendar with the structured doc and no description.
	if storedEvent == nil {
		t.Fatal("no event upserted")
	}
	if _, has := storedEvent["description"]; has {
		t.Fatal("event carries description (doc clobber trap)")
	}
	doc, _ := storedEvent["workout_doc"].(map[string]any)
	if doc == nil || len(doc["steps"].([]any)) != 3 {
		t.Fatalf("workout_doc malformed: %v", storedEvent)
	}
	extID, _ := storedEvent["external_id"].(string)
	if !strings.HasPrefix(extID, "cadenza-"+today) {
		t.Errorf("external_id = %q", extID)
	}

	// Ledger records the verified outcome on the emulator.
	snap, err := fsClient.Collection("events_written").Doc(extID).Get(ctx)
	if err != nil {
		t.Fatalf("ledger doc: %v", err)
	}
	var rec store.WriteRecord
	_ = snap.DataTo(&rec)
	if rec.Status != "verified" || rec.EventID != 777 {
		t.Errorf("ledger = %+v", rec)
	}

	// The athlete got the confirmation with the verdict footer.
	sink.mu.Lock()
	text := sink.texts[len(sink.texts)-1]
	sink.mu.Unlock()
	if !strings.Contains(text, "Scritto") || !strings.Contains(text, "VERDETTO") {
		t.Errorf("reply malformed:\n%s", text)
	}
}
