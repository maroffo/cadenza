// ABOUTME: Dashboard handler tests: rendering, clamped actions, chat pipeline, route gating.
// ABOUTME: Every action endpoint enforces the same bounds as the bot paths.

package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/maroffo/cadenza/internal/store"
	"github.com/maroffo/cadenza/internal/verdict"
)

type stubStatus struct{ err error }

func (s stubStatus) Compose(context.Context) (string, verdict.Verdict, error) {
	if s.err != nil {
		return "", verdict.Verdict{}, s.err
	}
	return "☀️ Check di prova", verdict.Verdict{Kind: verdict.Go, Version: "v1"}, nil
}

type stubChat struct{ lastText string }

func (s *stubChat) ConverseReply(_ context.Context, text string) (string, verdict.Verdict, error) {
	s.lastText = text
	return "Risposta del coach", verdict.Verdict{Kind: verdict.Go, Version: "v1"}, nil
}

type stubInjAdmin struct{ resolved []string }

func (s *stubInjAdmin) ListOpen(context.Context) ([]store.Injury, error) {
	return []store.Injury{{ID: "inj-1", BodyPart: "polpaccio", Pain: 5, OpenedAt: time.Now()}}, nil
}
func (s *stubInjAdmin) Resolve(_ context.Context, id string) error {
	s.resolved = append(s.resolved, id)
	return nil
}

type stubRuleAdmin struct{ deactivated []string }

func (s *stubRuleAdmin) ListActive(context.Context) ([]store.Rule, error) {
	return []store.Rule{{ID: "rule-1", Text: "Niente qualità dopo un volo"}}, nil
}
func (s *stubRuleAdmin) Deactivate(_ context.Context, id string) error {
	s.deactivated = append(s.deactivated, id)
	return nil
}

type stubProfileAdmin struct{ caps []float64 }

func (s *stubProfileAdmin) Profile(context.Context) (verdict.Baselines, float64, error) {
	return verdict.Baselines{HRVMean: 35, HRVSD: 8, RestingHR: 54}, 4.0, nil
}
func (s *stubProfileAdmin) SetRampCap(_ context.Context, c float64) error {
	s.caps = append(s.caps, c)
	return nil
}

type stubAudit struct{}

func (stubAudit) RecentWrites(context.Context, int) ([]store.WriteRecord, error) {
	return []store.WriteRecord{{Date: "2026-06-12", Title: "Easy", Status: "verified", Attempts: 1}}, nil
}
func (stubAudit) RecentMutations(context.Context, int) ([]store.MutationWithID, error) {
	return nil, nil
}
func (stubAudit) SpentToday(context.Context, string) (int, error) { return 3, nil }

type stubHistory struct{}

func (stubHistory) ActiveTurns(context.Context, int) ([]store.Turn, error) {
	return []store.Turn{
		{Role: "user", Content: "come sto?"},
		{Role: "assistant", Content: "bene, GO"},
	}, nil
}

func testServer() (*Server, *stubChat, *stubInjAdmin, *stubRuleAdmin, *stubProfileAdmin, *memSessions) {
	auth, sess := testAuth()
	chat := &stubChat{}
	inj := &stubInjAdmin{}
	rules := &stubRuleAdmin{}
	prof := &stubProfileAdmin{}
	return &Server{
		Auth: auth, Status: stubStatus{}, Chat: chat, History: stubHistory{},
		Injuries: inj, Rules: rules, Profiles: prof, Audit: stubAudit{},
		Now: func() time.Time { return time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC) },
		TZ:  time.UTC,
	}, chat, inj, rules, prof, sess
}

