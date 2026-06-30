// ABOUTME: Recipe/meal engine: foods -> recipes -> meals, macros + allergens DERIVED.
// ABOUTME: Embeds recipes.yaml, resolves units to grams via internal/foods, never stores macros.

package recipes

import (
	_ "embed"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/maroffo/cadenza/internal/foods"
	"gopkg.in/yaml.v3"
)

//go:embed recipes.yaml
var recipesYAML []byte

// Ingredient is one food plus a quantity, either inside a recipe or attached
// directly to a meal. Unita is the unit the quantity is expressed in
// (g|ml|pz|qb or a key from the misure table); empty means grams.
type Ingredient struct {
	Food  string  `yaml:"food"`
	Qta   float64 `yaml:"qta"`
	Unita string  `yaml:"unita"`
}

// Recipe is N foods that together make Porzioni servings. Macros and allergens
// are never stored here: they are derived from the bound food catalog on demand.
type Recipe struct {
	ID          string       `yaml:"id"`
	Nome        string       `yaml:"nome"`
	Categoria   string       `yaml:"categoria"`
	Porzioni    float64      `yaml:"porzioni"`
	Tag         []string     `yaml:"tag"`
	Stagioni    []string     `yaml:"stagioni"`
	Ingredienti []Ingredient `yaml:"ingredienti"`
	Fonte       string       `yaml:"fonte"`
}

// RecipeRef cites a recipe inside a meal and how many of its servings enter the
// meal. Porzioni == 0 means "the whole recipe" (its own Porzioni).
type RecipeRef struct {
	Ricetta  string  `yaml:"ricetta"`
	Porzioni float64 `yaml:"porzioni"`
}

// Meal is a composition of N recipe servings plus M directly attached foods.
type Meal struct {
	ID      string       `yaml:"id"`
	Nome    string       `yaml:"nome"`
	Tipo    string       `yaml:"tipo"`
	Ricette []RecipeRef  `yaml:"ricette"`
	Cibi    []Ingredient `yaml:"cibi"`
	Fonte   string       `yaml:"fonte"`
}

// Measure resolves a non-gram unit (cucchiaio, spicchio, ...) to grams. PerFood
// overrides DefaultG for specific foods (a spoon of oil weighs less than a spoon
// of honey).
type Measure struct {
	DefaultG float64            `yaml:"default_g"`
	PerFood  map[string]float64 `yaml:"per_food"`
}

// bookFile mirrors the on-disk recipes.yaml wrapper.
type bookFile struct {
	Misure  map[string]Measure `yaml:"misure"`
	Ricette []Recipe           `yaml:"ricette"`
	Pasti   []Meal             `yaml:"pasti"`
}

// Book is the parsed recipe collection bound to a food catalog. It owns the
// id indexes and all macro/allergen derivation.
type Book struct {
	misure      map[string]Measure
	recipes     []Recipe
	recipesByID map[string]Recipe
	meals       []Meal
	mealsByID   map[string]Meal
	cat         *foods.Catalog
}

// Load parses the embedded recipes.yaml, builds the id indexes, and binds the
// food catalog used for all derivations.
func Load(cat *foods.Catalog) (*Book, error) {
	if cat == nil {
		return nil, fmt.Errorf("recipes: nil catalog")
	}
	var bf bookFile
	if err := yaml.Unmarshal(recipesYAML, &bf); err != nil {
		return nil, fmt.Errorf("recipes: parse recipes.yaml: %w", err)
	}
	b := &Book{
		misure:      bf.Misure,
		recipes:     bf.Ricette,
		recipesByID: make(map[string]Recipe, len(bf.Ricette)),
		meals:       bf.Pasti,
		mealsByID:   make(map[string]Meal, len(bf.Pasti)),
		cat:         cat,
	}
	for _, r := range bf.Ricette {
		b.recipesByID[r.ID] = r
	}
	for _, m := range bf.Pasti {
		b.mealsByID[m.ID] = m
	}
	// Fail closed at load: every recipe/meal reference must resolve against the
	// catalog, so the allergen filter can never silently fail open on a missing
	// ingredient (a renamed/removed food would otherwise hide its lactose tag).
	if err := b.validate(); err != nil {
		return nil, err
	}
	return b, nil
}

