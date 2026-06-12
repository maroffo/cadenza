// ABOUTME: Morning subjective check-in: the athlete's own 2-tap signal (M9.4).
// ABOUTME: Fills verdict gaps conservatively; never overrides device data.

package store

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const checkinsCollection = "checkins"

// Checkin is one day's subjective taps; zero values mean "not answered".
type Checkin struct {
	Feeling    string    `firestore:"feeling,omitempty"`     // bene | cosi | stanco | dolorante
	TimeBudget string    `firestore:"time_budget,omitempty"` // full | short | none
	UpdatedAt  time.Time `firestore:"updated_at"`
	ExpiresAt  time.Time `firestore:"expires_at"`
}

type Checkins struct {
	client *firestore.Client
}

func NewCheckins(client *firestore.Client) *Checkins {
	return &Checkins{client: client}
}

// SetField records one answer; re-taps overwrite (the athlete's latest word
// wins, e.g. feeling worse later in the morning).
func (c *Checkins) SetField(ctx context.Context, date, field, value string) error {
	if field != "feeling" && field != "time_budget" {
		return fmt.Errorf("checkin: campo %q sconosciuto", field)
	}
	now := time.Now().UTC()
	_, err := c.client.Collection(checkinsCollection).Doc(date).Set(ctx, map[string]any{
		field: value, "updated_at": now, "expires_at": now.Add(90 * 24 * time.Hour),
	}, firestore.MergeAll)
	if err != nil {
		return fmt.Errorf("checkin set: %w", err)
	}
	return nil
}

// Get returns the day's check-in; missing doc = empty (never an error).
func (c *Checkins) Get(ctx context.Context, date string) (Checkin, error) {
	snap, err := c.client.Collection(checkinsCollection).Doc(date).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return Checkin{}, nil
	}
	if err != nil {
		return Checkin{}, fmt.Errorf("checkin get: %w", err)
	}
	var ci Checkin
	if err := snap.DataTo(&ci); err != nil {
		return Checkin{}, fmt.Errorf("checkin decode: %w", err)
	}
	return ci, nil
}
