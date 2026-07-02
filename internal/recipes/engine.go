// ABOUTME: Recipe/meal engine: foods -> recipes -> meals, macros + allergens DERIVED.
// ABOUTME: Embeds recipes.yaml, resolves units to grams via internal/foods, never stores macros.

package recipes

import (
	"context"
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
	// Personale marks a recipe as the athlete's own (not a shared family meal),
	// so the family allergen exclusion does not apply to it: he tolerates what
	// the family cannot (e.g. his lactose breakfast).
	Personale bool `yaml:"personale"`
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

// Load parses the embedded recipes.yaml and builds the book, binding the food
// catalog used for all derivations. The embedded book is CURATED, so any
// unresolved reference is a build-time bug: Load fails loud (strict), it does
// not quarantine. Firestore-sourced books quarantine instead (see buildBook,
// used by the provider) so one bad row can never take the coach's recipes down.
func Load(cat *foods.Catalog) (*Book, error) {
	if cat == nil {
		return nil, fmt.Errorf("recipes: nil catalog")
	}
	bf, err := parseEmbedded()
	if err != nil {
		return nil, err
	}
	b, problems := buildBook(bf.Misure, bf.Ricette, bf.Pasti, cat)
	if len(problems) > 0 {
		return nil, fmt.Errorf("recipes: %d riferimenti non risolti nel libro embedded: %s",
			len(problems), strings.Join(problems, "; "))
	}
	return b, nil
}

// parseEmbedded unmarshals the embedded recipes.yaml into its wrapper.
func parseEmbedded() (bookFile, error) {
	var bf bookFile
	if err := yaml.Unmarshal(recipesYAML, &bf); err != nil {
		return bookFile{}, fmt.Errorf("recipes: parse recipes.yaml: %w", err)
	}
	return bf, nil
}

// buildBook assembles a Book from any source (embedded YAML or Firestore),
// QUARANTINING every recipe/meal whose references don't resolve: the returned
// Book contains only fully-resolvable entries, and problems lists what was
// dropped and why. This keeps the allergen filter fail-closed (a recipe whose
// allergens can't be derived is simply absent, never suggested) without letting
// a single malformed row abort the whole book. cat must be non-nil.
func buildBook(misure map[string]Measure, rs []Recipe, ms []Meal, cat *foods.Catalog) (*Book, []string) {
	var problems []string
	validRecipes := make([]Recipe, 0, len(rs))
	recipesByID := make(map[string]Recipe, len(rs))
	for _, r := range rs {
		if bad := recipeUnresolved(cat, r); len(bad) > 0 {
			problems = append(problems, fmt.Sprintf("ricetta %q: alimento sconosciuto %s", r.ID, strings.Join(bad, ", ")))
			continue
		}
		validRecipes = append(validRecipes, r)
		recipesByID[r.ID] = r
	}
	validMeals := make([]Meal, 0, len(ms))
	mealsByID := make(map[string]Meal, len(ms))
	for _, mm := range ms {
		if bad := mealUnresolved(cat, recipesByID, mm); len(bad) > 0 {
			problems = append(problems, fmt.Sprintf("pasto %q: riferimenti non risolti %s", mm.ID, strings.Join(bad, ", ")))
			continue
		}
		validMeals = append(validMeals, mm)
		mealsByID[mm.ID] = mm
	}
	return &Book{
		misure:      misure,
		recipes:     validRecipes,
		recipesByID: recipesByID,
		meals:       validMeals,
		mealsByID:   mealsByID,
		cat:         cat,
	}, problems
}

// recipeUnresolved returns the ids of a recipe's ingredients that don't exist in
// the catalog (empty = fully resolvable, so its allergen profile is complete).
func recipeUnresolved(cat *foods.Catalog, r Recipe) []string {
	var bad []string
	for _, ing := range r.Ingredienti {
		if _, ok := cat.ByID(ing.Food); !ok {
			bad = append(bad, ing.Food)
		}
	}
	return bad
}

// mealUnresolved returns a meal's references (recipe refs + direct foods) that
// don't resolve against the surviving recipes / the catalog.
func mealUnresolved(cat *foods.Catalog, recipesByID map[string]Recipe, m Meal) []string {
	var bad []string
	for _, ref := range m.Ricette {
		if _, ok := recipesByID[ref.Ricetta]; !ok {
			bad = append(bad, "ricetta:"+ref.Ricetta)
		}
	}
	for _, ing := range m.Cibi {
		if _, ok := cat.ByID(ing.Food); !ok {
			bad = append(bad, ing.Food)
		}
	}
	return bad
}

// validate reports any recipe ingredient, meal food, or meal recipe-reference in
// the current book that does not resolve. A book from buildBook always passes
// (invalid entries are quarantined at build time); this guards hand-built books.
func (b *Book) validate() error {
	var problems []string
	for _, r := range b.recipes {
		if bad := recipeUnresolved(b.cat, r); len(bad) > 0 {
			problems = append(problems, fmt.Sprintf("ricetta %q: alimento sconosciuto %s", r.ID, strings.Join(bad, ", ")))
		}
	}
	for _, mm := range b.meals {
		if bad := mealUnresolved(b.cat, b.recipesByID, mm); len(bad) > 0 {
			problems = append(problems, fmt.Sprintf("pasto %q: riferimenti non risolti %s", mm.ID, strings.Join(bad, ", ")))
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
	return len(recipeUnresolved(b.cat, r)) == 0
}

// MustLoad is Load but panics on error; intended for main wiring and tests.
func MustLoad(cat *foods.Catalog) *Book {
	b, err := Load(cat)
	if err != nil {
		panic(err)
	}
	return b
}

// Book satisfies BookProvider for an already-built, static book: the book yields
// itself. Tests and any caller that doesn't need runtime mutability can pass a
// *Book directly where a BookProvider is expected; the Firestore-backed Provider
// is the mutable implementation.
func (b *Book) Book(context.Context) (*Book, error) { return b, nil }

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
	// Query is a by-name lookup ("hai il riso alla cantonese?"): when set, only
	// recipes whose name/id contain every query word are returned, and the family
	// allergen exclusion is bypassed (the athlete asked for THAT dish by name; its
	// allergens are surfaced, not hidden).
	Query string
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

	queryWords := strings.Fields(strings.ToLower(strings.TrimSpace(f.Query)))

	var out []Recipe
	for _, r := range b.recipes {
		if f.Categoria != "" && r.Categoria != f.Categoria {
			continue
		}
		if len(queryWords) > 0 {
			// By-name lookup: every query word must appear in the name or id.
			// Bypasses the allergen exclusion (direct request for a named dish).
			hay := strings.ToLower(r.Nome + " " + r.ID)
			if !containsAllWords(hay, queryWords) {
				continue
			}
		} else if len(exclude) > 0 && !r.Personale {
			// The family allergen exclusion applies only to SHARED family meals,
			// not to the athlete's personal recipes (he tolerates what the family
			// cannot). Fail closed: a recipe whose ingredients don't fully resolve
			// has an unknown allergen profile and must NOT be offered while an
			// exclusion is active (Load() guarantees this for the embedded book;
			// this defends a hand-built Book too).
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

// containsAllWords reports whether every word appears somewhere in hay.
func containsAllWords(hay string, words []string) bool {
	for _, w := range words {
		if !strings.Contains(hay, w) {
			return false
		}
	}
	return true
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
