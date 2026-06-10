// ABOUTME: Side-effect dedup via atomic create-if-absent docs with a TTL field.
// ABOUTME: Every at-least-once entry point (Telegram, Scheduler, Tasks) reserves before acting.

package store

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const dedupCollection = "dedup"

// Keys become Firestore document ids verbatim, so the charset is locked down:
// externally influenced values (Telegram update ids, dates) must never be able
// to alter the document path or collide across namespaces.
var validDedupKey = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,199}$`)

type Dedup struct {
	client *firestore.Client
}

func NewDedup(client *firestore.Client) *Dedup {
	return &Dedup{client: client}
}

type dedupDoc struct {
	ProcessedAt time.Time `firestore:"processed_at"`
	ExpiresAt   time.Time `firestore:"expires_at"` // Firestore TTL policy field
}

// Reserve atomically claims key. True means the caller owns the side effect;
// false with nil error means a previous delivery already claimed it and the
// caller must no-op. False with non-nil error means UNKNOWN: the caller must
// not proceed as owner, and must not treat the work as done.
//
// Expiry is a best-effort lower bound: Firestore TTL deletion lags ExpiresAt
// (documented up to 72h), so the effective dedup window is at least ttl.
func (d *Dedup) Reserve(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	if !validDedupKey.MatchString(key) {
		return false, fmt.Errorf("dedup: invalid key %q", key)
	}
	if ttl <= 0 {
		return false, fmt.Errorf("dedup: non-positive ttl %v for key %q", ttl, key)
	}
	now := time.Now().UTC()
	_, err := d.client.Collection(dedupCollection).Doc(key).Create(ctx, dedupDoc{
		ProcessedAt: now,
		ExpiresAt:   now.Add(ttl),
	})
	if status.Code(err) == codes.AlreadyExists {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
