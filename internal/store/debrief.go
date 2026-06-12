// ABOUTME: Debrief processed-set: each activity and missed-session fires exactly once.
// ABOUTME: Create-once semantics, 90-day TTL; the sweep is re-runnable any time.

package store

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const debriefsCollection = "debriefs"

type Debriefs struct {
	client *firestore.Client
}

func NewDebriefs(client *firestore.Client) *Debriefs {
	return &Debriefs{client: client}
}

// MarkOnce claims a debrief key; false means someone already did (the sweep
// runs morning AND evening, and task retries replay it: only Create-once
// keeps the athlete from hearing the same debrief twice).
func (d *Debriefs) MarkOnce(ctx context.Context, key string) (bool, error) {
	now := time.Now().UTC()
	_, err := d.client.Collection(debriefsCollection).Doc(key).Create(ctx, map[string]any{
		"created_at": now, "expires_at": now.Add(90 * 24 * time.Hour),
	})
	if status.Code(err) == codes.AlreadyExists {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("debrief mark: %w", err)
	}
	return true, nil
}
