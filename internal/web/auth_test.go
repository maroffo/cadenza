// ABOUTME: Auth tests: mint/redeem round trip, expiry, forgery, single-use, cookie gate.
// ABOUTME: Possession of the link IS the identity: every rejection path matters.

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
)

type memSessions struct {
	nonces   map[string]bool
	sessions map[string]bool
	saveErr  error
}

func newMemSessions() *memSessions {
	return &memSessions{nonces: map[string]bool{}, sessions: map[string]bool{}}
}

func (m *memSessions) RedeemNonce(_ context.Context, nonce string, _ time.Duration) (bool, error) {
	if m.nonces[nonce] {
		return false, nil
	}
	m.nonces[nonce] = true
	return true, nil
}

func (m *memSessions) SaveSession(_ context.Context, id string, _ time.Duration) error {
	if m.saveErr != nil {
		return m.saveErr
	}
	m.sessions[id] = true
	return nil
}

func (m *memSessions) DeleteSession(_ context.Context, id string) error {
	delete(m.sessions, id)
	return nil
}

func (m *memSessions) CheckSession(_ context.Context, id string) (bool, error) {
	return m.sessions[id], nil
}

func testAuth() (Auth, *memSessions) {
	sess := newMemSessions()
	now := time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC)
	return Auth{
		Secret: []byte("test-secret"), Sessions: sess,
		BaseURL: "https://cadenza.example", Now: func() time.Time { return now },
	}, sess
}

func loginPath(t *testing.T, link string) string {
	t.Helper()
	u, err := url.Parse(link)
	if err != nil {
		t.Fatalf("parse link: %v", err)
	}
	return u.Path + "?" + u.RawQuery
}

func tokenOf(t *testing.T, link string) string {
	t.Helper()
	u, _ := url.Parse(link)
	return u.Query().Get("t")
}

func postLogin(a Auth, token string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/app/login",
		strings.NewReader(url.Values{"t": {token}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	a.HandleLogin(rec, req)
	return rec
}

func TestAuth_MintRedeemRoundTrip(t *testing.T) {
	a, sess := testAuth()
	link := a.MintLink()
	if !strings.HasPrefix(link, "https://cadenza.example/app/login?t=") {
		t.Fatalf("link = %q", link)
	}

	rec := postLogin(a, tokenOf(t, link))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("redeem code = %d body=%s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != cookieName || !cookies[0].HttpOnly || !cookies[0].Secure {
		t.Fatalf("cookie = %+v", cookies)
	}
	if len(sess.sessions) != 1 {
		t.Fatalf("sessions = %v", sess.sessions)
	}

	// The cookie passes the gate.
	gated := a.Require(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	req := httptest.NewRequest(http.MethodGet, "/app", nil)
	req.AddCookie(cookies[0])
	rec2 := httptest.NewRecorder()
	gated(rec2, req)
	if rec2.Code != 200 {
		t.Fatalf("gate = %d, want 200", rec2.Code)
	}
}

func TestAuth_LinkIsSingleUse(t *testing.T) {
	a, _ := testAuth()
	token := tokenOf(t, a.MintLink())

	if first := postLogin(a, token); first.Code != http.StatusSeeOther {
		t.Fatalf("first = %d", first.Code)
	}
	second := postLogin(a, token)
	if second.Code != http.StatusForbidden || !strings.Contains(second.Body.String(), "già usato") {
		t.Fatalf("replayed link = %d %q", second.Code, second.Body.String())
	}
}

func TestAuth_CrawlerPrefetchDoesNotBurnTheLink(t *testing.T) {
	// THE live bug: Telegram's preview crawler GETs the link before the
	// athlete taps it. GET must be side-effect free: validate, render the
	// POST form, burn NOTHING.
	a, sess := testAuth()
	link := a.MintLink()
	path := loginPath(t, link)

	for range 3 { // crawler may fetch repeatedly
		rec := httptest.NewRecorder()
		a.HandleLoginPage(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `method="post"`) {
			t.Fatalf("GET = %d, want the harmless confirm form", rec.Code)
		}
	}
	if len(sess.nonces) != 0 {
		t.Fatal("GET burned the nonce (crawler kills the link again)")
	}
	// The athlete's tap still works after all those prefetches.
	if rec := postLogin(a, tokenOf(t, link)); rec.Code != http.StatusSeeOther {
		t.Fatalf("athlete POST after prefetch = %d", rec.Code)
	}
}

func TestAuth_ExpiredAndForgedRejected(t *testing.T) {
	a, _ := testAuth()
	link := a.MintLink()

	// Expired: shift the clock past the TTL (both GET page and POST).
	a.Now = func() time.Time { return time.Date(2026, 6, 12, 10, 11, 0, 0, time.UTC) }
	recPage := httptest.NewRecorder()
	a.HandleLoginPage(recPage, httptest.NewRequest(http.MethodGet, loginPath(t, link), nil))
	if recPage.Code != http.StatusForbidden {
		t.Fatalf("expired GET = %d", recPage.Code)
	}
	if rec := postLogin(a, tokenOf(t, link)); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "scaduto") {
		t.Fatalf("expired POST = %d %q", rec.Code, rec.Body.String())
	}

	// Forged signature.
	a2, _ := testAuth()
	if rec := postLogin(a2, "x"+tokenOf(t, a2.MintLink())); rec.Code != http.StatusForbidden {
		t.Fatalf("forged = %d", rec.Code)
	}
}

func TestAuth_GateRejectsForgedAndMissingCookies(t *testing.T) {
	a, sess := testAuth()
	gated := a.Require(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })

	// No cookie: friendly unauthorized page mentioning /web.
	rec := httptest.NewRecorder()
	gated(rec, httptest.NewRequest(http.MethodGet, "/app", nil))
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "/web") {
		t.Fatalf("no cookie = %d", rec.Code)
	}

	// Forged cookie value: valid format, wrong signature.
	sess.sessions["sid"] = true
	req := httptest.NewRequest(http.MethodGet, "/app", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: "sid.fakesig"})
	rec2 := httptest.NewRecorder()
	gated(rec2, req)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("forged cookie = %d", rec2.Code)
	}

	// Signed cookie for a session the store no longer has.
	req3 := httptest.NewRequest(http.MethodGet, "/app", nil)
	req3.AddCookie(&http.Cookie{Name: cookieName, Value: "ghost." + a.sign("cookie", "ghost")})
	rec3 := httptest.NewRecorder()
	gated(rec3, req3)
	if rec3.Code != http.StatusUnauthorized {
		t.Fatalf("ghost session = %d", rec3.Code)
	}
}

