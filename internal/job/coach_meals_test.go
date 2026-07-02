// ABOUTME: Tests for meal_targets (per-person, season-aware kcal) and scale_recipe (portion math).
// ABOUTME: The proportional split and the arithmetic are the coach's job, asserted here directly.

package job

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/maroffo/cadenza/internal/store"
)

type stubFamily struct{ fam store.Family }

func (s stubFamily) Family(context.Context) (store.Family, error) { return s.fam, nil }

func testFamily() store.Family {
	return store.Family{
		Distribuzione: map[string]float64{
			"colazione": 20, "spuntino_mattina": 5, "pranzo": 35,
			"spuntino_pomeriggio": 10, "cena": 30,
		},
		Membri: []store.FamilyMember{
			{Nome: "Max", KcalCaldo: 2000, KcalFreddo: 2000},
			{Nome: "Angela", KcalCaldo: 1600, KcalFreddo: 1600},
			{Nome: "Figlia", KcalCaldo: 2050, KcalFreddo: 2300},
			{Nome: "Figlio", KcalCaldo: 1850, KcalFreddo: 2050},
		},
	}
}

type mealTargetsReply struct {
	Tipo       string  `json:"tipo"`
	Stagione   string  `json:"stagione"`
	Totale     float64 `json:"totale_famiglia_kcal"`
	PerPersona []struct {
		Nome        string  `json:"nome"`
		TargetPasto float64 `json:"target_pasto_kcal"`
	} `json:"per_persona"`
}

func parseMealTargets(t *testing.T, reply string) mealTargetsReply {
	t.Helper()
	i := strings.Index(reply, "{")
	if i < 0 {
		t.Fatalf("no JSON in reply: %s", reply)
	}
	var out mealTargetsReply
	if err := json.Unmarshal([]byte(reply[i:]), &out); err != nil {
		t.Fatalf("bad JSON: %v\n%s", err, reply)
	}
	return out
}

func target(t *testing.T, r mealTargetsReply, nome string) float64 {
	t.Helper()
	for _, p := range r.PerPersona {
		if p.Nome == nome {
			return p.TargetPasto
		}
	}
	t.Fatalf("member %q missing from targets", nome)
	return 0
}

func TestMealTargets_SummerProportional(t *testing.T) {
	// fixedNow is 2026-06-10 -> estate (warm), pranzo = 35%.
	c := &Coach{Family: stubFamily{testFamily()}, Now: fixedNow, TZ: testTZ}
	reply, err := c.mealTargets(context.Background(), []byte(`{"tipo":"pranzo"}`))
	if err != nil {
		t.Fatalf("mealTargets: %v", err)
	}
	r := parseMealTargets(t, reply)
	if r.Stagione != "estate" {
		t.Errorf("stagione = %q, want estate", r.Stagione)
	}
	if got := target(t, r, "Max"); got != 700 {
		t.Errorf("Max pranzo = %v, want 700 (2000 x 35%%)", got)
	}
	if got := target(t, r, "Figlia"); got != 717.5 {
		t.Errorf("Figlia pranzo = %v, want 717.5 (2050 x 35%%)", got)
	}
	// Proportional split: the teen's lunch exceeds Angela's (evidence over 53/47).
	if target(t, r, "Figlia") <= target(t, r, "Angela") {
		t.Error("13yo lunch target should exceed the sedentary adult's")
	}
	if r.Totale != 2625 { // 700 + 560 + 717.5 + 647.5
		t.Errorf("totale = %v, want 2625", r.Totale)
	}
}

func TestMealTargets_WinterRaisesChildren(t *testing.T) {
	jan := func() time.Time { return time.Date(2026, 1, 15, 9, 0, 0, 0, testTZ) }
	c := &Coach{Family: stubFamily{testFamily()}, Now: jan, TZ: testTZ}
	reply, _ := c.mealTargets(context.Background(), []byte(`{"tipo":"pranzo"}`))
	r := parseMealTargets(t, reply)
	if r.Stagione != "inverno" {
		t.Errorf("stagione = %q, want inverno", r.Stagione)
	}
	if got := target(t, r, "Figlia"); got != 805 { // 2300 x 35%
		t.Errorf("Figlia winter pranzo = %v, want 805", got)
	}
	if got := target(t, r, "Max"); got != 700 { // adult unchanged by season
		t.Errorf("Max winter pranzo = %v, want 700 (season-invariant)", got)
	}
}

func TestMealTargets_UnknownTipoAndEmpty(t *testing.T) {
	c := &Coach{Family: stubFamily{testFamily()}, Now: fixedNow, TZ: testTZ}
	reply, _ := c.mealTargets(context.Background(), []byte(`{"tipo":"merenda_di_mezzanotte"}`))
	if !strings.Contains(reply, "non valido") {
		t.Errorf("unknown tipo not rejected: %q", reply)
	}
	empty := &Coach{Family: stubFamily{store.Family{}}, Now: fixedNow, TZ: testTZ}
	reply2, _ := empty.mealTargets(context.Background(), []byte(`{"tipo":"pranzo"}`))
	if !strings.Contains(reply2, "non è ancora configurato") {
		t.Errorf("empty family not handled: %q", reply2)
	}
}

func TestScaleRecipe_SizesToTarget(t *testing.T) {
	book := recipeBook(t)
	c := &Coach{Recipes: book, Now: fixedNow, TZ: testTZ}
	// Compute a known recipe's per-serving kcal, then ask to double it: the tool
	// must return exactly 2 servings (arithmetic in Go, not the model).
	r, ok := book.ByID("insalata-riso")
	if !ok {
		t.Fatal("expected known recipe insalata-riso")
	}
	per, _ := book.RecipePerServing(r)
	if per.Kcal <= 0 {
		t.Fatalf("insalata-riso has no per-serving kcal")
	}
	input := []byte(`{"ricetta":"insalata-riso","target_kcal":` + strconv.FormatFloat(per.Kcal*2, 'f', 2, 64) + `}`)
	reply, err := c.scaleRecipe(context.Background(), input)
	if err != nil {
		t.Fatalf("scaleRecipe: %v", err)
	}
	var out struct {
		Porzioni float64 `json:"porzioni_consigliate"`
		Kcal     float64 `json:"kcal_risultanti"`
	}
	i := strings.Index(reply, "{")
	if err := json.Unmarshal([]byte(reply[i:]), &out); err != nil {
		t.Fatalf("bad JSON: %v\n%s", err, reply)
	}
	if out.Porzioni != 2 {
		t.Errorf("porzioni = %v, want 2 (target = 2x per-serving)", out.Porzioni)
	}
}

func TestScaleRecipe_MissingRecipe(t *testing.T) {
	c := &Coach{Recipes: recipeBook(t), Now: fixedNow, TZ: testTZ}
	reply, _ := c.scaleRecipe(context.Background(), []byte(`{"ricetta":"piatto_inesistente_xyz","target_kcal":600}`))
	if !strings.Contains(reply, "non c'è una ricetta") {
		t.Errorf("missing recipe not handled: %q", reply)
	}
}
