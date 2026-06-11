# ABOUTME: Implementation plan for cadenza MVP: AI endurance coach, Go on Cloud Run, Claude tool loop
# ABOUTME: 7 milestones, deterministic-skeleton-first; 25 decisions; safety logic in Go outside the model

# Cadenza MVP: AI Endurance Coach (Go / Cloud Run / Claude)

## Context

Personal AI endurance coach for one athlete (MAx), distilled from 5+ weeks of real coaching chats into `docs/spec/coach-system-prompt-v1.md`. Service combines training science, intervals.icu biometrics, real-life context into adaptive coaching via Telegram. Safety-adjacent: prescribes load, handles injury escalation, runs autonomously at 07:00 with nobody watching. Core rule: safety logic deterministic in Go, model does judgment only.

Pre-work done: requirements refined (7 decisions), 11-agent verified research (`quality_reports/research/2026-06-10_cadenza-mvp.md`), complexity = complex, second opinions (Gemini isolated + fresh Claude) produced 7 safety amendments, all adopted. Repo bootstrapped: github.com/maroffo/cadenza (private), main+dev, Makefile gates active at `go mod init`.

## Decisions

| # | Decision | Choice | Rationale | Revisit if |
|---|----------|--------|-----------|------------|
| 1 | Scope v1 | Single athlete, no tenancy | It's MAx's coach; fastest to value | Second user appears |
| 2 | Interface v1 | Telegram bot only; webapp phase 2 | Coaching happens on phone; push built-in | Dashboard need proven |
| 3 | Runtime | Go on Cloud Run, scale-to-zero, request-based billing | Event-driven shape, ~zero cost, his stack | Sustained traffic |
| 4 | Integration | NO MCP; Messages API tool use + native REST client | We own the loop; fewer moving parts | Claude Desktop access wanted |
| 5 | Store | Firestore Native only | Zero-ops, free tier 100x headroom, emulator; SQLite split-brains on deploy overlap | Relational analytics needed |
| 6 | Models | Haiku 4.5 morning/notify; Opus 4.8 + adaptive thinking deep-dive/injury | Cost/quality tiering | Quality gap in morning narrative |
| 7 | Webhook model | Thin enqueuer, ack 200, Cloud Tasks re-enqueue; ONE executor endpoint, two front doors (Tasks+Scheduler) | Durability not billing: instance-billing still SIGTERMs post-ack work; Tasks = tracked retries | Never |
| 8 | icu client | COPY internal/icu from intervals-icu-mcp (663 LOC + 1246 test) | No second consumer; divergence expected | Both repos need same fix twice |
| 9 | Load metrics | Read ctl/atl/rampRate from wellness endpoint | Don't reimplement; golden-test trap avoided | What-if projections needed (then EWMA golden-tested vs API) |
| 10 | Caching | None on Haiku (single-shot at 07:00, nothing reads within TTL); Opus caches system+tools+profile prefix (1024 min) | Verified vs CacheReadInputTokens, not assumed | Multi-turn morning flows |
| 11 | Conversation state | Cadenza-owned schema, turns subcollection, schema:1, fresh-session fallback on ANY load error | SDK JSON churn bricks sessions; 1MiB doc limit | Never |
| 12 | Workout writes | Model emits STRUCTURED steps, Go renders DSL, POST, read-back resolve=true, structural diff, max 3 writes, then surface failure w/ plain-text plan | Silent step-drop confirmed; m=minutes trap; model never writes DSL text | workout_doc write path proven |
| 13 | SafetyGate | Two tiers: code ceilings (immutable) + Firestore tunables that can only TIGHTEN; verdict caps folded in; clamp cosmetic / reject substantive (2 regen) / block+ask athlete; fail toward less load | Model has a mutation tool; data layer must not loosen deploy-gated limits | Never |
| 14 | Memory | Append-only profile_events w/ provenance + seq; ALL mutations one-tap confirmed; >10% safety-field delta = warning line; snapshot fold-on-write; reconcile detects drift | Model writes its own future system prompt = top corruption channel | v1.1 may auto-commit non-safety fields |
| 15 | Verdict | Go-computed go/modify/skip; injected in prompt AND appended to outgoing message by composer | Model can argue, cannot suppress | Never |
| 16 | Absence detection | Watchdog job +15min checks run doc; degraded templates (LLM down / icu down / both); Monitoring log alert to EMAIL (never the bot) | Silent absence = missed escalation; alert channel must differ from monitored channel | Never |
| 17 | Escalation truth | Injury log in Firestore is truth; named tasks `injury-<id>-day<N>-r<rev>` are wake-ups; daily reconcile heals lost timers | 24h tombstone can eat tasks; lost timer degrades to 1-day delay not silent never | Never |
| 18 | Concurrency/spend | Tasks queue max-concurrent-dispatches=1, max-attempts=5; max-instances=1; update_id dedup; deterministic ids everywhere; Anthropic console spend cap | At-least-once delivery everywhere; retry storms must not fan out spend | Webhook ack collisions (then max-instances=2) |
| 19 | Build order | Deterministic skeleton FIRST (M1-M3 no LLM), then Haiku, Opus, writes, injury | Degraded mode = proven infra not aspiration | Never |
| 20 | Telegram | go-telegram/bot v1.21+; HTML mode + EscapeString; multi-message design; allowlist From.ID returns 200; dev bot long-polls | MarkdownV2 escaping fatal for pace text; 4096 limit | Never |
| 21 | Schedule | Morning 07:00 Europe/Rome, watchdog 07:15, reconcile 12:00; +45min self-retry x2 if HRV missing | MAx choice; sync margin | HRV-null rate high in burn-in |
| 22 | GCP | NEW dedicated project, europe-west1 | Blast radius, own spend view | Never |
| 23 | Profile seed | Hybrid: script computes baselines from icu wellness 30/60d + sport settings; YAML for goals/injuries/medical/patterns | Reality-grounded baselines, minimal typing | Never |
| 24 | Infra tool | Idempotent `deploy/setup.sh` (gcloud, describe-before-create), no Terraform | ~15 one-time resources, 1 env, no state babysitting; commands = runbook | 2nd env/service |
| 25 | Spec drift | Revise system prompt WORKOUT DELIVERY section: DSL mechanics moved to renderer; prompt says "emit structured steps via tool" | Stale rules waste cached tokens, instruct dead write path | n/a |
| 26 | Workout write path (supersedes the DSL-only assumption in 12) | JSON-first: structured steps marshal into workout_doc (hr_zone units), external_id upsert for idempotency, read-back resolve=true diff unchanged; DSL renderer demoted to fallback | Live spikes 2026-06-11: upsert works under API key; JSON steps accepted AND server-resolved (_hr bpm); bare DSL zone tokens resolve as POWER (trap) | Checkpoint shows JSON path renders/syncs poorly on watch/UI |

