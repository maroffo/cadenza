// ABOUTME: Magic-link auth: the bot mints a signed single-use link, possession of
// ABOUTME: the athlete's Telegram IS the identity; redemption sets a 30-day session cookie.

package web

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	linkTTL    = 10 * time.Minute
	sessionTTL = 30 * 24 * time.Hour
	cookieName = "cadenza_session"
	cookiePath = "/app"
)

// SessionStore persists redeemed sessions and single-use nonces; satisfied
// by store.WebSessions.
type SessionStore interface {
	// RedeemNonce marks a login nonce used; false when already used/unknown.
	RedeemNonce(ctx context.Context, nonce string, ttl time.Duration) (bool, error)
	// SaveSession / CheckSession / DeleteSession manage the cookie session.
	SaveSession(ctx context.Context, id string, ttl time.Duration) error
	CheckSession(ctx context.Context, id string) (bool, error)
	DeleteSession(ctx context.Context, id string) error
}

type Auth struct {
	Secret   []byte
	Sessions SessionStore
	BaseURL  string // canonical service URL
	Now      func() time.Time
}

func randomToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// sign is domain-separated by purpose: a captured link token must never
// double as a cookie signature (and vice versa).
func (a Auth) sign(purpose, payload string) string {
	mac := hmac.New(sha256.New, a.Secret)
	mac.Write([]byte(purpose + "|" + payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// MintLink creates a signed, expiring, single-use login URL for the bot to
// send privately to the athlete.
func (a Auth) MintLink() string {
	nonce := randomToken()
	exp := a.Now().Add(linkTTL).Unix()
	payload := fmt.Sprintf("%s|%d", nonce, exp)
	return fmt.Sprintf("%s/app/login?t=%s.%d.%s",
		a.BaseURL, nonce, exp, a.sign("link", payload))
}

// validateToken checks signature and expiry WITHOUT burning the nonce.
func (a Auth) validateToken(token string) (nonce string, ok bool, msg string) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", false, "link non valido"
	}
	nonce, expRaw, sig := parts[0], parts[1], parts[2]
	exp, err := strconv.ParseInt(expRaw, 10, 64)
	if err != nil || a.Now().Unix() > exp {
		return "", false, "link scaduto: richiedine uno nuovo con /web"
	}
	expected := a.sign("link", fmt.Sprintf("%s|%d", nonce, exp))
	if !hmac.Equal([]byte(sig), []byte(expected)) {
		return "", false, "link non valido"
	}
	return nonce, true, ""
}

// HandleLoginPage answers the GET: it must be SIDE-EFFECT FREE, because
// chat apps prefetch links for previews (Telegram's crawler burned the
// nonce before the athlete could click: live bug). It validates and shows
// a one-tap POST form; only the POST redeems.
func (a Auth) HandleLoginPage(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("t")
	if _, ok, msg := a.validateToken(token); !ok {
		http.Error(w, msg, http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>cadenza</title>
<body style="font-family:system-ui;max-width:32rem;margin:4rem auto;text-align:center">
<h2>🚴 cadenza</h2><p>Accesso alla dashboard.</p>
<form method="post" action="/app/login">
<input type="hidden" name="t" value="%s">
<button style="font-size:1.1rem;padding:.7rem 2rem;border-radius:.7rem;cursor:pointer">Entra</button>
</form></body>`, template.HTMLEscapeString(token))
}

// HandleLogin (POST) redeems the token: single-use nonce, then session.
func (a Auth) HandleLogin(w http.ResponseWriter, r *http.Request) {
	nonce, ok, msg := a.validateToken(r.FormValue("t"))
	if !ok {
		http.Error(w, msg, http.StatusForbidden)
		return
	}
	fresh, err := a.Sessions.RedeemNonce(r.Context(), nonce, linkTTL)
	if err != nil {
		http.Error(w, "errore interno", http.StatusInternalServerError)
		return
	}
	if !fresh {
		http.Error(w, "link già usato: richiedine uno nuovo con /web", http.StatusForbidden)
		return
	}
	sessionID := randomToken()
	if err := a.Sessions.SaveSession(r.Context(), sessionID, sessionTTL); err != nil {
		http.Error(w, "errore interno", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: sessionID + "." + a.sign("cookie", sessionID),
		Path: cookiePath, Expires: a.Now().Add(sessionTTL),
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/app", http.StatusSeeOther)
}

// HandleLogout revokes the session server-side and clears the cookie: a
// stolen cookie must be killable without touching Firestore by hand.
func (a Auth) HandleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		parts := strings.SplitN(c.Value, ".", 2)
		if len(parts) == 2 && hmac.Equal([]byte(parts[1]), []byte(a.sign("cookie", parts[0]))) {
			if err := a.Sessions.DeleteSession(r.Context(), parts[0]); err != nil {
				http.Error(w, "errore interno", http.StatusInternalServerError)
				return
			}
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: "", Path: cookiePath, MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/app", http.StatusSeeOther)
}

// Require wraps handlers with the cookie check (signature + store lookup).
func (a Auth) Require(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Health data: never let browsers or intermediaries cache it.
		w.Header().Set("Cache-Control", "no-store")
		// CSRF belt on top of SameSite=Lax: cross-site requests announce
		// themselves via Sec-Fetch-Site in every modern browser.
		if sfs := r.Header.Get("Sec-Fetch-Site"); sfs != "" && sfs != "same-origin" && sfs != "none" {
			http.Error(w, "richiesta cross-site rifiutata", http.StatusForbidden)
			return
		}
		c, err := r.Cookie(cookieName)
		if err == nil {
			parts := strings.SplitN(c.Value, ".", 2)
			if len(parts) == 2 && hmac.Equal([]byte(parts[1]), []byte(a.sign("cookie", parts[0]))) {
				ok, err := a.Sessions.CheckSession(r.Context(), parts[0])
				if err == nil && ok {
					next(w, r)
					return
				}
			}
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8"><title>cadenza</title>
<body style="font-family:system-ui;max-width:32rem;margin:4rem auto;text-align:center">
<h2>🚴 cadenza</h2><p>Sessione assente o scaduta.</p>
<p>Scrivi <b>/web</b> al bot Telegram per ricevere un link di accesso.</p></body>`))
	}
}
