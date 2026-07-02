// ABOUTME: Tests for the morning job: idempotency, stale data, error propagation, verdict wiring.
// ABOUTME: All dependencies stubbed; no Firestore, no network.

package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/maroffo/cadenza/internal/agent"
	"github.com/maroffo/cadenza/internal/exercises"
	"github.com/maroffo/cadenza/internal/icu"
	"github.com/maroffo/cadenza/internal/store"
	"github.com/maroffo/cadenza/internal/task"
	"github.com/maroffo/cadenza/internal/verdict"
)

func fp(v float64) *float64 { return &v }
func ip(v int) *int         { return &v }

type stubWellness struct {
	days []icu.Wellness
	err  error
}

func (s stubWellness) WellnessRange(context.Context, string, string) ([]icu.Wellness, error) {
	return s.days, s.err
}

type stubProfile struct{ identity store.Identity }

func (s stubProfile) Identity(context.Context) (store.Identity, error) {
	return s.identity, nil
}

func (stubProfile) Profile(context.Context) (verdict.Baselines, float64, error) {
	return verdict.Baselines{HRVMean: 68, HRVSD: 6, RestingHR: 47}, 4.0, nil
}

type stubMessenger struct {
	bodies   []string
	verdicts []verdict.Verdict
	plain    []string
	err      error
}

func (s *stubMessenger) SendWithVerdict(_ context.Context, body string, v verdict.Verdict) error {
	if s.err != nil {
		return s.err
	}
	s.bodies = append(s.bodies, body)
	s.verdicts = append(s.verdicts, v)
	return nil
}

func (s *stubMessenger) Send(_ context.Context, body string) error {
	if s.err != nil {
		return s.err
	}
	s.plain = append(s.plain, body)
	return nil
}

type stubRuns struct {
	completed map[string]string
	deferred  map[string]int
	checkErr  error
	markErr   error
}

func newStubRuns() *stubRuns {
	return &stubRuns{completed: map[string]string{}, deferred: map[string]int{}}
}

func (s *stubRuns) MorningCompleted(_ context.Context, date string) (bool, error) {
	if s.checkErr != nil {
		return false, s.checkErr
	}
	_, ok := s.completed[date]
	return ok, nil
}

func (s *stubRuns) MorningAlive(_ context.Context, date string) (bool, error) {
	if s.checkErr != nil {
		return false, s.checkErr
	}
	_, done := s.completed[date]
	_, def := s.deferred[date]
	return done || def, nil
}

func (s *stubRuns) MarkMorningCompleted(_ context.Context, date, status string) error {
	if s.markErr != nil {
		return s.markErr
	}
	s.completed[date] = status
	return nil
}

func (s *stubRuns) MarkMorningDeferred(_ context.Context, date string, attempt int) error {
	if s.markErr != nil {
		return s.markErr
	}
	s.deferred[date] = attempt
	return nil
}

var testTZ = time.FixedZone("Europe/Rome", 2*3600)

func fixedNow() time.Time {
	return time.Date(2026, 6, 10, 7, 0, 0, 0, testTZ)
}

func green(date string) icu.Wellness {
	return icu.Wellness{
		ID: date, HRV: fp(69), RestingHR: ip(47), SleepSecs: ip(7 * 3600),
		CTL: fp(41.3), ATL: fp(47.9), RampRate: fp(2.0),
	}
}

func newMorning(w stubWellness, out *stubMessenger, runs *stubRuns) Morning {
	return Morning{
		Wellness: w, Profiles: stubProfile{}, Out: out, Runs: runs,
		Now: fixedNow, TZ: testTZ,
	}
}