## Module layout (module github.com/maroffo/cadenza, Go 1.24+)

```
cmd/cadenza/main.go        wire config, clients, repos, mux; serve
cmd/seed/main.go           profile seed: icu history + YAML to profile/current + event log
internal/config/           env parse, fail fast; ENV=dev|prod
internal/icu/              COPIED from intervals-icu-mcp/internal/icu (5 files + 5 test files, pristine) + NEW types.go (pointer-field Wellness/Event/ResolvedWorkout; null is not 0)
internal/workout/          step schema (tool JSON schema source of truth), Validate, RenderDSL (total), FlattenSteps. Traps UNREPRESENTABLE: no distance field v1, Repeat cannot nest (type-level), renderer owns Nx blank-lines
internal/safety/           Gate.Vet (pure): Tier A code ceilings (max 300min workout, 10min Z4+ step, 40 hard min, daily TSS 250, ramp 6.0, write window today+14, 1 workout/day) + Tier B Firestore tunables (clamped INTO Tier A, fail-safe to code defaults); EstimateTSS (fixed IF table); audit record
internal/verdict/          Compute (pure): wellness vs personal baselines to Kind+Reasons+SessionCaps+DataGaps; rules incl. green-HRV-ramp-over-cap (anti-overreach core), 3d HRV degraded = Skip+anomaly, intensity creep = brakes; RenderVerdictBlock
internal/icuwrite/         WriteVerified: POST, GET resolve=true, diff (counts exact, durations exact, resolved bpm +-1 vs sport-settings zones, labels not diffed), max 2 repair PUTs (full doc), then UnverifiedSurfaced + plain-text plan in message
internal/store/            firestore repos: profile (events fold-on-write + Fold pure fn), rules, session+turns, injury+log, dedup (TTL), morning, events_written, state/chat
internal/agent/            loop.go (Messages.New loop, cap 10, every tool_use answered, is_error, pause_turn/refusal/max_tokens); tiers.go (separate Haiku/Opus builders); tools.go (registry: read tools, propose_profile_update, log_injury_update, write_workout); session.go (Firestore to params, fresh-session fallback)
internal/telegram/         bot.go, send.go (HTML, escape, 4096 multi-message, plain fallback; Send(llmText, verdict) appends verdict block), confirm.go (inline kb, answerCallbackQuery always, transaction status==proposed), polling.go (dev), templates.go (degraded modes)
internal/server/           mux; webhook.go (secret_token subtle compare, allowlist to 200, enqueue, ack); executor.go (in-app OIDC: idtoken audience+email, dispatch table); health.go
internal/task/             envelope {v,type,id,payload}; enqueue (named tasks, OIDC); local.go (dev in-process)
internal/job/              morning, message, injury, reconcile, watchdog
internal/fakes/            fakeanthropic (httptest, scripted ToolUse/Text/HTTPErr/PauseTurn), fakeicu, faketelegram
e2e/                       -tags=e2e: app + emulator + all fakes
deploy/                    setup.sh, set-webhook.sh, README runbook
.github/workflows/deploy.yml  WIF auth@v3 + deploy-cloudrun@v3
Dockerfile firestore.indexes.json .env.example
```

