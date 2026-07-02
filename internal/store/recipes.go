// ABOUTME: Firestore recipe/meal store: the runtime-mutable source behind the dashboard + coach.
// ABOUTME: Docs are id-keyed; conversions keep the recipes domain types free of firestore tags.

package store

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/maroffo/cadenza/internal/recipes"
)

const (
	recipesCollection = "recipes"
	mealsCollection   = "meals"
)

// Recipes is the Firestore-backed recipe/meal store. It satisfies
// recipes.Source (ListRecipes/ListMeals) so the engine provider can build its
// Book from it, and adds the write side the dashboard needs.
type Recipes struct {
	client *firestore.Client
}

func NewRecipes(client *firestore.Client) *Recipes {
	return &Recipes{client: client}
}

// --- Firestore document shapes (kept separate so the domain types stay pure) ---

type ingredientDoc struct {
	Food  string  `firestore:"food"`
	Qta   float64 `firestore:"qta"`
	Unita string  `firestore:"unita,omitempty"`
}

type recipeRefDoc struct {
	Ricetta  string  `firestore:"ricetta"`
	Porzioni float64 `firestore:"porzioni,omitempty"`
}

type recipeDoc struct {
	ID          string          `firestore:"id"`
	Nome        string          `firestore:"nome"`
	Categoria   string          `firestore:"categoria"`
	Porzioni    float64         `firestore:"porzioni"`
	Tag         []string        `firestore:"tag,omitempty"`
	Stagioni    []string        `firestore:"stagioni,omitempty"`
	Ingredienti []ingredientDoc `firestore:"ingredienti"`
	Fonte       string          `firestore:"fonte,omitempty"`
	Personale   bool            `firestore:"personale,omitempty"`
	UpdatedAt   time.Time       `firestore:"updated_at"`
}

type mealDoc struct {
	ID        string          `firestore:"id"`
	Nome      string          `firestore:"nome"`
	Tipo      string          `firestore:"tipo"`
	Ricette   []recipeRefDoc  `firestore:"ricette,omitempty"`
	Cibi      []ingredientDoc `firestore:"cibi,omitempty"`
	Fonte     string          `firestore:"fonte,omitempty"`
	UpdatedAt time.Time       `firestore:"updated_at"`
}

// --- conversions ---

func toIngredientDocs(in []recipes.Ingredient) []ingredientDoc {
	out := make([]ingredientDoc, len(in))
	for i, ing := range in {
		out[i] = ingredientDoc{Food: ing.Food, Qta: ing.Qta, Unita: ing.Unita}
	}
	return out
}

func fromIngredientDocs(in []ingredientDoc) []recipes.Ingredient {
	out := make([]recipes.Ingredient, len(in))
	for i, ing := range in {
		out[i] = recipes.Ingredient{Food: ing.Food, Qta: ing.Qta, Unita: ing.Unita}
	}
	return out
}

func toRecipeDoc(r recipes.Recipe) recipeDoc {
	return recipeDoc{
		ID: r.ID, Nome: r.Nome, Categoria: r.Categoria, Porzioni: r.Porzioni,
		Tag: r.Tag, Stagioni: r.Stagioni, Ingredienti: toIngredientDocs(r.Ingredienti),
		Fonte: r.Fonte, Personale: r.Personale, UpdatedAt: time.Now().UTC(),
	}
}

func fromRecipeDoc(d recipeDoc) recipes.Recipe {
	return recipes.Recipe{
		ID: d.ID, Nome: d.Nome, Categoria: d.Categoria, Porzioni: d.Porzioni,
		Tag: d.Tag, Stagioni: d.Stagioni, Ingredienti: fromIngredientDocs(d.Ingredienti),
		Fonte: d.Fonte, Personale: d.Personale,
	}
}

func toMealDoc(m recipes.Meal) mealDoc {
	refs := make([]recipeRefDoc, len(m.Ricette))
	for i, ref := range m.Ricette {
		refs[i] = recipeRefDoc{Ricetta: ref.Ricetta, Porzioni: ref.Porzioni}
	}
	return mealDoc{
		ID: m.ID, Nome: m.Nome, Tipo: m.Tipo, Ricette: refs,
		Cibi: toIngredientDocs(m.Cibi), Fonte: m.Fonte, UpdatedAt: time.Now().UTC(),
	}
}

