// ABOUTME: Web session store: single-use login nonces and 30-day cookie sessions.
// ABOUTME: TTL-expired via expires_at like every other ephemeral collection.

package store

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	webNoncesCollection   = "web_nonces"
	webSessionsCollection = "web_sessions"
)

type WebSessions struct {
	client *firestore.Client
}

func NewWebSessions(client *firestore.Client) *WebSessions {
	return &WebSessions{client: client}
}

// RedeemNonce is single-use by Create semantics: the second redemption of
// the same login link finds the doc and fails closed.
func (w *WebSessions) RedeemNonce(ctx context.Context, nonce string, ttl time.Duration) (bool, error) {
	_, err := w.client.Collection(webNoncesCollection).Doc(nonce).Create(ctx, map[string]any{
		"redeemed_at": time.Now().UTC(),
		"expires_at":  time.Now().UTC().Add(ttl + time.Hour),
	})
	if status.Code(err) == codes.AlreadyExists {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("web nonce: %w", err)
	}
	return true, nil
}

func (w *WebSessions) SaveSession(ctx context.Context, id string, ttl time.Duration) error {
	_, err := w.client.Collection(webSessionsCollection).Doc(id).Set(ctx, map[string]any{
		"created_at": time.Now().UTC(),
		"expires_at": time.Now().UTC().Add(ttl),
	})
	if err != nil {
		return fmt.Errorf("web session save: %w", err)
	}
	return nil
}

func (w *WebSessions) CheckSession(ctx context.Context, id string) (bool, error) {
	snap, err := w.client.Collection(webSessionsCollection).Doc(id).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("web session check: %w", err)
	}
	exp, err := snap.DataAt("expires_at")
	if err != nil {
		return false, nil
	}
	t, ok := exp.(time.Time)
	if !ok {
		// Corrupt doc: fail CLOSED, a session must never become eternal.
		return false, nil
	}
	if time.Now().After(t) {
		// TTL deletion is lazy (up to 24h): enforce expiry at read time.
		return false, nil
	}
	return true, nil
}

// DeleteSession revokes a session (logout / kill switch).
func (w *WebSessions) DeleteSession(ctx context.Context, id string) error {
	_, err := w.client.Collection(webSessionsCollection).Doc(id).Delete(ctx)
	if err != nil {
		return fmt.Errorf("web session delete: %w", err)
	}
	return nil
}