Hard boundary: `workout`, `safety`, `verdict` pure, zero I/O, no anthropic import (lint-enforced).

## HTTP surface (one Cloud Run service; --allow-unauthenticated for webhook; executor self-validates OIDC)

| Route | Auth | Does |
|---|---|---|
| POST /telegram/webhook | secret_token constant-time | parse minimal, allowlist (reject gets 200), enqueue `tg-update-<update_id>`, ack |
| POST /internal/execute | in-app OIDC (audience=service URL, email=invoker SA) | dispatch: telegram_update, morning_check (id=morning-<date> derived internally), injury_wakeup, daily_reconcile, watchdog |
| GET /healthz | none | static 200 |

## Firestore (Native, eur3)

profile/current (singleton, struct-marshaled for cache stability) - profile_events/{mut-<session>-<tool_use_id>} append-only w/ status proposed|confirmed|rejected|expired - rules/{rule-<id>} - sessions/{s-<chat>-<ts>} + turns/{seq} (cadenza schema, 100KB tool_result truncation, ~40 turn cap) - injuries/{inj-<date>-<part>} + log/{seq} append-only - mornings/{date} - runs/morning-{date} state machine - dedup/{key} TTL 7d - events_written/{evt-<date>-<hash8>} - gate_audits/{date-run-attempt} - state/chat

## Infra (deploy/setup.sh)

Cloud Run `cadenza`: min 0 max 1, timeout 600, 512Mi, cpu-boost, runtime SA cadenza-run@ (datastore.user, tasks.enqueuer, secretAccessor, actAs invoker SA). Tasks queue `cadenza-exec`: concurrency 1, 1/s, attempts 5, backoff 10-300s, dispatchDeadline 900s. Scheduler x3 OIDC cadenza-invoker@ (run.invoker), attempt-deadline 540s, Europe/Rome: morning `0 7 * * *`, watchdog `15 7 * * *`, reconcile `0 12 * * *`. Monitoring: log alerts (Scheduler failures, watchdog ERROR) to email. Secret Manager x4 single-region PINNED versions (telegram token, webhook secret, icu key, anthropic key). Artifact Registry. WIF pool bound to maroffo/cadenza for deploy SA. External: new GCP project, BotFather prod+dev bots, Anthropic spend cap.

## Milestones (feature branches to PR to dev; TDD inside each)

**M1 skeleton+client** (~500 new LOC + copy): go.mod, config, icu copy + types.go, server/health, store init, Dockerfile. Verify: make check green, /healthz local, integration-tag smoke reads real wellness typed, emulator dedup round-trip.

**M2 deterministic morning, deployed** (~1200): setup.sh, CI, executor+OIDC, task, job/morning+watchdog, verdict, templates, telegram send, store dedup/morning, seed cmd. Verify: real 07:00 message w/ numbers + Go verdict on phone; rerun = logged no-op; paused job triggers email alert; deploy via WIF on merge.
<!-- checkpoint:verify -->

**M3 interactive plumbing, no LLM** (~700): webhook, job/message, confirm, polling, state/chat, set-webhook.sh. Verify: /start persists chat; /status returns morning data; foreign From.ID dropped w/ 200; duplicate update_id = one reply; callback no stuck spinner; 2 rapid msgs strictly ordered.

