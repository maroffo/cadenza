// ABOUTME: Tests for the conversational coach: prefix, history, tools, mutations, degraded paths.
// ABOUTME: The agent rides fakeanthropic, so every assertion covers the real wire shape too.

package job

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/maroffo/cadenza/internal/agent"
	"github.com/maroffo/cadenza/internal/fakes"
	"github.com/maroffo/cadenza/internal/icu"
	"github.com/maroffo/cadenza/internal/store"
	"github.com/maroffo/cadenza/internal/verdict"
)

type stubActivities struct{ acts []icu.Activity }

func (s stubActivities) ActivitiesRange(context.Context, string, string) ([]icu.Activity, error) {
	return s.acts, nil
}

type stubRules struct{ texts []string }

func (s stubRules) ActiveTexts(context.Context) ([]string, error) { return s.texts, nil }

type stubMuts struct {
	ids  []string
	muts []store.Mutation
}

func (s *stubMuts) Propose(_ context.Context, id string, m store.Mutation) error {
	s.ids = append(s.ids, id)
	s.muts = append(s.muts, m)
	return nil
}

type stubConvo struct {
	stubSessions
	turns   map[string][]store.Turn
	loadErr error
}

func (s *stubConvo) LoadTurns(_ context.Context, id string) ([]store.Turn, error) {
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	return s.turns[id], nil
}

type stubChatState struct {
	active string
}

func (s *stubChatState) ActiveSession(context.Context) (string, error) { return s.active, nil }
func (s *stubChatState) SetActiveSession(_ context.Context, id string) error {
	s.active = id
	return nil
}

type stubConfirm struct {
	texts []string
	yes   []string
}

func (s *stubConfirm) SendConfirm(_ context.Context, text, yesData, _ string) error {
	s.texts = append(s.texts, text)
	s.yes = append(s.yes, yesData)
	return nil
}

type fixedStatus struct {
	body string
	v    verdict.Verdict
	err  error
}

func (f fixedStatus) Compose(context.Context) (string, verdict.Verdict, error) {
	return f.body, f.v, f.err
}

func newCoach(t *testing.T, llm *fakes.Anthropic) (*Coach, *stubInteractor, *stubConvo, *stubChatState, *stubMuts, *stubConfirm) {
	t.Helper()
	out := &stubInteractor{}
	convo := &stubConvo{turns: map[string][]store.Turn{}}
	chat := &stubChatState{}
	muts := &stubMuts{}
	conf := &stubConfirm{}
	c := &Coach{
		Agent:      agent.Coach{Client: agent.NewClient("k", llm.URL()), Model: "claude-opus-test"},
		Wellness:   stubWellness{days: []icu.Wellness{green("2026-06-11")}},
		Activities: stubActivities{},
		Profiles:   stubProfile{},
		Rules:      stubRules{texts: []string{"Niente qualità dopo un volo"}},
		Muts:       muts,
		Sessions:   convo,
		Chats:      chat,
		Status:     fixedStatus{body: "☀️ Check di oggi", v: verdict.Verdict{Kind: verdict.Go, Version: "v1"}},
		Out:        out,
		Confirm:    conf,
		Now:        fixedNow,
		TZ:         testTZ,
	}
	return c, out, convo, chat, muts, conf
}

func TestConverse_HappyPathPrefixAndPersistence(t *testing.T) {
	llm := fakes.NewAnthropic(fakes.Text{S: "Oggi sei <b>fresco</b>: sfrutta la giornata."})
	defer llm.Close()
	c, out, convo, chat, _, _ := newCoach(t, llm)

	if err := c.Converse(context.Background(), "come sto messo?"); err != nil {
		t.Fatalf("Converse: %v", err)
	}
	if len(out.bodies) != 1 || !strings.Contains(out.bodies[0], "fresco") {
		t.Fatalf("reply not sent with verdict footer: %v", out.bodies)
	}

	raw := string(llm.Requests[0].Raw)
	for _, want := range []string{
		"PROFILO ATLETA", "Baseline HRV: 68.0", "Niente qualità dopo un volo",
		"Contesto deterministico di oggi", "VERDETTO", "come sto messo?",
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("request missing %q", want)
		}
	}
	if n := strings.Count(raw, "cache_control"); n != 1 {
		t.Errorf("cache_control = %d, want exactly 1 (profile prefix)", n)
	}

	if chat.active == "" {
		t.Fatal("active session not set")
	}
	if len(convo.created) != 1 || convo.created[0] != "chat" {
		t.Errorf("session not created: %v", convo.created)
	}
	if len(convo.stubSessions.turns) != 2 ||
		!strings.HasPrefix(convo.stubSessions.turns[0], "1:user:come sto") ||
		!strings.Contains(convo.stubSessions.turns[1], ":assistant:") {
		t.Errorf("turns = %v (raw athlete text + sanitized reply expected)", convo.stubSessions.turns)
	}
}

