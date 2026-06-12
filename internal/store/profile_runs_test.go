// ABOUTME: Emulator tests for the profile and runs repositories.
// ABOUTME: Skips without FIRESTORE_EMULATOR_HOST; REQUIRE_EMULATOR=1 turns skips fatal.

package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/maroffo/cadenza/internal/verdict"
)

func TestProfiles_SeedAndGetRoundTrip(t *testing.T) {
	client := emulatorClient(t)
	p := NewProfiles(client)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	want := verdict.Baselines{HRVMean: 68, HRVSD: 6, RestingHR: 47}
	if err := p.Seed(ctx, want, 4.0); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	got, rampCap, err := p.Profile(ctx)
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	if got != want {
		t.Errorf("baselines = %+v, want %+v", got, want)
	}
	if rampCap != 4.0 {
		t.Errorf("rampCap = %v, want 4.0", rampCap)
	}
}

func TestProfiles_ImplausibleBaselinesRejected(t *testing.T) {
	client := emulatorClient(t)
	p := NewProfiles(client)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := p.Seed(ctx, verdict.Baselines{}, 4.0); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if _, _, err := p.Profile(ctx); err == nil {
		t.Fatal("zero baselines accepted; coaching on invented numbers must fail loudly")
	}
}

func TestRuns_MorningLifecycle(t *testing.T) {
	client := emulatorClient(t)
	r := NewRuns(client)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Opaque unique id: the repo treats dates as strings, and uniqueness
	// keeps reruns against a long-lived emulator instance independent.
	date := fmt.Sprintf("2099-test-%d", time.Now().UnixNano())

	done, err := r.MorningCompleted(ctx, date)
	if err != nil {
		t.Fatalf("MorningCompleted: %v", err)
	}
	if done {
		t.Fatal("fresh date reports completed")
	}
	if err := r.MarkMorningCompleted(ctx, date, "GO"); err != nil {
		t.Fatalf("MarkMorningCompleted: %v", err)
	}
	done, err = r.MorningCompleted(ctx, date)
	if err != nil {
		t.Fatalf("MorningCompleted after mark: %v", err)
	}
	if !done {
		t.Fatal("marked date reports not completed")
	}
}

func TestChats_SaveAndGet(t *testing.T) {
	client := emulatorClient(t)
	c := NewChats(client)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := c.Save(ctx, 424242, 424242); err != nil {
		t.Fatalf("Save: %v", err)
	}
	chatID, userID, err := c.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if chatID != 424242 || userID != 424242 {
		t.Errorf("got chat=%d user=%d, want 424242/424242", chatID, userID)
	}
}

func TestChats_GetBeforeStartReturnsZeros(t *testing.T) {
	client := emulatorClient(t)
	c := NewChats(client)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Force the not-found branch even on a long-lived emulator.
	_, _ = client.Collection("state").Doc("chat").Delete(ctx)
	chatID, userID, err := c.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if chatID != 0 || userID != 0 {
		t.Errorf("got chat=%d user=%d, want zeros (/start never happened)", chatID, userID)
	}
}

func TestRuns_DeferredLifecycle(t *testing.T) {
	client := emulatorClient(t)
	r := NewRuns(client)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	date := fmt.Sprintf("2099-defer-%d", time.Now().UnixNano())

	if err := r.MarkMorningDeferred(ctx, date, 1); err != nil {
		t.Fatalf("MarkMorningDeferred: %v", err)
	}
	done, err := r.MorningCompleted(ctx, date)
	if err != nil || done {
		t.Fatalf("deferred reports completed=%v err=%v, want false", done, err)
	}
	alive, err := r.MorningAlive(ctx, date)
	if err != nil || !alive {
		t.Fatalf("deferred reports alive=%v err=%v, want true (watchdog quiet)", alive, err)
	}

	if err := r.MarkMorningCompleted(ctx, date, "GO"); err != nil {
		t.Fatalf("MarkMorningCompleted: %v", err)
	}
	done, err = r.MorningCompleted(ctx, date)
	if err != nil || !done {
		t.Fatalf("completed after deferral reports done=%v err=%v, want true", done, err)
	}
}

