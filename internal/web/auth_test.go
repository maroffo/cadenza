// ABOUTME: Auth tests: mint/redeem round trip, expiry, forgery, single-use, cookie gate.
// ABOUTME: Possession of the link IS the identity: every rejection path matters.

package web

import (
	"context"
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
	m.sessions[id] = true
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

func TestAuth_MintRedeemRoundTrip(t *testing.T) {
	a, sess := testAuth()
	link := a.MintLink()
	if !strings.HasPrefix(link, "https://cadenza.example/app/login?t=") {
		t.Fatalf("link = %q", link)
	}

	rec := httptest.NewRecorder()
	a.HandleLogin(rec, httptest.NewRequest(http.MethodGet, loginPath(t, link), nil))
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
	link := a.MintLink()
	path := loginPath(t, link)

	first := httptest.NewRecorder()
	a.HandleLogin(first, httptest.NewRequest(http.MethodGet, path, nil))
	second := httptest.NewRecorder()
	a.HandleLogin(second, httptest.NewRequest(http.MethodGet, path, nil))
	if second.Code != http.StatusForbidden || !strings.Contains(second.Body.String(), "già usato") {
		t.Fatalf("replayed link = %d %q", second.Code, second.Body.String())
	}
}

func TestAuth_ExpiredAndForgedRejected(t *testing.T) {
	a, _ := testAuth()
	link := a.MintLink()

	// Expired: shift the clock past the TTL.
	a.Now = func() time.Time { return time.Date(2026, 6, 12, 10, 11, 0, 0, time.UTC) }
	rec := httptest.NewRecorder()
	a.HandleLogin(rec, httptest.NewRequest(http.MethodGet, loginPath(t, link), nil))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "scaduto") {
		t.Fatalf("expired = %d %q", rec.Code, rec.Body.String())
	}

	// Forged signature.
	a2, _ := testAuth()
	forged := strings.Replace(a2.MintLink(), "t=", "t=x", 1)
	rec2 := httptest.NewRecorder()
	a2.HandleLogin(rec2, httptest.NewRequest(http.MethodGet, loginPath(t, forged), nil))
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("forged = %d", rec2.Code)
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
	req3.AddCookie(&http.Cookie{Name: cookieName, Value: "ghost." + a.sign("ghost")})
	rec3 := httptest.NewRecorder()
	gated(rec3, req3)
	if rec3.Code != http.StatusUnauthorized {
		t.Fatalf("ghost session = %d", rec3.Code)
	}
}