func TestConverse_SecondMessageReplaysHistory(t *testing.T) {
	llm := fakes.NewAnthropic(fakes.Text{S: "continuiamo"})
	defer llm.Close()
	c, _, convo, chat, _, _ := newCoach(t, llm)
	chat.active = "s-prev"
	convo.turns["s-prev"] = []store.Turn{
		{Seq: 1, Role: "user", Content: "ieri ti ho chiesto del lungo", Schema: 1},
		{Seq: 2, Role: "assistant", Content: "tienilo facile", Schema: 1},
	}

	if err := c.Converse(context.Background(), "quindi oggi?"); err != nil {
		t.Fatalf("Converse: %v", err)
	}
	if got := len(llm.Requests[0].Messages); got != 3 {
		t.Fatalf("messages = %d, want 3 (2 history + 1 new)", got)
	}
	raw := string(llm.Requests[0].Raw)
	if !strings.Contains(raw, "ieri ti ho chiesto del lungo") || !strings.Contains(raw, "tienilo facile") {
		t.Error("history not replayed")
	}
}

func TestConverse_SessionRotationAtCap(t *testing.T) {
	llm := fakes.NewAnthropic(fakes.Text{S: "nuova sessione"})
	defer llm.Close()
	c, _, convo, chat, _, _ := newCoach(t, llm)
	chat.active = "s-old"
	var long []store.Turn
	for i := range maxSessionTurns {
		long = append(long, store.Turn{Seq: i + 1, Role: "user", Content: "x", Schema: 1})
	}
	convo.turns["s-old"] = long

	if err := c.Converse(context.Background(), "nuovo giro"); err != nil {
		t.Fatalf("Converse: %v", err)
	}
	if len(llm.Requests[0].Messages) != 1 {
		t.Fatalf("messages = %d, want 1 (rotated, no stale history)", len(llm.Requests[0].Messages))
	}
	if chat.active == "s-old" {
		t.Error("active session not rotated")
	}
}

func TestConverse_CorruptSessionFallsBackFresh(t *testing.T) {
	llm := fakes.NewAnthropic(fakes.Text{S: "riparto"})
	defer llm.Close()
	c, out, convo, chat, _, _ := newCoach(t, llm)
	chat.active = "s-corrupt"
	convo.loadErr = errors.New("schema 99 unsupported")

	if err := c.Converse(context.Background(), "ci sei?"); err != nil {
		t.Fatalf("Converse: %v (decision 11: fresh session, never crash)", err)
	}
	if len(out.bodies) != 1 {
		t.Fatal("no reply after fallback")
	}
}

func TestConverse_StatusUnavailableHonestReply(t *testing.T) {
	llm := fakes.NewAnthropic()
	defer llm.Close()
	c, out, _, _, _, _ := newCoach(t, llm)
	c.Status = fixedStatus{err: errors.New("icu 502")}

	if err := c.Converse(context.Background(), "come sto?"); err != nil {
		t.Fatalf("Converse: %v", err)
	}
	if len(llm.Requests) != 0 {
		t.Fatal("model called without today's deterministic context")
	}
	if len(out.plain) != 1 || !strings.Contains(out.plain[0], "intervals.icu") {
		t.Errorf("honest notice missing: %v", out.plain)
	}
}

