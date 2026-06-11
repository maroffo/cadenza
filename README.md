# ABOUTME: Cadenza, an AI endurance coach: Claude API + intervals.icu, Go on GCP Cloud Run
# ABOUTME: Single-athlete v1 with Telegram interface; webapp planned as phase 2

# Cadenza

An AI endurance coach for a single amateur athlete. Cadenza combines training science, biometric data from [intervals.icu](https://intervals.icu), and real-life context (work, travel, health, sleep) into adaptive, honest, personalized coaching, delivered through Telegram and running unattended on Google Cloud.

The name: a *cadenza* is the virtuoso solo passage in a concerto; *cadenza* is also Italian for cadence, the rhythm of running and cycling.

**Status: pre-MVP.** M1 (skeleton + intervals.icu client) complete; see [Roadmap](#roadmap).

## Design philosophy

One rule drives the whole architecture: **safety logic lives in deterministic Go code, never in the model**. The LLM does coaching judgment (narrative, context, empathy, planning trade-offs); code does everything that must not fail at 6 a.m. with nobody watching:

| Concern | Owner |
|---|---|
| go / modify / skip daily verdict | Go (`verdict`), computed from wellness vs personal baselines, appended to every outgoing message by code so the model cannot suppress it |
| Physiological bounds (ramp caps, max intensity dose, write windows) | Go (`safety`), two tiers: immutable code ceilings + athlete-tunable Firestore config that can only tighten them |
| Arithmetic (durations, paces, TSS) | Go, always; LLM arithmetic is a documented failure mode |
| Workout delivery to intervals.icu | Go renders the workout DSL from model-emitted structured steps, writes, reads back, structurally diffs, repairs |
| Injury escalation timers (day-5 physio referral) | Cloud Tasks wake-ups reconciled daily from the Firestore injury log; timers are never the source of truth |
| Profile memory ("learned rules") | Append-only event log with provenance; every model-proposed mutation requires one-tap confirmation in Telegram |
| Absence detection | Watchdog job + GCP Monitoring alert to email (never to the bot being monitored), plus deterministic degraded-mode messages when any dependency is down |

The coaching behavior itself is specified in [docs/spec/coach-system-prompt-v1.md](docs/spec/coach-system-prompt-v1.md), distilled from five-plus weeks of real coaching interaction.

## Architecture

```
Telegram ──webhook──▶ ┌──────────────────────────────┐
                      │  Cloud Run service "cadenza" │
Cloud Scheduler ────▶ │  /telegram/webhook (enqueue) │ ──▶ intervals.icu REST
 (07:00 morning,      │  /internal/execute (OIDC)    │ ──▶ Anthropic Claude API
  07:15 watchdog,     │  /health                    │ ──▶ Telegram Bot API
  12:00 reconcile)    └──────────────┬───────────────┘
Cloud Tasks ◀─── named timers ───────┘
   │                                 │
   └────── re-enqueued work ────────▶│
                                 Firestore
                    (profile, rules, sessions, injuries,
                     mornings, dedup, audit logs)
```

| Component | Choice | Why |
|-----------|--------|-----|
| Runtime | Go on Cloud Run, scale-to-zero, request-based billing | Event-driven workload, near-zero cost for one athlete |
| Webhook model | Ack fast, re-enqueue via Cloud Tasks; one executor endpoint with two front doors (Tasks + Scheduler) | Durable, tracked retries; post-ack goroutines die under any billing mode |
| LLM | Anthropic Claude, hand-rolled tool-use loop, no framework | Tiered: Haiku for templated morning checks, Opus (adaptive thinking) for deep dives and injury mode |
| Training data | Native Go client for the intervals.icu REST API (no MCP) | We own the agent loop; direct calls are fewer moving parts. Client seeded from [maroffo/intervals-icu-mcp](https://github.com/maroffo/intervals-icu-mcp) |
| Storage | Firestore Native | Zero-ops, free tier covers ~100x this workload, local emulator |
| Interface | Telegram bot (HTML mode, single-user allowlist) | Coaching happens on the phone; push built in. Webapp is phase 2 |

All 25 architecture decisions, each with rationale and revisit conditions, are recorded in [quality_reports/plans/2026-06-10_cadenza-mvp-plan.md](quality_reports/plans/2026-06-10_cadenza-mvp-plan.md). The verified pre-plan research (intervals.icu API quirks, Claude API facts, Telegram and GCP constraints) is in [quality_reports/research/2026-06-10_cadenza-mvp.md](quality_reports/research/2026-06-10_cadenza-mvp.md).

## Repository layout

```
cmd/cadenza/        service entrypoint: wiring + graceful shutdown
internal/
  config/           env-based configuration, fail-fast in prod
  icu/              intervals.icu REST client (Basic auth, rate limit, retry)
                    + typed pointer-field models (a missing HRV is nil, never 0)
  server/           HTTP surface: /health now; webhook + executor in M2/M3
  store/            Firestore repositories; dedup is the at-least-once backbone
e2e/                end-to-end tests (-tags=e2e)
deploy/             (M2) idempotent gcloud bootstrap + runbook
docs/spec/          the coaching behavior specification
quality_reports/    plan, research, session logs (traces are gitignored)
```

Planned packages (M2+): `workout` (step schema + DSL renderer), `safety` (bounds gate), `verdict` (deterministic daily verdict), `icuwrite` (write-verify-repair), `agent` (Claude tool loop), `telegram`, `task`, `job`, `fakes`. The pure cores (`workout`, `safety`, `verdict`) take zero I/O dependencies, enforced at review time.

## Development

Prerequisites: Go 1.26+, Docker (Firestore emulator + image builds), `golangci-lint`, `govulncheck`.

```bash
cp .env.example .env        # fill in what you have; dev boots with nothing
make check                  # lint + vet + fmt + vulncheck + race tests
make test-e2e               # end-to-end suite (-tags=e2e)
make emulator               # Firestore emulator via Docker on :8090 (no Java needed)
```

Test tiers:

| Tier | Command | Needs |
|------|---------|-------|
| Unit | `make check` | nothing |
| Emulator | `FIRESTORE_EMULATOR_HOST=localhost:8090 go test ./internal/store/...` | `make emulator` running; set `REQUIRE_EMULATOR=1` in CI so skips become failures |
| Integration (live API, read-only) | `INTERVALS_API_KEY=... go test -tags=integration ./internal/icu/` | intervals.icu API key |
| e2e | `make test-e2e` | nothing (fakes; grows emulator + fake Anthropic from M2) |

The pre-commit hook runs `make check && make test-e2e`; commits on `main`/`master` are blocked. Flow: feature branch → PR → `dev` → `main`.

## Deployment (M2)

Target stack: one Cloud Run service (max-instances 1), a Cloud Tasks queue with concurrency 1 (per-chat serialization and spend control), three Cloud Scheduler jobs (morning check 07:00 Europe/Rome, watchdog 07:15, reconcile 12:00), Firestore, Secret Manager with pinned secret versions, log-based Monitoring alerts to email, GitHub Actions deploy via Workload Identity Federation. Provisioning is an idempotent `deploy/setup.sh` (gcloud, describe-before-create); every command doubles as runbook documentation.

Expected running cost: GCP roughly zero at single-athlete volume; the Anthropic API is the only real spend, capped from the console.

## Roadmap

| Milestone | Scope | Status |
|-----------|-------|--------|
| M1 | Skeleton, intervals.icu client, config, health, dedup store | ✅ done |
| M2 | Deployed deterministic morning message (no LLM): Scheduler → executor → verdict → Telegram | ⏳ next |
| M3 | Interactive plumbing: webhook, commands, confirmations (still no LLM) | |
| M4 | Haiku morning narrative + degraded mode + session store | |
| M5 | Opus deep-dive coaching + append-only profile memory | |
| M6 | Workout writes: schema → DSL render → safety gate → read-back verify → repair | |
| M7 | Injury mode, escalation timers, anomaly alerts | |

The build order is deliberate: the deterministic skeleton ships first so the degraded mode is proven infrastructure before the first LLM call exists, and cost controls (queue caps, dedup, spend cap) are live before anything can spend.