func TestMorning_HappyPath(t *testing.T) {
	out := &stubMessenger{}
	runs := newStubRuns()
	m := newMorning(stubWellness{days: []icu.Wellness{
		green("2026-06-08"), green("2026-06-09"), green("2026-06-10"),
	}}, out, runs)

	if err := m.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(out.bodies) != 1 {
		t.Fatalf("sends = %d, want 1", len(out.bodies))
	}
	if !strings.Contains(out.bodies[0], "69") {
		t.Errorf("body missing today's HRV:\n%s", out.bodies[0])
	}
	if out.verdicts[0].Kind != verdict.Go {
		t.Errorf("verdict = %s, want GO (reasons %+v)", out.verdicts[0].Kind, out.verdicts[0].Reasons)
	}
	if runs.completed["2026-06-10"] != "GO" {
		t.Errorf("run not marked completed: %+v", runs.completed)
	}
}

func TestMorning_AlreadyCompletedIsNoop(t *testing.T) {
	out := &stubMessenger{}
	runs := newStubRuns()
	runs.completed["2026-06-10"] = "GO"
	m := newMorning(stubWellness{days: []icu.Wellness{green("2026-06-10")}}, out, runs)

	if err := m.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(out.bodies)+len(out.plain) != 0 {
		t.Fatalf("no-op must not send; sent %d", len(out.bodies)+len(out.plain))
	}
}

func TestMorning_ICUErrorPropagatesNoSend(t *testing.T) {
	out := &stubMessenger{}
	runs := newStubRuns()
	m := newMorning(stubWellness{err: errors.New("502")}, out, runs)

	if err := m.Run(context.Background()); err == nil {
		t.Fatal("Run = nil, want error so the caller's retry policy fires")
	}
	if len(out.bodies)+len(out.plain) != 0 {
		t.Fatal("must not send on fetch failure (watchdog covers absence)")
	}
	if len(runs.completed) != 0 {
		t.Fatal("must not mark completed on failure")
	}
}

func TestMorning_TodayMissingIsStaleAndConservative(t *testing.T) {
	out := &stubMessenger{}
	runs := newStubRuns()
	m := newMorning(stubWellness{days: []icu.Wellness{
		green("2026-06-08"), green("2026-06-09"), // nothing for the 10th
	}}, out, runs)

	if err := m.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	body := out.bodies[0]
	if !strings.Contains(body, "2026-06-09") {
		t.Errorf("stale body must carry the data date:\n%s", body)
	}
	v := out.verdicts[0]
	if v.Kind == verdict.Go {
		t.Errorf("verdict with no today data must be conservative, got GO")
	}
	if len(v.DataGaps) == 0 {
		t.Error("missing today must surface data gaps")
	}
}

func TestMorning_SendFailurePropagates(t *testing.T) {
	out := &stubMessenger{err: errors.New("telegram down")}
	runs := newStubRuns()
	m := newMorning(stubWellness{days: []icu.Wellness{green("2026-06-10")}}, out, runs)

	if err := m.Run(context.Background()); err == nil {
		t.Fatal("Run = nil, want send error")
	}
	if len(runs.completed) != 0 {
		t.Fatal("must not mark completed when send failed")
	}
}

func TestWatchdog_QuietWhenMorningCompleted(t *testing.T) {
	out := &stubMessenger{}
	runs := newStubRuns()
	runs.completed["2026-06-10"] = "GO"
	w := Watchdog{Runs: runs, Out: out, Now: fixedNow, TZ: testTZ}

	if err := w.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(out.plain) != 0 {
		t.Fatal("watchdog must stay quiet when morning completed")
	}
}

func TestWatchdog_AlertsWhenMorningMissing(t *testing.T) {
	out := &stubMessenger{}
	runs := newStubRuns()
	w := Watchdog{Runs: runs, Out: out, Now: fixedNow, TZ: testTZ}

	if err := w.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(out.plain) != 1 {
		t.Fatalf("sends = %d, want 1 missed-morning notice", len(out.plain))
	}
	if !strings.Contains(out.plain[0], "mancato") {
		t.Errorf("unexpected notice:\n%s", out.plain[0])
	}
}

