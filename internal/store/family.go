// ABOUTME: Family energy profile: per-person daily kcal (season-aware) + per-meal distribution.
// ABOUTME: YAML-seeded like the athlete identity; read by the coach's meal_targets tool.

package store

import (
	"context"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const familyDocID = "family"

// FamilyMember is one person's daily energy need. kcal_caldo applies in
// primavera/estate, kcal_freddo in autunno/inverno (equal for adults; the
// children swing with the season). Nota records how the figure was derived.
type FamilyMember struct {
	Nome       string  `firestore:"nome" yaml:"nome"`
	Ruolo      string  `firestore:"ruolo" yaml:"ruolo"` // atleta | adulto | bambino
	KcalCaldo  float64 `firestore:"kcal_caldo" yaml:"kcal_caldo"`
	KcalFreddo float64 `firestore:"kcal_freddo" yaml:"kcal_freddo"`
	Nota       string  `firestore:"nota,omitempty" yaml:"nota"`
}

// Family is the household energy profile: the per-meal distribution (percent of
// each person's day) plus every member. Slow-changing and editable by re-seed.
type Family struct {
	Distribuzione map[string]float64 `firestore:"distribuzione" yaml:"distribuzione"`
	Membri        []FamilyMember     `firestore:"membri" yaml:"membri"`
}

// SeedFamily overwrites the family doc (the seed is the owner).
func (p *Profiles) SeedFamily(ctx context.Context, f Family) error {
	if _, err := p.client.Collection(profileCollection).Doc(familyDocID).Set(ctx, f); err != nil {
		return fmt.Errorf("family seed: %w", err)
	}
	return nil
}

// Family loads the household profile; a missing doc returns an empty Family
// (the coach hides meal targets rather than inventing numbers).
func (p *Profiles) Family(ctx context.Context) (Family, error) {
	snap, err := p.client.Collection(profileCollection).Doc(familyDocID).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return Family{}, nil
	}
	if err != nil {
		return Family{}, fmt.Errorf("family get: %w", err)
	}
	var f Family
	if err := snap.DataTo(&f); err != nil {
		return Family{}, fmt.Errorf("family decode: %w", err)
	}
	return f, nil
}
