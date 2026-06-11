// ABOUTME: Tests for injury wake-ups: escalation ladder, stale revisions, reconcile healing.
// ABOUTME: The firm physio referral is deterministic and must fire exactly when promised.

package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/maroffo/cadenza/internal/store"
	"github.com/maroffo/cadenza/internal/task"
)

type stubInjuries struct {
	byID     map[string]*store.Injury
	feedback []string
	resolved []string
	logs     []string
	openErr  error
}

func newStubInjuries() *stubInjuries {
	return &stubInjuries{byID: map[string]*store.Injury{}}
}

func (s *stubInjuries) Get(_ context.Context, id string) (*store.Injury, error) {
	return s.byID[id], nil
}

func (s *stubInjuries) ListOpen(context.Context) ([]store.Injury, error) {
	var out []store.Injury
	for _, inj := range s.byID {
		if inj.Status == "open" {
			out = append(out, *inj)
		}
	}
	return out, nil
}

func (s *stubInjuries) RecordFeedback(_ context.Context, id, fb string) error {
	s.feedback = append(s.feedback, id+":"+fb)
	if inj := s.byID[id]; inj != nil {
		inj.LastFeedback = fb
	}
	return nil
}

func (s *stubInjuries) Resolve(_ context.Context, id string) error {
	s.resolved = append(s.resolved, id)
	if inj := s.byID[id]; inj != nil {
		inj.Status = "resolved"
		inj.Rev++
	}
	return nil
}

func (s *stubInjuries) AppendLog(_ context.Context, id, kind, note string) error {
	s.logs = append(s.logs, id+":"+kind)
	return nil
}

func (s *stubInjuries) Open(_ context.Context, id string, inj store.Injury) error {
	if s.openErr != nil {
		return s.openErr
	}
	inj.Status = "open"
	inj.Rev = 1
	inj.ID = id
	inj.OpenedAt = fixedNow()
	s.byID[id] = &inj
	return nil
}

type stubKeyboard struct {
	texts   []string
	buttons [][][2]string
}

func (s *stubKeyboard) SendKeyboard(_ context.Context, text string, buttons [][2]string) error {
	s.texts = append(s.texts, text)
	s.buttons = append(s.buttons, buttons)
	return nil
}

func newInjuryJob(inj *stubInjuries) (InjuryJob, *stubInteractor, *stubKeyboard, *stubDelayed) {
	out := &stubInteractor{}
	kb := &stubKeyboard{}
	retry := &stubDelayed{}
	return InjuryJob{
		Injuries: inj, Out: out, Keyboard: kb, Retry: retry,
		Now: fixedNow, TZ: testTZ,
	}, out, kb, retry
}

func wakeupEnv(t *testing.T, injID string, day, rev int) task.Envelope {
	t.Helper()
	payload, _ := json.Marshal(injuryPayload{InjuryID: injID, Day: day, Rev: rev})
	return task.Envelope{
		V: 1, Type: task.TypeInjuryWakeup,
		ID: WakeupID(injID, day, rev), Payload: payload,
	}
}

func openInjury(s *stubInjuries, id string, feedback string) {
	s.byID[id] = &store.Injury{
		ID: id, BodyPart: "polpaccio", Pain: 5, Status: "open", Rev: 1,
		LastFeedback: feedback, OpenedAt: fixedNow().Add(-48 * time.Hour),
	}
}

func TestInjuryWakeup_Day2CheckinWithButtons(t *testing.T) {
	inj := newStubInjuries()
	openInjury(inj, "inj-x", "")
	j, _, kb, retry := newInjuryJob(inj)

	if err := j.Wakeup(context.Background(), wakeupEnv(t, "inj-x", 2, 1)); err != nil {
		t.Fatalf("Wakeup: %v", err)
	}
	if len(kb.texts) != 1 || !strings.Contains(kb.texts[0], "giorno 2") {
		t.Fatalf("checkin = %v", kb.texts)
	}
	want := map[string]bool{"inj:inj-x:better": true, "inj:inj-x:worse": true, "inj:inj-x:resolve": true}
	for _, b := range kb.buttons[0] {
		delete(want, b[1])
	}
	if len(want) != 0 {
		t.Errorf("buttons missing: %v", want)
	}
	// Day 5 chained.
	if len(retry.envs) != 1 || retry.envs[0].ID != "inj-x-day5-r1" {
		t.Fatalf("next wakeup = %+v, want day5 r1", retry.envs)
	}
}

func TestInjuryWakeup_Day5NotImprovingIsFirm(t *testing.T) {
	inj := newStubInjuries()
	openInjury(inj, "inj-x", "same")
	j, _, kb, retry := newInjuryJob(inj)

	if err := j.Wakeup(context.Background(), wakeupEnv(t, "inj-x", 5, 1)); err != nil {
		t.Fatalf("Wakeup: %v", err)
	}
	if len(kb.texts) != 1 || !strings.Contains(kb.texts[0], "fisioterapista") {
		t.Fatalf("day5 unimproved must name the physio: %v", kb.texts)
	}
	if len(retry.envs) != 1 || retry.envs[0].ID != "inj-x-day7-r1" {
		t.Fatalf("day7 not chained: %+v", retry.envs)
	}
}

