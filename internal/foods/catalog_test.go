// ABOUTME: Tests for the embedded food catalog: load integrity, lookup ranking, portion math.
// ABOUTME: Stdlib testing only; all data comes from the embedded catalog.json, no network.

package foods

import (
	"math"
	"strings"
	"testing"
)

func load(t *testing.T) *Catalog {
	t.Helper()
	c, err := Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	return c
}

func TestLoad(t *testing.T) {
	c := load(t)
	if got := c.Len(); got != 82 {
		t.Errorf("Len() = %d, want 82", got)
	}
	if c.srRelease == "" {
		t.Error("srRelease is empty, want non-empty")
	}
}

func TestEveryFoodHasNameAndCategory(t *testing.T) {
	c := load(t)
	for _, f := range c.foods {
		if strings.TrimSpace(f.NameIT) == "" {
			t.Errorf("food %s has empty NameIT", f.ID)
		}
		if strings.TrimSpace(f.Category) == "" {
			t.Errorf("food %s (%q) has empty Category", f.ID, f.NameIT)
		}
		if f.Kcal < 0 {
			t.Errorf("food %s (%q) has negative kcal %v", f.ID, f.NameIT, f.Kcal)
		}
	}
}

func TestLookupExactNameWins(t *testing.T) {
	c := load(t)
	res := c.Lookup("banana", 8)
	if len(res) == 0 {
		t.Fatal("Lookup(\"banana\") returned no results")
	}
	if res[0].ID != "banana" {
		t.Errorf("Lookup(\"banana\")[0].ID = %q, want \"banana\"", res[0].ID)
	}
}

func TestLookupMatchesItalianName(t *testing.T) {
	c := load(t)
	res := c.Lookup("mozzarella", 8)
	if !containsID(res, "mozzarella") {
		t.Errorf("Lookup(\"mozzarella\") = %v, want it to contain \"mozzarella\"", ids(res))
	}
}

func TestLookupMatchesSynonym(t *testing.T) {
	c := load(t)
	// "grana" is a synonym of parmigiano; the lookup must surface it.
	res := c.Lookup("grana", 25)
	if !containsID(res, "parmigiano") {
		t.Errorf("Lookup(\"grana\") = %v, want it to contain \"parmigiano\" via synonym", ids(res))
	}
}

func TestLookupNoMatch(t *testing.T) {
	c := load(t)
	if res := c.Lookup("xyzzy-not-a-food", 8); len(res) != 0 {
		t.Errorf("Lookup of unknown term = %v, want empty", ids(res))
	}
	if res := c.Lookup("   ", 8); len(res) != 0 {
		t.Errorf("Lookup of blank query = %v, want empty", ids(res))
	}
}

func TestPerScalesPer100g(t *testing.T) {
	c := load(t)
	banana := byID(t, c, "banana")

	got := banana.Per(118).CarbG
	want := banana.CarbG * 1.18
	if math.Abs(got-want) > 0.5 {
		t.Errorf("banana.Per(118).CarbG = %v, want ~%v (within 0.5)", got, want)
	}

	half := banana.Per(50).CarbG
	full := banana.Per(100).CarbG
	if math.Abs(half-full/2) > 0.2 {
		t.Errorf("banana.Per(50).CarbG = %v, want ~half of Per(100)=%v", half, full)
	}
	if math.Abs(full-banana.CarbG) > 0.1 {
		t.Errorf("banana.Per(100).CarbG = %v, want ~%v (the per-100g figure)", full, banana.CarbG)
	}
}

func TestPerUnits(t *testing.T) {
	c := load(t)

	banana := byID(t, c, "banana")
	m, ok := banana.PerUnits(2)
	if !ok {
		t.Fatal("banana.PerUnits(2) ok = false, want true (banana has unit_grams)")
	}
	want := banana.CarbG * 2 * banana.UnitGrams / 100
	if math.Abs(m.CarbG-want) > 0.5 {
		t.Errorf("banana.PerUnits(2).CarbG = %v, want ~%v (within 0.5)", m.CarbG, want)
	}

	olio := byID(t, c, "olio_oliva")
	if _, ok := olio.PerUnits(1); ok {
		t.Error("olio_oliva.PerUnits(1) ok = true, want false (no unit_grams)")
	}
}

