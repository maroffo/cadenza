# ABOUTME: Living retrospective for Cadenza — architecture decisions, bugs, and hard-won lessons.
# ABOUTME: Conversational, dated, searchable; grows with the project.

# Cadenza — Learning Log

Cadenza is an AI endurance coach (Go on Cloud Run, Telegram, Anthropic Claude, intervals.icu,
Firestore). Core rule: **safety/determinism live in Go, the model does judgment.** This log
captures what we learned building on top of that, not the code itself (git has the code).

---

## Reference-data modules: the `go:embed` pattern (and its one sharp edge)

**2026-06 → 07.** We added three sibling reference libraries the same way: an exercise catalog
(775 items), a food-macros catalog (→225 foods), and a recipe book (~103 recipes). All follow
one shape: a build script (`scripts/build_*.py`, run via `uv`) produces a JSON/YAML asset that
Go pulls in with `//go:embed`, parsed once into an immutable in-memory struct with a
deterministic `Search`/`Lookup`. Zero infra, zero runtime deps, versioned atomically with the
binary, trivially testable offline. For a scale-to-zero single-user service this beat every
"call an API" option (cold-start latency, keys, ToS caching limits, a network failure mode).

**The edge that bit us later:** embedded data is *immutable at runtime*. The moment Max asked
for a dashboard to *edit* recipes, the whole `go:embed` choice became the blocker — you can't
write back into the binary. Embedding is perfect for author-time-curated data and exactly wrong
for user-mutable data. **Takeaway:** choose `go:embed` when the data changes at *build* time and
Firestore (or similar) when it changes at *runtime*. We picked right for v1 and knew the
migration cost we were deferring.

---

## The "coach can't see the database" bugs were never the database

**2026-07-02.** Three times in a row Max reported "Cadenza non legge il database" — the coach
said a dish or the breakfast "wasn't there." Three times the data was **100% present and being
read correctly** (embedded, Load-validated). Every failure was in the *tool surface / filter
logic* between the model and the data:

1. His personal lactose breakfast was hard-excluded by the *family* lactose filter.
2. `suggest_recipe` could only browse by categoria/season — no by-name search — so "hai il riso
   alla cantonese?" found nothing (wrong categoria guessed, not in the seasonal top-N).
3. `suggest_recipe` caps at the seasonal top-N, so "dammi l'elenco" only ever showed ~12 summer
   dishes; off-season recipes looked "missing." Needed a separate `list_recipes` (no limit).

**Takeaway:** when an LLM-with-tools agent says "I can't find X," suspect the *tool's filters and
which tool it chose*, not the storage layer. A 10-second deterministic audit (grep the embedded
file, run Load-validation) settled "is the data there?" instantly each time and refocused us on
the real bug. Also: an agent will confidently *rationalize* a gap ("non mi risulta sincronizzato")
— that inference is a symptom, not a diagnosis.

---

## Safety filters must fail CLOSED (the fail-open allergen bug)

**2026-06-30.** A three-model adversarial review caught a genuinely dangerous bug before it
shipped: in the recipe engine, an ingredient that didn't resolve against the food catalog was
skipped *silently*, dropping its allergens from the recipe's derived set. So a recipe whose
lactose came from a mis-typed/renamed ingredient would present as **lactose-free** and be
suggested to the lactose-intolerant family member. Latent (all ids resolved at the time) but
unguarded. Fix: `Load()` now validates every recipe/meal reference against the catalog (startup
error), and `Suggest` **fails closed** — a recipe with any unresolved ingredient is never offered
while an allergen exclusion is active.

**Takeaway:** for a safety filter, "unknown" must be treated as "unsafe," never as "absent."
Absence of a detected allergen is indistinguishable from a broken lookup — so make the broken
lookup loud (fail startup) *and* the runtime path conservative (exclude on uncertainty).

---

## Design the schema from real data, not upfront

**2026-06-30 → 07-01.** We didn't design the recipe schema in the abstract. Max pasted real
family recipes one at a time, and each surfaced exactly one new modeling need:
- all-grams breakfast → the base shape
- "250g riso" → **raw-vs-cooked** ambiguity (solved by encoding state in the food id:
  `riso_integrale_crudo` vs `_cotto`)