func TestMorning_WindowSortedBeforeConsecutiveRun(t *testing.T) {
	// API order [08-green, 07-low, 09-low] + today low. Without sorting, the
	// backwards scan would see 09,07 both low and SKIP after 2 real days;
	// sorted, 09-low then 08-green breaks the run: MODIFY hrv_low.
	low := func(date string) icu.Wellness {
		d := green(date)
		d.HRV = fp(58)
		return d
	}
	out := &stubMessenger{}
	runs := newStubRuns()
	m := newMorning(stubWellness{days: []icu.Wellness{
		green("2026-06-08"), low("2026-06-07"), low("2026-06-09"), low("2026-06-10"),
	}}, out, runs)

	if err := m.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(out.verdicts) != 1 {
		t.Fatalf("verdicts = %d, want 1", len(out.verdicts))
	}
	v := out.verdicts[0]
	if v.Kind != verdict.Modify {
		t.Errorf("Kind = %s, want MODIFY (green 06-08 must break the run)", v.Kind)
	}
	for _, r := range v.Reasons {
		if r.RuleID == "hrv_low_3d" {
			t.Error("hrv_low_3d fired on a non-consecutive run (window not sorted?)")
		}
	}
}

func TestMorning_DuplicateWindowDatesNotDoubleCounted(t *testing.T) {
	// The same low day delivered twice must not count as two low days.
	low := func(date string) icu.Wellness {
		d := green(date)
		d.HRV = fp(58)
		return d
	}
	out := &stubMessenger{}
	runs := newStubRuns()
	m := newMorning(stubWellness{days: []icu.Wellness{
		low("2026-06-09"), low("2026-06-09"), low("2026-06-10"),
	}}, out, runs)

	if err := m.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	v := out.verdicts[0]
	for _, r := range v.Reasons {
		if r.RuleID == "hrv_low_3d" {
			t.Error("hrv_low_3d fired with only 2 distinct low days (duplicates double-counted)")
		}
	}
}

func TestMorning_RunsCheckErrorPropagates(t *testing.T) {
	out := &stubMessenger{}
	runs := newStubRuns()
	runs.checkErr = errors.New("firestore down")
	m := newMorning(stubWellness{days: []icu.Wellness{green("2026-06-10")}}, out, runs)

	if err := m.Run(context.Background()); err == nil {
		t.Fatal("Run = nil, want completion-check error")
	}
	if len(out.bodies) != 0 {
		t.Fatal("must not send when the idempotency check is unavailable")
	}
}

func TestMorning_MarkErrorAfterSendPropagates(t *testing.T) {
	out := &stubMessenger{}
	runs := newStubRuns()
	runs.markErr = errors.New("firestore down")
	m := newMorning(stubWellness{days: []icu.Wellness{green("2026-06-10")}}, out, runs)

	err := m.Run(context.Background())
	if err == nil {
		t.Fatal("Run = nil, want mark error (caller must retry until the mark sticks)")
	}
	if len(out.bodies) != 1 {
		t.Fatalf("sends = %d, want 1 (message goes out before the mark)", len(out.bodies))
	}
}

func TestBaselines_RHRFloorRejected(t *testing.T) {
	days := wellnessWith(repeatF(60, 20), repeatI(47, 5))
	if _, err := ComputeBaselines(days); err == nil {
		t.Fatal("5 resting HR samples accepted; RHR baseline floor must apply")
	}
}

type stubDelayed struct {
	envs []task.Envelope
	ats  []time.Time
	err  error
}

func (s *stubDelayed) EnqueueAt(_ context.Context, e task.Envelope, at time.Time) error {
	if s.err != nil {
		return s.err
	}
	s.envs = append(s.envs, e)
	s.ats = append(s.ats, at)
	return nil
}

func noHRV(date string) icu.Wellness {
	d := green(date)
	d.HRV = nil
	return d
}

