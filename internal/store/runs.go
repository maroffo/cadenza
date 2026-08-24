// ABOUTME: Per-date job run tracking: runs/morning-<date> anchors idempotency and the watchdog.
// ABOUTME: States: completed (GO/MODIFY/SKIP) or deferred-rN (HRV retry in flight).

package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const runsCollection = "runs"

// deferredPrefix marks a run doc written by the HRV self-retry deferral.
const deferredPrefix = "deferred-r"

type Runs struct {
	client *firestore.Client
}

func NewRuns(client *firestore.Client) *Runs {
	return &Runs{client: client}
}

type runDoc struct {
	Status        string    `firestore:"status"`
	UpdatedAt     time.Time `firestore:"updated_at"`
	DeferredUntil time.Time `firestore:"deferred_until,omitempty"`
}

func morningDocID(date string) string { return "morning-" + date }

func (r *Runs) get(ctx context.Context, date string) (*runDoc, error) {
	snap, err := r.client.Collection(runsCollection).Doc(morningDocID(date)).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("runs get: %w", err)
	}
	var doc runDoc
	if err := snap.DataTo(&doc); err != nil {
		return nil, fmt.Errorf("runs decode: %w", err)
	}
	return &doc, nil
}

// MorningCompleted is true only for terminal runs: a deferral is not done.
func (r *Runs) MorningCompleted(ctx context.Context, date string) (bool, error) {
	doc, err := r.get(ctx, date)
	if err != nil || doc == nil {
		return false, err
	}
	return !strings.HasPrefix(doc.Status, deferredPrefix), nil
}

// MorningAlive is true for a completed run or an unexpired deferral. A stale
// deferral must not keep the watchdog quiet after its named retry went missing.
func (r *Runs) MorningAlive(ctx context.Context, date string, now time.Time) (bool, error) {
	doc, err := r.get(ctx, date)
	if err != nil || doc == nil {
		return false, err
	}
	if !strings.HasPrefix(doc.Status, deferredPrefix) {
		return true, nil
	}
	return !doc.DeferredUntil.IsZero() && now.Before(doc.DeferredUntil), nil
}

func (r *Runs) MarkMorningCompleted(ctx context.Context, date, jobStatus string) error {
	return r.set(ctx, date, jobStatus, time.Time{})
}

func (r *Runs) MarkMorningDeferred(ctx context.Context, date string, attempt int, until time.Time) error {
	if until.IsZero() {
		return fmt.Errorf("runs set: deferred deadline is required")
	}
	return r.set(ctx, date, fmt.Sprintf("%s%d", deferredPrefix, attempt), until.UTC())
}

func (r *Runs) set(ctx context.Context, date, jobStatus string, deferredUntil time.Time) error {
	_, err := r.client.Collection(runsCollection).Doc(morningDocID(date)).Set(ctx, runDoc{
		Status: jobStatus, UpdatedAt: time.Now().UTC(), DeferredUntil: deferredUntil,
	})
	if err != nil {
		return fmt.Errorf("runs set: %w", err)
	}
	return nil
}
