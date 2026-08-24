// ABOUTME: Lease-backed processed set for debrief and missed-session delivery.
// ABOUTME: Fenced claims complete only after Telegram succeeds; failed/crashed claims are releasable or expire.

package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	debriefsCollection = "debriefs"
	debriefProcessing  = "processing"
	debriefCompleted   = "completed"
	// One debrief has at most one bounded narrator call plus Telegram delivery.
	// Five minutes leaves margin above the two-minute HTTP timeout.
	debriefLease     = 5 * time.Minute
	debriefRetention = 90 * 24 * time.Hour
)

var ErrDebriefLeaseLost = errors.New("debrief: processing lease lost")

type Debriefs struct {
	client *firestore.Client
	now    func() time.Time
}

func NewDebriefs(client *firestore.Client) *Debriefs {
	return &Debriefs{client: client, now: time.Now}
}

type debriefDoc struct {
	Status      string    `firestore:"status"`
	LeaseID     string    `firestore:"lease_id,omitempty"`
	LeaseUntil  time.Time `firestore:"lease_until,omitempty"`
	CreatedAt   time.Time `firestore:"created_at"`
	CompletedAt time.Time `firestore:"completed_at,omitempty"`
	ExpiresAt   time.Time `firestore:"expires_at"`
}

// Claim obtains a fenced processing lease and returns its token. Completed and
// live processing documents return an empty token; an expired processing lease
// is reclaimed transactionally with a fresh token.
func (d *Debriefs) Claim(ctx context.Context, key string) (string, error) {
	leaseID, err := newLeaseToken()
	if err != nil {
		return "", fmt.Errorf("debrief claim %q: %w", key, err)
	}
	now := d.now().UTC()
	acquired := false
	ref := d.client.Collection(debriefsCollection).Doc(key)
	err = d.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		// Firestore may retry this closure after contention; never leak the
		// acquisition result from an aborted attempt.
		acquired = false
		snap, err := tx.Get(ref)
		if status.Code(err) == codes.NotFound {
			acquired = true
			return tx.Set(ref, debriefDoc{
				Status: debriefProcessing, LeaseID: leaseID, LeaseUntil: now.Add(debriefLease),
				CreatedAt: now, ExpiresAt: now.Add(debriefRetention),
			})
		}
		if err != nil {
			return err
		}
		var doc debriefDoc
		if err := snap.DataTo(&doc); err != nil {
			return err
		}
		// Legacy create-once docs represent messages already delivered.
		if doc.Status == debriefCompleted || (doc.Status == "" && !doc.CreatedAt.IsZero()) {
			return nil
		}
		if doc.Status != debriefProcessing {
			return fmt.Errorf("unknown status %q", doc.Status)
		}
		if doc.LeaseUntil.After(now) {
			return nil
		}
		acquired = true
		return tx.Set(ref, map[string]any{
			"status": debriefProcessing, "lease_id": leaseID,
			"lease_until": now.Add(debriefLease), "expires_at": now.Add(debriefRetention),
		}, firestore.MergeAll)
	})
	if err != nil {
		return "", fmt.Errorf("debrief claim %q: %w", key, err)
	}
	if !acquired {
		return "", nil
	}
	return leaseID, nil
}

// Complete records that the notification was delivered. A stale owner cannot
// complete a replacement claim; repeating completion after it is durable is OK.
func (d *Debriefs) Complete(ctx context.Context, key, leaseID string) error {
	if err := validateLeaseID(leaseID); err != nil {
		return fmt.Errorf("debrief complete %q: %w", key, err)
	}
	now := d.now().UTC()
	ref := d.client.Collection(debriefsCollection).Doc(key)
	err := d.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snap, err := tx.Get(ref)
		if status.Code(err) == codes.NotFound {
			return ErrDebriefLeaseLost
		}
		if err != nil {
			return err
		}
		var doc debriefDoc
		if err := snap.DataTo(&doc); err != nil {
			return err
		}
		if doc.Status == debriefCompleted {
			return nil
		}
		if doc.Status != debriefProcessing {
			return fmt.Errorf("cannot complete status %q", doc.Status)
		}
		if doc.LeaseID != leaseID {
			return ErrDebriefLeaseLost
		}
		return tx.Set(ref, map[string]any{
			"status": debriefCompleted, "completed_at": now,
			"lease_id": firestore.Delete, "lease_until": firestore.Delete,
		}, firestore.MergeAll)
	})
	if err != nil {
		return fmt.Errorf("debrief complete %q: %w", key, err)
	}
	return nil
}

// Release frees only the caller's processing claim after a failed send. A
// stale worker cannot delete a replacement lease or a completed marker.
func (d *Debriefs) Release(ctx context.Context, key, leaseID string) error {
	if err := validateLeaseID(leaseID); err != nil {
		return fmt.Errorf("debrief release %q: %w", key, err)
	}
	ref := d.client.Collection(debriefsCollection).Doc(key)
	err := d.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snap, err := tx.Get(ref)
		if status.Code(err) == codes.NotFound {
			return nil
		}
		if err != nil {
			return err
		}
		var doc debriefDoc
		if err := snap.DataTo(&doc); err != nil {
			return err
		}
		if doc.Status != debriefProcessing || doc.LeaseID != leaseID {
			return nil
		}
		return tx.Delete(ref)
	})
	if err != nil {
		return fmt.Errorf("debrief release %q: %w", key, err)
	}
	return nil
}