func TestSessions_RoundTripAndCorruptionFallback(t *testing.T) {
	client := emulatorClient(t)
	s := NewSessions(client)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	id, err := s.Create(ctx, "morning", time.Now())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.AppendTurn(ctx, id, 1, "user", "dati", ""); err != nil {
		t.Fatalf("AppendTurn 1: %v", err)
	}
	if err := s.AppendTurn(ctx, id, 2, "assistant", "narrativa", "haiku"); err != nil {
		t.Fatalf("AppendTurn 2: %v", err)
	}

	turns, err := s.LoadTurns(ctx, id)
	if err != nil {
		t.Fatalf("LoadTurns: %v", err)
	}
	if len(turns) != 2 || turns[0].Role != "user" || turns[1].Model != "haiku" {
		t.Errorf("turns = %+v", turns)
	}
	if turns[0].ExpiresAt.IsZero() {
		t.Error("turn without ExpiresAt: the retention TTL policy has nothing to act on")
	}

	// Ordering is a query contract, not an insertion accident: append a
	// lower seq AFTER higher ones and verify LoadTurns still sorts.
	if err := s.AppendTurn(ctx, id, 0, "user", "prima", ""); err != nil {
		t.Fatalf("AppendTurn 0: %v", err)
	}
	turns, err = s.LoadTurns(ctx, id)
	if err != nil {
		t.Fatalf("LoadTurns: %v", err)
	}
	if len(turns) != 3 || turns[0].Seq != 0 || turns[2].Seq != 2 {
		t.Errorf("ordering broken: %+v", turns)
	}

	// Corrupt a turn by hand (wrong schema): load must FAIL loudly so the
	// caller starts a fresh session instead of trusting partial history.
	_, err = client.Collection("sessions").Doc(id).Collection("turns").Doc("000003").
		Set(ctx, map[string]any{"seq": 3, "role": "assistant", "content": "x", "schema": 99})
	if err != nil {
		t.Fatalf("corrupt: %v", err)
	}
	if _, err := s.LoadTurns(ctx, id); err == nil {
		t.Fatal("corrupted turn loaded without error (fresh-session fallback impossible)")
	}
}

func TestMutations_LifecycleRampCap(t *testing.T) {
	client := emulatorClient(t)
	muts := NewMutations(client)
	profiles := NewProfiles(client)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := profiles.Seed(ctx, verdict.Baselines{HRVMean: 35, HRVSD: 8, RestingHR: 54}, 4.0); err != nil {
		t.Fatalf("seed: %v", err)
	}
	id := MutationID("s-test", fmt.Sprintf("tu-%d", time.Now().UnixNano()))
	mut := Mutation{
		Kind: MutationRampCap, NewValue: "3.5", OldValue: "4.0",
		Rationale: "fase di scarico", SourceQuote: "voglio scaricare",
		SessionID: "s-test", ToolUseID: "tu-x",
	}
	if err := muts.Propose(ctx, id, mut); err != nil {
		t.Fatalf("Propose: %v", err)
	}
	// Agent-loop retry: same deterministic id, must not duplicate or error.
	if err := muts.Propose(ctx, id, mut); err != nil {
		t.Fatalf("Propose retry: %v", err)
	}

	resolved, err := muts.Resolve(ctx, id, true)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.Status != "confirmed" {
		t.Errorf("status = %q", resolved.Status)
	}
	_, rampCap, err := profiles.Profile(ctx)
	if err != nil || rampCap != 3.5 {
		t.Fatalf("profile ramp_cap = %v, %v; want 3.5 applied transactionally", rampCap, err)
	}

	// Double tap: idempotent, still confirmed, no re-apply error.
	again, err := muts.Resolve(ctx, id, false) // even a late "reject" tap
	if err != nil {
		t.Fatalf("Resolve double-tap: %v", err)
	}
	if again.Status != "confirmed" {
		t.Errorf("double tap changed status to %q", again.Status)
	}
}

