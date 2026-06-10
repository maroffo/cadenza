// ABOUTME: Athlete profile repository: baselines + tunable ramp cap from profile/current.
// ABOUTME: Mutations arrive in M5 via the append-only event log; M2 reads what the seeder wrote.

package store

import (
	"context"
	"fmt"

	"cloud.google.com/go/firestore"

	"github.com/maroffo/cadenza/internal/verdict"
)

const (
	profileCollection = "profile"
	profileDocID      = "current"
)

type Profiles struct {
	client *firestore.Client
}

func NewProfiles(client *firestore.Client) *Profiles {
	return &Profiles{client: client}
}

type profileDoc struct {
	Baselines struct {
		HRVMean   float64 `firestore:"hrv_mean"`
		HRVSD     float64 `firestore:"hrv_sd"`
		RestingHR float64 `firestore:"resting_hr"`
	} `firestore:"baselines"`
	RampCap float64 `firestore:"ramp_cap"`
}

// Profile satisfies job.ProfileSource. A missing or unreadable profile is an
// error, not a default: coaching on invented baselines is worse than retrying.
func (p *Profiles) Profile(ctx context.Context) (verdict.Baselines, float64, error) {
	snap, err := p.client.Collection(profileCollection).Doc(profileDocID).Get(ctx)
	if err != nil {
		return verdict.Baselines{}, 0, fmt.Errorf("profile/current: %w", err)
	}
	var doc profileDoc
	if err := snap.DataTo(&doc); err != nil {
		return verdict.Baselines{}, 0, fmt.Errorf("profile/current decode: %w", err)
	}
	if doc.Baselines.HRVMean <= 0 || doc.Baselines.HRVSD <= 0 {
		return verdict.Baselines{}, 0, fmt.Errorf("profile/current: implausible baselines %+v", doc.Baselines)
	}
	return verdict.Baselines{
		HRVMean:   doc.Baselines.HRVMean,
		HRVSD:     doc.Baselines.HRVSD,
		RestingHR: doc.Baselines.RestingHR,
	}, doc.RampCap, nil
}

// Seed writes profile/current. Used by cmd/seed; overwrites are deliberate
// there (the seeder is the only writer until M5 mutations land).
func (p *Profiles) Seed(ctx context.Context, baselines verdict.Baselines, rampCap float64) error {
	var doc profileDoc
	doc.Baselines.HRVMean = baselines.HRVMean
	doc.Baselines.HRVSD = baselines.HRVSD
	doc.Baselines.RestingHR = baselines.RestingHR
	doc.RampCap = rampCap
	_, err := p.client.Collection(profileCollection).Doc(profileDocID).Set(ctx, doc)
	return err
}