// validate reports any recipe ingredient, meal food, or meal recipe-reference
// that does not resolve against the bound catalog / recipe set.
func (b *Book) validate() error {
	var problems []string
	for _, r := range b.recipes {
		for _, ing := range r.Ingredienti {
			if _, ok := b.cat.ByID(ing.Food); !ok {
				problems = append(problems, fmt.Sprintf("ricetta %q: alimento sconosciuto %q", r.ID, ing.Food))
			}
		}
	}
	for _, mm := range b.meals {
		for _, ref := range mm.Ricette {
			if _, ok := b.recipesByID[ref.Ricetta]; !ok {
				problems = append(problems, fmt.Sprintf("pasto %q: ricetta sconosciuta %q", mm.ID, ref.Ricetta))
			}
		}
		for _, ing := range mm.Cibi {
			if _, ok := b.cat.ByID(ing.Food); !ok {
				problems = append(problems, fmt.Sprintf("pasto %q: alimento sconosciuto %q", mm.ID, ing.Food))
			}
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("recipes: %d riferimenti non risolti: %s", len(problems), strings.Join(problems, "; "))
	}
	return nil
}

// resolves reports whether every ingredient of a recipe exists in the catalog.
// A recipe that does not fully resolve has an UNKNOWN allergen profile.
func (b *Book) resolves(r Recipe) bool {
	for _, ing := range r.Ingredienti {
		if _, ok := b.cat.ByID(ing.Food); !ok {
			return false
		}
	}
	return true
}

// MustLoad is Load but panics on error; intended for main wiring.
func MustLoad(cat *foods.Catalog) *Book {
	b, err := Load(cat)
	if err != nil {
		panic(err)
	}
	return b
}

// grams resolves an ingredient's quantity+unit to grams of edible food. The
// food must exist in the catalog (needed for pz/per-food lookups). Unknown units
// and pz on a weight-only food are errors; qb contributes 0.
func (b *Book) grams(ing Ingredient) (float64, error) {
	food, ok := b.cat.ByID(ing.Food)
	if !ok {
		return 0, fmt.Errorf("recipes: unknown food %q", ing.Food)
	}
	unit := ing.Unita
	if unit == "" {
		unit = "g"
	}
	switch unit {
	case "g", "ml":
		return ing.Qta, nil
	case "qb":
		return 0, nil
	case "pz":
		if food.UnitGrams <= 0 {
			return 0, fmt.Errorf("recipes: %q: unit %q but food has no unit_grams", ing.Food, unit)
		}
		return ing.Qta * food.UnitGrams, nil
	default:
		m, ok := b.misure[unit]
		if !ok {
			return 0, fmt.Errorf("recipes: unknown unit %q", unit)
		}
		if g, ok := m.PerFood[ing.Food]; ok {
			return ing.Qta * g, nil
		}
		return ing.Qta * m.DefaultG, nil
	}
}

// RecipeTotals sums macros over every ingredient of the WHOLE recipe (all its
// porzioni) and unions their allergens. A missing food is flagged ("MANCA: <id>")
// and skipped, never aborting; a qb ingredient adds a "<nome>: q.b." note and 0
// macros. Allergens are returned sorted, distinct, and lowercased.
func (b *Book) RecipeTotals(r Recipe) (m foods.Macros, allergens []string, flags []string) {
	allergenSet := make(map[string]bool)
	for _, ing := range r.Ingredienti {
		food, ok := b.cat.ByID(ing.Food)
		if !ok {
			flags = append(flags, "MANCA: "+ing.Food)
			continue
		}
		g, err := b.grams(ing)
		if err != nil {
			flags = append(flags, fmt.Sprintf("ERRORE: %s: %v", ing.Food, err))
			continue
		}
		if ing.Unita == "qb" {
			flags = append(flags, food.NameIT+": q.b.")
		}
		m = addMacros(m, food.Per(g))
		for _, a := range food.Allergens {
			allergenSet[strings.ToLower(a)] = true
		}
	}
	return m, sortedKeys(allergenSet), flags
}

// RecipePerServing returns the recipe's macros for a single serving (totals
// divided by max(1, Porzioni)) plus its allergens, each macro rounded to one
// decimal. The dropped flag channel is safe for any Load()-validated Book: every
// ingredient resolves, so no MANCA/ERRORE flag can occur and the macros are
// complete (only benign q.b. notes are suppressed).
func (b *Book) RecipePerServing(r Recipe) (foods.Macros, []string) {
	tot, allergens, _ := b.RecipeTotals(r)
	return Split(tot, 1/servings(r.Porzioni)), allergens
}

// MealTotals sums each referenced recipe's per-serving macros times its serving
// count (defaulting to the recipe's own Porzioni when the ref omits it), adds
// every directly attached food, and unions all allergens. Missing recipes/foods
// are flagged and skipped. The result macros are rounded to one decimal; the
// per-serving math runs unrounded to stay faithful to the executable spec.
func (b *Book) MealTotals(m Meal) (foods.Macros, []string, []string) {
	var tot foods.Macros
	allergenSet := make(map[string]bool)
	var flags []string

	for _, ref := range m.Ricette {
		r, ok := b.recipesByID[ref.Ricetta]
		if !ok {
			flags = append(flags, "MANCA ricetta: "+ref.Ricetta)
			continue
		}
		rt, ra, rf := b.RecipeTotals(r)
		n := ref.Porzioni
		if n == 0 {
			n = r.Porzioni
		}
		tot = addMacros(tot, scaleRaw(rt, n/servings(r.Porzioni)))
		for _, a := range ra {
			allergenSet[a] = true
		}
		flags = append(flags, rf...)
	}

	for _, ing := range m.Cibi {
		food, ok := b.cat.ByID(ing.Food)
		if !ok {
			flags = append(flags, "MANCA cibo: "+ing.Food)
			continue
		}
		g, err := b.grams(ing)
		if err != nil {
			flags = append(flags, fmt.Sprintf("ERRORE: %s: %v", ing.Food, err))
			continue
		}
		tot = addMacros(tot, food.Per(g))
		for _, a := range food.Allergens {
			allergenSet[strings.ToLower(a)] = true
		}
	}

	return Split(tot, 1), sortedKeys(allergenSet), flags
}

// ByID returns the recipe with the given id and whether it exists.
func (b *Book) ByID(id string) (Recipe, bool) {
	r, ok := b.recipesByID[id]
	return r, ok
}

// MealByID returns the meal with the given id and whether it exists.
func (b *Book) MealByID(id string) (Meal, bool) {
	m, ok := b.mealsByID[id]
	return m, ok
}

// Recipes returns the recipes in book order. The slice is read-only.
func (b *Book) Recipes() []Recipe { return b.recipes }

// Meals returns the meals in book order. The slice is read-only.
func (b *Book) Meals() []Meal { return b.meals }

// Season maps a date to its Italian season: inverno dec-feb, primavera mar-may,
// estate jun-aug, autunno sep-nov.
func Season(t time.Time) string {
	switch t.Month() {
	case time.December, time.January, time.February:
		return "inverno"
	case time.March, time.April, time.May:
		return "primavera"
	case time.June, time.July, time.August:
		return "estate"
	default:
		return "autunno"
	}
}

// InSeason reports whether a recipe fits the given season. A recipe with no
// declared seasons fits every season (all year round).
func InSeason(r Recipe, season string) bool {
	if len(r.Stagioni) == 0 {
		return true
	}
	for _, s := range r.Stagioni {
		if s == season {
			return true
		}
	}
	return false
}

// SuggestFilter constrains and ranks recipe suggestions. ExcludeAllergens is a
// HARD filter; Season is a SOFT ranking (in-season first); Categoria is an exact
// match. Tipo is reserved for future meal-type filtering and is currently unused.
type SuggestFilter struct {
	Tipo             string
	Categoria        string
	ExcludeAllergens []string
	Season           string
	Limit            int
}

const (
	defaultSuggestLimit = 6
	maxSuggestLimit     = 20
)

// Suggest returns recipes matching the filter. It hard-excludes any recipe whose
// derived allergens intersect ExcludeAllergens (case-insensitive), keeps only the
// requested Categoria when set, then stably ranks in-season recipes first when a
// Season is given (book order preserved within each group). Limit defaults to 6
// and is capped at 20.
func (b *Book) Suggest(f SuggestFilter) []Recipe {
	exclude := make(map[string]bool, len(f.ExcludeAllergens))
	for _, a := range f.ExcludeAllergens {
		exclude[strings.ToLower(strings.TrimSpace(a))] = true
	}

	var out []Recipe
	for _, r := range b.recipes {
		if f.Categoria != "" && r.Categoria != f.Categoria {
			continue
		}
		if len(exclude) > 0 {
			// Fail closed: a recipe whose ingredients don't fully resolve has an
			// unknown allergen profile and must NOT be offered while an exclusion
			// is active. Load() already guarantees this for the embedded book;
			// this defends a hand-built Book too.
			if !b.resolves(r) || excludes(b.recipeAllergens(r), exclude) {
				continue
			}
		}
		out = append(out, r)
	}

	if f.Season != "" {
		sort.SliceStable(out, func(i, j int) bool {
			return seasonRank(out[i], f.Season) < seasonRank(out[j], f.Season)
		})
	}

	limit := f.Limit
	if limit <= 0 {
		limit = defaultSuggestLimit
	}
	if limit > maxSuggestLimit {
		limit = maxSuggestLimit
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Split scales every macro field by share and rounds each result to one decimal.
// Used for per-serving (share = 1/porzioni) and family per-person splits.
func Split(m foods.Macros, share float64) foods.Macros {
	return foods.Macros{
		Kcal:       round1(m.Kcal * share),
		CarbG:      round1(m.CarbG * share),
		ProteinG:   round1(m.ProteinG * share),
		FatG:       round1(m.FatG * share),
		FiberG:     round1(m.FiberG * share),
		SugarG:     round1(m.SugarG * share),
		SodiumMg:   round1(m.SodiumMg * share),
		CaffeineMg: round1(m.CaffeineMg * share),
	}
}

// recipeAllergens returns just the derived allergen set for a recipe.
func (b *Book) recipeAllergens(r Recipe) []string {
	_, allergens, _ := b.RecipeTotals(r)
	return allergens
}

// seasonRank ranks in-season recipes ahead (0) of out-of-season ones (1).
func seasonRank(r Recipe, season string) int {
	if InSeason(r, season) {
		return 0
	}
	return 1
}

// excludes reports whether any allergen (already lowercased) is in the exclude
// set.
func excludes(allergens []string, exclude map[string]bool) bool {
	for _, a := range allergens {
		if exclude[a] {
			return true
		}
	}
	return false
}

// servings returns a safe divisor: porzioni clamped to a minimum of 1.
func servings(porzioni float64) float64 {
	if porzioni < 1 {
		return 1
	}
	return porzioni
}

// addMacros returns the field-wise sum of two macro breakdowns.
func addMacros(a, b foods.Macros) foods.Macros {
	return foods.Macros{
		Kcal:       a.Kcal + b.Kcal,
		CarbG:      a.CarbG + b.CarbG,
		ProteinG:   a.ProteinG + b.ProteinG,
		FatG:       a.FatG + b.FatG,
		FiberG:     a.FiberG + b.FiberG,
		SugarG:     a.SugarG + b.SugarG,
		SodiumMg:   a.SodiumMg + b.SodiumMg,
		CaffeineMg: a.CaffeineMg + b.CaffeineMg,
	}
}

// scaleRaw scales every field by f without rounding, for intermediate meal math.
func scaleRaw(m foods.Macros, f float64) foods.Macros {
	return foods.Macros{
		Kcal:       m.Kcal * f,
		CarbG:      m.CarbG * f,
		ProteinG:   m.ProteinG * f,
		FatG:       m.FatG * f,
		FiberG:     m.FiberG * f,
		SugarG:     m.SugarG * f,
		SodiumMg:   m.SodiumMg * f,
		CaffeineMg: m.CaffeineMg * f,
	}
}

// round1 rounds x to one decimal place.
func round1(x float64) float64 {
	return math.Round(x*10) / 10
}

// sortedKeys returns the non-empty keys of m, sorted ascending.
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		if k != "" {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
