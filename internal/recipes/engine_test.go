// ABOUTME: Tests for the recipe/meal engine: parity with scripts/recipe_macros.py.
// ABOUTME: Stdlib testing only; data comes from the embedded recipes.yaml + foods catalog.

package recipes

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/maroffo/cadenza/internal/foods"
)

// tol is the parity tolerance against the executable spec's numbers. The Go
// engine rounds each ingredient via foods.Per before summing, so totals drift
// from the prototype's raw-accumulate-then-round by well under half a unit.
const tol = 0.5

func book(t *testing.T) *Book {
	t.Helper()
	b, err := Load(foods.MustLoad())
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	return b
}

// approx fails the test if got is more than tol away from want.
func approx(t *testing.T, label string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %.4f, want %.4f (±%.1f)", label, got, want, tol)
	}
}

// contains reports whether s is in xs.
func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func TestLoad(t *testing.T) {
	b := book(t)
	// Lower bound, not exact: the recipe book grows over time. Load() also
	// validates every ingredient resolves, so a successful load already proves
	// referential integrity.
	if got := len(b.Recipes()); got < 4 {
		t.Errorf("len(Recipes()) = %d, want >= 4", got)
	}
	if _, ok := b.ByID("insalata-riso"); !ok {
		t.Error("expected known recipe insalata-riso to be present")
	}
	if got := len(b.Meals()); got < 1 {
		t.Errorf("len(Meals()) = %d, want >= 1", got)
	}
}

func TestMustLoadNilCatalog(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("MustLoad(nil) did not panic")
		}
	}()
	MustLoad(nil)
}

func TestRecipePerServingColazione(t *testing.T) {
	b := book(t)
	r, ok := b.ByID("colazione-avena-chia-yogurt")
	if !ok {
		t.Fatal("recipe colazione-avena-chia-yogurt not found")
	}
	m, allergens := b.RecipePerServing(r)
	approx(t, "kcal", m.Kcal, 353)
	approx(t, "carb", m.CarbG, 38.0)
	approx(t, "prot", m.ProteinG, 28.4)
	approx(t, "fat", m.FatG, 10.5)
	approx(t, "fiber", m.FiberG, 11.6)
	if !contains(allergens, "lactose") || !contains(allergens, "milk") {
		t.Errorf("allergens = %v, want to contain lactose and milk", allergens)
	}
}

func TestRecipePerServingInsalataRiso(t *testing.T) {
	b := book(t)
	r, _ := b.ByID("insalata-riso")
	m, allergens := b.RecipePerServing(r)
	approx(t, "kcal", m.Kcal, 427)
	for _, want := range []string{"fish", "egg", "milk"} {
		if !contains(allergens, want) {
			t.Errorf("allergens = %v, want to contain %q", allergens, want)
		}
	}
	// mozzarella_delattosata carries milk but not lactose: the distinction matters.
	if contains(allergens, "lactose") {
		t.Errorf("allergens = %v, must NOT contain lactose", allergens)
	}
}

func TestRecipePerServingPastaZucchineTempeh(t *testing.T) {
	b := book(t)
	r, _ := b.ByID("pasta-zucchine-tempeh")
	m, allergens := b.RecipePerServing(r)
	// Spec brief said ~446; per-ingredient rounding yields 445.5 (within tol).
	approx(t, "kcal", m.Kcal, 445.5)
	approx(t, "prot", m.ProteinG, 22.0)
	if !contains(allergens, "gluten") || !contains(allergens, "soy") {
		t.Errorf("allergens = %v, want {gluten, soy}", allergens)
	}
	if len(allergens) != 2 {
		t.Errorf("allergens = %v, want exactly 2", allergens)
	}
}

func TestRecipePerServingTonnoFagioli(t *testing.T) {
	b := book(t)
	r, _ := b.ByID("tonno-fagioli")
	m, allergens := b.RecipePerServing(r)
	approx(t, "kcal", m.Kcal, 321)
	approx(t, "fiber", m.FiberG, 8.6)
	if !contains(allergens, "fish") {
		t.Errorf("allergens = %v, want to contain fish", allergens)
	}
}

func TestMealTotals(t *testing.T) {
	b := book(t)
	m, ok := b.MealByID("pranzo-tonno-fagioli")
	if !ok {
		t.Fatal("meal pranzo-tonno-fagioli not found")
	}
	tot, allergens, flags := b.MealTotals(m)
	approx(t, "kcal", tot.Kcal, 1560)
	approx(t, "fiber", tot.FiberG, 50.6)
	approx(t, "sodium", tot.SodiumMg, 2994)
	if !contains(allergens, "fish") {
		t.Errorf("allergens = %v, want to contain fish", allergens)
	}
	if contains(allergens, "lactose") {
		t.Errorf("allergens = %v, must NOT contain lactose", allergens)
	}
	if len(flags) != 0 {
		t.Errorf("flags = %v, want none (all refs resolve)", flags)
	}

	// Family split: 53% over 2 adults, 47% over 2 children.
	approx(t, "adult kcal", Split(tot, 0.265).Kcal, 413.5)
	approx(t, "child kcal", Split(tot, 0.235).Kcal, 366.7)
}

