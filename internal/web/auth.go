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
	// SaveSession / CheckSession manage the long-lived cookie session.
	SaveSession(ctx context.Context, id string, ttl time.Duration) error
	CheckSession(ctx context.Context, id string) (bool, error)
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

func (a Auth) sign(payload string) string {
	mac := hmac.New(sha256.New, a.Secret)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// MintLink creates a signed, expiring, single-use login URL for the bot to
// send privately to the athlete.
func (a Auth) MintLink() string {
	nonce := randomToken()
	exp := a.Now().Add(linkTTL).Unix()
	payload := fmt.Sprintf("%s|%d", nonce, exp)
	return fmt.Sprintf("%s/app/login?t=%s.%d.%s",
		a.BaseURL, nonce, exp, a.sign(payload))
}

// HandleLogin redeems the token: signature, expiry, then single-use nonce.
// On success it sets the session cookie and lands on the dashboard.
func (a Auth) HandleLogin(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(r.URL.Query().Get("t"), ".")
	if len(parts) != 3 {
		http.Error(w, "link non valido", http.StatusForbidden)
		return
	}
	nonce, expRaw, sig := parts[0], parts[1], parts[2]
	exp, err := strconv.ParseInt(expRaw, 10, 64)
	if err != nil || a.Now().Unix() > exp {
		http.Error(w, "link scaduto: richiedine uno nuovo con /web", http.StatusForbidden)
		return
	}
	expected := a.sign(fmt.Sprintf("%s|%d", nonce, exp))
	if !hmac.Equal([]byte(sig), []byte(expected)) {
		http.Error(w, "link non valido", http.StatusForbidden)
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
		Name: cookieName, Value: sessionID + "." + a.sign(sessionID),
		Path: cookiePath, Expires: a.Now().Add(sessionTTL),
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/app", http.StatusSeeOther)
}

// Require wraps handlers with the cookie check (signature + store lookup).
func (a Auth) Require(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(cookieName)
		if err == nil {
			parts := strings.SplitN(c.Value, ".", 2)
			if len(parts) == 2 && hmac.Equal([]byte(parts[1]), []byte(a.sign(parts[0]))) {
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
