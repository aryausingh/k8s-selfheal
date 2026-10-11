# Owner-1 report draft

Arya's assigned report material: Chapter 5 (Implementation), Chapter 6
(Testing), the appendix inventory, and the individual contribution statement.
Scope is the operator core — detection, evidence, action execution, the
incident budget, the workloads and the deployment. The safety layer is
Chapter 4 material and belongs to Owner 2; the classifier, validator and
experiment analysis belong to Owner 3.

No number in this draft is stated unless `hack/experiment/results.py`
regenerates it from `sage-final-runs/`, or a named test asserts it.

## Chapter 5 — Implementation

### 5.1 Platform and structure

The operator is written in Go 1.26 against `controller-runtime`, scaffolded
with Kubebuilder, module `github.com/aryausingh/k8s-selfheal`. It is a
controller without a Custom Resource Definition: there is nothing for a user
to declare, because the desired state it enforces — "no Deployment is left
crash-looping" — is implicit. It therefore reconciles the built-in `Pod` type
directly.

| Package | Responsibility | Owner |
|---|---|---|
| `internal/controller` | reconciler, detection, evidence, incident budget | Owner 1 |
| `internal/actions` | the two allowlisted remediation actions | Owner 1 |
| `internal/contracts` | structs exchanged between the three packages | shared |
| `internal/classifier` | providers, prompt, deterministic validator | Owner 3 |
| `internal/safety` | snapshot, resolver, verifier, rollback, audit | Owner 2 |
| `cmd/main.go` | composition — where the pipeline is actually assembled | Owner 1 |

The packages depend on `internal/contracts` and on interfaces, never on each
other's concrete types. `PodReconciler` holds `classifier.IncidentClassifier`,
`safety.SnapshotStore`, `safety.PodVerifier` and `safety.AuditWriter` as
interface fields, which is what allowed three people to work in parallel and
what makes the reconciler unit-testable without a cluster.

### 5.2 Detection

`PodReconciler.Reconcile` is registered for `corev1.Pod`. Detection reads
`status.containerStatuses[*].state.waiting.reason == "CrashLoopBackOff"` and
records the container name and its `restartCount`.

Three implementation points matter:

**It is level-triggered, not edge-triggered.** `controller-runtime` delivers a
work-queue key, not an event payload. The reconciler re-reads live Pod state
and decides from that. A missed or duplicated watch event therefore cannot
produce a missed or duplicated remediation; the same conclusion is reached on
the next reconcile either way. Everything downstream is written to be
idempotent for the same reason.

**It scans every container, not the first.** A sidecar can be the one
crash-looping while the application container is healthy
(`TestReconcile_MultiContainerLoop_DetectsNonFirstContainer`).

**A namespace guard skips system namespaces** — `kube-system`,
`kube-node-lease` and `local-path-storage` — before the Pod is even fetched.
Those are the cluster's own control-plane and storage components; remediating
them is out of scope and `rollout_undo` against a control-plane Deployment is
precisely the kind of action an unsupervised operator must not take
(`TestReconcile_NamespaceGuard_SkipsExcludedNamespaces`).

### 5.3 Owner resolution and the in-flight guard

A Pod is the wrong unit of remediation. `restart_pod` deletes the Pod, and the
ReplicaSet controller creates a replacement with a new name and a new UID. If
state were keyed on Pod UID, the replacement re-crashing would read as a
brand-new incident and the attempt budget would never bind.

The reconciler therefore walks Pod → ReplicaSet → Deployment using
`metav1.IsControlledBy`, which compares controller owner references by UID,
and keys all incident state on `namespace/deployment`
(`TestIncident_KeyedByNamespaceAndDeployment_NotPodUID`). A Pod with no owner
Deployment is escalated without taking the guard, because neither allowlisted
action can act on a bare Pod.

