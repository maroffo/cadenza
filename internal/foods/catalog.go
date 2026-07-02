// ABOUTME: Embedded food catalog (82 entries) with case-insensitive name/synonym lookup.
// ABOUTME: Parsed once from catalog.json; Go owns all portion arithmetic (per-100g, per-unit).

package foods

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

//go:embed catalog.json
var catalogJSON []byte

// Food is a single catalog entry. Composition figures are per 100 g of edible
// portion. Italian copy lives in NameIT/Synonyms; NameEN/Category/Allergens carry
// the dataset's English vocabulary. UnitGrams is the typical weight of one
// countable unit (a banana, an egg, a gel) and is 0 for foods sold by weight.
type Food struct {
	ID         string   `json:"id"`
	NameEN     string   `json:"name_en"`
	NameIT     string   `json:"name_it"`
	Synonyms   []string `json:"synonyms"`
	Category   string   `json:"category"`
	Allergens  []string `json:"allergens"`
	Source     string   `json:"source"`
	SourceID   string   `json:"source_id"`
	AsOf       string   `json:"as_of"`
	Kcal       float64  `json:"kcal"`
	CarbG      float64  `json:"carb_g"`
	ProteinG   float64  `json:"protein_g"`
	FatG       float64  `json:"fat_g"`
	FiberG     float64  `json:"fiber_g"`
	SugarG     float64  `json:"sugar_g"`
	SodiumMg   float64  `json:"sodium_mg"`
	CaffeineMg float64  `json:"caffeine_mg"`
	UnitGrams  float64  `json:"unit_grams"`
}

// Macros is a computed nutrient breakdown for a concrete portion, with every
// figure rounded to one decimal. It mirrors Food's per-nutrient fields minus the
// metadata, so callers never re-derive portion math themselves.
type Macros struct {
	Kcal       float64
	CarbG      float64
	ProteinG   float64
	FatG       float64
	FiberG     float64
	SugarG     float64
	SodiumMg   float64
	CaffeineMg float64
}

// Catalog is the parsed, immutable in-memory food dataset.
type Catalog struct {
	foods     []Food
	index     map[string]Food
	srRelease string
}

// catalogFile mirrors the on-disk wrapper around the food list.
type catalogFile struct {
	Source    string `json:"source"`
	SRRelease string `json:"sr_release"`
	Foods     []Food `json:"foods"`
}

// Load parses the embedded catalog once and builds the id index.
func Load() (*Catalog, error) {
	var cf catalogFile
	if err := json.Unmarshal(catalogJSON, &cf); err != nil {
		return nil, fmt.Errorf("foods: parse catalog: %w", err)
	}
	index := make(map[string]Food, len(cf.Foods))
	for _, f := range cf.Foods {
		index[f.ID] = f
	}
	return &Catalog{
		foods:     cf.Foods,
		index:     index,
		srRelease: cf.SRRelease,
	}, nil
}

// MustLoad is Load but panics on error; intended for main wiring.
func MustLoad() *Catalog {
	c, err := Load()
	if err != nil {
		panic(err)
	}
	return c
}

const (
	defaultLookupLimit = 8
	maxLookupLimit     = 25
)

// rank values: lower is a better match.
const (
	matchExact      = 0 // query equals NameIT or NameEN
	matchWordPrefix = 1 // query is the prefix of a word in NameIT or NameEN
	matchSubstring  = 2 // query is a substring of NameIT or NameEN
	matchSynonym    = 3 // query is a substring of a synonym
)

