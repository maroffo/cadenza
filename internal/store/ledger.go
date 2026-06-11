// ABOUTME: Write ledger: every workout write attempt recorded with intent, outcome, diffs.
// ABOUTME: The audit trail that makes "what did cadenza put on my calendar?" answerable.

package store

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"
)

const ledgerCollection = "events_written"

type Ledger struct {
	client *firestore.Client
}

func NewLedger(client *firestore.Client) *Ledger {
	return &Ledger{client: client}
}

type WriteRecord struct {
	Date       string    `firestore:"date"`
	Title      string    `firestore:"title"`
	ExternalID string    `firestore:"external_id"`
	EventID    int64     `firestore:"event_id,omitempty"`
	Status     string    `firestore:"status"` // verified | unverified_surfaced
	Attempts   int       `firestore:"attempts"`
	Diffs      []string  `firestore:"diffs,omitempty"`
	PlanJSON   string    `firestore:"plan_json"`
	SessionID  string    `firestore:"session_id,omitempty"`
	CreatedAt  time.Time `firestore:"created_at"`
}

// Record upserts by external id: a task retry overwrites its own record
// instead of duplicating the audit line.
func (l *Ledger) Record(ctx context.Context, rec WriteRecord) error {
	rec.CreatedAt = time.Now().UTC()
	_, err := l.client.Collection(ledgerCollection).Doc(rec.ExternalID).Set(ctx, rec)
	if err != nil {
		return fmt.Errorf("ledger record: %w", err)
	}
	return nil
}