func TestMorning_MissingHRVDefersWithRetry(t *testing.T) {
	out := &stubMessenger{}
	runs := newStubRuns()
	retry := &stubDelayed{}
	m := newMorning(stubWellness{days: []icu.Wellness{
		green("2026-06-09"), noHRV("2026-06-10"),
	}}, out, runs)
	m.Retry = retry

	if err := m.RunAttempt(context.Background(), 0); err != nil {
		t.Fatalf("RunAttempt: %v", err)
	}
	if len(out.bodies)+len(out.plain) != 0 {
		t.Fatal("deferred run must not send")
	}
	if runs.deferred["2026-06-10"] != 1 {
		t.Errorf("deferred mark = %v, want attempt 1", runs.deferred)
	}
	if len(retry.envs) != 1 {
		t.Fatalf("retries scheduled = %d, want 1", len(retry.envs))
	}
	env := retry.envs[0]
	if env.ID != "morning-2026-06-10-r1" || env.Type != task.TypeMorningCheck {
		t.Errorf("retry envelope = %+v", env)
	}
	if want := fixedNow().Add(MorningRetryDelay); !retry.ats[0].Equal(want) {
		t.Errorf("retry at = %v, want %v", retry.ats[0], want)
	}
	if _, done := runs.completed["2026-06-10"]; done {
		t.Fatal("deferred run must not be marked completed")
	}
}

func TestMorning_TerminalAttemptSendsDespiteMissingHRV(t *testing.T) {
	out := &stubMessenger{}
	runs := newStubRuns()
	retry := &stubDelayed{}
	m := newMorning(stubWellness{days: []icu.Wellness{
		green("2026-06-09"), noHRV("2026-06-10"),
	}}, out, runs)
	m.Retry = retry

	if err := m.RunAttempt(context.Background(), MaxMorningRetries); err != nil {
		t.Fatalf("RunAttempt: %v", err)
	}
	if len(retry.envs) != 0 {
		t.Fatal("terminal attempt must not schedule another retry")
	}
	if len(out.bodies) != 1 {
		t.Fatalf("sends = %d, want 1 (late beats silent)", len(out.bodies))
	}
	if len(out.verdicts[0].DataGaps) == 0 {
		t.Error("terminal send without HRV must surface data gaps")
	}
	if _, done := runs.completed["2026-06-10"]; !done {
		t.Fatal("terminal attempt must mark completed")
	}
}

func TestMorning_NoRetryConfiguredSendsImmediately(t *testing.T) {
	out := &stubMessenger{}
	runs := newStubRuns()
	m := newMorning(stubWellness{days: []icu.Wellness{noHRV("2026-06-10")}}, out, runs)
	// m.Retry nil: deferral disabled.

	if err := m.RunAttempt(context.Background(), 0); err != nil {
		t.Fatalf("RunAttempt: %v", err)
	}
	if len(out.bodies) != 1 {
		t.Fatalf("sends = %d, want 1", len(out.bodies))
	}
}

func TestMorning_PresentHRVNeverDefers(t *testing.T) {
	out := &stubMessenger{}
	runs := newStubRuns()
	retry := &stubDelayed{}
	m := newMorning(stubWellness{days: []icu.Wellness{green("2026-06-10")}}, out, runs)
	m.Retry = retry

	if err := m.RunAttempt(context.Background(), 0); err != nil {
		t.Fatalf("RunAttempt: %v", err)
	}
	if len(retry.envs) != 0 {
		t.Fatal("retry scheduled despite synced HRV")
	}
	if len(out.bodies) != 1 {
		t.Fatalf("sends = %d, want 1", len(out.bodies))
	}
}

func TestMorning_RetryScheduleFailurePropagates(t *testing.T) {
	out := &stubMessenger{}
	runs := newStubRuns()
	retry := &stubDelayed{err: errors.New("queue down")}
	m := newMorning(stubWellness{days: []icu.Wellness{noHRV("2026-06-10")}}, out, runs)
	m.Retry = retry

	if err := m.RunAttempt(context.Background(), 0); err == nil {
		t.Fatal("RunAttempt = nil, want error (caller retry must re-drive the deferral)")
	}
	if len(out.bodies) != 0 {
		t.Fatal("must not send when deferral failed")
	}
}