// Lookup returns foods whose Italian name, English name, or synonyms match the
// query, ranked best-match-first (exact > word-prefix > substring > synonym),
// with a stable tiebreak on ID. Matching is case-insensitive. limit<=0 defaults
// to 8 and is capped at 25.
func (c *Catalog) Lookup(query string, limit int) []Food {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	if limit <= 0 {
		limit = defaultLookupLimit
	}
	if limit > maxLookupLimit {
		limit = maxLookupLimit
	}

	type ranked struct {
		food Food
		rank int
	}
	var matches []ranked
	for _, f := range c.foods {
		r := foodRank(f, q)
		if r < 0 {
			continue
		}
		matches = append(matches, ranked{food: f, rank: r})
	}

	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].rank != matches[j].rank {
			return matches[i].rank < matches[j].rank
		}
		return matches[i].food.ID < matches[j].food.ID
	})

	if len(matches) > limit {
		matches = matches[:limit]
	}
	out := make([]Food, len(matches))
	for i, m := range matches {
		out[i] = m.food
	}
	return out
}

// foodRank returns the best (lowest) rank at which q matches f, or -1 if it does
// not. q is assumed already lowercased and trimmed by the caller.
func foodRank(f Food, q string) int {
	nameIT := strings.ToLower(f.NameIT)
	nameEN := strings.ToLower(f.NameEN)
	if nameIT == q || nameEN == q {
		return matchExact
	}
	if hasWordPrefix(nameIT, q) || hasWordPrefix(nameEN, q) {
		return matchWordPrefix
	}
	if strings.Contains(nameIT, q) || strings.Contains(nameEN, q) {
		return matchSubstring
	}
	for _, s := range f.Synonyms {
		if strings.Contains(strings.ToLower(s), q) {
			return matchSynonym
		}
	}
	return -1
}

// hasWordPrefix reports whether q is the prefix of any whitespace-delimited word
// in name. Both arguments are assumed already lowercased.
func hasWordPrefix(name, q string) bool {
	for _, w := range strings.Fields(name) {
		if strings.HasPrefix(w, q) {
			return true
		}
	}
	return false
}

// Per scales the food's per-100g composition to grams, rounding each nutrient to
// one decimal. This is the single source of truth for portion arithmetic.
func (f Food) Per(grams float64) Macros {
	s := grams / 100
	return Macros{
		Kcal:       round1(f.Kcal * s),
		CarbG:      round1(f.CarbG * s),
		ProteinG:   round1(f.ProteinG * s),
		FatG:       round1(f.FatG * s),
		FiberG:     round1(f.FiberG * s),
		SugarG:     round1(f.SugarG * s),
		SodiumMg:   round1(f.SodiumMg * s),
		CaffeineMg: round1(f.CaffeineMg * s),
	}
}

// PerUnits scales the food to n countable units. It returns false when the food
// has no defined unit weight (sold by weight, not by piece).
func (f Food) PerUnits(n float64) (Macros, bool) {
	if f.UnitGrams <= 0 {
		return Macros{}, false
	}
	return f.Per(n * f.UnitGrams), true
}

// round1 rounds x to one decimal place.
func round1(x float64) float64 {
	return math.Round(x*10) / 10
}

// ByID returns the food with the given id and whether it exists.
func (c *Catalog) ByID(id string) (Food, bool) {
	f, ok := c.index[id]
	return f, ok
}

// Vocabulary returns the sorted, distinct, lowercased category and allergen
// values, for building tool descriptions and validating inputs.
func (c *Catalog) Vocabulary() (categories, allergens []string) {
	categorySet := make(map[string]bool)
	allergenSet := make(map[string]bool)
	for _, f := range c.foods {
		categorySet[strings.ToLower(f.Category)] = true
		for _, a := range f.Allergens {
			allergenSet[strings.ToLower(a)] = true
		}
	}
	return sortedKeys(categorySet), sortedKeys(allergenSet)
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

// Len returns the number of foods in the catalog.
func (c *Catalog) Len() int {
	return len(c.foods)
}

// IDs returns every food id, sorted, for building input datalists (the recipe
// dashboard references foods by id).
func (c *Catalog) IDs() []string {
	out := make([]string, 0, len(c.foods))
	for _, f := range c.foods {
		out = append(out, f.ID)
	}
	sort.Strings(out)
	return out
}
