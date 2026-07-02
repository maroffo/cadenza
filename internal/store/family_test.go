// ABOUTME: Emulator round-trip test for the family energy profile store.
// ABOUTME: Skips unless FIRESTORE_EMULATOR_HOST is set; REQUIRE_EMULATOR=1 makes skips fatal.

package store

import (
	"context"
	"testing"
	"time"
)

func TestFamily_SeedAndReadRoundTrip(t *testing.T) {
	client := emulatorClient(t)
	p := NewProfiles(client)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	want := Family{
		Distribuzione: map[string]float64{"colazione": 20, "pranzo": 35, "cena": 30, "spuntino_mattina": 5, "spuntino_pomeriggio": 10},
		Membri: []FamilyMember{
			{Nome: "Max", Ruolo: "atleta", KcalCaldo: 2000, KcalFreddo: 2000, Nota: "baseline"},
			{Nome: "Figlia", Ruolo: "bambino", KcalCaldo: 2050, KcalFreddo: 2300},
		},
	}
	if err := p.SeedFamily(ctx, want); err != nil {
		t.Fatalf("SeedFamily: %v", err)
	}
	got, err := p.Family(ctx)
	if err != nil {
		t.Fatalf("Family: %v", err)
	}
	if len(got.Membri) != 2 || got.Membri[0].Nome != "Max" || got.Membri[1].KcalFreddo != 2300 {
		t.Errorf("membri did not round-trip: %+v", got.Membri)
	}
	if got.Distribuzione["pranzo"] != 35 {
		t.Errorf("distribuzione did not round-trip: %+v", got.Distribuzione)
	}
}
