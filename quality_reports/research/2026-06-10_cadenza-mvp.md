# ABOUTME: Pre-plan research report for the cadenza MVP: five tracks, adversarially verified, with complexity verdict
# ABOUTME: Produced 2026-06-10 by an 11-agent research workflow (research + per-track fact-check + synthesis)

# Cadenza pre-plan research report (2026-06-10)

## Executive summary

The decided stack survives verification intact: Go on Cloud Run, Firestore Native, Telegram webhook bot, hand-rolled Anthropic tool loop, native intervals.icu REST client, Scheduler plus Tasks. Nothing needs replacing. Two findings force design amendments. First, Cloud Run request-based billing throttles CPU to near zero after the response, so the Telegram webhook cannot ack 200 and continue the agent loop in a goroutine; the handler must ack fast and re-enqueue work via Cloud Tasks, or the service moves to instance-based billing. Second, prompt-cache minimums were corrected by verification (Opus 4.8: 1,024 tokens; Haiku 4.5: 4,096), so the Haiku morning-check prompt will likely silently fail to cache. The intervals.icu silent step-drop quirk is empirically confirmed; the planned read-back repair layer is validated, with `resolve=true` as the verification primitive. Biggest reuse win: seed the REST client from MAx's own Apache-2.0 `intervals-icu-mcp` `internal/icu` package. GCP cost is effectively $0-1/month; the Anthropic API is the only real spend.

## Track 1: intervals.icu API

Verifier: 12/14 confirmed against the live OpenAPI spec (all 25 Wellness, 15 Activity, 7 Interval fields checked mechanically), 2 corrected for precision.

**Confirmed.** Basic auth, username literal `API_KEY`, athlete id `0` = key owner. Base `https://intervals.icu/api/v1`, live spec at `/api/v1/docs`. `ctl`/`atl`/`rampRate` live on the wellness record, so one cheap endpoint serves both the morning HRV poll and the ramp-rate detector. Rate limits 30/s and 132/10s are forum-sourced (developer reply, not contractual): keep the throttle configurable.

| Purpose | Endpoint |
|---|---|
| Wellness range | `GET /athlete/{id}/wellness?oldest&newest&fields` (fields filter also drops nulls) |
| Wellness day write | `PUT /athlete/{id}/wellness/{date}` (partial merge) |
| Wellness bulk | `PUT /athlete/{id}/wellness-bulk` (id = local ISO date) |
| Activities | `GET /athlete/{id}/activities?oldest&newest&limit&fields` |
| Activity detail | `GET /activity/{id}?intervals=true` |
| Events CRUD | `GET\|POST /athlete/{id}/events`, `GET\|PUT\|DELETE .../events/{eventId}` |
| Bulk upsert | `POST /events/bulk?upsert=true` (caveat below) |
| Parse verification | `GET /events?resolve=true` (targets resolved to watts/bpm/m/s); `ext=zwo\|mrc\|erg\|fit` |

**Corrected.** (1) Bulk upsert matches `external_id` only for events created by the same OAuth application; behavior under plain API-key auth is unverified (`upsertOnUid` is the alternative). (2) Date-range `PUT /events` only toggles `hide_from_athlete`/`athlete_cannot_edit`; it is not general update. (3) `Warmup` is a section header or free text; the deterministic per-step mechanism is `intensity=warmup|cooldown|recovery`. Distrust zonepace.cc (lost two disputes against developer forum posts).

**Pitfalls.** Malformed DSL lines silently demote to text cues or drop from load; `m` = minutes, never meters (`mtr`/`km` for distance); repeats need a bare `Nx` line with blank lines around it, no nesting; all dates athlete-local with no timezone suffix; events list defaults to today+6d if range omitted; null fields are omitted entirely (pointer-field structs mandatory); wellness PUT is partial merge, clearing values is awkward; `workout_doc` is untyped in the spec: write via description DSL, read `workout_doc` only for verification.