func TestWatchdog_QuietWhileRetryInFlight(t *testing.T) {
	out := &stubMessenger{}
	runs := newStubRuns()
	runs.deferred["2026-06-10"] = 1
	w := Watchdog{Runs: runs, Out: out, Now: fixedNow, TZ: testTZ}

	if err := w.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(out.plain) != 0 {
		t.Fatal("watchdog alerted during an in-flight HRV retry (false alarm)")
	}
}

func TestDispatch_MorningAttemptFromPayload(t *testing.T) {
	out := &stubMessenger{}
	runs := newStubRuns()
	retry := &stubDelayed{}
	m := newMorning(stubWellness{days: []icu.Wellness{noHRV("2026-06-10")}}, out, runs)
	m.Retry = retry
	d := Deps{Morning: m}

	// Terminal attempt via payload: must send, not defer.
	env := task.Envelope{
		V: 1, Type: task.TypeMorningCheck, ID: "morning-2026-06-10-r2",
		Payload: json.RawMessage(`{"attempt":2}`),
	}
	if err := d.Dispatch(context.Background(), env); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(retry.envs) != 0 || len(out.bodies) != 1 {
		t.Errorf("attempt not honored: retries=%d sends=%d", len(retry.envs), len(out.bodies))
	}

	// Malformed payload is poison.
	bad := task.Envelope{V: 1, Type: task.TypeMorningCheck, ID: "x", Payload: json.RawMessage(`{broken`)}
	if err := d.Dispatch(context.Background(), bad); !errors.Is(err, task.ErrPoison) {
		t.Fatalf("err = %v, want ErrPoison", err)
	}
}

type stubNarrator struct {
	out string
	err error
}

func (s stubNarrator) MorningNarrative(_ context.Context, in agent.NarrativeInput) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	return s.out, nil
}

type stubSessions struct {
	created []string
	turns   []string
	err     error
}

func (s *stubSessions) Create(_ context.Context, mode string, _ time.Time) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	s.created = append(s.created, mode)
	return "s-test-1", nil
}

func (s *stubSessions) AppendTurn(_ context.Context, _ string, seq int, role, content, model string) error {
	if s.err != nil {
		return s.err
	}
	s.turns = append(s.turns, fmt.Sprintf("%d:%s:%s:%s", seq, role, content[:min(8, len(content))], model))
	return nil
}

func TestMorning_NarrativePrependedAndSessionRecorded(t *testing.T) {
	out := &stubMessenger{}
	runs := newStubRuns()
	sess := &stubSessions{}
	m := newMorning(stubWellness{days: []icu.Wellness{green("2026-06-10")}}, out, runs)
	m.Narrator = stubNarrator{out: "Giornata verde, corri sereno."}
	m.Sessions = sess
	m.ModelName = "claude-haiku-test"

	if err := m.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	msg := out.bodies[0]
	if !strings.HasPrefix(msg, "Giornata verde") {
		t.Errorf("narrative not prepended:\n%s", msg)
	}
	if !strings.Contains(msg, "Check mattutino") {
		t.Errorf("deterministic body lost:\n%s", msg)
	}
	if runs.completed["2026-06-10"] != "GO" {
		t.Errorf("status = %q, want GO", runs.completed["2026-06-10"])
	}
	if len(sess.created) != 1 || sess.created[0] != "morning" {
		t.Errorf("session not created: %+v", sess.created)
	}
	if len(sess.turns) != 2 ||
		!strings.HasPrefix(sess.turns[0], "1:user:") ||
		!strings.HasPrefix(sess.turns[1], "2:assistant:Giornata") ||
		!strings.HasSuffix(sess.turns[1], ":claude-haiku-test") {
		t.Errorf("turns = %v, want narrative content and model stamped", sess.turns)
	}
}

