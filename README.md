# k8s-selfheal — SAGE-K8s

[![Tests](https://github.com/aryausingh/k8s-selfheal/actions/workflows/test.yml/badge.svg)](https://github.com/aryausingh/k8s-selfheal/actions/workflows/test.yml)
[![Lint](https://github.com/aryausingh/k8s-selfheal/actions/workflows/lint.yml/badge.svg)](https://github.com/aryausingh/k8s-selfheal/actions/workflows/lint.yml)
[![E2E](https://github.com/aryausingh/k8s-selfheal/actions/workflows/test-e2e.yml/badge.svg)](https://github.com/aryausingh/k8s-selfheal/actions/workflows/test-e2e.yml)

A Kubernetes controller that safely auto-remediates `CrashLoopBackOff`, and —
unlike existing tools that stop at a suggestion or gate on a human — **verifies
that the fix actually held, and automatically rolls back when it didn't.** We
build and measure the safety layer production tools leave out, on one failure
class, deeply.

An LLM is in the loop but it cannot act. It proposes; a deterministic Go
validator checks the proposal against a two-action allowlist, against live
cluster state, and against the evidence that was collected. Anything else is
rejected and escalated to a human.

## The loop

For one incident, from detection to a line in the audit log:

1. **Detect** — a Pod enters `CrashLoopBackOff`; resolve its owner Deployment.
2. **Guard** — one in-flight remediation per Deployment; drop duplicate work.
3. **Collect evidence** — previous-container logs plus Events, frozen at
   detection so a later restart cannot rewrite the input to the decision.
4. **Classify** — the LLM returns a proposed action, a sub-cause, a target,
   and `safe_for_automation`.
5. **Validate** — allowlist, live-target existence, and a semantic guard that
   the evidence supports the claimed sub-cause. Fail → escalate.
6. **Budget** — 3 attempts per incident, 30s then 60s backoff measured from
   the previous attempt's *end*, then a 5-minute cooldown. `exhausted` is
   sticky until the Deployment's generation changes.
7. **Snapshot → act → verify** — capture the exact Deployment spec and the
   pre-action Pod UIDs, run `restart_pod` or `rollout_undo`, then require a
   *genuinely new* Pod to be Ready within 30s and continuously stable for 60s.
8. **Recover or roll back** — stability held → `recovered`. Otherwise restore
   the snapshot byte-for-byte → `rolled_back`, and try again until the budget
   runs out → `exhausted`.

Every state transition is one JSON object on one line in an append-only
`audit.jsonl` on a mounted volume. Durations are never stored — raw timestamps
are the evidence, and every published number is recomputed from them.

## Architecture

```mermaid
flowchart LR
    FI[Fault injector / run harness]
    API[(K3s API server)]
    PC[Pod controller\nCrashLoopBackOff detection]
    EC[Evidence collector\nlogs + Events]
    CL[LLM classifier\nproposal only]
    VA[Deterministic validator\nsemantic guard + allowlist]
    IM[Incident manager\nbudget + backoff + cooldown]
    SS[Safety service\nsnapshot → act → verify → restore]
    AC[Injected actions\nrestart_pod | rollout_undo]
    VR[Deployment Pod resolver + verifier\n30s Ready + 60s stable]
    AU[(Append-only audit.jsonl\nPVC + per-write sync)]
    PM[Prometheus]
    GF[Grafana]

    FI -->|apply W1/W2/W3| API
    API -->|Pod events| PC
    PC --> EC
    EC --> CL
    CL -->|raw proposal| VA
    VA -->|validated action or escalate| IM
    IM -->|admitted attempt| SS
    SS -->|execute injected callback| AC
    AC --> API
    SS --> VR
    VR -->|Deployment → ReplicaSet → Pod| API
    SS -->|restore exact pre-action spec on failure| API
    SS --> AU
    PC --> AU
    PC --> PM
    PM --> GF
```

Sequence, control-loop, state-machine and deployment diagrams:
[`docs/final-week/diagrams.md`](docs/final-week/diagrams.md).

## Results

28 runs on a single-node K3s cluster on an Azure VM, 2026-10-05, live Claude
classifier, three workloads × two arms (controller enabled / scaled to zero),
N=5 per cell (N=3 for W3-disabled). Raw logs: [`sage-final-runs/`](sage-final-runs/).

| Workload | What it breaks | Enabled | Disabled | Attributable recovery |
|---|---|---:|---:|---:|
| W1 transient | crashes 3× then serves | 5/5 | 5/5 | +0.0 pp |
| W2 fixable rollout | bad current revision, good previous | 5/5 | 0/5 | **+100.0 pp** |
| W3 unrecoverable rollout | both revisions bad | 0/5 | 0/3 | +0.0 pp |

| Metric | Value |
|---|---|
| Rollback rate, attempt level | 20/25 = **80.0%** |
| Rollback rate, incident level | 5/10 = 50.0% |
| Terminal outcomes | 5 `recovered` · 5 `exhausted` · 5 abandoned · 0 `escalated` · 0 `rejected` |
| Mean time to detect | 15.0 s |
| Mean time to mitigate | 162.5 s |
| Classifier latency | 6.1 s/call |
| Validator, adversarial set | 15/15 rejected |
| Validator, legitimate set | 30/30 accepted |

**W1 is a negative result and it is the point of the control arm.**
`restart_pod` deletes the Pod, which destroys the `emptyDir` holding W1's
restart counter, so the replacement crash-loops from scratch. The enabled arm
recovered unaided at 121–128 s; the disabled arm at 99–104 s. The controller
made that workload roughly 22 s *slower*, and because the replacement cannot
be Ready inside the 30 s readiness timeout, the attempt is recorded
`rolled_back`. Without a paired null-action arm, "100% recovery" on W1 would
have read as a success.

Note the level of measurement when reading those two tables together: the
recovery columns are measured per *run* against the experiment's own
criterion (Ready and continuously stable for 60s), while `recovered` /
`rolled_back` / `exhausted` are the controller's own per-*attempt* and
per-*incident* outcomes. W1-enabled is 5/5 recovered **and** 5/5
`rolled_back`; both are true of different things.

Every number above is regenerated from the archived logs, never hand-typed:

```bash
python3 hack/experiment/results.py --runs-dir sage-final-runs \
  --md-out docs/week3-live-final-results.md
```

## Quickstart

Needs Go 1.26, Docker, `kubectl` and `kind`.

```bash
kind create cluster --name selfheal
kubectl config use-context kind-selfheal

make test lint                 # unit tests + linter
make run                       # run the controller against your kubeconfig
```

In a second terminal, break something and watch it get fixed:

```bash
# The workload remembers the first Pod that ever ran, on the node's disk.
# Clear it, or the scenario starts healthy and proves nothing.
docker exec selfheal-control-plane rm -rf /tmp/k8s-selfheal

kubectl apply -f hack/manifests/transient-recovers.yaml
kubectl get pods -l app=transient-recovers-demo -w
```

Expected, from a real run:

```
DETECTED CrashLoopBackOff
Collected incident evidence   logBytes=54 eventCount=10
remediation finished          action=restart_pod result=recovered mttr=1m5.12s
```

Three more scenarios — including the two-revision `rollout_undo` setup and the
one that deliberately ends in `rolled_back` — are in
[`hack/manifests/README.md`](hack/manifests/README.md), with the exact command
sequence for each. The rollout scenarios need two revisions applied in order,
so they are not a single `kubectl apply`.

`logBytes=0 eventCount=0` means evidence collection is not working — check the
`Evidence` wiring in `cmd/main.go`, and RBAC if running deployed.

The classifier defaults to a deterministic mock that makes no network calls and
reaches the same decisions as the live model on these workloads. Set
`CLASSIFIER_PROVIDER=anthropic` (plus `ANTHROPIC_API_KEY`) only when the LLM
itself is the point of the run — it adds ~6 s per incident and costs money.

Deployed mode (which is the only mode that exercises the manager's RBAC and
the audit PVC):

```bash
make docker-build docker-push IMG=<your-registry>/k8s-selfheal:tag
make deploy IMG=<your-registry>/k8s-selfheal:tag
```

Full demo runbook, including the SSH tunnel to a remote K3s API and teardown:
[`docs/demo-script.md`](docs/demo-script.md).

## Repository map

| Path | What's in it |
|---|---|
| `internal/controller/` | reconciler, detection, evidence freezing, incident budget |
| `internal/classifier/` | LLM providers, prompt, deterministic validator, labelled-set tests |
| `internal/safety/` | snapshot, verifier, rollback, append-only audit sink |
| `internal/actions/` | the two allowlisted actions |
| `internal/contracts/` | the structs the three packages exchange |
| `cmd/main.go` | wiring; this is where the pipeline is composed |
| `hack/manifests/` | the fault workloads, W1/W2/W3 and the kind scenarios |
| `hack/experiment/` | `results.py` and the run matrix |
| `sage-final-runs/` | the 28 archived VM runs — raw audit logs and metadata |
| `runs/` | the earlier 10-run kind cross-check |
| `docs/measurement-definitions.md` | frozen definitions every number is computed against |
| `dashboards/grafana/` | exported Grafana dashboard JSON |

## Who built what

Final-year project, Dept. of CSE, BMS College of Engineering.

| | Component |
|---|---|
| **Arya Singh** | operator core — reconciler, detection, evidence freezing, action execution, attempt budget / backoff / cooldown, workloads, demo runbook |
| **Ananya** | safety layer — snapshot, UID-based replacement-Pod attribution, verifier, rollback, durable audit |
| **Subhashini** | classifier and validator, labelled evaluation sets, experiment harness, `results.py`, Prometheus/Grafana |

## Limitations

Stated up front, because they bound what the numbers mean:

- **One fault class.** `CrashLoopBackOff` only. `ImagePullBackOff`, OOMKills
  and `Pending`-on-no-capacity are detected by nobody here.
- **Two actions.** Both chosen for reversibility — an action is admissible
  only if restoring the snapshot undoes it. That is what makes running
  ungated defensible at all.
- **A 60 s verification floor.** No incident can be declared recovered faster
  than the stability window, so mean time to mitigate cannot go below it.
- **30 s and 60 s were chosen a priori and never tuned.** No sensitivity
  analysis; W1 self-heals at ~36 s on kind, about 6 s outside the readiness
  timeout, so that workload sits close to the decision boundary.
- **N=5, one cluster, one node.** No multi-node scheduling effects, no
  concurrent-incident load testing beyond three Deployments at once.
- **The LLM is not evaluated as a classifier** against ground-truth labels at
  scale; the validator is, on 45 labelled cases.