- "1 cucchiaio d'olio" → a **measures table** (non-gram units → grams, per-food overrides)
- tonno/mozzarella *delattosata* → **lactose-free variants tagged milk-only**
- "aggiungi pomodori e frutta" → a **meal = N recipes + M foods** composition layer
- "53% adulti / 47% bambini" → a **family portion-split** on the meal total
- "non faccio l'insalata di riso d'inverno" → **soft seasonality**

Each of these would have been guessed wrong on a whiteboard. **Takeaway:** for data modeling,
five concrete examples beat an hour of abstract schema design. Keep an executable spec
(`scripts/recipe_macros.py`) alongside so "does the schema actually work?" is one command.

---

## Parallelize research, keep integration deterministic

**2026-07-01.** To compose ~70 remaining recipes we fanned out an 83-agent Workflow: one agent
per dish, each web-researched a reference and returned a structured recipe mapped to the catalog
(+ new-food specs). That's the part where LLM judgment scales. The *integration* stayed
deterministic and mine: canonicalize the 65 messy new-food names into ~44 unique ids (agents
named "salsiccia" four different ways), resolve each against USDA SR Legacy with the existing
picker, insert recipes, and lean on `Load()` validation + an allergen audit as the safety net.

**Takeaway:** let agents do the fuzzy, parallel, judgment-heavy work; do the correctness-critical
merge yourself with deterministic validation. 83 agents proposed; one deterministic pipeline
verified.

---

## Pitfalls & Gotchas

- **`git checkout -b` inside a compound command silently didn't switch branches** (interaction
  with the main-branch-guard hook), so the follow-up `git commit` fired *on main* and was
  refused. Run `git checkout -b <name>` as its **own** command, verify the branch, *then* commit.
  Hit this ~4 times before internalizing it.
- **Exact-count test assertions rot as data grows.** `Len() == 82`, `Suggest returns 4`,
  `categoria piatto-unico == 2` all broke the moment the catalog/book grew. Rewrote them as
  invariants: lower bounds (`>= 150`), "every result matches the filter," "ordering is
  monotonic," or controlled in-package fixtures. Assert the *property*, not the *count*.
- **Build-time data guards need real-edge-case tuning.** An "energy must be present" guard
  rejected *salt* (0 kcal, only sodium). A different guard was needed to catch *raisins* whose
  sugar field was empty in USDA and got silently zero-filled. Validate embedded data at build
  time, but a guard too blunt is as bad as none.
- **Config vocab mismatch can silently disable a safety filter.** The exclusion default was the
  English `lactose` while every UI string said `lattosio`; an operator setting
  `CADENZA_MEAL_EXCLUDE_ALLERGENS=lattosio` would have matched nothing and turned the lactose
  filter *off* with no error. Fixed with Italian→English synonym normalization + lactose as a
  non-removable baseline.
- **Upstream data licensing:** the exercise dataset was almost certainly scraped from a
  commercial app — "non-commercial" is the repo author's assertion, not a right that flows to us.
  We kept the honest posture (personal-use tolerance, private/no-rehost, attribution in NOTICE)
  rather than pretending the license was clean. For the food data we deliberately chose USDA FDC
  (CC0, public domain) so the numbers are unimpeachable.

---

## Best Practices Discovered

- **Second/third opinion before building the expensive thing.** Three isolated models
  (Claude+Gemini+DeepSeek) unanimously killed an over-engineered GCS+signed-URL media design in
  favor of a lazy Telegram `file_id` cache — before a line of the bucket infra was written.
- **Adversarial multi-agent review with deterministic verification** (find → refute → confirm)
  found the one real safety bug (fail-open allergens) amid mostly-refuted noise. Worth it for
  anything touching a safety guarantee.
- **A deterministic audit script is the fastest way to end a "is the data wrong?" debate.** One
  Python pass over the embedded catalog ("any recipe with lactose? any implausible kcal? any
  unresolved ingredient?") repeatedly proved the data was fine in seconds.
