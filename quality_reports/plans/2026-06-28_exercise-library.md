# ABOUTME: Plan to integrate a home-strength exercise library into Cadenza's coach
# ABOUTME: Two phases: embedded catalog + search tool (A), then GIF delivery via Telegram (B)

# Exercise Library Integration

## Goal

Give Cadenza's coach a concrete, named library of exercises it can prescribe for
(1) prevention/core/mobility (already mandated by the system prompt), (2) injury
rehab (band work), and (3) **progressive strength for arms / legs / back** (new goal,
2026-06-28). Source: `hasaneyldrm/exercises-dataset` (1324 exercises, IT instructions
already present, animation GIFs + thumbnails).

## Source data

- Upstream: https://github.com/hasaneyldrm/exercises-dataset
- License: educational / non-commercial only; media belongs to copyright holders.
  Cadenza is single-athlete, personal, non-commercial → compatible. Mitigation:
  private GCS bucket + signed URLs (no public re-hosting), attribution in NOTICE.
- Catalog subset (equipment available at home): body weight, band, resistance band,
  dumbbell, kettlebell, stability ball, medicine ball, roller, bosu ball, wheel roller.
- **775 exercises**, all with complete IT instructions. GIF total 72.5 MB.
- Coverage: abs 120, pecs 98, biceps 91, delts 86, triceps 78, glutes 77,
  upper-back+lats 79, quads+hams 40, calves 26.

## Decisions

