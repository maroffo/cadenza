# ABOUTME: Plan for a family-nutrition module in Cadenza: embedded food-macros DB + reasonable meal guidance
# ABOUTME: Two phases: shared foods DB + lookup (also serves athlete fueling), then the family meal module

# Family Nutrition Module

## Goal

Help Max plan reasonable, balanced FAMILY meals grounded in REAL food macros, respecting
allergies/intolerances and dietary preferences, with children present. Scope chosen
(2026-06-29): "guida ragionevole" (no per-person clinical targets), family = children +
allergies/intolerances + dietary preferences (NO medical conditions), lives as a separate
MODULE inside Cadenza (own prompt/guardrails, shared food DB).

## Hard boundary (safety)

The coach prompt already forbids clinical meal plans / diagnosis. This module STAYS there:
general, sensible guidance, explicitly NOT a substitute for a dietitian/pediatrician,
especially for the children. The one thing code OWNS deterministically is **allergen
exclusion** (data layer, never model discretion) because allergens are safety-relevant.
Nutrient adequacy stays soft/general guidance.

## Decisions (from research 2026-06-29 + scope answers)

| # | Decision | Choice | Rationale |
|---|----------|--------|-----------|
| 1 | Data delivery | Embed via go:embed, NO live API | Scale-to-zero cold-start, no API key, no ToS storage limits; mirrors exercise catalog |
| 2 | Primary source | USDA FoodData Central (CC0 public domain) | Cleanest possible license (the exercise-dataset provenance lesson) |
| 3 | Italian names | Hand-authored name_it + synonyms | Food NAMES aren't the licensed asset; the CC0 numbers are. Avoids CREA's murky license entirely |
| 4 | Gels/drinks (~20) | Hand-curated from manufacturer labels | No clean dataset exists; label numbers are facts |
| 5 | Arithmetic | Go owns ALL portion scaling | Per-100g vs per-portion is the #1 silent error; model passes grams/units, never converts |
| 6 | Raw vs cooked | Separate rows | "100g pasta" dry ~75g carbs, cooked ~30g; never let one row mean both |
| 7 | Allergen exclusion | Deterministic filter at the data layer | Safety with kids: excluded foods never reach the model from the DB; best-effort scan as backstop |
| 8 | Family profile | family.yaml seed -> Firestore (like athlete.yaml -> Identity) | Reuses the existing cmd/seed pattern |
| 9 | Module shape | Same Telegram coach, distinct family-nutrition toolset + prompt section | Honors "separate module" without a second agent/routing layer |

## Schema (per food, per-100g basis; Go scales)

`id, name_en, name_it, synonyms[], category, basis(100g|unit), unit_grams?, kcal,
carb_g, protein_g, fat_g, fiber_g, sugar_g, sodium_mg, caffeine_mg, allergens[],
source, source_id, as_of`

Allergen tags (controlled vocab): gluten, lactose, milk, egg, nuts, peanuts, soy, fish,
shellfish, sesame. Family profile lists which to EXCLUDE.

## Phase 1 — Foods DB + lookup (shared; also answers the original athlete-fueling ask)

Files:
- `scripts/build_foods_catalog.py` (uv) — download USDA FDC bulk (SR Legacy + Foundation,
  CC0), select a curated ~150-food list by FDC id/name, map nutrient numbers
  (1008 kcal, 1003 protein, 1004 fat, 1005 carb, 1079 fiber, 2000 sugar, 1093 sodium,
  1057 caffeine), normalize to per-100g, merge hand-curated Italian foods + gels JSON,
  write `internal/foods/catalog.json`. Pin FDC release + record `source`/`as_of` per row.
- `internal/foods/catalog.go` — go:embed; `Food` struct; `Catalog` with `Lookup(query, FoodFilter)`,
  `ByID`, `Scale(food, grams)` / `ScaleUnits(food, n)` returning a `Macros` struct (Go arithmetic),
  `Vocabulary()`. Deterministic, tested.