An in-flight guard admits one remediation per Deployment at a time. It is a
map guarded by a mutex rather than a `sync.Map`: an incident record is
read-modify-write state — budget, backoff and cooldown are evaluated against
one snapshot and written back — so the value needs a lock regardless, and
`LoadOrStore`'s atomic claim-if-absent buys nothing. The guard is released on
every exit path, including remediation error and classifier failure
(`TestReconcile_RemediationFailure_StillReleasesGuard`,
`TestReconcile_NilClassifier_EscalatesByDefaultAndReleasesGuard`). A leaked
guard would silently disable remediation for that Deployment for the lifetime
of the process, with no error anywhere, so each path is asserted separately.

Because the guard is per Deployment and not global, three workloads can be
remediated concurrently. That is what let the experiment run one arm in about
six minutes instead of thirty
(`TestIncident_DistinctDeploymentsDoNotBlockEachOther`).

### 5.4 Evidence collection, frozen at detection

`EvidenceCollector` (`internal/controller/evidence.go`) builds the classifier's
input from two sources:

1. **Logs** — `pods/log` with `previous=true` for the crashed instance, since
   the running container's log is empty after a restart. It falls back to the
   current instance when the previous read fails, degrades to events-only when
   both fail, truncates long logs keeping the *tail* (the error is at the end,
   not the beginning).
2. **Events** — Events for the Pod, its ReplicaSet *and* its Deployment,
   de-duplicated across the three queries, capped at 25 newest, with stale
   events dropped. The Deployment query is not optional: a `bad_deploy`
   diagnosis rests on a recent `ScalingReplicaSet` event, and for W2 there are
   no logs at all — the container dies without printing — so classification
   runs purely on Events.

Evidence is collected **once per incident, on the first attempt**, and the
frozen copy is reused for attempts 2 and 3
(`TestIncident_EvidenceIsCollectedOncePerIncident`,
`TestIncident_EvidenceIsCapturedOnTheFirstAttempt`). This was a correctness
fix, not an optimisation. The operator's own actions generate Events —
`Killing`, `ScalingReplicaSet` — and re-collecting before attempt 2 would feed
the classifier evidence the operator itself produced on attempt 1. The
diagnosis would then drift toward the remediation and away from the fault.
The frozen slice is also copied rather than shared, so a caller cannot mutate
it (`TestIncident_FrozenEvidenceCannotBeMutatedByACaller`).

Freezing has a cost, stated in the threats to validity: a cause that only
becomes visible after the first action is invisible to attempts 2 and 3.

### 5.5 Action execution

Two actions, both in `internal/actions`, both admitted on one criterion —
restoring the pre-action Deployment snapshot must undo them:

**`restart_pod`** deletes the crash-looping Pod and lets the ReplicaSet
recreate it. An already-deleted Pod is treated as success, not error
(`TestRestartPod_PodAlreadyGone_TreatedAsSuccess`) — the desired state is
"that Pod is gone", which is already true, and `IsNotFound` is the normal race
with the kubelet, not a failure.

**`rollout_undo`** reverts a Deployment to the immediately preceding revision.
`apps/v1` has **no rollback API** — `extensions/v1beta1`'s was removed — so
`kubectl rollout undo` is client-side logic, and the action reimplements it:
list the Deployment's ReplicaSets, read
`deployment.kubernetes.io/revision` from each, select the highest revision
below the current one, and patch its `spec.template` back onto the Deployment.
Old revisions survive as scaled-down ReplicaSets, which is why
`revisionHistoryLimit` must be ≥ 2 for the action to have a target at all;
with no prior revision it returns an error rather than acting
(`TestRolloutUndo_NoPriorRevision_ReturnsError`).

ReplicaSet selection uses `metav1.IsControlledBy`, not label matching. Two
Deployments in a namespace can carry labels that select each other's
ReplicaSets, and a label-matched `rollout_undo` would then patch a foreign
Deployment's template onto the target — a remediation that corrupts an
unrelated workload. The UID check makes that impossible
(`TestRolloutUndo_IgnoresLabelMatchingReplicaSetOwnedByAnotherDeployment`).

