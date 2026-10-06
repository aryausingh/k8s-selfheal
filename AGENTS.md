# Owner 2 Week 3 Rules

## Authority

Implementation in this checkout is governed by:

1. the shared definitions frozen in `docs/measurement-definitions.md`;
2. the **Ananya — Safety Layer: Durable Audit, Timing Decomposition, Attribution** section of `Week3_Team_Plan.pdf`;
3. explicit cross-owner handoffs confirmed by Arya and Subhashini.

Do not import implementation requirements from other owners' Week 3 tasks, later weeks, or general Kubernetes practice.

## Mission

Instrument and harden the existing safety layer without changing its remediation policy: provide a durable append-only audit sink, preserve raw timing evidence for later metrics, replace timestamp-based replacement-Pod attribution with exact pre-action Pod UID exclusion, and support the Week 3 experiment runs.

## Frozen shared definitions

- `MaxAttempts = 3` belongs to the operator.
- Attempt backoff is 30 seconds after attempt 1 and 60 seconds after attempt 2, measured from the previous attempt's end.
- Post-terminal cooldown is 5 minutes. `exhausted` is sticky until Deployment generation changes.
- An incident is one detection through one terminal incident outcome.
- An attempt is one snapshot -> action -> verification cycle.
- Terminal outcomes are `recovered`, `rolled_back`, `exhausted`, `escalated`, and `rejected`; `escalated` and `rejected` use attempt number 0.
- `rolled_back` is emitted by the safety layer for a failed attempt; the operator may keep that incident active for another attempt and eventually emit `exhausted`.
- Raw timestamps are evidence. Durations are calculated later in seconds as `float64`; never serialize `time.Duration` directly as JSON.
- The 30-second readiness timeout and 60-second stability window remain unchanged and untuned.

## Required Owner 2 work

### Exact replacement-Pod attribution

- Immediately before the injected remediation action, capture the UIDs of Pods actually controlled through Deployment -> ReplicaSet -> Pod ownership.
- Pass that immutable UID set into verification.
- Reject every candidate whose UID was present before the action.
- Continue rejecting the original Pod, terminating Pods, foreign Pods, and Pods without the named container.
- Remove `ActionStartedAt` timestamp truncation as an attribution mechanism. Do not retain timestamp filtering alongside the UID rule.

### Durable audit

- Retain append-only JSONL with one complete JSON object per line.
- File appends must be concurrency-safe and persist on a mounted volume.
- Preserve stdout writer support for unit tests and local composition.
- Every shared audit event has exactly these camelCase fields: `incidentID`, `attemptNumber`, `timestamp`, `state`, `action`, `result`, `workload`, `armLabel`.
- `armLabel` values are exactly `enabled` or `disabled`.
- Snake_case fields such as `incident_id`, `experiment_arm`, and `terminal_outcome` belong to Owner 3's internal aggregate representation and must not be added to the shared safety audit schema.

### Timing evidence

- Record raw timestamps only; Owner 3 computes durations.
- Do not calculate or serialize invented `t_detect`, `t_classify`, `t_apply`, or `t_verify` values.
- `t_apply` is derived as the `VERIFYING` transition timestamp minus the `REMEDIATING` transition timestamp.
- Timing originating outside `internal/safety` must be passed through an agreed shared contract rather than guessed.

## Cross-owner boundary

- Consume `IncidentID` and `AttemptNumber` from `contracts.DetectionEvent`; never generate or renumber them in safety.
- Do not implement the attempt budget, backoff, cooldown, incident guard, evidence freezing, workloads, classifier, metrics parser, experiment runner, or Grafana.
- Do not change controller or classifier behavior to make Owner 2 tests pass.
- Cross-package integration-only edits must be minimal, called out explicitly, and reviewed by the owning teammate.

## External metadata boundary

`workload` and `armLabel` originate from the experiment harness. Fault-injection time and classifier start/completion timestamps originate from their owning components. Safety exposes an explicit metadata handoff but must not infer, fabricate, or rename those values. The transport from the harness/classifier into that handoff remains integration work until the owning components wire it.

## Verification

- Unit tests must prove capture happens before action execution and that a pre-action UID cannot satisfy verification even when timestamps share a second.
- Unit tests must prove a genuinely new UID can be selected.
- Run `go test ./...` before claiming Owner 2 implementation complete.
- Do not claim mounted-volume durability or experiment success until deployed-mode evidence exists.
