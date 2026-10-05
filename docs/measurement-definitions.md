# Measurement definitions

Frozen on 2026-10-01, before any experiment run. **These cannot change after
runs begin** — every metric in the report depends on them, and a definition
that shifts mid-experiment invalidates every number collected before the shift.

Agreed by all three owners. Changing anything here requires re-running every
arm already collected.

## 1. Incident and attempt

- **Incident** — one detection through to one terminal outcome. At most one
  incident per Deployment at a time.
- **Attempt** — one `snapshot → action → verify` cycle.
- An incident contains 1..N attempts, N bounded by `MaxAttempts`.

An incident is identified by `incidentID`, generated at detection. Attempts
within it are numbered `attemptNumber`, starting at 1.

## 2. Terminal outcomes

Exactly five. Every incident ends in exactly one of these, and nothing else is
a terminal state.

| Outcome | Meaning |
|---|---|
| `recovered` | Verification passed — the pod became Ready and stayed Ready for the full stability window. |
| `rolled_back` | Verification failed; the pre-action snapshot was restored. |
| `exhausted` | The attempt budget was spent without recovery. |
| `escalated` | The classifier returned `safe_for_automation: false`, or sub-cause `unknown`. No action was taken. |
| `rejected` | The validator refused the proposed action — off-allowlist, malformed, or semantically unsupported. No action was taken. |

`escalated` and `rejected` are terminal at attempt 0: no snapshot is taken and
no action runs, so they consume no attempt budget.

## 3. Rollback trigger rate

Two formulas, both reported. The attempt-level rate is **primary**.

```
rollback_rate_attempts  = attempts ending in rolled_back / total attempts
rollback_rate_incidents = incidents containing >= 1 rolled_back attempt / total incidents
```

Attempts is the primary denominator because it matches "rate over a population
of remediation attempts". The incident-level figure is reported as secondary
because an incident with three failed attempts is one incident but three
rollbacks, and collapsing those hides the retry behaviour.

Denominators exclude `escalated` and `rejected` incidents — no action was
taken, so there was nothing to roll back.

## 4. Timing

```
TTD = fault injection timestamp  -> DetectionEvent timestamp
TTM = DetectionEvent timestamp   -> terminal outcome timestamp
```

TTM is reported decomposed into four stages per attempt, not as a single
number:

| Field | Span |
|---|---|
| `t_detect` | fault injection → DetectionEvent |
| `t_classify` | classifier call duration (the LLM inference component) |
| `t_apply` | action dispatch → action confirmed applied |
| `t_verify` | verification window duration |

`t_verify` is constant by design and is reported as such. **TTM has a hard
~60s floor**, because the stability window is 60s by construction. This is a
design property of the verification method, not a performance result, and the
report states it before presenting any TTM figure.

## 5. Attempt budget

| Parameter | Value |
|---|---|
| `MaxAttempts` | 3 per incident |
| Backoff between attempts | 30s after attempt 1, 60s after attempt 2 — measured from when the previous attempt **finished** |
| Cooldown after terminal outcome | 5 minutes per Deployment |

After `MaxAttempts` is reached without recovery, the incident ends as
`exhausted` and the controller stops acting on that Deployment.

