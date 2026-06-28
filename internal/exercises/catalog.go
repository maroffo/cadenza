// ABOUTME: Embedded exercise catalog (775 entries) with case-insensitive muscle/equipment/name search.
// ABOUTME: Parsed once from catalog.json; ranks Target > BodyPart > Secondary > Name match.

package exercises

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

//go:embed catalog.json
var catalogJSON []byte

// Exercise is a single catalog entry. Italian copy lives in InstructionsIT/StepsIT;
// everything else (target, body_part, equipment, secondary) is the dataset's English vocabulary.
type Exercise struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Equipment      string   `json:"equipment"`
	Target         string   `json:"target"`
	BodyPart       string   `json:"body_part"`
	Secondary      []string `json:"secondary"`
	InstructionsIT string   `json:"it"`
	StepsIT        []string `json:"steps_it"`
	GIF            string   `json:"gif"`
}

// Catalog is the parsed, immutable in-memory exercise dataset.
type Catalog struct {
	exercises []Exercise
	sourceSHA string
	index     map[string]Exercise
}

// catalogFile mirrors the on-disk wrapper around the exercise list.
type catalogFile struct {
	SourceSHA string     `json:"source_sha"`
	Exercises []Exercise `json:"exercises"`
}

// Load parses the embedded catalog once and builds the id index.
func Load() (*Catalog, error) {
	var cf catalogFile
	if err := json.Unmarshal(catalogJSON, &cf); err != nil {
		return nil, fmt.Errorf("exercises: parse catalog: %w", err)
	}
	index := make(map[string]Exercise, len(cf.Exercises))
	for _, ex := range cf.Exercises {
		index[ex.ID] = ex
	}
	return &Catalog{
		exercises: cf.Exercises,
		sourceSHA: cf.SourceSHA,
		index:     index,
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

// SearchFilter selects and ranks exercises. Empty fields are ignored.
type SearchFilter struct {
	Muscle    string
	Equipment []string
	Query     string
	Limit     int
}

const (
	defaultSearchLimit = 8
	maxSearchLimit     = 25
)

// rank values: lower is a better match.
const (
	rankTarget    = 0 // muscle matched the primary target
	rankBodyPart  = 1 // muscle matched the body part
	rankSecondary = 2 // muscle matched a secondary muscle
	rankName      = 3 // only the name query matched
)

// Search returns exercises matching every active filter, ranked best-match first
// (Target > BodyPart > Secondary > Name), with a stable tiebreak on ID. All matching
// is case-insensitive; muscle/name use substring containment.
func (c *Catalog) Search(f SearchFilter) []Exercise {
	muscle := strings.ToLower(strings.TrimSpace(f.Muscle))
	query := strings.ToLower(strings.TrimSpace(f.Query))

	var equip map[string]bool
	if len(f.Equipment) > 0 {
		equip = make(map[string]bool, len(f.Equipment))
		for _, e := range f.Equipment {
			if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
				equip[e] = true
			}
		}
	}

	limit := f.Limit
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	if limit > maxSearchLimit {
		limit = maxSearchLimit
	}

	type ranked struct {
		ex   Exercise
		rank int
	}
	var matches []ranked

	for _, ex := range c.exercises {
		// Equipment: exact (lowercased) membership; required when the set is non-empty.
		if equip != nil && !equip[strings.ToLower(ex.Equipment)] {
			continue
		}

		rank := -1 // unranked until a muscle/name reason matches

		if muscle != "" {
			mr := muscleRank(ex, muscle)
			if mr < 0 {
				continue // muscle required but not matched
			}
			rank = mr
		}

		if query != "" {
			if !strings.Contains(strings.ToLower(ex.Name), query) {
				continue // name required but not matched
			}
			if rank < 0 { // muscle rank, when present, is always better than name
				rank = rankName
			}
		}

		if muscle == "" && query == "" {
			rank = rankTarget // equipment-only (or no) filter: keep stable ID order
		}

		matches = append(matches, ranked{ex: ex, rank: rank})
	}

	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].rank != matches[j].rank {
			return matches[i].rank < matches[j].rank
		}
		return matches[i].ex.ID < matches[j].ex.ID
	})

	if len(matches) > limit {
		matches = matches[:limit]
	}
	out := make([]Exercise, len(matches))
	for i, m := range matches {
		out[i] = m.ex
	}
	return out
}

// muscleRank returns the best (lowest) rank at which muscle matches ex, or -1 if it does not.
func muscleRank(ex Exercise, muscle string) int {
	if fieldMatches(ex.Target, muscle) {
		return rankTarget
	}
	if fieldMatches(ex.BodyPart, muscle) {
		return rankBodyPart
	}
	for _, s := range ex.Secondary {
		if fieldMatches(s, muscle) {
			return rankSecondary
		}
	}
	return -1
}

// fieldMatches reports whether term equals or is contained in field (case-insensitive).
// term is assumed already lowercased by the caller.
func fieldMatches(field, term string) bool {
	return strings.Contains(strings.ToLower(field), term)
}

// ByID returns the exercise with the given id and whether it exists.
func (c *Catalog) ByID(id string) (Exercise, bool) {
	ex, ok := c.index[id]
	return ex, ok
}

// GIFSourceURL builds the raw GitHub URL for an exercise's demonstration GIF.
func (c *Catalog) GIFSourceURL(ex Exercise) string {
	return "https://raw.githubusercontent.com/hasaneyldrm/exercises-dataset/" + c.sourceSHA + "/videos/" + ex.GIF
}

// Vocabulary returns the sorted, distinct, lowercased values for targets, body parts
// and equipment, for building tool descriptions and validating inputs.
func (c *Catalog) Vocabulary() (targets, bodyParts, equipment []string) {
	targetSet := make(map[string]bool)
	bodyPartSet := make(map[string]bool)
	equipmentSet := make(map[string]bool)
	for _, ex := range c.exercises {
		targetSet[strings.ToLower(ex.Target)] = true
		bodyPartSet[strings.ToLower(ex.BodyPart)] = true
		equipmentSet[strings.ToLower(ex.Equipment)] = true
	}
	return sortedKeys(targetSet), sortedKeys(bodyPartSet), sortedKeys(equipmentSet)
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

// Len returns the number of exercises in the catalog.
func (c *Catalog) Len() int {
	return len(c.exercises)
}

// SourceSHA is the upstream dataset commit the catalog (and its GIF URLs) is
// pinned to. The attribution NOTICE must agree with it; a test enforces this.
func (c *Catalog) SourceSHA() string {
	return c.sourceSHA
}