**Recommendation.** Five resource groups (wellness, activities, events, athlete/sport-settings for real FTP/LTHR/zone boundaries, load metrics off wellness). Token bucket ~10 req/s plus 429 backoff. Workout writes: emit DSL, POST, immediately GET back with `resolve=true`, structurally diff step count/durations/targets, regenerate on mismatch.

## Track 2: Claude API + Go SDK

Verifier: 11/14 confirmed to the symbol level against docs and SDK source; 3 corrected (all traced to a stale skill file, live docs win).

| Model | In/Out $/MTok | Cache write 5m/1h | Cache read | Ctx / max out | Min cacheable prefix (corrected) |
|---|---|---|---|---|---|
| Opus 4.8 | 5 / 25 | 6.25 / 10 | 0.50 | 1M / 128K | 1,024 |
| Sonnet 4.6 | 3 / 15 | 3.75 / 6 | 0.30 | 1M / 64K | 1,024 |
| Haiku 4.5 | 1 / 5 | 1.25 / 2 | 0.10 | 200K / 64K | 4,096 |

**Confirmed.** Manual tool-loop surface verified in SDK source: `ToolParam`/`ToolUnionParam`, `resp.ToParam()`, `block.AsAny()` type switch, `NewToolResultBlock(id, content, isError)`, loop on `StopReasonToolUse`. SDK auto-retries 2x (connection, 408/409/429/5xx) honoring `retry-after`. v1.50.1 current, minimum Go 1.24 (docs page saying 1.23 is stale). Stop reasons to handle: `end_turn`, `tool_use`, `max_tokens`, `pause_turn` (re-send as-is), `refusal` (never auto-retry), `model_context_window_exceeded`. Opus 4.8 rejects non-default `temperature`/`top_p`/`top_k`, `budget_tokens` (use `thinking:{type:"adaptive"}` + `output_config.effort`), and assistant prefills; Haiku 4.5 supports neither adaptive thinking nor effort: keep tier request builders separate. The 4.7+ tokenizer yields ~30% more tokens; baseline budgets with `CountTokens` per model. Tier 1 rate limits dwarf single-athlete volume; cache reads don't count toward ITPM.

**Corrected.** (1) Cache minimums as in the table (the "4096 everywhere" claim was stale; Haiku's real 4,096 is the one threatening the cheap tier). (2) `tool_choice: none` takes no `disable_parallel_tool_use` (auto/any/tool do). (3) TTL type is `CacheControlEphemeralTTL`, constant `CacheControlEphemeralTTLTTL1h`.

**Recommendation.** Manual loop on `Messages.New` (~30 lines, gives the validation/repair and logging hooks). Two builders: Haiku 4.5 for morning checks/notifications, Opus 4.8 (adaptive thinking, effort high) for deep-dive/injury mode. Skip Sonnet and streaming in v1; MaxTokens ≤16K non-streaming. Cache layout: frozen system prompt + static sorted tool list, breakpoint, athlete profile, breakpoint, volatile data in user turn; marshal structs, never `map[string]any` (random key order breaks byte-exact prefix match). Verify `CacheReadInputTokens` in logs instead of assuming savings (scale-to-zero means cold caches across sessions). Hard iteration cap (~10); every `tool_use` id gets exactly one `tool_result`, failures as `is_error: true`.

## Track 3: Telegram bot

Verifier: 12/13 confirmed verbatim against Bot API docs and library source; 1 corrected (immaterial).

| Library | Status | Verdict |
|---|---|---|
| go-telegram/bot | v1.21.0 (2026-05-22), Bot API 10.0, zero deps, webhook + middleware | Use |
| telebot | v4 beta-only since Oct 2024; stable head v3.3.8 Aug 2024 (corrected from v3.3.6), ~2 years stale | Avoid |
| go-telegram-bot-api | v5.5.1 Dec 2021, dormant | Avoid |

**Confirmed.** `setWebhook` `secret_token` echoed in `X-Telegram-Bot-Api-Secret-Token`; `WithWebhookSecretToken` validation verified in library source. Retry schedule deliberately undocumented; undelivered updates kept ≤24h. 4096-char message limit. MarkdownV2 needs 18 characters escaped (fatal for pace/percentage-heavy coaching text); HTML needs only `&` `<` `>`. `callback_data` 1-64 bytes; `answerCallbackQuery` mandatory or the spinner hangs. Bots can't initiate: persist `chat_id` at `/start`. Voice download ≤20 MB via `getFile` (mime_type not contractually OGG/Opus; don't hard-assert).