func TestInjuryWakeup_Day7NotImprovingStopsFirmly(t *testing.T) {
	inj := newStubInjuries()
	openInjury(inj, "inj-x", "same")
	j, out, kb, retry := newInjuryJob(inj)

	if err := j.Wakeup(context.Background(), wakeupEnv(t, "inj-x", 7, 1)); err != nil {
		t.Fatalf("Wakeup: %v", err)
	}
	if len(out.plain) != 1 || !strings.Contains(out.plain[0], "prenota un fisioterapista") {
		t.Fatalf("day7 firm stop missing: %v", out.plain)
	}
	if len(kb.texts) != 0 {
		t.Error("day7 unimproved is a statement, not a question")
	}
	if len(retry.envs) != 0 {
		t.Error("no further wakeups after the firm stop")
	}
}

func TestInjuryWakeup_StaleRevAndResolvedDropSilently(t *testing.T) {
	inj := newStubInjuries()
	openInjury(inj, "inj-x", "")
	j, out, kb, _ := newInjuryJob(inj)

	// Wrong revision: resolved-and-reopened elsewhere.
	if err := j.Wakeup(context.Background(), wakeupEnv(t, "inj-x", 5, 99)); err != nil {
		t.Fatalf("stale rev: %v", err)
	}
	// Resolved injury.
	inj.byID["inj-x"].Status = "resolved"
	if err := j.Wakeup(context.Background(), wakeupEnv(t, "inj-x", 5, 1)); err != nil {
		t.Fatalf("resolved: %v", err)
	}
	// Unknown injury.
	if err := j.Wakeup(context.Background(), wakeupEnv(t, "inj-ghost", 2, 1)); err != nil {
		t.Fatalf("unknown: %v", err)
	}
	if len(out.plain)+len(kb.texts) != 0 {
		t.Fatal("stale wakeups produced athlete-visible noise")
	}
}

func TestInjuryReconcile_HealsFutureWakeupsOnly(t *testing.T) {
	inj := newStubInjuries()
	// Opened 3 days ago: day2 is past, day5 and day7 are future.
	inj.byID["inj-old"] = &store.Injury{
		ID: "inj-old", BodyPart: "tendine", Pain: 4, Status: "open", Rev: 1,
		OpenedAt: fixedNow().Add(-3 * 24 * time.Hour),
	}
	j, _, _, retry := newInjuryJob(inj)

	if err := j.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := map[string]bool{}
	for _, e := range retry.envs {
		got[e.ID] = true
	}
	if !got["inj-old-day5-r1"] || !got["inj-old-day7-r1"] {
		t.Fatalf("future wakeups not healed: %v", got)
	}
	if got["inj-old-day2-r1"] {
		t.Error("past wakeup re-enqueued (would fire immediately)")
	}
}

func TestInjuryWakeup_BadPayloadIsPoison(t *testing.T) {
	inj := newStubInjuries()
	j, _, _, _ := newInjuryJob(inj)
	env := task.Envelope{V: 1, Type: task.TypeInjuryWakeup, ID: "x", Payload: json.RawMessage(`{broken`)}
	if err := j.Wakeup(context.Background(), env); !errors.Is(err, task.ErrPoison) {
		t.Fatalf("err = %v, want ErrPoison", err)
	}
}

func TestMessage_InjuryCallbacks(t *testing.T) {
	inj := newStubInjuries()
	openInjury(inj, "inj-x", "")
	out := &stubInteractor{}
	m := newMessage(out, newStubDedup(), &stubChats{})
	m.Injuries = inj

	cases := []struct {
		action, want string
	}{
		{"better", "monitorare"},
		{"same", "fisioterapista"},
		{"worse", "Mai allenarsi attraverso"},
		{"resolve", "risolto"},
	}
	for n, tc := range cases {
		cb := fmt.Sprintf(`{"update_id":%d,"callback_query":{"id":"cb","data":"inj:inj-x:%s","from":{"id":%d}}}`, 60+n, tc.action, allowedID)
		if err := m.Run(context.Background(), envelopeFor(t, int64(60+n), cb)); err != nil {
			t.Fatalf("%s: %v", tc.action, err)
		}
		reply := out.plain[len(out.plain)-1]
		if !strings.Contains(strings.ToLower(reply), strings.ToLower(tc.want)) {
			t.Errorf("%s reply = %q, want mention of %q", tc.action, reply, tc.want)
		}
	}
	if len(inj.feedback) != 3 || len(inj.resolved) != 1 {
		t.Errorf("feedback=%v resolved=%v", inj.feedback, inj.resolved)
	}
}

func TestConverse_LogInjuryToolOpensAndSchedules(t *testing.T) {
	llm := newCoachLLM(t,
		`{"body_part":"polpaccio destro","pain":6,"notes":"fitta in salita"}`)
	defer llm.Close()
	c, out, _, _, _, _ := newCoach(t, llm)
	injuries := newStubInjuries()
	retry := &stubDelayed{}
	c.Injuries = injuries
	c.InjurySched = &InjuryJob{Injuries: injuries, Retry: retry, Now: fixedNow, TZ: testTZ}

	if err := c.Converse(context.Background(), "ho una fitta al polpaccio destro"); err != nil {
		t.Fatalf("Converse: %v", err)
	}
	id := store.InjuryID("2026-06-10", "polpaccio destro")
	if injuries.byID[id] == nil || injuries.byID[id].Pain != 6 {
		t.Fatalf("injury not opened: %+v", injuries.byID)
	}
	if len(retry.envs) != 1 || !strings.Contains(retry.envs[0].ID, "day2-r1") {
		t.Fatalf("day2 wakeup not scheduled: %+v", retry.envs)
	}
	if len(out.bodies) != 1 {
		t.Fatal("reply missing")
	}
}