func TestAuth_ForgeryByExpiryTampering(t *testing.T) {
	// THE attack HMAC exists for: stretch the expiry, keep the signature.
	a, _ := testAuth()
	token := tokenOf(t, a.MintLink())
	parts := strings.SplitN(token, ".", 3)
	tampered := parts[0] + ".9999999999." + parts[2]
	rec := postLogin(a, tampered)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "non valido") {
		t.Fatalf("expiry-tampered token = %d %q", rec.Code, rec.Body.String())
	}
	// Malformed shapes die too.
	for _, bad := range []string{"", "a.b", "a.b.c.d"} {
		if rec := postLogin(a, bad); rec.Code != http.StatusForbidden {
			t.Errorf("malformed %q = %d", bad, rec.Code)
		}
	}
}

func TestAuth_SaveFailureSetsNoCookie(t *testing.T) {
	a, sess := testAuth()
	sess.saveErr = errors.New("firestore down")
	rec := postLogin(a, tokenOf(t, a.MintLink()))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("save failure = %d, want 500", rec.Code)
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Fatal("cookie issued for an unsaved session")
	}
}

func TestAuth_CookieAttributesPinned(t *testing.T) {
	a, _ := testAuth()
	rec := postLogin(a, tokenOf(t, a.MintLink()))
	c := rec.Result().Cookies()[0]
	if c.Path != "/app" || c.SameSite != http.SameSiteLaxMode || !c.HttpOnly || !c.Secure {
		t.Fatalf("cookie attrs = %+v", c)
	}
}

func TestAuth_LogoutRevokesServerSide(t *testing.T) {
	a, sess := testAuth()
	rec := postLogin(a, tokenOf(t, a.MintLink()))
	cookie := rec.Result().Cookies()[0]

	out := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/app/logout", nil)
	req.AddCookie(cookie)
	a.HandleLogout(out, req)
	if out.Code != http.StatusSeeOther {
		t.Fatalf("logout = %d", out.Code)
	}
	if len(sess.sessions) != 0 {
		t.Fatal("session not revoked server-side (stolen cookie would survive)")
	}
	// The old cookie is now dead at the gate.
	gated := a.Require(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	req2 := httptest.NewRequest(http.MethodGet, "/app", nil)
	req2.AddCookie(cookie)
	rec2 := httptest.NewRecorder()
	gated(rec2, req2)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("revoked cookie still passes = %d", rec2.Code)
	}
}
