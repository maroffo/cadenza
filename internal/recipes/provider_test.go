// ABOUTME: Tests for the Firestore Book provider: caching, invalidation, fallback, quarantine.
// ABOUTME: Uses a fake Source; the embedded seed is the real curated book (fallback assertions).

package recipes

import (
	"context"
	"errors"
	"testing"

	"github.com/maroffo/cadenza/internal/foods"
)

type fakeSource struct {
	recipes []Recipe
	meals   []Meal
	err     error
	calls   int // ListRecipes invocations, to observe caching
}

func (f *fakeSource) ListRecipes(context.Context) ([]Recipe, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.recipes, nil
}

func (f *fakeSource) ListMeals(context.Context) ([]Meal, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.meals, nil
}

func goodRecipe(id string) Recipe {
	return Recipe{ID: id, Nome: id, Categoria: "primo", Porzioni: 1,
		Ingredienti: []Ingredient{{Food: "banana", Qta: 1, Unita: "pz"}}}
}

func TestProvider_CachesUntilInvalidated(t *testing.T) {
	cat := foods.MustLoad()
	src := &fakeSource{recipes: []Recipe{goodRecipe("fs-a")}}
	p := NewProvider(src, cat)
	ctx := context.Background()

	b1, _ := p.Book(ctx)
	if _, ok := b1.ByID("fs-a"); !ok {
		t.Fatal("first build missing fs-a")
	}
	if _, _ = p.Book(ctx); src.calls != 1 {
		t.Fatalf("expected a cache hit (calls=1), got %d", src.calls)
	}

	// A write happened: update the source and invalidate.
	src.recipes = []Recipe{goodRecipe("fs-a"), goodRecipe("fs-b")}
	p.Invalidate()
	b3, _ := p.Book(ctx)
	if src.calls != 2 {
		t.Fatalf("expected a rebuild after invalidate (calls=2), got %d", src.calls)
	}
	if _, ok := b3.ByID("fs-b"); !ok {
		t.Error("edit not visible after invalidate")
	}
}

func TestProvider_FallsBackToEmbedOnSourceError(t *testing.T) {
	cat := foods.MustLoad()
	src := &fakeSource{err: errors.New("firestore down")}
	p := NewProvider(src, cat)
	ctx := context.Background()

	b, err := p.Book(ctx)
	if err != nil {
		t.Fatalf("Book must never error to the caller, got %v", err)
	}
	if _, ok := b.ByID("insalata-riso"); !ok {
		t.Error("expected the embedded seed as fallback (known recipe missing)")
	}
	// The error path must NOT cache, so it keeps retrying the source.
	_, _ = p.Book(ctx)
	if src.calls < 2 {
		t.Errorf("error path cached the fallback (calls=%d, want >=2)", src.calls)
	}
}

func TestProvider_FallsBackToEmbedWhenEmpty(t *testing.T) {
	cat := foods.MustLoad()
	p := NewProvider(&fakeSource{recipes: nil}, cat)
	b, _ := p.Book(context.Background())
	if len(b.Recipes()) == 0 {
		t.Fatal("empty firestore should fall back to the non-empty embedded seed")
	}
	if _, ok := b.ByID("insalata-riso"); !ok {
		t.Error("fallback book missing a known embedded recipe")
	}
}

func TestProvider_QuarantinesBadRecipe(t *testing.T) {
	cat := foods.MustLoad()
	bad := Recipe{ID: "fs-bad", Categoria: "primo", Porzioni: 1,
		Ingredienti: []Ingredient{{Food: "non_esiste", Qta: 1, Unita: "g"}}}
	src := &fakeSource{recipes: []Recipe{goodRecipe("fs-a"), bad}}
	b, _ := NewProvider(src, cat).Book(context.Background())
	if _, ok := b.ByID("fs-a"); !ok {
		t.Error("good recipe dropped")
	}
	if _, ok := b.ByID("fs-bad"); ok {
		t.Error("recipe with an unknown ingredient was NOT quarantined (allergen fail-open risk)")
	}
}
