// ABOUTME: Session store: cadenza-owned turn schema in a subcollection, never raw SDK JSON.
// ABOUTME: Any load failure degrades to a fresh session (decision 11); retention via TTL field.

package store

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"
)

const (
	sessionsCollection = "sessions"
	turnsSubcollection = "turns"
	// SchemaVersion travels with every turn so future migrations stay additive.
	sessionSchemaVersion = 1
)

type Sessions struct {
	client *firestore.Client
}

func NewSessions(client *firestore.Client) *Sessions {
	return &Sessions{client: client}
}

type sessionDoc struct {
	Mode      string    `firestore:"mode"` // morning | chat | injury
	StartedAt time.Time `firestore:"started_at"`
	TurnCount int       `firestore:"turn_count"`
	Schema    int       `firestore:"schema"`
}

// Turn is the cadenza-owned exchange record. Content is plain text in M4;
// tool blocks join in M5 (additive fields, same schema version policy).
type Turn struct {
	Seq     int       `firestore:"seq"`
	Role    string    `firestore:"role"` // user | assistant
	Content string    `firestore:"content"`
	Model   string    `firestore:"model,omitempty"`
	TS      time.Time `firestore:"ts"`
	Schema  int       `firestore:"schema"`
	// ExpiresAt drives the Firestore TTL policy: health-adjacent personal
	// data does not live forever by accident (18 months).
	ExpiresAt time.Time `firestore:"expires_at"`
}

// turnRetention bounds how long narrative/health content persists.
const turnRetention = 18 * 30 * 24 * time.Hour

// Create opens a session and returns its id.
func (s *Sessions) Create(ctx context.Context, mode string, now time.Time) (string, error) {
	// UnixNano avoids collisions at 1-second granularity (forced re-runs).
	id := fmt.Sprintf("s-%s-%d", mode, now.UTC().UnixNano())
	_, err := s.client.Collection(sessionsCollection).Doc(id).Set(ctx, sessionDoc{
		Mode: mode, StartedAt: now.UTC(), Schema: sessionSchemaVersion,
	})
	if err != nil {
		return "", fmt.Errorf("session create: %w", err)
	}
	return id, nil
}

// AppendTurn persists one turn with a zero-padded sequence id.
func (s *Sessions) AppendTurn(ctx context.Context, sessionID string, seq int, role, content, model string) error {
	now := time.Now().UTC()
	turn := Turn{
		Seq: seq, Role: role, Content: content, Model: model,
		TS: now, Schema: sessionSchemaVersion, ExpiresAt: now.Add(turnRetention),
	}
	doc := s.client.Collection(sessionsCollection).Doc(sessionID).
		Collection(turnsSubcollection).Doc(fmt.Sprintf("%06d", seq))
	if _, err := doc.Set(ctx, turn); err != nil {
		return fmt.Errorf("session append: %w", err)
	}
	_, err := s.client.Collection(sessionsCollection).Doc(sessionID).
		Set(ctx, map[string]any{"turn_count": seq}, firestore.MergeAll)
	if err != nil {
		return fmt.Errorf("session count: %w", err)
	}
	return nil
}

// LoadTurns returns the session turns in order. ANY decode failure returns
// an error: callers must treat it as "start a fresh session", never crash
// and never trust a partial history.
func (s *Sessions) LoadTurns(ctx context.Context, sessionID string) ([]Turn, error) {
	iter := s.client.Collection(sessionsCollection).Doc(sessionID).
		Collection(turnsSubcollection).OrderBy("seq", firestore.Asc).Documents(ctx)
	defer iter.Stop()

	var turns []Turn
	for {
		snap, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("session load: %w", err)
		}
		var t Turn
		if err := snap.DataTo(&t); err != nil {
			return nil, fmt.Errorf("session decode (turn %s): %w", snap.Ref.ID, err)
		}
		// Reject only NEWER schemas: older turns stay readable so additive
		// bumps never orphan existing history (the version is for breaking
		// changes a rollback might encounter).
		if t.Schema > sessionSchemaVersion {
			return nil, fmt.Errorf("session schema %d unsupported (turn %s)", t.Schema, snap.Ref.ID)
		}
		turns = append(turns, t)
	}
	return turns, nil
}
