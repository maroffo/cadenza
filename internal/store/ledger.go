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
	Date        string    `firestore:"date"`
	Title       string    `firestore:"title"`
	ExternalID  string    `firestore:"external_id"`
	ContentHash string    `firestore:"content_hash"`
	EventID     int64     `firestore:"event_id,omitempty"`
	Status      string    `firestore:"status"` // verified | unverified_surfaced
	Attempts    int       `firestore:"attempts"`
	Diffs       []string  `firestore:"diffs,omitempty"`
	PlanJSON    string    `firestore:"plan_json"`
	SessionID   string    `firestore:"session_id,omitempty"`
	CreatedAt   time.Time `firestore:"created_at"`
	// ExpiresAt drives the TTL policy: training prescriptions are
	// health-adjacent personal data (24 months).
	ExpiresAt time.Time `firestore:"expires_at"`
}

const ledgerRetention = 24 * 30 * 24 * time.Hour

// Record keys by external id + content hash: a task retry of the SAME plan
// overwrites its own record; a regenerated plan gets its own audit line
// (the slot-keyed event id alone would erase superseded plans' history).
func (l *Ledger) Record(ctx context.Context, rec WriteRecord) error {
	rec.CreatedAt = time.Now().UTC()
	rec.ExpiresAt = rec.CreatedAt.Add(ledgerRetention)
	id := rec.ExternalID
	if rec.ContentHash != "" {
		id = rec.ExternalID + "-" + rec.ContentHash
	}
	_, err := l.client.Collection(ledgerCollection).Doc(id).Set(ctx, rec)
	if err != nil {
		return fmt.Errorf("ledger record: %w", err)
	}
	return nil
}

// RecentWrites lists the latest calendar writes for the dashboard.
func (l *Ledger) RecentWrites(ctx context.Context, limit int) ([]WriteRecord, error) {
	docs, err := l.client.Collection(ledgerCollection).
		OrderBy("created_at", firestore.Desc).Limit(limit).Documents(ctx).GetAll()
	if err != nil {
		return nil, fmt.Errorf("ledger recent: %w", err)
	}
	out := make([]WriteRecord, 0, len(docs))
	for _, d := range docs {
		var rec WriteRecord
		if err := d.DataTo(&rec); err != nil {
			return nil, fmt.Errorf("ledger decode %s: %w", d.Ref.ID, err)
		}
		out = append(out, rec)
	}
	return out, nil
}
