// ABOUTME: Athlete identity: the slow-changing half of the profile (D23 split, M9).
// ABOUTME: YAML-seeded facts + icu-derived zones; mutable state stays in confirmed memory.

package store

import (
	"context"
	"fmt"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const identityDocID = "identity"

// Race is a dated goal; the prefix renders a countdown.
type Race struct {
	Name     string `firestore:"name" yaml:"name"`
	Date     string `firestore:"date" yaml:"date"`         // YYYY-MM-DD
	Priority string `firestore:"priority" yaml:"priority"` // A | B | C
	Notes    string `firestore:"notes,omitempty" yaml:"notes"`
}

// SportZones is the athlete's HR scheme for one sport, read from icu sport
// settings at seed time (his scheme has 7 zones; workout writes stay on the
// 1-5 schema which icu resolves against this).
type SportZones struct {
	Sport    string   `firestore:"sport"`
	LTHR     int      `firestore:"lthr"`
	MaxHR    int      `firestore:"max_hr"`
	Zones    []int    `firestore:"zones"` // upper bpm bounds, low to high
	ZoneName []string `firestore:"zone_names"`
}

// Identity is the slow half of the athlete profile. Everything here changes
// rarely (redeploy-by-seed is fine); current goals/constraints that shift
// week to week belong to confirmed memory instead.
type Identity struct {
	Sports        []string     `firestore:"sports"` // priority order
	Races         []Race       `firestore:"races,omitempty"`
	Availability  string       `firestore:"availability,omitempty"`
	InjuryHistory string       `firestore:"injury_history,omitempty"`
	Preferences   string       `firestore:"preferences,omitempty"`
	Zones         []SportZones `firestore:"zones,omitempty"`
}

// SeedIdentity overwrites the identity doc (seed is the owner).
func (p *Profiles) SeedIdentity(ctx context.Context, id Identity) error {
	_, err := p.client.Collection(profileCollection).Doc(identityDocID).Set(ctx, id)
	if err != nil {
		return fmt.Errorf("identity seed: %w", err)
	}
	return nil
}

// Identity loads the slow profile half; missing doc = empty identity (the
// coach degrades to asking, exactly like day one).
func (p *Profiles) Identity(ctx context.Context) (Identity, error) {
	snap, err := p.client.Collection(profileCollection).Doc(identityDocID).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return Identity{}, nil
	}
	if err != nil {
		return Identity{}, fmt.Errorf("identity get: %w", err)
	}
	var id Identity
	if err := snap.DataTo(&id); err != nil {
		return Identity{}, fmt.Errorf("identity decode: %w", err)
	}
	return id, nil
}

var _ = firestore.DocumentID // keep import if unused elsewhere
