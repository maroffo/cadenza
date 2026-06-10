// ABOUTME: Tests for the morning job: idempotency, stale data, error propagation, verdict wiring.
// ABOUTME: All dependencies stubbed; no Firestore, no network.

package job

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/maroffo/cadenza/internal/icu"
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

type stubProfile struct{}

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
	checkErr  error
	markErr   error
}

func newStubRuns() *stubRuns { return &stubRuns{completed: map[string]string{}} }

func (s *stubRuns) MorningCompleted(_ context.Context, date string) (bool, error) {
	if s.checkErr != nil {
		return false, s.checkErr
	}
	_, ok := s.completed[date]
	return ok, nil
}

func (s *stubRuns) MarkMorningCompleted(_ context.Context, date, status string) error {
	if s.markErr != nil {
		return s.markErr
	}
	s.completed[date] = status
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
