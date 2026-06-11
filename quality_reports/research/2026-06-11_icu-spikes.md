# ABOUTME: Live intervals.icu spike results (plan spikes 3-4 + typed smoke): upsert, DSL, workout_doc
# ABOUTME: Executed 2026-06-11 against the real athlete account; all probe events deleted after

# intervals.icu live spikes, 2026-06-11

All probes on date 2026-07-02 (+21d), tagged `[cadenza-smoke]`, deleted after (verified 0 left).

## 1. Typed wellness smoke (live)

`go test -tags=integration` green on the real account. Pointer-field contract confirmed on real data: **today's `restingHR` was nil mid-morning while HRV was present** (42): exactly the partial-sync state the 07:00 deferral handles. Athlete reality check: HRV range 20-53 over the week, CTL ~20, ramp negative: baselines MUST come from personal history (the seeder), not population numbers.

## 2. Upsert under API-key auth: WORKS (plan unresolved question 1: RESOLVED)

`POST /athlete/0/events/bulk?upsert=true` with the same `external_id` twice:
- POST 1 → id 115475888 created
- POST 2 (changed name/description) → **same id returned, event updated, no duplicate**

The OpenAPI caveat ("matches events created by the same OAuth application") does not bite plain API-key usage. **M6 impact:** workout writes can be natively idempotent via deterministic `external_id` (`cadenza-<date>-<hash>`); GET-then-PUT demoted to fallback.

## 3. DSL round-trip: parses structure, but zones resolve as POWER (trap found)

Description DSL:
```
Warmup
- 10m Z1-Z2

3x
- 4m Z4
- 2m Z1

Cooldown
- 10m Z1
```
Read back with `resolve=true`:
- Structure perfect: step0 600s `warmup:true`, step1 `reps:3` with 2 substeps (240s + 120s, total 1080), step2 600s `cooldown:true`. Section headers DID set the warmup/cooldown flags. Total duration 2280s correct.
- **Trap:** `zoneTimes` came back with `maxWatts`/`percentRange`: bare `Z4` tokens resolved against POWER zones, not HR, even for `type: Run`. Substep `hr` fields: none. A naive renderer emitting `- 4m Z4` produces a power-target workout. The HR-explicit DSL syntax needs its own probe if we ever use the DSL path.

## 4. Direct JSON `workout_doc` write: WORKS (plan unresolved question 2: RESOLVED, flips the research)

`POST /athlete/0/events` with `workout_doc: {steps:[{duration:600, hr:{units:"hr_zone", value:2}}]}` and NO description:
- Accepted; read-back shows the step present with `hr` preserved AND server-side resolution: `"_hr": {"start":137, "end":144}` (real bpm from the athlete's zones), `"target":"HR"`.
- Community reports of the JSON path being silently ignored are outdated: the API evolved.
- Caveat: the stored doc lacks the aggregates the DSL parse produces (`zoneTimes`, total `duration`, `locales`): UI rendering and watch sync quality remain unverified until something lands on the real calendar during a checkpoint.

## M6 design impact (decision D26 appended to the plan)

JSON-first write path: the model's structured steps marshal straight into `workout_doc` (no DSL rendering, the `m`=minutes trap becomes unrepresentable at the wire too), HR targets explicit by construction, `external_id` upsert for idempotency, read-back `resolve=true` diff on `_hr`/durations stays as the verification layer. DSL rendering demoted to fallback if checkpoint verification shows the JSON path renders or syncs poorly; if the fallback activates, HR-explicit DSL syntax needs a probe first (bare zone tokens are a power trap).