**Pitfalls.** `WebhookHandler()` acks 200 then processes on a background goroutine: deadly under request-based CPU (see Track 4). Dedupe by `update_id` in Firestore before any side effect. `From.ID` allowlist middleware mandatory, rejects must still return HTTP 200 (else 24h of retries). No tables render in any parse mode: bold-label key-value lines, `<pre>` only for short blocks. Design multi-message output (verdict + inline-button drill-down) instead of splitting blobs; a split inside an HTML tag 400s the whole message.

## Track 4: GCP serverless

Verifier: all 14 facts confirmed, zero corrections; the strongest track.

**Confirmed.** Cloud Run timeout configurable to 60 min (600s is plenty); request-based billing disables/severely limits CPU post-response, docs say avoid background threads entirely; instance-based requires ≥512 MiB. Image size doesn't affect cold start (image streaming); startup CPU boost available. Scheduler: OIDC SA with `roles/run.invoker`, audience = service URL without query params, `--time-zone=Europe/Rome`, and raise `--attempt-deadline` (default ~180s) above handler duration or Scheduler marks long morning checks failed and retries them mid-flight. Cloud Tasks: 30-day horizon covers the day-5 physio timer; named tasks (`injury-<id>-day5`) give dedup with a 24h tombstone after deletion, so cancel-and-recreate needs a new name suffix; dispatch deadline 15s-30m. Firestore Native mode, free tier 50k reads/20k writes/day (cadenza uses ~1%); emulator is in-memory and does not enforce composite indexes (deploy `firestore.indexes.json`, smoke-test real queries once). Secret Manager: pinned-version env vars, never `latest` (a bad version bricks every cold start); $0.06/version/month is per replication location, so single-region. Deploy: GitHub Actions WIF (`auth@v3` + `deploy-cloudrun@v3`). No VPC needed for either external API. Cap `max-instances` at 1-2 so retry storms can't fan out into parallel Anthropic spend.

## Track 5: Prior art

Verifier: 11/14 exact; 3 corrections, all overstatements (community evidence upgraded to stronger categories than warranted).

**Confirmed.** No mature importable Go client exists; closest is GPL-3.0 `tgrangeray/go-intervals` (license disqualifies copying). MAx's `maroffo/intervals-icu-mcp` (Apache 2.0) contains the needed client: Basic auth, 3 req/s `x/time/rate` limiter, 429/5xx retry with Retry-After, fitness series; it lives under `internal/`, so extract/copy rather than import. `icuvisor` (MIT, active) is a good error-shaping reference. Steps are generated only by parsing `description` (empirically confirmed: JSON `steps` silently ignored, event created without steps). mvilanova #89: trim/project tool results before they reach the model (event-creation echoes are huge). Strava-routed activities come back empty via the API: athlete devices must sync to intervals.icu directly. Montis.icu independently validates the arithmetic-in-Go rule ("any gap it just makes up an answer"). derrix060 lessons: 4096 splitting, container TZ must match athlete TZ, fresh-session fallback.

**Corrected.** (1) "No workout_doc write path" is unproven, not refuted: the field exists in the schema, no success is demonstrated; treat description DSL as the working assumption, not an invariant. (2) The library-workout UI caching issue is a single unacknowledged intermittent report, not a known bug (still supports read-back verification). (3) Montis "largest community AI coach" is unsupported (and it pairs with ChatGPT). (4) CTL/ATL: TrainingPeaks confirms tau 42/7 and TSB = CTL minus ATL but not the exponential form; coefficient variants differ <1.5%, so any local implementation must be golden-tested against actual intervals.icu output, or simply read `ctl`/`atl`/`rampRate` from the wellness endpoint.