func TestSeason(t *testing.T) {
	cases := []struct {
		t    time.Time
		want string
	}{
		{time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC), "estate"},
		{time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC), "inverno"},
		{time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), "primavera"},
		{time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC), "autunno"},
		{time.Date(2026, 12, 25, 0, 0, 0, 0, time.UTC), "inverno"},
	}
	for _, c := range cases {
		if got := Season(c.t); got != c.want {
			t.Errorf("Season(%s) = %q, want %q", c.t.Format("2006-01-02"), got, c.want)
		}
	}
}

func TestInSeason(t *testing.T) {
	allYear := Recipe{Stagioni: nil}
	if !InSeason(allYear, "inverno") {
		t.Error("recipe with no seasons should be in season year-round")
	}
	summer := Recipe{Stagioni: []string{"primavera", "estate"}}
	if InSeason(summer, "inverno") {
		t.Error("summer recipe should not be in season in winter")
	}
	if !InSeason(summer, "estate") {
		t.Error("summer recipe should be in season in summer")
	}
}

func TestSuggestExcludesAllergenHard(t *testing.T) {
	b := book(t)
	out := b.Suggest(SuggestFilter{ExcludeAllergens: []string{"lactose"}})
	if len(out) == 0 {
		t.Fatal("Suggest returned no recipes")
	}
	for _, r := range out {
		allergens := b.recipeAllergens(r)
		if contains(allergens, "lactose") {
			t.Errorf("recipe %q has lactose but was not excluded", r.ID)
		}
		if r.ID == "colazione-avena-chia-yogurt" {
			t.Error("lactose recipe colazione-avena-chia-yogurt should be excluded")
		}
	}
}

func TestSuggestSeasonSoftRanking(t *testing.T) {
	// Controlled book: the ranking invariant must hold regardless of how large
	// the real recipe book grows. No exclusion, so ingredient resolution is not
	// involved (these synthetic recipes carry no ingredients).
	b := rawBook(
		Recipe{ID: "annuale", Categoria: "primo"},                              // no seasons -> always in season
		Recipe{ID: "estiva", Categoria: "primo", Stagioni: []string{"estate"}}, // out of season in winter
		Recipe{ID: "invernale", Categoria: "primo", Stagioni: []string{"inverno"}},
	)
	out := b.Suggest(SuggestFilter{Season: "inverno", Limit: 10})
	if len(out) != 3 {
		t.Fatalf("got %v", ids(out))
	}
	// In-season (annuale, invernale) rank before the out-of-season summer recipe.
	if indexOf(out, "estiva") != len(out)-1 {
		t.Errorf("summer recipe should rank last in winter: %v", ids(out))
	}
	// Book order preserved within the in-season group (annuale before invernale).
	if indexOf(out, "annuale") > indexOf(out, "invernale") {
		t.Errorf("book order not preserved within group: %v", ids(out))
	}
}

func TestSuggestLimit(t *testing.T) {
	b := book(t)
	// Default limit is 6: with fewer recipes than that, all come back; capped at 6.
	wantDefault := len(b.Recipes())
	if wantDefault > defaultSuggestLimit {
		wantDefault = defaultSuggestLimit
	}
	if got := len(b.Suggest(SuggestFilter{})); got != wantDefault {
		t.Errorf("default Suggest returned %d recipes, want %d", got, wantDefault)
	}
	if got := len(b.Suggest(SuggestFilter{Limit: 2})); got != 2 {
		t.Errorf("Suggest with Limit 2 returned %d recipes, want 2", got)
	}
}

func TestSuggestCategoria(t *testing.T) {
	b := book(t)
	out := b.Suggest(SuggestFilter{Categoria: "piatto-unico"})
	if len(out) == 0 {
		t.Fatal("Suggest categoria piatto-unico returned nothing")
	}
	// The invariant is the filter, not an exact count (the book grows): every
	// returned recipe must be of the requested categoria.
	for _, r := range out {
		if r.Categoria != "piatto-unico" {
			t.Errorf("recipe %q categoria = %q, want piatto-unico", r.ID, r.Categoria)
		}
	}
}