The reconciler selects an action by name from a registered map and *injects*
it into the safety service. The safety service never chooses an action; an
unmatched name escalates (`TestReconcile_Escalates_WhenNoMatchingActionRegistered`).
This keeps the allowlist a property of composition in `cmd/main.go`: an action
that is not registered cannot be executed, whatever the model proposes.

### 5.6 The automation gate

A remediation runs only if the classifier's final validated decision sets
`safe_for_automation`, the action name matches a registered action, and the
target resolves. Any other combination is escalated — including a classifier
error, an explicitly false flag, and a nil classifier
(`TestShouldEscalate_ClassifyError_EscalatesRegardlessOfBool`,
`TestShouldEscalate_ExplicitFalse_Escalates`). A nil classifier is treated as
misconfiguration and escalates by default rather than automating blind.

Escalation and rejection consume **no** attempt budget and are written at
attempt number 0 (`TestIncident_EscalationConsumesNoAttemptBudget`): nothing
was attempted, so counting them against the budget would mean a
misconfiguration could exhaust an incident that was never remediated.

### 5.7 The attempt budget — bounding the loop

`internal/controller/incident.go` is the mechanism that makes unsupervised
execution defensible. Without it, a workload that cannot be fixed is
remediated, rolled back, detected again and remediated again, for as long as
the operator runs.

```go
const (
    MaxAttempts        = 3
    AttemptBackoffBase = 30 * time.Second
    CooldownPeriod     = 5 * time.Minute
)
```

Four rules:

1. **At most 3 attempts per incident.** The fourth admission request writes a
   terminal `LOGGED` line with `result=exhausted` at attempt 3 and then goes
   quiet (`TestIncident_BudgetStopsAtMaxAttemptsAndGoesQuiet`,
   `TestIncident_ExhaustionWritesATerminalLoggedLine`).
2. **Exponential backoff between attempts**, 30 s then 60 s
   (`attemptBackoff` is `AttemptBackoffBase << (completedAttempts - 1)`),
   measured from the **end** of the previous attempt, not its start
   (`TestIncident_BackoffIsExponential`,
   `TestIncident_BackoffRunsFromAttemptEndNotAttemptStart`).
3. **A 5-minute post-terminal cooldown**, and `exhausted` is *sticky*: it does
   not expire on a timer at all
   (`TestIncident_CooldownExpiresForRecoveredButNeverForExhausted`). An
   exhausted incident has been proven unfixable by this operator's two
   actions; retrying it five minutes later would produce the same three
   failures. Only a change to the Deployment's `metadata.generation` — a human
   or a pipeline actually changing the spec — clears it
   (`TestIncident_GenerationChangeClearsAnExhaustedIncident`).
4. **`recovered` ends an incident; `rolled_back` does not.**
   `rolled_back` is an *attempt* outcome. The incident stays open for its next
   attempt and only becomes `exhausted` when the budget runs out
   (`TestIncident_RecoveredEndsIncident_RolledBackDoesNot`). The two
   denominators are different and the results chapter must say which it means.

An attempt that errors out mid-flight retries rather than holding the guard
forever (`TestIncident_ErroredAttemptRetriesRatherThanHangingTheGuard`).

Measured effect in the archived data: the five W3-enabled runs each show
exactly three `rolled_back` attempts followed by one `exhausted`, and nothing
afterwards. In a separate deployed observation the audit log stayed silent for
4 h 43 min while the W3 Pod continued to crash-loop.

### 5.8 Dispatch context and concurrency

Remediation runs in a goroutine, called with the **manager-lifetime** context
from `ctrl.SetupSignalHandler`, never with `Reconcile`'s own context.
`Reconcile`'s context is cancelled the moment that reconcile returns, and a
remediation runs up to about 90 s (30 s readiness + 60 s stability). Using the
reconcile context cancels every verification almost immediately and every
attempt fails for reasons that have nothing to do with the workload.

A fresh `safety.Service` is constructed per remediation with only the injected
action varying, rather than mutating one shared service mid-flight.

### 5.9 Composition and operational wiring