## Ecosystem solutions (reuse, don't hand-roll)

| Need | Solution |
|---|---|
| intervals.icu client | Extract/extend `internal/icu` from maroffo/intervals-icu-mcp (Apache 2.0); cross-check icuvisor (MIT) |
| Telegram | github.com/go-telegram/bot |
| Agent loop | anthropics/anthropic-sdk-go v1.50.1+, manual loop, no framework |
| Persistence | cloud.google.com/go/firestore v1.22.0 + emulator for tests |
| Timers/cron | Cloud Tasks (named tasks, OIDC) + Cloud Scheduler |
| Rate limiting | golang.org/x/time/rate |
| Deploy | google-github-actions auth@v3 + deploy-cloudrun@v3 (WIF, keyless) |
| Load metrics | Read ctl/atl/rampRate from wellness; tiny EWMA package only for forward projections, golden-tested vs the API |

## Top pitfalls

1. **Silent workout step drops.** POST never errors on bad DSL. Mitigation: read back the created event (`resolve=true`), diff `workout_doc` step count/durations/targets against intent, regenerate on mismatch.
2. **Cloud Run CPU throttling kills post-ack work.** Mitigation: webhook acks fast, agent loop runs in a second request via Cloud Tasks (or instance-based billing); never post-response goroutines.
3. **DSL unit trap: `m` = minutes.** A model-emitted "400m" rep becomes 400 minutes. Mitigation: the model proposes structured steps; Go renders the DSL deterministically with unit tests. Never let the model write DSL text.
4. **At-least-once delivery everywhere** (Telegram retries, Scheduler, Tasks). Mitigation: `update_id` dedup doc, deterministic Firestore IDs (`wellness/2026-06-10`), named tasks; every proactive handler idempotent.
5. **Haiku cache silently no-ops under 4,096 tokens.** Mitigation: check `CacheCreationInputTokens > 0` once, or skip `cache_control` on the cheap tier.
6. **Formatting 400s.** MarkdownV2 escaping mangles paces/percentages; one bad HTML entity rejects the whole message. Mitigation: HTML mode + `html.EscapeString`, plain-text fallback retry, multi-message design.
7. **Tool-loop wedging.** Missing `tool_result` for any `tool_use` id rejects the request. Mitigation: always emit results, failures as `is_error: true`; iteration cap; handle `pause_turn`/`refusal` explicitly.
8. **Null vs zero.** intervals.icu omits null fields. Mitigation: pointer-field structs throughout the client; a missing HRV must not read as 0.

## Open questions for the plan

