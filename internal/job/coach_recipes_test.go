// ABOUTME: Tests for the suggest_recipe tool: catalog gating, hard allergen exclusion, season.
// ABOUTME: The lactose exclusion and seasonal ranking are the engine's job, asserted here end to end.

package job

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/maroffo/cadenza/internal/fakes"
	"github.com/maroffo/cadenza/internal/foods"
	"github.com/maroffo/cadenza/internal/recipes"
)

func recipeBook(t *testing.T) *recipes.Book {
	t.Helper()
	return recipes.MustLoad(foods.MustLoad())
}

func parseSuggest(t *testing.T, reply string) []struct {
	ID          string             `json:"id"`
	Allergeni   []string           `json:"allergeni"`
	PerPorzione map[string]float64 `json:"per_porzione"`
} {
	t.Helper()
	i := strings.Index(reply, "{")
	var out struct {
		Ricette []struct {
			ID          string             `json:"id"`
			Allergeni   []string           `json:"allergeni"`
			PerPorzione map[string]float64 `json:"per_porzione"`
		} `json:"ricette"`
	}
	if err := json.Unmarshal([]byte(reply[i:]), &out); err != nil {
		t.Fatalf("bad JSON: %v\n%s", err, reply)
	}
	return out.Ricette
}

func TestSuggestRecipe_ExcludesLactoseHard(t *testing.T) {
	c := &Coach{
		Recipes:              recipeBook(t),
		MealExcludeAllergens: []string{"lactose"},
		Now:                  fixedNow,
		TZ:                   testTZ,
	}
	// FAMILY meals (a shared course) must never carry lactose.
	reply, err := c.suggestRecipe(context.Background(), []byte(`{"categoria":"primo"}`))
	if err != nil {
		t.Fatalf("suggestRecipe: %v", err)
	}
	rs := parseSuggest(t, reply)
	if len(rs) == 0 {
		t.Fatal("no family primi suggested")
	}
	for _, r := range rs {
		for _, a := range r.Allergeni {
			if a == "lactose" {
				t.Errorf("family recipe %s leaked lactose despite the exclusion", r.ID)
			}
		}
		if r.PerPorzione["kcal"] <= 0 {
			t.Errorf("recipe %s has no per-serving kcal", r.ID)
		}
	}

	// The athlete's PERSONAL breakfast (lactose) is exempt from the family
	// exclusion and must still be offered when he asks for it.
	repC, _ := c.suggestRecipe(context.Background(), []byte(`{"categoria":"colazione"}`))
	found := false
	for _, r := range parseSuggest(t, repC) {
		if r.ID == "colazione-avena-chia-yogurt" {
			found = true
		}
	}
	if !found {
		t.Error("personal lactose breakfast should be suggested to the athlete, not filtered out")
	}
}

func TestSuggestRecipe_SeasonFromClock(t *testing.T) {
	// fixedNow is in June -> estate; the summer recipes should be in season.
	c := &Coach{Recipes: recipeBook(t), MealExcludeAllergens: []string{"lactose"}, Now: fixedNow, TZ: testTZ}
	reply, _ := c.suggestRecipe(context.Background(), []byte(`{}`))
	if !strings.Contains(reply, `"stagione":"estate"`) {
		t.Errorf("expected estate in June, got: %s", reply)
	}
}

func TestSuggestRecipe_WinterDeprioritisesSummer(t *testing.T) {
	jan := func() time.Time { return time.Date(2026, 1, 15, 9, 0, 0, 0, testTZ) }
	c := &Coach{Recipes: recipeBook(t), MealExcludeAllergens: []string{"lactose"}, Now: jan, TZ: testTZ}
	reply, _ := c.suggestRecipe(context.Background(), []byte(`{}`))
	if !strings.Contains(reply, `"stagione":"inverno"`) {
		t.Errorf("expected inverno in January, got: %s", reply)
	}
}

func TestSuggestRecipeToolGatedByBook(t *testing.T) {
	const marker = "ricettario di FAMIGLIA"
	llm := fakes.NewAnthropic(fakes.Text{S: "ok"})
	defer llm.Close()
	c, _, _, _, _, _ := newCoach(t, llm)
	if err := c.Converse(context.Background(), "cosa cucino?"); err != nil {
		t.Fatalf("Converse: %v", err)
	}
	if strings.Contains(string(llm.Requests[0].Raw), marker) {
		t.Error("suggest_recipe registered without a recipe book")
	}

	llm2 := fakes.NewAnthropic(fakes.Text{S: "ok"})
	defer llm2.Close()
	c2, _, _, _, _, _ := newCoach(t, llm2)
	c2.Recipes = recipeBook(t)
	c2.MealExcludeAllergens = []string{"lactose"}
	if err := c2.Converse(context.Background(), "cosa cucino?"); err != nil {
		t.Fatalf("Converse: %v", err)
	}
	if !strings.Contains(string(llm2.Requests[0].Raw), marker) {
		t.Error("suggest_recipe missing with a recipe book wired")
	}
}

func TestSuggestRecipe_EmptyResult(t *testing.T) {
	c := &Coach{Recipes: recipeBook(t), MealExcludeAllergens: []string{"lactose"}, Now: fixedNow, TZ: testTZ}
	reply, err := c.suggestRecipe(context.Background(), []byte(`{"categoria":"categoria_inesistente"}`))
	if err != nil {
		t.Fatalf("suggestRecipe: %v", err)
	}
	if !strings.Contains(reply, "Nessuna ricetta adatta") {
		t.Errorf("expected empty-result message, got: %q", reply)
	}
}

func TestListRecipes_ReturnsWholeBookIncludingOffSeason(t *testing.T) {
	c := &Coach{Recipes: recipeBook(t), MealExcludeAllergens: []string{"lactose"}, Now: fixedNow, TZ: testTZ}
	reply, err := c.listRecipes(context.Background(), []byte(`{}`))
	if err != nil {
		t.Fatalf("listRecipes: %v", err)
	}
	i := strings.Index(reply, "{")
	var out struct {
		Totale  int `json:"totale"`
		Ricette []struct {
			ID string `json:"id"`
		} `json:"ricette"`
	}
	if err := json.Unmarshal([]byte(reply[i:]), &out); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if out.Totale < 100 {
		t.Errorf("expected the whole book (>=100), got %d", out.Totale)
	}
	seen := map[string]bool{}
	for _, r := range out.Ricette {
		seen[r.ID] = true
	}
	// off-season / non-summer dishes must appear in a full listing
	for _, id := range []string{"riso-cantonese", "riso-cantonese-veg", "lasagne", "minestrone"} {
		if !seen[id] {
			t.Errorf("full listing is missing %q", id)
		}
	}
}
