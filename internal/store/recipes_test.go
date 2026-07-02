// ABOUTME: Emulator round-trip tests for the Firestore recipe/meal store.
// ABOUTME: Skips unless FIRESTORE_EMULATOR_HOST is set; REQUIRE_EMULATOR=1 makes skips fatal.

package store

import (
	"context"
	"testing"
	"time"

	"github.com/maroffo/cadenza/internal/recipes"
)

func TestRecipes_SaveGetListDeleteRoundTrip(t *testing.T) {
	client := emulatorClient(t)
	s := NewRecipes(client)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Distinct ids so a shared local emulator doesn't collide with other tests.
	rA := recipes.Recipe{
		ID: "zzt-recipe-a", Nome: "Test A", Categoria: "primo", Porzioni: 2,
		Tag: []string{"veloce"}, Stagioni: []string{"estate"},
		Ingredienti: []recipes.Ingredient{{Food: "pasta_secca", Qta: 200, Unita: "g"}},
		Fonte:       "test",
	}
	rB := recipes.Recipe{
		ID: "zzt-recipe-b", Nome: "Test B", Categoria: "colazione", Porzioni: 1,
		Personale:   true,
		Ingredienti: []recipes.Ingredient{{Food: "banana", Qta: 1, Unita: "pz"}},
	}
	m := recipes.Meal{
		ID: "zzt-meal-a", Nome: "Test meal", Tipo: "pranzo",
		Ricette: []recipes.RecipeRef{{Ricetta: "zzt-recipe-a", Porzioni: 2}},
		Cibi:    []recipes.Ingredient{{Food: "pomodoro", Qta: 100, Unita: "g"}},
	}
	t.Cleanup(func() {
		_ = s.DeleteRecipe(context.Background(), rA.ID)
		_ = s.DeleteRecipe(context.Background(), rB.ID)
		_, _ = client.Collection(mealsCollection).Doc(m.ID).Delete(context.Background())
	})

	for _, r := range []recipes.Recipe{rA, rB} {
		if err := s.SaveRecipe(ctx, r); err != nil {
			t.Fatalf("SaveRecipe %s: %v", r.ID, err)
		}
	}
	if err := s.SaveMeal(ctx, m); err != nil {
		t.Fatalf("SaveMeal: %v", err)
	}

	// Get round-trips every field we care about.
	got, err := s.GetRecipe(ctx, rB.ID)
	if err != nil || got == nil {
		t.Fatalf("GetRecipe(%s) = %v, %v", rB.ID, got, err)
	}
	if !got.Personale || got.Categoria != "colazione" ||
		len(got.Ingredienti) != 1 || got.Ingredienti[0].Food != "banana" ||
		got.Ingredienti[0].Unita != "pz" {
		t.Errorf("recipe did not round-trip: %+v", *got)
	}

	// Missing recipe is (nil, nil), not an error.
	if r, err := s.GetRecipe(ctx, "does-not-exist-xyz"); err != nil || r != nil {
		t.Errorf("GetRecipe(missing) = %v, %v; want nil, nil", r, err)
	}

	// List contains both test recipes, ordered by id (a before b).
	list, err := s.ListRecipes(ctx)
	if err != nil {
		t.Fatalf("ListRecipes: %v", err)
	}
	ia, ib := indexOfRecipe(list, rA.ID), indexOfRecipe(list, rB.ID)
	if ia < 0 || ib < 0 {
		t.Fatalf("saved recipes missing from list: a=%d b=%d", ia, ib)
	}
	if ia > ib {
		t.Errorf("ListRecipes not ordered by id: a at %d, b at %d", ia, ib)
	}

	meals, err := s.ListMeals(ctx)
	if err != nil {
		t.Fatalf("ListMeals: %v", err)
	}
	if indexOfMeal(meals, m.ID) < 0 {
		t.Errorf("saved meal missing from list")
	}

	// Delete removes it from the listing.
	if err := s.DeleteRecipe(ctx, rB.ID); err != nil {
		t.Fatalf("DeleteRecipe: %v", err)
	}
	if list, _ := s.ListRecipes(ctx); indexOfRecipe(list, rB.ID) >= 0 {
		t.Errorf("recipe still listed after delete")
	}
}

func indexOfRecipe(rs []recipes.Recipe, id string) int {
	for i, r := range rs {
		if r.ID == id {
			return i
		}
	}
	return -1
}

func indexOfMeal(ms []recipes.Meal, id string) int {
	for i, m := range ms {
		if m.ID == id {
			return i
		}
	}
	return -1
}
