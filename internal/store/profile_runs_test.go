// ABOUTME: Emulator tests for the profile and runs repositories.
// ABOUTME: Skips without FIRESTORE_EMULATOR_HOST; REQUIRE_EMULATOR=1 turns skips fatal.

package store

import (
	"context"
	"fmt"
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
