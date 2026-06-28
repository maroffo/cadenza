// ABOUTME: Tests for the embedded exercise catalog: load integrity, search filters, ranking, lookups.
// ABOUTME: Stdlib testing only; all data comes from the embedded catalog.json, no network.

package exercises

import (
	"os"
	"regexp"
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
	if got := c.Len(); got != 775 {
		t.Errorf("Len() = %d, want 775", got)
	}
	if c.sourceSHA == "" {
		t.Error("sourceSHA is empty, want non-empty")
	}
}

func TestEveryExerciseHasInstructionsAndGIF(t *testing.T) {
	c := load(t)
	for _, ex := range c.exercises {
		if strings.TrimSpace(ex.InstructionsIT) == "" {
			t.Errorf("exercise %s (%q) has empty InstructionsIT", ex.ID, ex.Name)
		}
		if strings.TrimSpace(ex.GIF) == "" {
			t.Errorf("exercise %s (%q) has empty GIF", ex.ID, ex.Name)
		}
	}
}

func TestSearchByMuscle(t *testing.T) {
	c := load(t)
	results := c.Search(SearchFilter{Muscle: "abs", Limit: 25})
	if len(results) == 0 {
		t.Fatal("Search(Muscle: abs) returned no results")
	}
	for _, ex := range results {
		if !muscleContains(ex, "abs") {
			t.Errorf("exercise %s (%q) matched muscle=abs but term is absent from target/body_part/secondary (target=%q body_part=%q secondary=%v)",
				ex.ID, ex.Name, ex.Target, ex.BodyPart, ex.Secondary)
		}
	}
}

func TestSearchMuscleCaseInsensitive(t *testing.T) {
	c := load(t)
	lower := ids(c.Search(SearchFilter{Muscle: "abs", Limit: 25}))
	upper := ids(c.Search(SearchFilter{Muscle: "ABS", Limit: 25}))
	if !equalIDs(lower, upper) {
		t.Errorf("case-insensitivity broken: abs=%v ABS=%v", lower, upper)
	}
}

func TestSearchEquipmentFilterExcludesOthers(t *testing.T) {
	c := load(t)
	results := c.Search(SearchFilter{Equipment: []string{"band", "body weight"}, Limit: 25})
	if len(results) == 0 {
		t.Fatal("Search with equipment filter returned no results")
	}
	for _, ex := range results {
		eq := strings.ToLower(ex.Equipment)
		if eq != "band" && eq != "body weight" {
			t.Errorf("exercise %s (%q) has equipment %q, outside the requested set", ex.ID, ex.Name, ex.Equipment)
		}
		if eq == "dumbbell" {
			t.Errorf("exercise %s (%q) is a dumbbell exercise but slipped through the band/body weight filter", ex.ID, ex.Name)
		}
	}
}

func TestSearchMuscleAndQueryBothRequired(t *testing.T) {
	c := load(t)
	results := c.Search(SearchFilter{Muscle: "abs", Query: "crunch", Limit: 25})
	if len(results) == 0 {
		t.Fatal("Search(Muscle: abs, Query: crunch) returned no results")
	}
	for _, ex := range results {
		if !muscleContains(ex, "abs") {
			t.Errorf("exercise %s (%q) does not satisfy muscle=abs", ex.ID, ex.Name)
		}
		if !strings.Contains(strings.ToLower(ex.Name), "crunch") {
			t.Errorf("exercise %s (%q) does not satisfy query=crunch", ex.ID, ex.Name)
		}
	}
}

func TestSearchDeterministicOrdering(t *testing.T) {
	c := load(t)
	f := SearchFilter{Muscle: "abs", Limit: 25}
	first := ids(c.Search(f))
	second := ids(c.Search(f))
	if !equalIDs(first, second) {
		t.Errorf("non-deterministic ordering: first=%v second=%v", first, second)
	}
}

func TestSearchLimit(t *testing.T) {
	c := load(t)
	if got := c.Search(SearchFilter{Muscle: "abs", Limit: 3}); len(got) > 3 {
		t.Errorf("Limit: 3 returned %d results, want <= 3", len(got))
	}
	if got := c.Search(SearchFilter{Muscle: "abs", Limit: 0}); len(got) > 8 {
		t.Errorf("Limit: 0 (default) returned %d results, want <= 8", len(got))
	}
	if got := c.Search(SearchFilter{Muscle: "abs", Limit: 1000}); len(got) > 25 {
		t.Errorf("Limit: 1000 returned %d results, want <= 25 (cap)", len(got))
	}
}