func fromMealDoc(d mealDoc) recipes.Meal {
	refs := make([]recipes.RecipeRef, len(d.Ricette))
	for i, ref := range d.Ricette {
		refs[i] = recipes.RecipeRef{Ricetta: ref.Ricetta, Porzioni: ref.Porzioni}
	}
	return recipes.Meal{
		ID: d.ID, Nome: d.Nome, Tipo: d.Tipo, Ricette: refs,
		Cibi: fromIngredientDocs(d.Cibi), Fonte: d.Fonte,
	}
}

// --- recipes ---

// ListRecipes returns every recipe, ordered by id for a stable, deterministic
// book order (the engine re-ranks by season anyway).
func (s *Recipes) ListRecipes(ctx context.Context) ([]recipes.Recipe, error) {
	docs, err := s.client.Collection(recipesCollection).OrderBy("id", firestore.Asc).Documents(ctx).GetAll()
	if err != nil {
		return nil, fmt.Errorf("recipes list: %w", err)
	}
	out := make([]recipes.Recipe, 0, len(docs))
	for _, d := range docs {
		var rd recipeDoc
		if err := d.DataTo(&rd); err != nil {
			return nil, fmt.Errorf("recipes decode %s: %w", d.Ref.ID, err)
		}
		out = append(out, fromRecipeDoc(rd))
	}
	return out, nil
}

// GetRecipe returns one recipe by id, or (nil, nil) when it does not exist.
func (s *Recipes) GetRecipe(ctx context.Context, id string) (*recipes.Recipe, error) {
	snap, err := s.client.Collection(recipesCollection).Doc(id).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("recipe get %q: %w", id, err)
	}
	var rd recipeDoc
	if err := snap.DataTo(&rd); err != nil {
		return nil, fmt.Errorf("recipe decode %q: %w", id, err)
	}
	r := fromRecipeDoc(rd)
	return &r, nil
}

// SaveRecipe writes a recipe by id (create or overwrite), stamping updated_at.
// Idempotent: the same recipe id always maps to the same document.
func (s *Recipes) SaveRecipe(ctx context.Context, r recipes.Recipe) error {
	if r.ID == "" {
		return fmt.Errorf("recipe save: empty id")
	}
	if _, err := s.client.Collection(recipesCollection).Doc(r.ID).Set(ctx, toRecipeDoc(r)); err != nil {
		return fmt.Errorf("recipe save %q: %w", r.ID, err)
	}
	return nil
}

// DeleteRecipe removes a recipe by id (idempotent: deleting a missing id is ok).
func (s *Recipes) DeleteRecipe(ctx context.Context, id string) error {
	if _, err := s.client.Collection(recipesCollection).Doc(id).Delete(ctx); err != nil {
		return fmt.Errorf("recipe delete %q: %w", id, err)
	}
	return nil
}

// --- meals ---

// ListMeals returns every meal, ordered by id.
func (s *Recipes) ListMeals(ctx context.Context) ([]recipes.Meal, error) {
	docs, err := s.client.Collection(mealsCollection).OrderBy("id", firestore.Asc).Documents(ctx).GetAll()
	if err != nil {
		return nil, fmt.Errorf("meals list: %w", err)
	}
	out := make([]recipes.Meal, 0, len(docs))
	for _, d := range docs {
		var md mealDoc
		if err := d.DataTo(&md); err != nil {
			return nil, fmt.Errorf("meals decode %s: %w", d.Ref.ID, err)
		}
		out = append(out, fromMealDoc(md))
	}
	return out, nil
}

// SaveMeal writes a meal by id (create or overwrite), stamping updated_at.
func (s *Recipes) SaveMeal(ctx context.Context, m recipes.Meal) error {
	if m.ID == "" {
		return fmt.Errorf("meal save: empty id")
	}
	if _, err := s.client.Collection(mealsCollection).Doc(m.ID).Set(ctx, toMealDoc(m)); err != nil {
		return fmt.Errorf("meal save %q: %w", m.ID, err)
	}
	return nil
}