`cmd/main.go` assembles the pipeline and is therefore where the policy lives:
which classifier provider, which actions are registered, where the audit file
is written, which namespaces are excluded. Two flags, `--audit-workload` and
`--audit-arm`, populate `safety.AuditMetadata` so the experiment harness can
stamp `workload` and `armLabel` onto every audit line; before they existed the
struct was declared but never filled, and every archived line carried empty
labels.

`--audit-arm` is process-level, so it labels a whole run slot correctly, but it
cannot distinguish three workloads running under one manager. For the
concurrent runs the workload is derived from the `pod` field's Deployment
prefix instead, which is documented in `runs/README.md`. Restoring `pod` to the
shared audit schema was a cross-owner change made for exactly this reason: a
line with no Pod name cannot be attributed to a workload when incidents
overlap.

RBAC is generated from Kubebuilder markers. The manager needs `get/list/watch`
on Pods, Deployments, ReplicaSets and Events, `pods/log` for evidence,
`delete` on Pods for `restart_pod`, and `update/patch` on Deployments for
`rollout_undo` and for snapshot restoration. `watch` is not optional —
informers are LIST + WATCH, and without it the cache never syncs and the
manager does not start. The deployed manager runs as UID 65532 under
`runAsNonRoot`, which has a consequence recorded in §6.6.

### 5.10 The fault workloads

Three Deployments in `hack/manifests/`, each built to force one path:

| | Deployment | Fault | Intended path |
|---|---|---|---|
| **W1** | `w1-transient` | counts starts in a file on an `emptyDir`; starts 1–3 exit non-zero, start 4 serves | `restart_pod`; Kubernetes can also fix this unaided |
| **W2** | `rollout-fixable-demo` | revision 2's command exits non-zero; revision 1 is known-good | `rollout_undo` → `recovered` |
| **W3** | `rollout-unrecoverable-demo` | both revisions are bad | `rollout_undo` → `rolled_back` ×3 → `exhausted` |

Design decisions that are not incidental:

- **The fault is always a command change, never an image tag.** A bad tag
  produces `ImagePullBackOff`, which the detector does not watch for, so the
  incident would never start.
- **Images are pinned (`busybox:1.36`), never `:latest`.** A `:latest` tag
  makes Kubernetes default `imagePullPolicy` to `Always`, which produced an
  `ImagePullBackOff` on a kind cluster that already had the image loaded.
- **W1's log line reads `connection refused`.** The classifier's semantic
  guard for `transient_failure` requires evidence wording it recognises;
  without it the incident escalates and `restart_pod` is never exercised at
  all.
- **W1 defines no readiness probe,** deliberately. With none, Ready follows
  Running, so the verifier's readiness phase measures container start rather
  than application health, and W1 measures the crash loop and nothing else.
- **`revisionHistoryLimit: 2`** on W2 and W3, so `rollout_undo` always has
  exactly one prior revision to target.

W1 exists to give the null-action arm a non-zero baseline. W2 and W3 cannot be
self-healed by Kubernetes, so their control arm returns 0/5 and "recovery net
of baseline" would be a vacuous claim; W1 is the workload where the baseline
is not zero, which is the only reason the subtraction means anything.

### 5.11 Deployment

Single-node K3s on an Azure VM. The manager runs in `k8s-selfheal-system` with
the audit file on a PersistentVolumeClaim, so it survives a manager Pod
restart. Prometheus scrapes the manager through a ServiceMonitor and Grafana
reads Prometheus. The K3s API server is not publicly reachable on 6443, so all
remote operation goes through an SSH tunnel; the full procedure, including
teardown, is in `docs/demo-script.md`.

## Chapter 6 — Testing

### 6.1 Strategy

Three levels, each answering a question the level below cannot:

| Level | Mechanism | What only it can prove |
|---|---|---|
| Unit | `go test`, fake client, injected clock | branch-level logic: budget arithmetic, backoff origin, guard release on every path |
| Integration (`make run`) | real cluster, host kubeconfig | the pipeline composes; evidence collection returns non-zero bytes; actions have the intended effect on real objects |
| Deployed (`make deploy`) | in-cluster manager, generated RBAC, mounted PVC | RBAC is actually sufficient; the audit file is durable; the security context is satisfiable |