func TestByID(t *testing.T) {
	c := load(t)
	f, ok := c.ByID("banana")
	if !ok {
		t.Fatal("ByID(\"banana\") not found, want found")
	}
	if f.ID != "banana" {
		t.Errorf("ByID(\"banana\").ID = %q, want \"banana\"", f.ID)
	}
	if _, ok := c.ByID("does-not-exist"); ok {
		t.Error("ByID(\"does-not-exist\") returned found, want not found")
	}
}

func TestVocabulary(t *testing.T) {
	c := load(t)
	categories, allergens := c.Vocabulary()
	for name, vals := range map[string][]string{"categories": categories, "allergens": allergens} {
		if len(vals) == 0 {
			t.Errorf("Vocabulary() %s is empty", name)
		}
		if !isSortedDistinctLower(vals) {
			t.Errorf("Vocabulary() %s is not sorted/distinct/lowercased: %v", name, vals)
		}
	}
}

func TestLookupLimit(t *testing.T) {
	c := load(t)
	// "a" matches a great many names; exercises the default and the cap.
	if got := c.Lookup("a", 3); len(got) > 3 {
		t.Errorf("limit 3 returned %d results, want <= 3", len(got))
	}
	if got := c.Lookup("a", 0); len(got) > 8 {
		t.Errorf("limit 0 (default) returned %d results, want <= 8", len(got))
	}
	if got := c.Lookup("a", 1000); len(got) > 25 {
		t.Errorf("limit 1000 returned %d results, want <= 25 (cap)", len(got))
	}
}

func TestLookupDeterministic(t *testing.T) {
	c := load(t)
	first := ids(c.Lookup("o", 25))
	second := ids(c.Lookup("o", 25))
	if !equalIDs(first, second) {
		t.Errorf("non-deterministic ordering: first=%v second=%v", first, second)
	}
}

func byID(t *testing.T, c *Catalog, id string) Food {
	t.Helper()
	f, ok := c.ByID(id)
	if !ok {
		t.Fatalf("ByID(%q) not found", id)
	}
	return f
}

func containsID(foods []Food, id string) bool {
	for _, f := range foods {
		if f.ID == id {
			return true
		}
	}
	return false
}

func ids(foods []Food) []string {
	out := make([]string, len(foods))
	for i, f := range foods {
		out[i] = f.ID
	}
	return out
}

func equalIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func isSortedDistinctLower(vals []string) bool {
	for i, v := range vals {
		if v != strings.ToLower(v) {
			return false
		}
		if i > 0 && vals[i-1] >= v { // strictly ascending => sorted and distinct
			return false
		}
	}
	return true
}

// TestLookupRankingAllTiers pins the full rank ladder Exact > WordPrefix >
// Substring > Synonym on a controlled catalog, so an inverted comparator can
// never pass. Query "mela" hits each food at a different tier.
func TestLookupRankingAllTiers(t *testing.T) {
	c := &Catalog{foods: []Food{
		{ID: "f4_syn", NameIT: "Dolce", Synonyms: []string{"mela"}},
		{ID: "f2_prefix", NameIT: "Mela verde"},
		{ID: "f1_exact", NameIT: "Mela"},
		{ID: "f3_substr", NameIT: "Caramela"},
	}}
	got := c.Lookup("mela", 10)
	want := []string{"f1_exact", "f2_prefix", "f3_substr", "f4_syn"}
	if len(got) != len(want) {
		t.Fatalf("got %d results, want %d: %v", len(got), len(want), ids(got))
	}
	for i := range want {
		if got[i].ID != want[i] {
			t.Fatalf("rank order = %v, want %v (Exact>WordPrefix>Substring>Synonym)", ids(got), want)
		}
	}
}

// TestLookupWordPrefixBeatsSynonymRealData guards the same boundary on the real
// catalog: "grana" is a word-prefix of "Grana Padano" (tier 1) and a synonym of
// "Parmigiano" (tier 3), so grana_padano must come first.
func TestLookupWordPrefixBeatsSynonymRealData(t *testing.T) {
	res := load(t).Lookup("grana", 10)
	pos := map[string]int{}
	for i, f := range res {
		pos[f.ID] = i
	}
	gp, okGP := pos["grana_padano"]
	pa, okPA := pos["parmigiano"]
	if !okGP || !okPA {
		t.Fatalf("expected both grana_padano and parmigiano in results: %v", ids(res))
	}
	if res[0].ID != "grana_padano" {
		t.Errorf("res[0] = %s, want grana_padano (word-prefix beats synonym)", res[0].ID)
	}
	if gp >= pa {
		t.Errorf("grana_padano (%d) should precede parmigiano (%d)", gp, pa)
	}
}