func TestMorning_NarratorFailureDegradesNeverSilent(t *testing.T) {
	out := &stubMessenger{}
	runs := newStubRuns()
	m := newMorning(stubWellness{days: []icu.Wellness{green("2026-06-10")}}, out, runs)
	m.Narrator = stubNarrator{err: errors.New("anthropic 529")}

	if err := m.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v (LLM failure must never fail the morning)", err)
	}
	if len(out.bodies) != 1 {
		t.Fatalf("sends = %d, want 1 (degraded, not silent)", len(out.bodies))
	}
	if !strings.Contains(out.bodies[0], "Coach offline") {
		t.Errorf("degraded notice missing:\n%s", out.bodies[0])
	}
	if !strings.Contains(out.bodies[0], "Check mattutino") {
		t.Errorf("raw numbers missing in degraded message:\n%s", out.bodies[0])
	}
	if runs.completed["2026-06-10"] != "GO-degraded" {
		t.Errorf("status = %q, want GO-degraded", runs.completed["2026-06-10"])
	}
}

func TestMorning_NilNarratorKeepsSkeleton(t *testing.T) {
	out := &stubMessenger{}
	runs := newStubRuns()
	m := newMorning(stubWellness{days: []icu.Wellness{green("2026-06-10")}}, out, runs)

	if err := m.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.HasPrefix(out.bodies[0], "☀️") {
		t.Errorf("skeleton message changed:\n%s", out.bodies[0])
	}
}

func TestMorning_SessionFailureNeverBlocksSend(t *testing.T) {
	out := &stubMessenger{}
	runs := newStubRuns()
	m := newMorning(stubWellness{days: []icu.Wellness{green("2026-06-10")}}, out, runs)
	m.Narrator = stubNarrator{out: "ok"}
	m.Sessions = &stubSessions{err: errors.New("firestore down")}

	if err := m.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v (session persistence is best-effort)", err)
	}
	if len(out.bodies) != 1 {
		t.Fatal("message lost to a session-store failure")
	}
	if runs.completed["2026-06-10"] != "GO" {
		t.Errorf("status = %q, want GO (not degraded: narrative succeeded)", runs.completed["2026-06-10"])
	}
}

type flakyTurnSessions struct {
	stubSessions
	failTurn int
}

func (f *flakyTurnSessions) AppendTurn(ctx context.Context, id string, seq int, role, content, model string) error {
	if seq == f.failTurn {
		return errors.New("firestore hiccup")
	}
	return f.stubSessions.AppendTurn(ctx, id, seq, role, content, model)
}

func TestMorning_TurnFailureAfterCreateNeverBlocksSend(t *testing.T) {
	out := &stubMessenger{}
	runs := newStubRuns()
	sess := &flakyTurnSessions{failTurn: 1}
	m := newMorning(stubWellness{days: []icu.Wellness{green("2026-06-10")}}, out, runs)
	m.Narrator = stubNarrator{out: "ok narrativa"}
	m.Sessions = sess

	if err := m.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(out.bodies) != 1 {
		t.Fatal("send lost to a turn failure")
	}
	if runs.completed["2026-06-10"] != "GO" {
		t.Errorf("status = %q, want GO", runs.completed["2026-06-10"])
	}
	if len(sess.turns) != 0 {
		t.Errorf("turn 2 written after turn 1 failed: %v (partial history)", sess.turns)
	}
}

func TestMorning_NarrativeSanitized(t *testing.T) {
	out := &stubMessenger{}
	runs := newStubRuns()
	m := newMorning(stubWellness{days: []icu.Wellness{green("2026-06-10")}}, out, runs)
	m.Narrator = stubNarrator{out: `Vai <b>forte</b> <a href="http://x">qui</a>`}

	if err := m.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(out.bodies[0], "<a") {
		t.Errorf("model markup not sanitized:\n%s", out.bodies[0])
	}
	if !strings.Contains(out.bodies[0], "<b>forte</b>") {
		t.Errorf("allowed markup lost:\n%s", out.bodies[0])
	}
}