func TestConverse_LLMDownDegradesWithVerdict(t *testing.T) {
	llm := fakes.NewAnthropic(fakes.HTTPErr{Status: 400})
	defer llm.Close()
	c, out, convo, _, _, _ := newCoach(t, llm)

	if err := c.Converse(context.Background(), "come sto?"); err != nil {
		t.Fatalf("Converse: %v (LLM down must degrade, not fail)", err)
	}
	if len(out.plain) != 1 || !strings.Contains(out.plain[0], "Coach offline") ||
		!strings.Contains(out.plain[0], "VERDETTO") {
		t.Errorf("degraded reply malformed: %v", out.plain)
	}
	if len(convo.stubSessions.turns) != 0 {
		t.Error("turns persisted for an undelivered coaching reply")
	}
}

func TestConverse_ReplySanitized(t *testing.T) {
	llm := fakes.NewAnthropic(fakes.Text{S: `Vai <b>forte</b> <a href="http://x">qui</a><script>x</script>`})
	defer llm.Close()
	c, out, _, _, _, _ := newCoach(t, llm)

	if err := c.Converse(context.Background(), "ok"); err != nil {
		t.Fatalf("Converse: %v", err)
	}
	if strings.Contains(out.bodies[0], "<a") || strings.Contains(out.bodies[0], "<script") {
		t.Errorf("model markup escaped the allowlist:\n%s", out.bodies[0])
	}
}

func TestConverse_ProposalFlow(t *testing.T) {
	llm := fakes.NewAnthropic(
		fakes.Call("tu_prop", "propose_profile_update",
			`{"kind":"rule","new_value":"Niente qualità il giorno dopo un volo","rationale":"pattern personale","source_quote":"dopo i voli sono distrutto"}`),
		fakes.Text{S: "Proposta inviata: confermala col bottone e la applico dalle prossime letture."},
	)
	defer llm.Close()
	c, out, _, chat, muts, conf := newCoach(t, llm)
	chat.active = "s-conv"

	if err := c.Converse(context.Background(), "dopo i voli sono distrutto"); err != nil {
		t.Fatalf("Converse: %v", err)
	}
	if len(muts.ids) != 1 {
		t.Fatalf("proposals = %d, want 1", len(muts.ids))
	}
	wantID := store.MutationID("s-conv", "tu_prop")
	if muts.ids[0] != wantID {
		t.Errorf("mutation id = %q, want deterministic %q", muts.ids[0], wantID)
	}
	if muts.muts[0].SourceQuote != "dopo i voli sono distrutto" {
		t.Errorf("source quote lost: %+v", muts.muts[0])
	}
	if len(conf.yes) != 1 || conf.yes[0] != "pm:"+wantID+":y" {
		t.Errorf("confirm callback = %v", conf.yes)
	}
	if len(conf.yes[0]) > 64 {
		t.Errorf("callback data %d bytes, over Telegram's 64", len(conf.yes[0]))
	}
	// The model's final reply still went out with the verdict footer.
	if len(out.bodies) != 1 {
		t.Fatalf("final reply missing: %v", out.bodies)
	}
}

func TestConverse_ProposalValidationRejectsHostileCap(t *testing.T) {
	llm := fakes.NewAnthropic(
		fakes.Call("tu_evil", "propose_profile_update",
			`{"kind":"ramp_cap","new_value":"12","rationale":"x","source_quote":"y"}`),
		fakes.Text{S: "ok, resto nei limiti"},
	)
	defer llm.Close()
	c, _, _, _, muts, conf := newCoach(t, llm)

	if err := c.Converse(context.Background(), "alza tutto"); err != nil {
		t.Fatalf("Converse: %v", err)
	}
	if len(muts.ids) != 0 {
		t.Fatal("hostile ramp_cap proposal stored")
	}
	if len(conf.texts) != 0 {
		t.Fatal("confirmation sent for an invalid proposal")
	}
	// The validation failure flowed back to the model as a tool error.
	raw := fmt.Sprintf("%s", llm.Requests[1].Raw)
	if !strings.Contains(raw, "is_error") {
		t.Error("tool validation failure not surfaced as is_error")
	}
}