The distinction between the last two is not a formality. `make run` uses the
developer's kubeconfig, so the manager's RBAC is never exercised and
`config/rbac/role.yaml` cannot be called correct on the strength of a
`make run` session. Equally, no claim of mounted-volume durability or
experiment success is made without deployed-mode evidence.

Time is never read from the wall clock inside the logic under test. The
incident model takes the current instant as a parameter
(`beginAttempt(key, generation, now)`, `endAttempt(key, outcome, now)`) and
the safety layer injects a `Clock` interface. Backoff and cooldown are
therefore asserted by handing the code a chosen instant: a test that proves a
60-second backoff runs in microseconds, and its assertion is exact rather than
tolerance-based.

### 6.2 Test inventory

214 test functions, all green at tag `v1.0-viva`; `golangci-lint` reports 0
issues.

| Package | Tests | Focus |
|---|---:|---|
| `internal/controller` | 45 | detection, owner resolution, guard, evidence, incident budget |
| `internal/actions` | 11 | `restart_pod` and `rollout_undo` against a fake cluster |
| `internal/safety` | 28 | snapshot, UID attribution, two-phase verification, audit sink |
| `internal/classifier` | 129 | prompt, providers, validator, labelled evaluation sets |
| `test/e2e` | 1 (`TestE2E`) | builds the image, loads it into kind, deploys the manager, asserts a single controller-manager Pod reaches `Running` and that the metrics endpoint serves |

Three GitHub Actions workflows run on every push — `test`, `lint`, `test-e2e`
— and all three are green on the tagged commit.

### 6.3 Representative test cases — operator core

| # | Test | Input / setup | Expected | Why it exists |
|---|---|---|---|---|
| 1 | `TestReconcile_HealthyPod_NoDetection` | Pod with no waiting reason | no remediation | the detector must not fire on healthy state |
| 2 | `TestReconcile_MultiContainerLoop_DetectsNonFirstContainer` | container 2 of 2 crash-looping | detected | a sidecar can be the failing container |
| 3 | `TestReconcile_NoOwnerDeployment_EscalatesWithoutTakingTheGuard` | bare Pod | escalated, guard free | neither action can act on a bare Pod |
| 4 | `TestReconcile_InFlightGuard_SkipsAlreadyRemediatingDeployment` | second reconcile during remediation | skipped | no concurrent remediation of one Deployment |
| 5 | `TestReconcile_RemediationFailure_StillReleasesGuard` | action returns error | guard released | a leaked guard disables the Deployment silently |
| 6 | `TestReconcile_Escalates_WhenNotSafeForAutomation` | `safe_for_automation=false` | escalated, no action | the model cannot act on its own authority |
| 7 | `TestEvidenceCollector_PrefersPreviousContainerInstance` | restarted container | reads `previous=true` | the live log is empty after a restart |
| 8 | `TestEvidenceCollector_BothLogReadsFail_DegradesToEventsOnly` | both log reads error | events-only evidence | W2 has no logs at all |
| 9 | `TestEvidenceCollector_TruncatesLongLogsKeepingTheTail` | oversized log | tail retained | the error is at the end |
| 10 | `TestIncident_EvidenceIsCollectedOncePerIncident` | 3 attempts | 1 collection | prevents evidence poisoning by our own actions |
| 11 | `TestIncident_BudgetStopsAtMaxAttemptsAndGoesQuiet` | 4th admission | `exhausted`, then silence | bounds the remediation loop |
| 12 | `TestIncident_BackoffRunsFromAttemptEndNotAttemptStart` | attempt lasting ~90 s | next attempt 30 s after it ends | regression test for a measured bug (§6.5) |
| 13 | `TestIncident_CooldownExpiresForRecoveredButNeverForExhausted` | clock +6 min | `recovered` clears, `exhausted` does not | an unfixable workload must not be retried on a timer |
| 14 | `TestIncident_GenerationChangeClearsAnExhaustedIncident` | generation bumped | incident re-admitted | a human fixing the spec must re-arm the operator |
| 15 | `TestIncident_KeyedByNamespaceAndDeployment_NotPodUID` | replacement Pod crash-loops | same incident | otherwise the budget never binds |
| 16 | `TestIncident_EscalationConsumesNoAttemptBudget` | escalation ×3 | budget untouched, attempt 0 | nothing was attempted |
| 17 | `TestRolloutUndo_RevertsToImmediatelyPrecedingRevision` | revisions 1 and 2 | revision 1's template applied | `apps/v1` has no rollback API; this is our own implementation |
| 18 | `TestRolloutUndo_IgnoresLabelMatchingReplicaSetOwnedByAnotherDeployment` | foreign RS matching labels | ignored | a label-matched undo corrupts an unrelated workload |
| 19 | `TestRolloutUndo_NoPriorRevision_ReturnsError` | single revision | error, no action | acting with no target is worse than not acting |
| 20 | `TestRestartPod_PodAlreadyGone_TreatedAsSuccess` | Pod already deleted | success | `IsNotFound` is the normal kubelet race |