func TestGramsUnits(t *testing.T) {
	b := book(t)

	// A spoon of olive oil resolves to 10 g via the misure per-food override.
	g, err := b.grams(Ingredient{Food: "olio_oliva", Qta: 1, Unita: "cucchiaio"})
	if err != nil {
		t.Fatalf("grams(cucchiaio olio): %v", err)
	}
	approx(t, "cucchiaio olio_oliva grams", g, 10)

	// qb contributes 0.
	g, err = b.grams(Ingredient{Food: "olio_oliva", Qta: 5, Unita: "qb"})
	if err != nil {
		t.Fatalf("grams(qb): %v", err)
	}
	if g != 0 {
		t.Errorf("qb grams = %v, want 0", g)
	}

	// pz on a food WITH unit_grams works (banana = 118 g).
	g, err = b.grams(Ingredient{Food: "banana", Qta: 1, Unita: "pz"})
	if err != nil {
		t.Fatalf("grams(pz banana): %v", err)
	}
	approx(t, "1 pz banana grams", g, 118)

	// pz on a food WITHOUT unit_grams is an error.
	if _, err := b.grams(Ingredient{Food: "olio_oliva", Qta: 1, Unita: "pz"}); err == nil {
		t.Error("grams(pz olio_oliva) should error (no unit_grams)")
	}

	// Unknown unit is an error.
	if _, err := b.grams(Ingredient{Food: "olio_oliva", Qta: 1, Unita: "barile"}); err == nil {
		t.Error("grams(unknown unit) should error")
	}

	// Unknown food is an error.
	if _, err := b.grams(Ingredient{Food: "non_esiste", Qta: 1, Unita: "g"}); err == nil {
		t.Error("grams(unknown food) should error")
	}

	// Plain grams pass through.
	g, err = b.grams(Ingredient{Food: "olio_oliva", Qta: 42, Unita: "g"})
	if err != nil {
		t.Fatalf("grams(g): %v", err)
	}
	if g != 42 {
		t.Errorf("42 g = %v, want 42", g)
	}
}

func indexOf(rs []Recipe, id string) int {
	for i, r := range rs {
		if r.ID == id {
			return i
		}
	}
	return -1
}

func ids(rs []Recipe) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.ID
	}
	return out
}

// rawBook builds an UNVALIDATED Book directly (bypassing Load) to exercise the
// defensive paths against recipes that reference foods missing from the catalog.
func rawBook(recipes ...Recipe) *Book {
	b := &Book{
		recipes:     recipes,
		recipesByID: make(map[string]Recipe, len(recipes)),
		cat:         foods.MustLoad(),
		misure:      map[string]Measure{},
	}
	for _, r := range recipes {
		b.recipesByID[r.ID] = r
	}
	return b
}

func TestRecipeTotalsFlagsMissingFoodAndUnits(t *testing.T) {
	b := rawBook()
	r := Recipe{ID: "x", Porzioni: 1, Ingredienti: []Ingredient{
		{Food: "non_esiste", Qta: 100, Unita: "g"},
		{Food: "banana", Qta: 100, Unita: "g"},
		{Food: "olio_oliva", Qta: 5, Unita: "qb"},
		{Food: "olio_oliva", Qta: 1, Unita: "pz"}, // no unit_grams -> ERRORE
	}}
	m, _, flags := b.RecipeTotals(r)
	joined := strings.Join(flags, "|")
	if !contains(flags, "MANCA: non_esiste") {
		t.Errorf("missing-food not flagged: %v", flags)
	}
	if !strings.Contains(joined, "q.b.") || !strings.Contains(joined, "ERRORE") {
		t.Errorf("expected qb note and ERRORE flag: %v", flags)
	}
	// The resolvable banana is still summed (no abort).
	if m.CarbG < 20 || m.CarbG > 24 {
		t.Errorf("banana carbs not summed despite other failures: %.1f", m.CarbG)
	}
}

func TestValidateRejectsUnknownFood(t *testing.T) {
	b := rawBook(Recipe{ID: "bad", Ingredienti: []Ingredient{{Food: "non_esiste", Qta: 1, Unita: "g"}}})
	if err := b.validate(); err == nil {
		t.Fatal("validate accepted a recipe with an unknown food")
	}
}

func TestSuggestFailsClosedOnUnresolvedIngredient(t *testing.T) {
	// A recipe whose ingredient is missing has an UNKNOWN allergen profile; with
	// an exclusion active it must never be suggested (could secretly contain it).
	b := rawBook(Recipe{ID: "ignota", Nome: "Ignota", Categoria: "primo",
		Ingredienti: []Ingredient{{Food: "non_esiste", Qta: 100, Unita: "g"}}})
	got := b.Suggest(SuggestFilter{ExcludeAllergens: []string{"lactose"}})
	for _, r := range got {
		if r.ID == "ignota" {
			t.Error("recipe with an unresolved ingredient was suggested despite an active exclusion (fail-open)")
		}
	}
	// With no exclusion active it may appear (no safety claim to uphold).
	if open := b.Suggest(SuggestFilter{}); len(open) != 1 {
		t.Errorf("without exclusion the recipe should pass: got %d", len(open))
	}
}