func TestByID(t *testing.T) {
	c := load(t)
	ex, ok := c.ByID("0001")
	if !ok {
		t.Fatal("ByID(\"0001\") not found, want found")
	}
	if ex.ID != "0001" {
		t.Errorf("ByID(\"0001\").ID = %q, want \"0001\"", ex.ID)
	}
	if _, ok := c.ByID("does-not-exist"); ok {
		t.Error("ByID(\"does-not-exist\") returned found, want not found")
	}
}

func TestGIFSourceURL(t *testing.T) {
	c := load(t)
	ex, ok := c.ByID("0001")
	if !ok {
		t.Fatal("ByID(\"0001\") not found")
	}
	url := c.GIFSourceURL(ex)
	if !strings.Contains(url, c.sourceSHA) {
		t.Errorf("GIFSourceURL = %q, missing source SHA %q", url, c.sourceSHA)
	}
	if !strings.HasSuffix(url, ".gif") {
		t.Errorf("GIFSourceURL = %q, want .gif suffix", url)
	}
}

func TestVocabulary(t *testing.T) {
	c := load(t)
	targets, bodyParts, equipment := c.Vocabulary()
	for name, vals := range map[string][]string{"targets": targets, "bodyParts": bodyParts, "equipment": equipment} {
		if len(vals) == 0 {
			t.Errorf("Vocabulary() %s is empty", name)
		}
		if !isSortedDistinctLower(vals) {
			t.Errorf("Vocabulary() %s is not sorted/distinct/lowercased: %v", name, vals)
		}
	}
}

// muscleContains reports whether term appears in ex's target, body part, or secondary muscles.
func muscleContains(ex Exercise, term string) bool {
	term = strings.ToLower(term)
	if strings.Contains(strings.ToLower(ex.Target), term) {
		return true
	}
	if strings.Contains(strings.ToLower(ex.BodyPart), term) {
		return true
	}
	for _, s := range ex.Secondary {
		if strings.Contains(strings.ToLower(s), term) {
			return true
		}
	}
	return false
}

func ids(exs []Exercise) []string {
	out := make([]string, len(exs))
	for i, ex := range exs {
		out[i] = ex.ID
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

// rankOf recomputes a result's match tier from its own fields, mirroring the
// production ladder Target(0) > BodyPart(1) > Secondary(2). Used to prove the
// search actually orders best-match-first, not just returns the right SET.
func rankOf(ex Exercise, muscle string) int {
	m := strings.ToLower(muscle)
	if strings.Contains(strings.ToLower(ex.Target), m) {
		return rankTarget
	}
	if strings.Contains(strings.ToLower(ex.BodyPart), m) {
		return rankBodyPart
	}
	for _, s := range ex.Secondary {
		if strings.Contains(strings.ToLower(s), m) {
			return rankSecondary
		}
	}
	return rankName
}

// TestSearchRankingBestMatchFirst pins the advertised core behavior: a muscle
// that is the primary Target on some entries and only a Secondary on others
// must return all Target matches before any Secondary-only match. "glutes" with
// the band filter spans both tiers (target and secondary) within the limit.
func TestSearchRankingBestMatchFirst(t *testing.T) {
	c := load(t)
	res := c.Search(SearchFilter{Muscle: "glutes", Equipment: []string{"band"}, Limit: 25})
	if len(res) < 2 {
		t.Fatalf("expected several glutes+band results, got %d", len(res))
	}
	seenTarget, seenSecondary := false, false
	prev := -1
	for i, ex := range res {
		r := rankOf(ex, "glutes")
		if r == rankTarget {
			seenTarget = true
		}
		if r == rankSecondary {
			seenSecondary = true
		}
		if r < prev {
			t.Errorf("result %d (%s) rank %d follows a worse rank %d: not sorted best-first", i, ex.ID, r, prev)
		}
		prev = r
	}
	// Both tiers must actually appear, or the ordering assertion proves nothing.
	if !seenTarget || !seenSecondary {
		t.Fatalf("test data no longer spans tiers (target=%v secondary=%v); pick another muscle/equipment", seenTarget, seenSecondary)
	}
}

// TestNoticeSHAMatchesCatalog guards against the attribution NOTICE drifting from
// the SHA the catalog (and its GIF URLs) is actually pinned to.
func TestNoticeSHAMatchesCatalog(t *testing.T) {
	notice, err := os.ReadFile("NOTICE")
	if err != nil {
		t.Fatalf("read NOTICE: %v", err)
	}
	m := regexp.MustCompile(`[0-9a-f]{40}`).FindString(string(notice))
	if m == "" {
		t.Fatal("no 40-char commit SHA found in NOTICE")
	}
	if got := load(t).SourceSHA(); m != got {
		t.Errorf("NOTICE SHA %s != catalog source_sha %s: update NOTICE on every SHA bump", m, got)
	}
}