func TestMutations_RejectLeavesProfileUntouched(t *testing.T) {
	client := emulatorClient(t)
	muts := NewMutations(client)
	profiles := NewProfiles(client)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := profiles.Seed(ctx, verdict.Baselines{HRVMean: 35, HRVSD: 8, RestingHR: 54}, 4.0); err != nil {
		t.Fatalf("seed: %v", err)
	}
	id := MutationID("s-rej", fmt.Sprintf("tu-%d", time.Now().UnixNano()))
	if err := muts.Propose(ctx, id, Mutation{Kind: MutationRampCap, NewValue: "2.0"}); err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if _, err := muts.Resolve(ctx, id, false); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	_, rampCap, err := profiles.Profile(ctx)
	if err != nil || rampCap != 4.0 {
		t.Fatalf("ramp_cap = %v after reject, want untouched 4.0", rampCap)
	}
}

func TestMutations_HostileRampCapBlockedAtApply(t *testing.T) {
	client := emulatorClient(t)
	muts := NewMutations(client)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	id := MutationID("s-evil", fmt.Sprintf("tu-%d", time.Now().UnixNano()))
	if err := muts.Propose(ctx, id, Mutation{Kind: MutationRampCap, NewValue: "12"}); err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if _, err := muts.Resolve(ctx, id, true); err == nil {
		t.Fatal("confirmed ramp_cap 12 applied; Tier A ceiling must block even confirmed values")
	}
}

func TestMutations_RuleConfirmAppearsActive(t *testing.T) {
	client := emulatorClient(t)
	muts := NewMutations(client)
	rules := NewRules(client)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	id := MutationID("s-rule", fmt.Sprintf("tu-%d", time.Now().UnixNano()))
	text := fmt.Sprintf("Niente qualità il giorno dopo un volo (%d)", time.Now().UnixNano())
	if err := muts.Propose(ctx, id, Mutation{Kind: MutationRule, NewValue: text}); err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if _, err := muts.Resolve(ctx, id, true); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	active, err := rules.ListActive(ctx)
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	found := false
	for _, r := range active {
		if r.Text == text {
			found = true
		}
	}
	if !found {
		t.Fatalf("confirmed rule not active: %+v", active)
	}
}

func TestChats_ActiveSessionPointer(t *testing.T) {
	client := emulatorClient(t)
	c := NewChats(client)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := c.Save(ctx, 424242, 424242); err != nil {
		t.Fatalf("Save: %v", err)
	}
	sid := fmt.Sprintf("s-chat-%d", time.Now().UnixNano())
	if err := c.SetActiveSession(ctx, sid); err != nil {
		t.Fatalf("SetActiveSession: %v", err)
	}
	got, err := c.ActiveSession(ctx)
	if err != nil || got != sid {
		t.Fatalf("ActiveSession = %q, %v; want %q", got, err, sid)
	}
	// Save must not wipe the pointer (merge semantics on /start re-runs)?
	// Save overwrites by design (fresh /start = fresh state); pin it.
	if err := c.Save(ctx, 424242, 424242); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, _ = c.ActiveSession(ctx)
	if got != "" {
		t.Fatalf("re-/start kept stale session %q, want reset", got)
	}
}

func TestMutations_ConcurrentTapsSingleTerminalStatus(t *testing.T) {
	client := emulatorClient(t)
	muts := NewMutations(client)
	profiles := NewProfiles(client)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := profiles.Seed(ctx, verdict.Baselines{HRVMean: 35, HRVSD: 8, RestingHR: 54}, 4.0); err != nil {
		t.Fatalf("seed: %v", err)
	}
	id := MutationID("s-race", fmt.Sprintf("tu-%d", time.Now().UnixNano()))
	if err := muts.Propose(ctx, id, Mutation{Kind: MutationRampCap, NewValue: "3.0"}); err != nil {
		t.Fatalf("Propose: %v", err)
	}

	results := make(chan string, 2)
	for _, approve := range []bool{true, false} {
		go func(a bool) {
			m, err := muts.Resolve(ctx, id, a)
			if err != nil {
				results <- "err:" + err.Error()
				return
			}
			results <- m.Status
		}(approve)
	}
	a, b := <-results, <-results
	if a != b {
		t.Fatalf("divergent terminal statuses: %q vs %q (transaction must serialize taps)", a, b)
	}
}