### 6.4 Integration and deployed verification

- `make run` against kind, all four scenarios in `hack/manifests/`, one
  terminal outcome each: `recovered` (restart), `rolled_back` (restart),
  `recovered` (undo), `rolled_back` (undo).
- Ten-run cross-check on kind under `runs/` (A-01…A-05 enabled, B-01…B-05
  disabled).
- Deployed mode on K3s for the 28 archived runs in `sage-final-runs/`,
  which is the only evidence that RBAC, the PVC and the security context are
  all satisfiable at once.
- `TestE2E` builds the manager image in CI, loads it into kind, deploys it,
  and asserts that exactly one controller-manager Pod reaches `Running` and
  that the metrics endpoint serves. It is the only check that the Dockerfile,
  the kustomize overlays and the generated RBAC still compose.

Evidence collection is checked by a log assertion, not only by a test:
`logBytes=0 eventCount=0` followed by `ESCALATED — not safe for automation`
is the signature of broken evidence wiring or missing `pods/log` RBAC, and it
escalates everything while looking healthy.

### 6.5 Defects found by testing, and their fixes

Each of these was found by a test or a measured run, not by reading the code,
and each has a regression test named above.

1. **Replacement attribution by timestamp was unsound.** `metav1.Time`
   serialises at second granularity, so a replacement Pod created in the same
   second as the action could compare as "not newer" and be rejected, or an
   old Pod could be accepted. Replaced with an exact pre-action **UID set**
   (Owner 2's Week 3 work;
   `TestDeploymentPodResolverRejectsCapturedUIDFromTheSameSecond`).
2. **Backoff was measured from attempt start, not attempt end.** An attempt
   takes up to ~90 s, so a "30-second backoff" from the start could elapse
   before the attempt even finished; the first deployed W3 run showed a 12 s
   gap where 30 s was promised. `lastAttemptAt` moved into `endAttempt`;
   re-verified at 40 s and 91 s on the cluster.
3. **`rollout_undo` selected ReplicaSets by label.** Fixed to
   `metav1.IsControlledBy`.
4. **A lint failure reached `main`.** The push command chained
   `make lint | tail`, which discarded the exit code, and an unused parameter
   (`unparam`) went red on CI. Fixed, and pushes are now gated on
   `make lint >/dev/null && git push`. Recorded because the lesson is about
   the verification step, not the code.
5. **A truncated audit file crash-looped the manager for ten minutes.** The
   file was cleared with `: > audit.jsonl` from a root reader Pod; the manager
   runs `runAsNonRoot` as UID 65532 and could not open a root-owned file.
   Fixed by *deleting* the file so the manager recreates it with its own
   ownership. Documented in `runs/README.md` as a standing rule: do not create
   files in that PVC from a root Pod.
