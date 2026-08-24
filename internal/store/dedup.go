// ABOUTME: Lease-backed side-effect dedup with explicit processing/completed states.
// ABOUTME: Fenced claims let crashed work expire without stale workers mutating a replacement lease.

package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	dedupCollection = "dedup"
	dedupProcessing = "processing"
	dedupCompleted  = "completed"
	// Message.Run has a four-minute end-to-end deadline. The lease is longer,
	// so valid work cannot be reclaimed while still running; the queue keeps
	// enough attempts to retry after this lease when a process crashes.
	dedupLease = 5 * time.Minute
)

var (
	// ErrDedupInProgress tells an at-least-once caller to retry rather than ack
	// a delivery whose processing lease is still live.
	ErrDedupInProgress = errors.New("dedup: processing lease active")
	// ErrDedupLeaseLost means this worker's fenced claim was superseded. The
	// current owner must be allowed to finish; stale Complete/Release calls may
	// never mutate its state.
	ErrDedupLeaseLost = errors.New("dedup: processing lease lost")
)

// Keys become Firestore document ids verbatim, so the charset is locked down:
// externally influenced values (Telegram update ids, dates) must never be able
// to alter the document path or collide across namespaces.
var validDedupKey = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,199}$`)

// Lease ids are generated internally and carried back by the owner as fencing
// tokens. Their format is deliberately strict so they can never become paths.
var validLeaseID = regexp.MustCompile(`^[0-9a-f]{32}$`)

type Dedup struct {
	client *firestore.Client
	now    func() time.Time
}

func NewDedup(client *firestore.Client) *Dedup {
	return &Dedup{client: client, now: time.Now}
}

type dedupDoc struct {
	Status      string    `firestore:"status"`
	LeaseID     string    `firestore:"lease_id,omitempty"`
	LeaseUntil  time.Time `firestore:"lease_until,omitempty"`
	ProcessedAt time.Time `firestore:"processed_at,omitempty"`
	UpdatedAt   time.Time `firestore:"updated_at"`
	ExpiresAt   time.Time `firestore:"expires_at"` // Firestore TTL policy field
}

// Reserve transactionally claims key and returns its fencing token. An empty
// token with nil error means the key is already completed. A live processing
// claim returns ErrDedupInProgress so queue consumers retry rather than acking
// unfinished work. Expired processing is reclaimed with a fresh token.
//
// Expiry is a best-effort lower bound: Firestore TTL deletion lags ExpiresAt
// (documented up to 72h), so the effective completed dedup window is at least ttl.
func (d *Dedup) Reserve(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if err := validateDedupKey(key); err != nil {
		return "", err
	}
	if ttl <= 0 {
		return "", fmt.Errorf("dedup: non-positive ttl %v for key %q", ttl, key)
	}
	leaseID, err := newLeaseToken()
	if err != nil {
		return "", fmt.Errorf("dedup reserve %q: %w", key, err)
	}
	now := d.now().UTC()
	acquired := false
	ref := d.client.Collection(dedupCollection).Doc(key)
	err = d.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		// Firestore may retry this closure after contention; never leak the
		// acquisition result from an aborted attempt.
		acquired = false
		snap, err := tx.Get(ref)
		if status.Code(err) == codes.NotFound {
			acquired = true
			return tx.Set(ref, dedupDoc{
				Status: dedupProcessing, LeaseID: leaseID, LeaseUntil: now.Add(dedupLease),
				UpdatedAt: now, ExpiresAt: now.Add(ttl),
			})
		}
		if err != nil {
			return err
		}
		var doc dedupDoc
		if err := snap.DataTo(&doc); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		// Legacy create-once docs had ProcessedAt but no Status. Treat them as
		// completed so rollout never replays historical Telegram updates.
		if doc.Status == dedupCompleted || (doc.Status == "" && !doc.ProcessedAt.IsZero()) {
			return nil
		}
		if doc.Status != dedupProcessing && doc.Status != "" {
			return fmt.Errorf("unknown status %q", doc.Status)
		}
		if doc.LeaseUntil.After(now) {
			return ErrDedupInProgress
		}
		acquired = true
		return tx.Set(ref, map[string]any{
			"status": dedupProcessing, "lease_id": leaseID,
			"lease_until": now.Add(dedupLease), "updated_at": now,
			"expires_at": now.Add(ttl), "processed_at": firestore.Delete,
		}, firestore.MergeAll)
	})
	if err != nil {
		return "", fmt.Errorf("dedup reserve %q: %w", key, err)
	}
	if !acquired {
		return "", nil
	}
	return leaseID, nil
}

// Complete makes the caller's processing claim duplicate-safe for its TTL.
// A stale owner is fenced with ErrDedupLeaseLost and cannot complete a newer
// claim. Re-completing an already completed key is harmless.
func (d *Dedup) Complete(ctx context.Context, key, leaseID string) error {
	if err := validateDedupKey(key); err != nil {
		return err
	}
	if err := validateLeaseID(leaseID); err != nil {
		return err
	}
	now := d.now().UTC()
	ref := d.client.Collection(dedupCollection).Doc(key)
	err := d.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snap, err := tx.Get(ref)
		if status.Code(err) == codes.NotFound {
			return ErrDedupLeaseLost
		}
		if err != nil {
			return err
		}
		var doc dedupDoc
		if err := snap.DataTo(&doc); err != nil {
			return err
		}
		if doc.Status == dedupCompleted {
			return nil
		}
		if doc.Status != dedupProcessing {
			return fmt.Errorf("cannot complete status %q", doc.Status)
		}
		if doc.LeaseID != leaseID {
			return ErrDedupLeaseLost
		}
		return tx.Set(ref, map[string]any{
			"status": dedupCompleted, "processed_at": now, "updated_at": now,
			"lease_id": firestore.Delete, "lease_until": firestore.Delete,
		}, firestore.MergeAll)
	})
	if err != nil {
		return fmt.Errorf("dedup complete %q: %w", key, err)
	}
	return nil
}

// Release compensates a transient failure. Only the exact fenced owner may
// delete its processing claim; a stale worker cannot erase a replacement lease
// or a completed marker.
func (d *Dedup) Release(ctx context.Context, key, leaseID string) error {
	if err := validateDedupKey(key); err != nil {
		return err
	}
	if err := validateLeaseID(leaseID); err != nil {
		return err
	}
	ref := d.client.Collection(dedupCollection).Doc(key)
	err := d.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snap, err := tx.Get(ref)
		if status.Code(err) == codes.NotFound {
			return nil
		}
		if err != nil {
			return err
		}
		var doc dedupDoc
		if err := snap.DataTo(&doc); err != nil {
			return err
		}
		if doc.Status != dedupProcessing || doc.LeaseID != leaseID {
			return nil
		}
		return tx.Delete(ref)
	})
	if err != nil {
		return fmt.Errorf("dedup release %q: %w", key, err)
	}
	return nil
}

func newLeaseToken() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("lease token: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func validateDedupKey(key string) error {
	if !validDedupKey.MatchString(key) {
		return fmt.Errorf("dedup: invalid key %q", key)
	}
	return nil
}

func validateLeaseID(leaseID string) error {
	if !validLeaseID.MatchString(leaseID) {
		return fmt.Errorf("store: invalid lease id")
	}
	return nil
}