**M4 Haiku narrative + degraded + sessions** (~900): agent loop/tiers/session (read-only tools), morning narrative. Verify: message = narrative + code-appended verdict; broken key produces degraded template, mornings/{date}=degraded; corrupted turn doc produces fresh session; no cache_control on Haiku (logs).

**M5 Opus deep-dive + memory** (~1000): full read registry, Opus builder w/ caching, mutations flow, confirm wiring. Verify: free-text Q runs loop max 10 iter on real data; 2nd msg CacheReadInputTokens>0; rule proposal, tap, confirmed mutation w/ provenance, next session prefix; decline leaves profile untouched; task retry creates no dup mutation.

**M6 workout writes, full gauntlet** (~900, test-heavy): workout schema/render/diff, safety gate, write tool, events_written. Verify: renderer goldens pin m-trap (400 meters unrepresentable) + Nx rules; over-bounds proposal rejected pre-POST; read-back match; mangled write repaired (e2e fake); real workout correctly stepped on watch.
<!-- checkpoint:verify -->

**M7 injury mode + escalation + anomalies** (~800): job/injury+reconcile full, injury Opus prompt, named tasks r<rev>. Verify: open injury creates day5 task; hand-deleted task healed by reconcile; day5 unimproved produces firm physio rec; resolve cancels wakeups; 3d HRV + ramp breach produce deterministic alerts; dup wakeup notifies once.

## Testing

Golden: renderer testdata (consecrated once via live smoke write + resolve read-back, then network-free contract). Scenario fixtures: verdict (hrv_degraded_3d, injury_day6, intensity_creep, green_hrv_ramp_over_cap, missing_hrv, all_green). Fuzz: gate never passes Tier A violation regardless of Tier B. Emulator: memory transactions, double-tap, dedup races; indexes deployed + smoke-tested live (emulator doesn't enforce). fakeanthropic: scripted, asserts every tool_use answered, gate-reject regen path. e2e: full 07:00 run, idempotent rerun, LLM-down degraded, gate-reject-regen-pass. Live smoke (-tags=live, env-guarded, manual): read-only + write [cadenza-smoke] today+21d, verify, delete. NOT tested: LLM judgment quality; compensating controls = gate audit log + weekly "gate clamped N times" digest + Go verdict floor.

## Spikes (during M1-M2, curl-scale)

1. BotFather prod+dev bots (MAx action; blocks M2)
2. New GCP project bootstrap + WIF (blocks M2; org-policy friction)
3. Events upsert under API key probe (decides M6 idempotency; GET-then-PUT safe default)
4. DSL round-trip probe: hand-POST known workout, inspect resolve=true workout_doc (pins diff schema)
5. Log HRV-null rate at 07:00 during M2 burn-in

## Verification (UAT, goal-backward)

Per-milestone observable truths above + final table: morning message arrives daily with real data and code verdict; coach answers deep-dive with real metrics; workout lands correctly on watch; safety gate audit shows proposed-vs-final; injury day-5 escalation fires; killing any dependency produces degraded message + email, never silence.

## Post-approval process

Save plan to vault (`Plans/2026-06-10 - cadenza MVP`); annotation cycle (complex verdict): MAx annotates inline, address all, repeat until approved; session log to vault; then orchestrator loop M1.

## Unresolved questions

1. Upsert semantics under API-key auth (spike 3; default GET-then-PUT). RISOLTO 2026-06-11: upsert via external_id FUNZIONA sotto API key (stesso id, update in place). Vedi quality_reports/research/2026-06-11_icu-spikes.md.
2. workout_doc direct write path exists? (assume no; DSL is the path; revisit if spike 4 shows otherwise.) RISOLTO 2026-06-11: ESISTE e risolve i target (_hr bpm). Decisione 26: JSON-first, DSL fallback. Trappola scoperta: token zona nudi nel DSL risolvono come POWER.
3. HRV reliably synced by 07:00? (burn-in data decides; retry mitigates.)
4. MAx actions needed before M2: create GCP project billing OK, BotFather bots, provide icu API key + Anthropic key for Secret Manager.
5. Vault note name OK as `Plans/2026-06-10 - cadenza MVP`?
| 27 | GCP project (supersedes 22) | Existing playground-maroffo (208669631335), europe-west1 | MAx call; billing already linked, no existing Firestore (default DB free), no WIF pool conflicts; faster to checkpoint | Playground experiments start competing for Firestore default DB or IAM hygiene degrades |