type stubOpenInjuries struct {
	open []store.Injury
	err  error
}

func (s stubOpenInjuries) ListOpen(context.Context) ([]store.Injury, error) {
	return s.open, s.err
}

func TestMorning_OpenInjurySkipsViaVerdict(t *testing.T) {
	out := &stubMessenger{}
	runs := newStubRuns()
	m := newMorning(stubWellness{days: []icu.Wellness{green("2026-06-10")}}, out, runs)
	m.Injuries = stubOpenInjuries{open: []store.Injury{{BodyPart: "polpaccio", Pain: 6, Status: "open"}}}

	if err := m.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if runs.completed["2026-06-10"] != "SKIP" {
		t.Fatalf("status = %q, want SKIP (open injury pain 6)", runs.completed["2026-06-10"])
	}
	found := false
	for _, r := range out.verdicts[0].Reasons {
		if r.RuleID == "injury_active" && strings.Contains(r.Message, "polpaccio") {
			found = true
		}
	}
	if !found {
		t.Errorf("injury_active not in verdict reasons: %+v", out.verdicts[0].Reasons)
	}
}

func TestMorning_InjuryRegistryDownIsAGap(t *testing.T) {
	out := &stubMessenger{}
	runs := newStubRuns()
	m := newMorning(stubWellness{days: []icu.Wellness{green("2026-06-10")}}, out, runs)
	m.Injuries = stubOpenInjuries{err: errors.New("firestore down")}

	if err := m.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v (registry blip must not kill the morning)", err)
	}
	found := false
	for _, g := range out.verdicts[0].DataGaps {
		if strings.Contains(g, "registro infortuni") {
			found = true
		}
	}
	if !found {
		t.Errorf("injury gap not in verdict: %+v", out.verdicts[0].DataGaps)
	}
}

type stubEvents struct {
	events []icu.Event
	err    error
}

func (s stubEvents) EventsRange(context.Context, string, string) ([]icu.Event, error) {
	return s.events, s.err
}

func strPtr(s string) *string { return &s }

func TestMorning_PlannedTodayLineAppears(t *testing.T) {
	out := &stubMessenger{}
	runs := newStubRuns()
	m := newMorning(stubWellness{days: []icu.Wellness{green("2026-06-10")}}, out, runs)
	m.Events = stubEvents{events: []icu.Event{
		{Category: "WORKOUT", StartDateLocal: "2026-06-10T00:00:00", Name: strPtr("Fondo Z2 50min")},
		{Category: "RACE_A", StartDateLocal: "2026-06-10T00:00:00", Name: strPtr("non-workout ignorato")},
	}}

	if err := m.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.bodies[0], "In programma oggi:</b> Fondo Z2 50min") {
		t.Errorf("planned line missing:\n%s", out.bodies[0])
	}
	if strings.Contains(out.bodies[0], "ignorato") {
		t.Error("non-workout event leaked into the line")
	}
}

func TestMorning_EventsDownSkipsLineQuietly(t *testing.T) {
	out := &stubMessenger{}
	runs := newStubRuns()
	m := newMorning(stubWellness{days: []icu.Wellness{green("2026-06-10")}}, out, runs)
	m.Events = stubEvents{err: errors.New("icu 502")}

	if err := m.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v (events are enrichment, never a blocker)", err)
	}
	if strings.Contains(out.bodies[0], "In programma") {
		t.Error("line rendered despite events failure")
	}
}