6. **`:latest` caused `ImagePullBackOff` on a cluster that had the image.**
   `imagePullPolicy` defaults to `Always` for `:latest`. All workload images
   are now pinned.
7. **An estimate was wrong by a factor that mattered.** W1's unaided recovery
   was estimated at ~70 s and measured at 36 s on kind — against a 30 s
   readiness timeout, a ~6 s margin. The estimate had hidden a genuine threat
   to validity: W1 sits close to the verifier's decision boundary. The
   measurement is now in the manifest's own comments and the spread is
   reported across N=5 rather than from a single observation.
8. **`restart_pod` makes W1 worse.** Deleting the Pod destroys the `emptyDir`
   holding W1's restart counter, so the replacement crash-loops from scratch,
   cannot be Ready inside 30 s, and the attempt is recorded `rolled_back`. In
   the archived runs the enabled arm recovered unaided at 121–128 s and the
   disabled arm at 99–104 s: the controller cost about 22 s on that workload.
   This is reported as a result, not fixed. It is the clearest evidence in the
   study that the paired null-action arm was necessary.

### 6.6 What testing does not establish

- Readiness and restart count are proxies for recovery. A container can report
  Ready while being semantically wrong.
- Polling observes state at its sampling interval; a failure shorter than one
  interval can be missed.
- The unit tests use a fake client, which does not reproduce API server
  admission, resource quotas, or scheduler behaviour.
- One cluster, one node, N=5. No multi-node scheduling effects and no
  concurrent-incident load beyond three Deployments.

## Appendix inventory

| Appendix | Content | Source |
|---|---|---|
| A | W1 / W2 / W3 workload manifests | `hack/manifests/w1-transient.yaml`, `rollout-fixable.yaml`, `rollout-unrecoverable.yaml` |
| B | Attempt budget, backoff and cooldown | `internal/controller/incident.go` |
| C | `rollout_undo` revision selection | `internal/actions/actions.go` |
| D | Evidence collection and freezing | `internal/controller/evidence.go` |
| E | Pipeline composition and flags | `cmd/main.go` |
| F | Generated RBAC role | `config/rbac/role.yaml` |
| G | Frozen measurement definitions | `docs/measurement-definitions.md` |
| H | One complete audit log for one incident | `sage-final-runs/C1-01/audit.jsonl` |
| I | Results generator | `hack/experiment/results.py` |
| J | Demo and operations runbook | `docs/demo-script.md` |

`sage-final-runs/C1-01/audit.jsonl` is the appendix listing worth printing in
full: 22 lines covering three attempts and the `exhausted` outcome, which is
the whole contribution visible in one page of evidence.

## Individual contribution statement — Arya Singh

Arya designed and implemented the operator core: the `Pod` reconciler and
CrashLoopBackOff detection across all containers, owner-Deployment resolution
by controller UID, the per-Deployment in-flight guard, evidence collection
from previous-container logs plus de-duplicated Pod/ReplicaSet/Deployment
Events with freezing at detection, both allowlisted remediation actions
(`restart_pod`, and a client-side `rollout_undo` written because `apps/v1`
exposes no rollback API), the automation gate and escalation paths, the
incident model — three-attempt budget, exponential backoff measured from
attempt end, five-minute cooldown with sticky `exhausted` cleared only by a
generation change — the manager-lifetime dispatch context, pipeline
composition and the audit metadata flags in `cmd/main.go`, the three fault
workloads, the deployment and tunnelled remote operation, and the demo and
operations runbook.

He did not implement the safety layer (snapshot capture and restoration,
pre-action UID attribution, the two-phase verifier, the durable audit sink),
which is Ananya's; nor the LLM classifier, the deterministic validator, the
labelled evaluation sets, the experiment harness, `results.py`, Prometheus or
Grafana, which are Subhashini's. Cross-package edits he made into those
packages — restoring the `pod` field to the shared audit schema, and adding
the `--audit-workload` / `--audit-arm` flags that populate
`safety.AuditMetadata` — were raised explicitly with their owners and merged
by pull request.