| # | Decision | Choice | Rationale | Revisit if |
|---|----------|--------|-----------|------------|
| 1 | Coach access | Tool + explicit prompt | Matches existing tool-loop; prompt steers coach to use library instead of inventing | Coach ignores it |
| 2 | Media | GIF via Telegram | Max wants visual demos | Hosting cost/license becomes a problem |
| 3 | Equipment scope | bodyweight+band+dumbbell+kettlebell+ball/roller/bosu (775) | Full home kit Max owns | Equipment changes |
| 4 | Catalog storage | `//go:embed catalog.json` | 775 trimmed records ~0.5-0.7MB, fine in binary; deterministic, no runtime dep | Catalog grows >few MB |
| 5 | Media hosting | NONE — upstream raw GitHub URL (pinned SHA) on first send, Telegram returns a `file_id`, cache it in Firestore | 3-reviewer consensus: GCS/signed-URL is over-engineered. We don't even re-host: point at upstream's own public URL; Telegram CDN caches it; subsequent sends use the cached file_id (instant, zero network). No bucket, no Terraform, no signed URLs, no media in repo or container. | Upstream repo disappears (cached file_ids survive) |
| 6 | Delivery mechanism | code-owned post-processing, NOT a mid-loop action-tool | 3-reviewer consensus: a side-effect tool pushes the GIF mid-loop with non-deterministic ordering vs the text. Cadenza's philosophy: side effects belong to code. Coach appends `@demo: <id>,<id>`; code strips it, sends text+verdict first, then the animations. | — |
| 8 | Search vocabulary | expose valid muscle/equipment tokens in the tool description; handler normalizes | Coach thinks in Italian ("schiena"); without a controlled vocabulary the substring match returns empty and the coach silently invents exercises (tool looks like it works but doesn't). | — |
| 9 | Default equipment | `CADENZA_DEFAULT_EQUIPMENT` env (empty = full kit) fed into the profile prefix | DeepSeek: avoids restating the kit every conversation. ~15 lines; per-day conversational override still wins. | — |
| 7 | Equipment availability | conversational, no new persistence | Athlete states the day's kit; coach filters. `equipment` param is a LIST; bodyweight always allowed as floor | Athlete wants a saved default kit |

## Equipment selection (2026-06-28)

The athlete can constrain a session to the equipment available that day
("oggi ho solo gli elastici"). Implemented as part of the search tool, no new state:
- `search_exercises.equipment` accepts an **array** of equipment types.
- Coach default: full home kit (decision 3). When the athlete names available
  equipment, the coach restricts the filter to that list, **plus body weight**
  (always doable). The prompt rule makes this behavior explicit.
- A persisted default kit (athlete.yaml) is deferred: not needed for the per-day ask.

## Phasing

Two independently-shippable phases. Ship A first (low-risk, immediately useful,
text-only). B adds the media Max asked for but carries the infra + license weight.

### Phase A — Embedded catalog + search tool (no infra)

Files:
- `scripts/build_exercise_catalog.py` (uv/PEP-723) — reads upstream json, filters to
  the 10 equipment types, trims to needed fields, writes `internal/exercises/catalog.json`.
  Committed for reproducibility. Records the upstream commit SHA in a manifest.
- `internal/exercises/catalog.json` — generated, embedded.
- `internal/exercises/catalog.go` — `//go:embed`; `Exercise` struct
  (ID, Name, Equipment, Target, BodyPart, Secondary, InstructionsIT, StepsIT, GIF);
  `Catalog.Search(SearchFilter) []Exercise` where `SearchFilter` carries
  `Target string`, `Equipment []string` (empty = any), `Query string`, `Limit int` —
  deterministic filter + rank (exact-target > body_part > name substring).
- `internal/exercises/catalog_test.go` — load count, search by target, equipment
  list filter (e.g. only band+bodyweight), IT non-empty, deterministic ordering.
- `internal/exercises/NOTICE` — attribution + license note.
- `internal/job/coach.go` — new read-only tool `search_exercises`
  `{target?, equipment?: array, query?, limit?}`; handler returns JSON with the
  data-not-instructions framing. Add `Catalog` field to `Coach` (nil hides tool).
- `internal/agent/coach.go` — extend `coachSystem`: (a) when prescribing
  prevention/strength/rehab work, query `search_exercises` and prescribe named
  exercises from it rather than inventing; (b) honor stated equipment availability
  (restrict `equipment` to the day's kit + body weight); (c) concurrent-training
  caveat (strength must not compromise the endurance plan — after key sessions /
  on easy days).
- `cmd/cadenza/*` — wire the catalog into the Coach.

Verify: `make check && make test` green; manual: ask coach "dammi 3 esercizi per la
schiena con manubri" → returns named catalog exercises.

### Phase B — GIF delivery via Telegram (NO infra, post-review)

Depends on A. The whole GCS apparatus is gone. Files:
- `internal/exercises/media.go` — `GIFSourceURL(ex)` builds the upstream raw GitHub
  URL at the pinned SHA: `https://raw.githubusercontent.com/hasaneyldrm/exercises-dataset/<SHA>/videos/<gif>`.
- `internal/store/media_cache.go` — `MediaCache` (Firestore collection `exercise_media`):
  `Get(ctx, exerciseID) (fileID string, ok bool, err error)`, `Set(ctx, exerciseID, fileID)`.
  Same minimal pattern as `dedup.go`.
- `internal/telegram/sender.go` — `SendAnimation(ctx, source, caption string) (fileID string, error)`
  via `bot.SendAnimationParams{Animation: &models.InputFileString{Data: source}}`; `source`
  is a file_id OR a URL (Telegram accepts both); returns `msg.Animation.FileID`.
- `internal/job/coach.go` —
  - `extractDemos(text) (clean string, ids []string)`: strips a trailing `@demo: id,id`
    line, returns the ids (cap to e.g. 4 per turn).
  - `converse` strips the annotation from the reply; `Converse` (Telegram path) sends the
    text+verdict first, then for each id: cached file_id if present else the GitHub URL,
    sends animation, persists the returned file_id.
  - New `Coach` fields: `Catalog *exercises.Catalog`, `MediaCache MediaCacheStore`,
    `Animator Animator` (all nil-safe; nil hides demos and/or the search tool).
- `internal/agent/coach.go` — prompt: when a visual demo helps a prescribed exercise,
  append `@demo: <id>` with ids from `search_exercises`.
- `cmd/cadenza/main.go` — wire `Catalog`, `store.NewMediaCache`, `Animator: sender`.
- Tests: fake Animator + fake MediaCache; assert ordering (text before GIF), file_id
  caching (URL first time, file_id second), annotation stripping, per-turn cap.

Verify: ask coach "fammi vedere come si fa il goblet squat" → text arrives, then GIF.

### Pre-seed (optional, deferred)

Not needed: the lazy URL→file_id cache warms itself on first real use. A
`scripts/preseed_media.py` that bulk-warms file_ids is possible later but would spam a
chat with 775 GIFs; skip unless first-send latency becomes a problem.

## Risks

- License: non-commercial media. Mitigated by private bucket + signed URLs +
  attribution. If Cadenza ever monetizes, media must be removed/relicensed.
- GIF size (72.5MB): trivial GCS cost (~$0.002/mo) + egress per send (negligible).
- Concurrent training: strength load can blunt endurance adaptation in masters.
  Handled in prompt (decision 1), not data.
- Prompt bloat: catalog is NOT in the prompt (decision 4); only a short usage rule.

## Open questions

- None blocking. Phase B bucket: Terraform vs one-off gcloud — follow existing
  `deploy/` convention (check before implementing).