Backoff runs from the **end** of the previous attempt, not its start. An
attempt occupies roughly 30s of wall clock on a workload that never becomes
Ready (the verifier's readiness timeout), so measuring from the start would let
that duration consume the backoff — the first deployed run produced a 12s gap
between attempts where this table promises 30s. With one incident spanning at
most three attempts, expect it to reach `exhausted` about 3.5 minutes after
detection.

The backoff is a floor, not an exact interval. A new attempt can only begin on
the next reconcile, and reconciles fire when the Pod's status changes — on the
kubelet's own crash-loop schedule. Measured on W3 in deployed mode, the gaps
between attempts were 40s and 91s against promised minima of 30s and 60s.

Cooldown suppresses re-detection of the same Deployment after `recovered`,
`escalated` or `rejected`. It is **reset early if the Deployment's
`metadata.generation` changes**, since a generation change means a human or a
new rollout intervened and the situation is no longer the one we saw.

`exhausted` is the exception: it does **not** expire with the cooldown. Only a
generation change clears it. Letting it expire would re-arm the controller on
a Deployment we already gave up on — the same unbounded loop the budget exists
to stop. With three attempts at roughly 90s each plus backoff, an incident
exhausts at about minute 3; a 5-minute cooldown would have the controller
acting again at about minute 8, which fails the acceptance test ("confirm it
goes quiet; if it is still acting at minute 10 the fix is incomplete"). It also
keeps the rollback denominator well defined: one injection produces exactly one
incident, not one every eight minutes.

## 6. Shared audit fields

Every audit line carries these. Field names are frozen — the metrics module
parses them positionally by name.

| Field | Type | Written by |
|---|---|---|
| `timestamp` | RFC3339 | safety |
| `pod` | string (`namespace/name`) | safety |
| `state` | string | safety |
| `action` | string | safety |
| `result` | string | safety |
| `incidentID` | string | operator |
| `attemptNumber` | int | operator |
| `classifierMillis` | int64 | operator, CLOSED line only |

`incidentID`, `attemptNumber` and `classifierMillis` are `omitempty`, so every
line Owner 2's `Service.Remediate` already wrote serialises unchanged.

### The CLOSED line

Owner 2's `Service` only writes while `Remediate()` is running, so three of
the five terminal outcomes never reached the audit log: `escalated` and
`rejected` are decided before `Remediate()` is called, and `exhausted` is
decided by the attempt budget. The operator therefore writes **one extra line
per incident** when it terminates:

```json
{"state":"CLOSED","result":"exhausted","attemptNumber":3,"incidentID":"...","classifierMillis":412}
```

- `result` is one of `recovered` · `exhausted` · `escalated` · `rejected`.
- `rolled_back` never appears here — it is an **attempt** outcome. Owner 2's
  per-attempt `LOGGED` lines carry it and are unchanged.
- `attemptNumber` is the attempts consumed: `0` for `escalated` and
  `rejected`, which take no action.
- **No CLOSED line means the incident was `abandoned`** (§6a).

Adapter rule, in one line: group by `incidentID`, find the `CLOSED` entry,
read `result`.

### `workload`, `armLabel` and the injection time are per-run, not per-line

These three are **not** audit fields. The controller cannot know them, and the
disabled arm produces no audit lines at all — so an in-line `armLabel` could
only ever read `enabled` and would carry no information.

Each run is archived as its own directory instead:

```
runs/B1-03/audit.jsonl
runs/B1-03/meta.json    {"workload":"W2","arm":"enabled","injectedAt":"...","run":3}
```

`TTD = DETECTED.timestamp − meta.injectedAt`, with no join key needed. The
controller is restarted between arms anyway, so one log per run falls out of
the procedure already in the runbook.

## 6a. Abandoned incidents

A fifth shape exists that §2 does not cover, found in the first deployed W1
run: an incident that records one or more attempts and then **never reaches a
terminal outcome**, because the workload healed itself and the controller
stopped seeing a crash loop.

Observed: `restart_pod` fired, the attempt rolled back at the 30s readiness
timeout, and the replacement pod self-healed about 18 seconds later. No second
attempt was triggered because there was no longer anything to detect.

How to count these:

- The **attempt-level** record is complete and correct, so
  `rollback_rate_attempts` — the primary metric — is unaffected.
- The incident has no terminal outcome, so it is excluded from the
  **incident-level** denominator and reported separately as `abandoned`, with
  a count.
- An abandoned incident is **not** a recovery by us. The recovery happened
  unaided, after our attempt failed, which is exactly what the paired control
  arm is there to reveal.

This is expected to affect W1 only. W2 and W3 cannot self-heal, so every
incident on them reaches a terminal outcome.

## 7. Experiment arms

Three workloads × two conditions. The disabled arm is the null-action control:
Kubernetes' own back-off can recover a crash loop unaided, so without it a
recovery rate is not attributable to the controller.

| Arm | Workload | Controller | N |
|---|---|---|---|
| A1 | W1 transient crasher | enabled | 5 |
| A2 | W1 transient crasher | disabled | 5 |
| B1 | W2 bad current revision | enabled | 5 |
| B2 | W2 bad current revision | disabled | 5 |
| C1 | W3 bad current and previous | enabled | 5 |
| C2 | W3 bad current and previous | disabled | 3 |

```
attributable_recovery(workload) = recovery_rate(enabled) - recovery_rate(disabled)
```

C2 is N=3 rather than 5 because it is a sanity check on a baseline expected to
be exactly zero, not a measurement.

## Out of scope, stated

- The verifier constants (30s readiness, 60s stability) were chosen a priori
  and are **not tuned against data**. No paper in our corpus evaluates
  stability-window sensitivity either.
- N=5 per arm gives a direction, not a confidence interval. We report counts,
  never a bare percentage.
- **Evidence is frozen at detection and reused for every attempt in an
  incident.** A cause that only becomes visible *after* the first attempt is
  therefore never seen, and all three attempts are classified against
  pre-action evidence. This is deliberate — it preserves attribution, because
  every attempt then reasons about the same fault rather than about our own
  remediation — but it is a real loss. Re-collecting per attempt was worse:
  the collector keeps the newest 25 Events, and remediation generates a burst
  of its own (`Killing`, `SuccessfulDelete`, `SuccessfulCreate`,
  `ScalingReplicaSet`), so by attempt 2 the original cause has been pushed out
  of the window entirely.
- The plan called for filtering Events whose source is the SAGE controller.
  **That filter would match nothing** — SAGE emits no Kubernetes Events at all
  (no `EventRecorder` exists anywhere in the repo). The crowding Events come
  from Kubernetes' own controllers reacting to our action, and filtering by
  their reporting component would also drop `BackOff`, reported by the kubelet
  and the signal we most need. Freezing at detection is the filter: nothing
  that happened after we began acting can enter the bundle, whoever reported
  it.