func TestRules_ActiveTextsStableOrdering(t *testing.T) {
	client := emulatorClient(t)
	muts := NewMutations(client)
	rules := NewRules(client)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	for i := range 3 {
		id := MutationID("s-ord", fmt.Sprintf("tu-%d-%d", time.Now().UnixNano(), i))
		text := fmt.Sprintf("Regola ordinamento %d %d", i, time.Now().UnixNano())
		if err := muts.Propose(ctx, id, Mutation{Kind: MutationRule, NewValue: text}); err != nil {
			t.Fatalf("Propose: %v", err)
		}
		if _, err := muts.Resolve(ctx, id, true); err != nil {
			t.Fatalf("Resolve: %v", err)
		}
	}
	first, err := rules.ActiveTexts(ctx)
	if err != nil {
		t.Fatalf("ActiveTexts: %v", err)
	}
	second, err := rules.ActiveTexts(ctx)
	if err != nil {
		t.Fatalf("ActiveTexts: %v", err)
	}
	if len(first) < 3 {
		t.Fatalf("rules = %d, want >= 3", len(first))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("ordering not stable at %d: %q vs %q (cache prefix would churn)", i, first[i], second[i])
		}
	}
}

func TestBudget_SpendCapsAtLimit(t *testing.T) {
	client := emulatorClient(t)
	b := NewBudget(client)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	date := fmt.Sprintf("2099-budget-%d", time.Now().UnixNano())

	for i := range 3 {
		spent, ok, err := b.Spend(ctx, date, 3)
		if err != nil || !ok {
			t.Fatalf("Spend %d = %v, %v; want allowed", i, ok, err)
		}
		if spent != i+1 {
			t.Fatalf("spent = %d after call %d, want post-increment count", spent, i+1)
		}
	}
	_, ok, err := b.Spend(ctx, date, 3)
	if err != nil {
		t.Fatalf("Spend over: %v", err)
	}
	if ok {
		t.Fatal("4th call allowed past a limit of 3 (decision 18 not mechanical)")
	}
}