- `internal/foods/catalog_test.go`; `internal/foods/NOTICE` (CC0 attribution, honest).
- `internal/job/coach.go` — `lookup_food` tool `{query, grams?, units?}`: returns the matched
  food + Go-scaled macros, data-not-instructions framing. New `Foods *foods.Catalog` field (nil hides).
- `internal/agent/coach.go` — prompt: for fueling numbers call lookup_food, never invent macros;
  pass grams/units, never convert portions yourself.
- `cmd/cadenza/main.go` — wire Foods.

Verify: ask "quanti carboidrati in 100g di pasta?" / "carbo in 2 banane?" -> real scaled numbers.

## Phase 2 — Family meal module (depends on Phase 1)

Files:
- `internal/store/family.go` — `Family` doc: Members[]{Name, AgeBand?, Notes}, ExcludeAllergens[],
  ExcludeFoods[], Preferences string. Get/Seed.
- `cmd/seed/main.go` + `family.example.yaml` — seed the family profile from YAML.
- `internal/foods` — allergen-aware filtering: `Lookup`/`Suggest` exclude foods whose
  allergens intersect the family's ExcludeAllergens (or ExcludeFoods).
- `internal/job/coach.go` — family-nutrition tools: `lookup_food` already exclusion-aware;
  `suggest_foods{category, goal}` returns SAFE options; optional `check_meal{foods[]}`
  deterministic allergen backstop. Surface the family profile (members, exclusions,
  preferences) in the context as HARD constraints (data framing).
- `internal/agent/coach.go` — a delimited "Nutrizione famiglia" prompt section: general
  balanced guidance; allergens are hard constraints (already enforced in code); children
  present -> age-appropriate general advice, refer to pediatra/dietologo for specifics;
  numbers only from lookup_food; weekly plans stay conversational, grounded in lookups.

Verify: "proponi un piano settimanale per la famiglia" -> balanced meals, zero excluded
allergens, respects preferences, real macros, explicit professional-referral caveat.

## Honest limits

- Free-text recipes can't be fully allergen-validated by code (NLP-fuzzy); code enforces
  exclusion at the DB layer + surfaces constraints + best-effort scan. Hence the general-
  guidance boundary and the professional-referral caveat, especially for the children.
- Not nutrient-complete planning (that was the rejected "target completi" option).

## Inputs needed from Max (Phase 2)

- Family members (count + rough ages, esp. children's ages — drives age-appropriate caveats).
- EXACT allergies/intolerances (safety-critical; seeds the deterministic exclusion).
- Dietary preferences/constraints (e.g. vegetarian, no pork, dislikes).

## Open questions

- Curated ~150-food list: I draft it (carb staples, fruits, proteins, dairy, fats, gels,
  veg), Max tweaks. OK?
- Ship Phase 1 first (immediately useful, zero safety surface), then Phase 2? Recommended.

## Family captured + refinement (2026-06-29)

Family: Max (athlete, data in Cadenza); wife 160 cm / 55 kg, SLIGHTLY lactose intolerant
-> family exclusion = `lactose` (NOT `milk`): high-lactose dairy out, aged cheese
(parmigiano/grana) + butter stay; daughter 13; son 9 (children present -> pediatric caveat
mandatory).

Max steered UP from pure "guida ragionevole" to EVIDENCE-BASED. Reconciled: practical meal
suggestions (output) grounded in citable reference values (orientation), still NOT a clinical
per-person prescription, deferring to a pediatrician/dietitian for the children.

Real workflow: build foods DB -> Max uploads RECIPES the family likes -> assistant proposes
meals from that recipe book when out of ideas. So Phase 2 = RECIPE BOOK + MEAL ASSISTANT,
not a from-scratch clinical planner.

Evidence layer (Phase 2, deterministic Go, cited): EFSA DRV / Italian LARN reference values
for orientation; energy estimate via published BMR equations (Schofield for children,
Mifflin-St Jeor for adults) x PAL. Needs Max's height for the adult estimate.

Phase 1 status: foods catalog built (82 foods, USDA FDC CC0 + hand-curated IT/sport),
internal/foods package + lookup_food tool in progress. Checkpoint Phase 2 with Max before
building (children + allergen safety surface).