func authedRequest(t *testing.T, s *Server, sess *memSessions, method, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	sess.sessions["sid"] = true
	var req *http.Request
	if form != nil {
		req = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.AddCookie(&http.Cookie{Name: cookieName, Value: "sid." + s.Auth.sign("sid")})
	mux := http.NewServeMux()
	s.Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestWeb_AllPagesGatedAndRender(t *testing.T) {
	s, _, _, _, _, sess := testServer()
	pages := map[string]string{
		"/app":          "Check di prova",
		"/app/injuries": "polpaccio",
		"/app/audit":    "Tetto rampa",
		"/app/calendar": "verificato",
		"/app/chat":     "come sto?",
	}
	for path, want := range pages {
		// Without cookie: unauthorized.
		mux := http.NewServeMux()
		s.Register(mux)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without cookie = %d, want 401", path, rec.Code)
		}
		// With session: renders the content.
		rec2 := authedRequest(t, s, sess, http.MethodGet, path, nil)
		if rec2.Code != 200 || !strings.Contains(rec2.Body.String(), want) {
			t.Errorf("%s = %d, body missing %q", path, rec2.Code, want)
		}
	}
}

func TestWeb_RampCapClampedLikeEverywhereElse(t *testing.T) {
	s, _, _, _, prof, sess := testServer()
	rec := authedRequest(t, s, sess, http.MethodPost, "/app/profile/rampcap",
		url.Values{"ramp_cap": {"7.5"}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("ramp_cap 7.5 = %d, want 400 (Tier A holds on the web too)", rec.Code)
	}
	if len(prof.caps) != 0 {
		t.Fatal("over-ceiling cap reached the store")
	}
	rec2 := authedRequest(t, s, sess, http.MethodPost, "/app/profile/rampcap",
		url.Values{"ramp_cap": {"3.5"}})
	if rec2.Code != http.StatusSeeOther || len(prof.caps) != 1 || prof.caps[0] != 3.5 {
		t.Fatalf("valid cap = %d caps=%v", rec2.Code, prof.caps)
	}
}

func TestWeb_InjuryResolveAndRuleDeactivate(t *testing.T) {
	s, _, inj, rules, _, sess := testServer()
	rec := authedRequest(t, s, sess, http.MethodPost, "/app/injuries/resolve",
		url.Values{"id": {"inj-1"}})
	if rec.Code != http.StatusSeeOther || len(inj.resolved) != 1 {
		t.Fatalf("resolve = %d %v", rec.Code, inj.resolved)
	}
	rec2 := authedRequest(t, s, sess, http.MethodPost, "/app/rules/deactivate",
		url.Values{"id": {"rule-1"}})
	if rec2.Code != http.StatusSeeOther || len(rules.deactivated) != 1 {
		t.Fatalf("deactivate = %d %v", rec2.Code, rules.deactivated)
	}
}

func TestWeb_ChatSendThroughCoachPipeline(t *testing.T) {
	s, chat, _, _, _, sess := testServer()
	rec := authedRequest(t, s, sess, http.MethodPost, "/app/chat",
		url.Values{"text": {"posso spingere oggi?"}})
	if rec.Code != 200 {
		t.Fatalf("chat = %d", rec.Code)
	}
	if chat.lastText != "posso spingere oggi?" {
		t.Fatalf("coach got %q", chat.lastText)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Risposta del coach") || !strings.Contains(body, "VERDETTO") {
		t.Errorf("reply fragment missing pieces:\n%s", body)
	}
}

func TestWeb_OverviewDegradesHonestly(t *testing.T) {
	s, _, _, _, _, sess := testServer()
	s.Status = stubStatus{err: errors.New("icu down")}
	rec := authedRequest(t, s, sess, http.MethodGet, "/app", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "non disponibile") {
		t.Fatalf("degraded overview = %d", rec.Code)
	}
}

func TestCoachHTML_AllowlistOnly(t *testing.T) {
	in := "<b>VERDETTO: GO</b> e <i>margini</i> & <script>alert(1)</script><a href=x>link</a>"
	out := string(coachHTML(in))
	for _, want := range []string{"<b>VERDETTO: GO</b>", "<i>margini</i>", "&amp;",
		"&lt;script&gt;", "&lt;a href=x&gt;"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "<script>") || strings.Contains(out, "<a ") {
		t.Fatalf("live tag leaked:\n%s", out)
	}
}

func TestWeb_OverviewRendersBoldNotLiteralTags(t *testing.T) {
	// The live formatting bug: <b> showed as literal text on the dashboard.
	s, _, _, _, _, sess := testServer()
	s.Status = stubStatus{} // body contains no tags; the verdict block does
	rec := authedRequest(t, s, sess, http.MethodGet, "/app", nil)
	body := rec.Body.String()
	if strings.Contains(body, "&lt;b&gt;") {
		t.Fatalf("escaped telegram tags leaked to the page:\n%s", body)
	}
	if !strings.Contains(body, "<b>VERDETTO: GO</b>") {
		t.Errorf("verdict bold not rendered:\n%s", body)
	}
}

func TestWeb_ChatUserTextStaysFullyEscaped(t *testing.T) {
	// Athlete-typed text gets NO tag re-enabling: full escape.
	s, _, _, _, _, sess := testServer()
	rec := authedRequest(t, s, sess, http.MethodPost, "/app/chat",
		url.Values{"text": {"<b>furbo</b><script>x</script>"}})
	body := rec.Body.String()
	if !strings.Contains(body, "&lt;b&gt;furbo&lt;/b&gt;") {
		t.Errorf("user text not fully escaped:\n%s", body)
	}
	if strings.Contains(body, "<script>") {
		t.Fatal("script tag live in user bubble")
	}
}