func TestLedger_RecordUpsertsByExternalID(t *testing.T) {
	client := emulatorClient(t)
	l := NewLedger(client)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ext := fmt.Sprintf("cadenza-2099-01-01-%d", time.Now().UnixNano())

	if err := l.Record(ctx, WriteRecord{Date: "2099-01-01", ExternalID: ext, Status: "unverified_surfaced", Attempts: 3}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	// Retry overwrites its own audit line, no duplicates.
	if err := l.Record(ctx, WriteRecord{Date: "2099-01-01", ExternalID: ext, Status: "verified", Attempts: 1}); err != nil {
		t.Fatalf("Record retry: %v", err)
	}
	snap, err := client.Collection("events_written").Doc(ext).Get(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	var rec WriteRecord
	_ = snap.DataTo(&rec)
	if rec.Status != "verified" {
		t.Errorf("status = %q, want overwritten to verified", rec.Status)
	}
}

func TestInjuries_Lifecycle(t *testing.T) {
	client := emulatorClient(t)
	inj := NewInjuries(client)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	id := InjuryID(fmt.Sprintf("2099-%d", time.Now().UnixNano()), "Polpaccio Destro!")

	if _, err := inj.Open(ctx, id, Injury{BodyPart: "polpaccio destro", Pain: 6}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Idempotent re-report.
	if _, err := inj.Open(ctx, id, Injury{BodyPart: "polpaccio destro", Pain: 7}); err != nil {
		t.Fatalf("Open retry: %v", err)
	}
	got, err := inj.Get(ctx, id)
	if err != nil || got == nil || got.Pain != 6 || got.Rev != 1 || got.Status != "open" {
		t.Fatalf("Get = %+v, %v (retry must not overwrite)", got, err)
	}

	if err := inj.RecordFeedback(ctx, id, "same"); err != nil {
		t.Fatalf("RecordFeedback: %v", err)
	}
	open, err := inj.ListOpen(ctx)
	if err != nil {
		t.Fatalf("ListOpen: %v", err)
	}
	found := false
	for _, o := range open {
		if o.ID == id && o.LastFeedback == "same" {
			found = true
		}
	}
	if !found {
		t.Fatalf("open injury with feedback not listed: %+v", open)
	}

	if err := inj.Resolve(ctx, id); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if err := inj.Resolve(ctx, id); err != nil { // double tap
		t.Fatalf("Resolve again: %v", err)
	}
	got, _ = inj.Get(ctx, id)
	if got.Status != "resolved" || got.Rev != 2 {
		t.Fatalf("after resolve = %+v, want resolved rev2 (stale wakeups must die)", got)
	}
	if got.ExpiresAt.IsZero() {
		t.Error("injury without ExpiresAt: retention TTL has nothing to act on")
	}

	// Same-day pain-came-back: Open on the resolved doc must REOPEN with a
	// rev bump, never swallow (false-assurance hole from the review).
	reopened, err := inj.Open(ctx, id, Injury{BodyPart: "polpaccio destro", Pain: 5})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if reopened.Status != "open" || reopened.Rev != 3 || reopened.Pain != 5 {
		t.Fatalf("reopen = %+v, want open rev3 pain5", reopened)
	}

	// Ghost operations are named, not silently confirmed.
	if err := inj.Resolve(ctx, "inj-ghost-xyz"); !errors.Is(err, ErrInjuryNotFound) {
		t.Fatalf("ghost resolve = %v, want ErrInjuryNotFound", err)
	}
	if err := inj.RecordFeedback(ctx, "inj-ghost-xyz", "better"); !errors.Is(err, ErrInjuryNotFound) {
		t.Fatalf("ghost feedback = %v, want ErrInjuryNotFound", err)
	}
	if ghost, _ := inj.Get(ctx, "inj-ghost-xyz"); ghost != nil {
		t.Fatal("ghost feedback created a phantom doc")
	}

	// Log is sequenced append-only: opened, feedback, resolved (>=3 entries).
	logs, err := client.Collection("injuries").Doc(id).Collection("log").Documents(ctx).GetAll()
	if err != nil || len(logs) < 3 {
		t.Fatalf("log entries = %d, %v; want >= 3", len(logs), err)
	}
}

func TestWebSessions_FullLifecycle(t *testing.T) {
	client := emulatorClient(t)
	ws := NewWebSessions(client)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	nonce := fmt.Sprintf("n-%d", time.Now().UnixNano())

	// Single-use semantics: first redemption wins, second fails closed.
	ok, err := ws.RedeemNonce(ctx, nonce, 10*time.Minute)
	if err != nil || !ok {
		t.Fatalf("first redeem = %v, %v", ok, err)
	}
	ok, err = ws.RedeemNonce(ctx, nonce, 10*time.Minute)
	if err != nil || ok {
		t.Fatalf("replay = %v, %v; want false (Create semantics)", ok, err)
	}

	// Concurrent redemption of ONE nonce: exactly one winner.
	nonce2 := nonce + "-race"
	wins := make(chan bool, 2)
	for range 2 {
		go func() {
			ok, _ := ws.RedeemNonce(ctx, nonce2, 10*time.Minute)
			wins <- ok
		}()
	}
	a, b := <-wins, <-wins
	if a == b {
		t.Fatalf("concurrent redemption: wins=%v,%v; want exactly one true", a, b)
	}

	// Session round trip, expiry at read time, revocation.
	sid := fmt.Sprintf("s-%d", time.Now().UnixNano())
	if err := ws.SaveSession(ctx, sid, time.Hour); err != nil {
		t.Fatalf("save: %v", err)
	}
	if ok, _ := ws.CheckSession(ctx, sid); !ok {
		t.Fatal("fresh session rejected")
	}
	if ok, _ := ws.CheckSession(ctx, "ghost-id"); ok {
		t.Fatal("unknown session accepted")
	}
	// Past expiry must fail at READ time (Firestore TTL is lazy ~24h).
	expired := sid + "-old"
	_, _ = client.Collection("web_sessions").Doc(expired).Set(ctx, map[string]any{
		"created_at": time.Now().UTC().Add(-48 * time.Hour),
		"expires_at": time.Now().UTC().Add(-time.Hour),
	})
	if ok, _ := ws.CheckSession(ctx, expired); ok {
		t.Fatal("expired session accepted (read-time enforcement broken)")
	}
	// Corrupt expires_at type: fail CLOSED, never an eternal session.
	corrupt := sid + "-corrupt"
	_, _ = client.Collection("web_sessions").Doc(corrupt).Set(ctx, map[string]any{
		"expires_at": "not-a-time",
	})
	if ok, _ := ws.CheckSession(ctx, corrupt); ok {
		t.Fatal("corrupt session doc accepted (fail-open)")
	}
	if err := ws.DeleteSession(ctx, sid); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if ok, _ := ws.CheckSession(ctx, sid); ok {
		t.Fatal("revoked session still valid")
	}
}

func TestIdentity_SeedAndLoadRoundTrip(t *testing.T) {
	client := emulatorClient(t)
	p := NewProfiles(client)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	want := Identity{
		Sports:       []string{"Ride", "Run"},
		Races:        []Race{{Name: "GF", Date: "2026-09-20", Priority: "A"}},
		Availability: "60-75 min feriali",
		Zones: []SportZones{{Sport: "Ride", LTHR: 162, MaxHR: 179,
			Zones: []int{130, 144, 151, 161, 165, 170, 179}}},
	}
	if err := p.SeedIdentity(ctx, want); err != nil {
		t.Fatalf("SeedIdentity: %v", err)
	}
	got, err := p.Identity(ctx)
	if err != nil {
		t.Fatalf("Identity: %v", err)
	}
	if got.Sports[0] != "Ride" || got.Races[0].Date != "2026-09-20" || got.Zones[0].LTHR != 162 {
		t.Errorf("roundtrip = %+v", got)
	}

	// Missing doc degrades to empty, never errors (day-one behavior).
	_, _ = client.Collection("profile").Doc("identity").Delete(ctx)
	empty, err := p.Identity(ctx)
	if err != nil || len(empty.Sports) != 0 {
		t.Fatalf("missing identity = %+v, %v; want empty, nil", empty, err)
	}
}

func TestLedger_LatestPlanFor(t *testing.T) {
	client := emulatorClient(t)
	l := NewLedger(client)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ext := fmt.Sprintf("cadenza-2099-02-02-run-%d", time.Now().UnixNano())

	if err := l.Record(ctx, WriteRecord{Date: "2099-02-02", ExternalID: ext,
		ContentHash: "aaaa", Status: "verified", PlanJSON: `{"title":"vecchio"}`}); err != nil {
		t.Fatalf("record 1: %v", err)
	}
	time.Sleep(20 * time.Millisecond) // created_at ordering needs distinct stamps
	if err := l.Record(ctx, WriteRecord{Date: "2099-02-02", ExternalID: ext,
		ContentHash: "bbbb", Status: "verified", PlanJSON: `{"title":"nuovo"}`}); err != nil {
		t.Fatalf("record 2: %v", err)
	}

	got, err := l.LatestPlanFor(ctx, ext)
	if err != nil {
		t.Fatalf("LatestPlanFor: %v", err)
	}
	if !strings.Contains(got, "nuovo") {
		t.Fatalf("got %q, want the NEWEST plan (the one on the calendar)", got)
	}
	if got, err := l.LatestPlanFor(ctx, "cadenza-ghost"); err != nil || got != "" {
		t.Fatalf("no-match = %q, %v; want empty, nil", got, err)
	}
}

func TestCheckins_SetAndGetMerge(t *testing.T) {
	client := emulatorClient(t)
	c := NewCheckins(client)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	date := fmt.Sprintf("2099-ci-%d", time.Now().UnixNano())

	if err := c.SetField(ctx, date, "feeling", "stanco"); err != nil {
		t.Fatalf("SetField: %v", err)
	}
	if err := c.SetField(ctx, date, "time_budget", "short"); err != nil {
		t.Fatalf("SetField 2: %v", err)
	}
	// Re-tap overwrites (latest word wins).
	if err := c.SetField(ctx, date, "feeling", "dolorante"); err != nil {
		t.Fatalf("SetField 3: %v", err)
	}
	ci, err := c.Get(ctx, date)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ci.Feeling != "dolorante" || ci.TimeBudget != "short" {
		t.Fatalf("checkin = %+v", ci)
	}
	if ci.ExpiresAt.IsZero() {
		t.Error("no ExpiresAt: TTL has nothing to act on")
	}
	// Unknown field rejected; missing day = empty.
	if err := c.SetField(ctx, date, "mood", "x"); err == nil {
		t.Fatal("unknown field accepted")
	}
	empty, err := c.Get(ctx, "2099-mai")
	if err != nil || empty.Feeling != "" {
		t.Fatalf("missing = %+v, %v", empty, err)
	}
}