func TestMorning_CheckinKeyboardAfterMessage(t *testing.T) {
	out := &stubMessenger{}
	runs := newStubRuns()
	m := newMorning(stubWellness{days: []icu.Wellness{green("2026-06-10")}}, out, runs)
	kb := &stubKeyboardMsg{}
	m.Keyboard = kb

	if err := m.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(kb.texts) != 1 || !strings.Contains(kb.texts[0], "Come ti senti") {
		t.Fatalf("checkin question missing: %v", kb.texts)
	}
	if !strings.HasPrefix(kb.sent[0][0], "ci:2026-06-10:feel:") {
		t.Errorf("buttons = %v", kb.sent)
	}
}

type stubCheckinSource struct{ ci store.Checkin }

func (s stubCheckinSource) Get(context.Context, string) (store.Checkin, error) {
	return s.ci, nil
}

func TestMorning_CheckinFillsVerdictGapsOnly(t *testing.T) {
	out := &stubMessenger{}
	runs := newStubRuns()
	// Green day WITHOUT icu subjective data: the tap fills the gap and the
	// D32 fatigue rule fires (conservative-only).
	m := newMorning(stubWellness{days: []icu.Wellness{green("2026-06-10")}}, out, runs)
	m.Checkins = stubCheckinSource{ci: store.Checkin{Feeling: "stanco", TimeBudget: "short"}}

	if err := m.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if runs.completed["2026-06-10"] != "MODIFY" {
		t.Fatalf("status = %q, want MODIFY (athlete says stanco)", runs.completed["2026-06-10"])
	}
	if !strings.Contains(out.bodies[0], "Check-in:</b> stanco · tempo ridotto") {
		t.Errorf("checkin line missing:\n%s", out.bodies[0])
	}

	// Device data present: the tap must NOT override it.
	day := green("2026-06-10")
	one := 1
	day.Fatigue = &one // device says fresh
	m2 := newMorning(stubWellness{days: []icu.Wellness{day}}, &stubMessenger{}, newStubRuns())
	m2.Checkins = stubCheckinSource{ci: store.Checkin{Feeling: "stanco"}}
	_, in, err := m2.prepare(context.Background(), "2026-06-10")
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if *in.Today.Fatigue != 1 {
		t.Fatalf("tap overrode device data: fatigue=%d", *in.Today.Fatigue)
	}
}

func TestMorning_RoutineAppendedWhenCatalogWired(t *testing.T) {
	out := &stubMessenger{}
	runs := newStubRuns()
	m := newMorning(stubWellness{days: []icu.Wellness{green("2026-06-10")}}, out, runs)
	m.Exercises = exercises.MustLoad() // full kit (Equipment nil)

	if err := m.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(out.bodies) != 1 {
		t.Fatalf("sends = %d, want 1", len(out.bodies))
	}
	body := out.bodies[0]
	if !strings.Contains(body, "Prevenzione") {
		t.Errorf("routine block missing from morning message:\n%s", body)
	}
	for _, g := range exercises.RoutineGroups {
		if !strings.Contains(body, g.Label) {
			t.Errorf("routine missing group %q:\n%s", g.Label, body)
		}
	}
	// Deterministic: the same day must render the same routine bytes.
	out2 := &stubMessenger{}
	m2 := newMorning(stubWellness{days: []icu.Wellness{green("2026-06-10")}}, out2, newStubRuns())
	m2.Exercises = exercises.MustLoad()
	if err := m2.Run(context.Background()); err != nil {
		t.Fatalf("Run(2): %v", err)
	}
	if out2.bodies[0] != body {
		t.Errorf("routine not deterministic across runs:\n--- run1 ---\n%s\n--- run2 ---\n%s", body, out2.bodies[0])
	}
}

func TestMorning_NoRoutineWhenCatalogAbsent(t *testing.T) {
	out := &stubMessenger{}
	m := newMorning(stubWellness{days: []icu.Wellness{green("2026-06-10")}}, out, newStubRuns())
	// Exercises left nil: the block must not appear.
	if err := m.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(out.bodies[0], "Prevenzione") {
		t.Errorf("routine block present without a catalog:\n%s", out.bodies[0])
	}
}
