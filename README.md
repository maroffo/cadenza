# ABOUTME: Cadenza, an AI endurance coach: Claude API + intervals.icu, Go on GCP Cloud Run
# ABOUTME: Single-athlete v1 with Telegram interface; webapp planned as phase 2

# Cadenza

An AI endurance coach for a single amateur athlete. Combines training science, biometric data from [intervals.icu](https://intervals.icu), and real-life context (work, travel, health) into adaptive, honest, personalized coaching.

The name: a *cadenza* is the virtuoso solo passage in a concerto; *cadenza* is also Italian for cadence, the rhythm of running and cycling.

## Architecture (v1)

| Component | Choice |
|-----------|--------|
| Runtime | Go on GCP Cloud Run (scale to zero) |
| LLM | Anthropic Claude API, hand-rolled tool-use loop; tiered models (cheap for templated morning checks, top-tier for deep dives and incident mode) |
| Training data | intervals.icu REST API via native Go client (no MCP in v1) |
| Storage | Firestore (profile, baselines, learned rules, injury log, session facts) |
| Interface | Telegram bot (webapp in phase 2) |
| Proactive jobs | Cloud Scheduler (morning readiness, anomaly alerts) + Cloud Tasks (escalation timers) |

Core design rule: safety logic lives in code, not in the model. Ramp-rate caps, arithmetic, injury escalation timers and workout-write validation are deterministic Go; the model does coaching judgment.

## Source spec

The coaching behavior is specified in [docs/spec/coach-system-prompt-v1.md](docs/spec/coach-system-prompt-v1.md), distilled from five-plus weeks of real coaching interaction.

## Status

Pre-plan: requirements refined, research in progress, implementation plan pending.