1. Webhook execution model: request-based billing with ack-then-Cloud-Tasks re-enqueue (research recommendation) vs instance-based billing keeping the agent loop in-request. Affects handler design, timeout settings, and Scheduler attempt-deadline.
2. Does `POST /events/bulk?upsert=true` match `external_id` under plain API-key auth, given the spec's "created by the same OAuth application" constraint? Needs a live probe; fallback is `upsertOnUid` or GET-then-PUT on single events.
3. CTL/ATL/ramp-rate strategy: read `ctl`/`atl`/`rampRate` from the wellness endpoint only, or also implement a local EWMA package for forward projections (what-if replanning)? If local, golden tests must pin to actual intervals.icu /fitness output, not the TrainingPeaks formula.
4. Prompt caching on the Haiku tier: pad the morning-check prefix past 4,096 tokens, or skip `cache_control` on the cheap tier entirely and cache only the Opus deep-dive path?
5. Conversation/session state persistence in Firestore: raw SDK message JSON restored via `param.SetJSON`, or a neutral cadenza-owned schema rebuilt into params? Affects memory design and SDK upgrade resilience.
6. Reuse mechanics for `maroffo/intervals-icu-mcp` `internal/icu`: copy into cadenza, or extract into a shared importable module maintained across both projects?
7. Workout generation contract: confirm the model outputs structured steps that Go renders into DSL (never raw DSL text), and define the canonical step schema the validation layer diffs against.
8. Morning check timing and timezone: confirm Europe/Rome for Scheduler jobs and that wellness data (HRV sync from the athlete's device) is reliably present by the chosen cron hour.

## Complexity verdict: complex

Greenfield service with six interacting subsystems (REST client, agent loop, Telegram webhook, Firestore persistence, scheduler/tasks handlers, workout validation layer), far more than 5 files, plus cross-cutting concerns (idempotency, billing-mode constraint on the webhook path, tiered prompting). Each piece individually has prior art, but their integration does not, and several decisions (upsert auth semantics, billing mode) are genuinely open. Extended flow applies: research file, second opinion on approach, annotation cycle.

---

## Appendix: per-track verification assessments

**intervals-icu-api.** Highly trustworthy track: 12/14 facts confirmed against primary sources, 2 corrected only for material precision, zero fabrications. Endpoint paths, parameter descriptions, and every claimed schema field (25 Wellness + 15 Activity + 7 Interval fields) verified mechanically against the live OpenAPI spec downloaded 2026-06-10. The two corrections matter for planning: upsert semantics under plain API-key auth need a live probe (`upsertOnUid` is the alternative), and date-range `PUT /events` cannot edit workouts. `workout_doc` is untyped in the spec, so the validation layer must verify via round-trip (`resolve=true`, `ext=`), which is confirmed to exist. Rate limits are forum-sourced: keep the client throttle configurable. Distrust zonepace.cc on format details.

**claude-api-go.** Trustworthy with two corrections applied. 11/14 confirmed verbatim against platform.claude.com, the Messages API reference, GitHub releases, and the SDK source tarball; Go SDK surface claims accurate to the symbol level. Corrections: real cache minimums are 1,024 (Opus 4.8, Sonnet 4.6) and 4,096 (Haiku 4.5); `tool_choice: none` takes no `disable_parallel_tool_use`; TTL type is `CacheControlEphemeralTTL` / `CacheControlEphemeralTTLTTL1h`. Systemic: every error traced to a stale local skill snapshot, not live docs; treat skill-sourced numeric limits as hints to re-verify. Nuances: sampling params 400 only on non-default values; effort defaults to high on Opus 4.8 (set explicitly); Go minimum is 1.24 per go.mod.

**telegram-go.** Trustworthy. 12/13 confirmed against the Bot API page and go-telegram/bot source (down to webhook_handler.go and go.mod). One correction: telebot stable head is v3.3.8, not v3.3.6; conclusion unchanged. Soft spots: open-issue counts are point-in-time; Voice mime_type is optional; per-chat 1 msg/s is guidance, not contract; MarkdownV2 escaping is context-dependent though escaping all 18 chars is the safe rule.

**gcp-serverless.** Trustworthy. All 14 facts traced to primary sources with every number, flag name, and verbatim quote holding up, including the "disabled or severely limited" CPU phrasing, the [15s, 30m] dispatchDeadline interval, the 24h named-task tombstone, firestore Go client v1.22.0, and the OIDC audience-without-query-params rule. Nuances: Secret Manager $0.06/version/month is per replication location (prefer single-region, ≤6 active versions for free tier); quoted Cloud Run prices are for base-price regions (europe-west1 qualifies); Cloud Tasks bills create and dispatch separately (irrelevant at this volume).

**prior-art.** Highly trustworthy. 11/14 exact, including license SPDX IDs, star/commit counts, issue numbers and dates, release tags, function names verified in cloned source, and the exact `defaultRatePerSec = 3` constant in maroffo/intervals-icu-mcp. Three corrections, all overstatements of community evidence ("confirmed"/"known bug"/"largest" downgraded). Plan implications unaffected: description-DSL-only workout creation and read-back verification remain justified; CTL/ATL must be golden-tested against actual intervals.icu output if implemented locally.
