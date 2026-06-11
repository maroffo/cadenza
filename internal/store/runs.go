// ABOUTME: Per-date job run tracking: runs/morning-<date> is the morning idempotency anchor.
// ABOUTME: The watchdog reads the same doc to detect a silent morning failure.

package store

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const runsCollection = "runs"

type Runs struct {
	client *firestore.Client
}

func NewRuns(client *firestore.Client) *Runs {
	return &Runs{client: client}
}

type runDoc struct {
	Status      string    `firestore:"status"`
	CompletedAt time.Time `firestore:"completed_at"`
}

func morningDocID(date string) string { return "morning-" + date }

func (r *Runs) MorningCompleted(ctx context.Context, date string) (bool, error) {
	snap, err := r.client.Collection(runsCollection).Doc(morningDocID(date)).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("runs get: %w", err)
	}
	return snap.Exists(), nil
}

func (r *Runs) MarkMorningCompleted(ctx context.Context, date, jobStatus string) error {
	_, err := r.client.Collection(runsCollection).Doc(morningDocID(date)).Set(ctx, runDoc{
		Status:      jobStatus,
		CompletedAt: time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("runs set: %w", err)
	}
	return nil
}
